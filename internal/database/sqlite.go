package database

import (
	"database/sql"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/Provincia-di-Pescara/e-conomato/internal/models"
)

// DB wraps the underlying sql.DB connection.
type DB struct {
	conn *sql.DB
}

// InitDB opens (or creates) the SQLite database at dbPath and runs migrations.
// Enables WAL journal mode and foreign key enforcement.
func InitDB(dbPath string) (*DB, error) {
	conn, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := migrate(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return &DB{conn: conn}, nil
}

// migrate crea le tabelle del e-conomato se non esistono già.
// Viene invocata ad ogni avvio dell'applicazione (idempotente grazie a IF NOT EXISTS).
func migrate(conn *sql.DB) error {
	_, err := conn.Exec(`
		-- Anagrafica settori / uffici
		CREATE TABLE IF NOT EXISTS settori (
			id                   TEXT PRIMARY KEY,
			nome                 TEXT NOT NULL,
			funzionario_username TEXT
		);

		-- Utenti sincronizzati con LDAP al primo login
		-- ruolo: 'user' | 'funzionario' | 'magazziniere' | 'admin'
		CREATE TABLE IF NOT EXISTS utenti (
			username   TEXT PRIMARY KEY,
			email      TEXT,
			ruolo      TEXT NOT NULL DEFAULT 'user',
			settore_id TEXT,
			FOREIGN KEY(settore_id) REFERENCES settori(id)
		);

		-- Categorie del catalogo prodotti
		CREATE TABLE IF NOT EXISTS categorie (
			id   INTEGER PRIMARY KEY AUTOINCREMENT,
			nome TEXT NOT NULL UNIQUE
		);

		-- Anagrafica prodotti con immagine salvata come BLOB
		CREATE TABLE IF NOT EXISTS prodotti (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			codice_articolo TEXT UNIQUE,
			nome            TEXT NOT NULL,
			descrizione     TEXT,
			categoria_id    INTEGER,
			scorta_minima   INTEGER NOT NULL DEFAULT 0,
			immagine_blob   BLOB,
			FOREIGN KEY(categoria_id) REFERENCES categorie(id)
		);

		-- Anagrafica fornitori (opzionale, referenziata dagli acquisti).
		CREATE TABLE IF NOT EXISTS fornitori (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			nome        TEXT NOT NULL UNIQUE,
			partita_iva TEXT,
			email       TEXT,
			telefono    TEXT,
			note        TEXT,
			attivo      INTEGER NOT NULL DEFAULT 1
		);

		-- Documento di acquisto (head). Raggruppa N righe lotti_acquisto in
		-- un'unica bolla/ordine al fornitore. Retro-compatibile: i lotti
		-- pre-esistenti restano con acquisto_id NULL.
		CREATE TABLE IF NOT EXISTS acquisti (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			data_acquisto DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			fornitore_id  INTEGER,
			numero_doc    TEXT,
			note          TEXT,
			created_by    TEXT,
			created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY(fornitore_id) REFERENCES fornitori(id)
		);

		-- Lotti di acquisto (base per algoritmo FIFO).
		-- Ogni riga è una coppia (prodotto, lotto) con costo congelato.
		-- acquisto_id opzionale: lega più lotti allo stesso documento (head).
		CREATE TABLE IF NOT EXISTS lotti_acquisto (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			prodotto_id         INTEGER NOT NULL,
			data_acquisto       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			quantita_iniziale   INTEGER NOT NULL,
			quantita_rimanente  INTEGER NOT NULL,
			costo_unitario      REAL NOT NULL,
			acquisto_id         INTEGER,
			FOREIGN KEY(prodotto_id) REFERENCES prodotti(id),
			FOREIGN KEY(acquisto_id) REFERENCES acquisti(id)
		);

		-- Ordini di materiale
		-- stato: 'in_approvazione' | 'approvato' | 'in_preparazione' | 'pronto' | 'ritirato' | 'rifiutato'
		CREATE TABLE IF NOT EXISTS ordini (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			utente_username  TEXT NOT NULL,
			settore_id       TEXT NOT NULL,
			data_creazione   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			stato            TEXT NOT NULL,
			note_funzionario TEXT,
			FOREIGN KEY(utente_username) REFERENCES utenti(username),
			FOREIGN KEY(settore_id)      REFERENCES settori(id)
		);

		-- Singole righe di un ordine (un prodotto per riga)
		-- stato_riga: 'in_attesa' | 'evasa_parziale' | 'evasa'
		CREATE TABLE IF NOT EXISTS righe_ordine (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			ordine_id     INTEGER NOT NULL,
			prodotto_id   INTEGER NOT NULL,
			qta_richiesta INTEGER NOT NULL,
			qta_approvata INTEGER,
			qta_evasa     INTEGER NOT NULL DEFAULT 0,
			stato_riga    TEXT NOT NULL DEFAULT 'in_attesa',
			FOREIGN KEY(ordine_id)   REFERENCES ordini(id),
			FOREIGN KEY(prodotto_id) REFERENCES prodotti(id)
		);

		-- Storico movimenti: congela il costo al momento dello scarico
		CREATE TABLE IF NOT EXISTS movimenti_magazzino (
			id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			riga_ordine_id     INTEGER,
			lotto_id           INTEGER NOT NULL,
			quantita_prelevata INTEGER NOT NULL,
			costo_totale       REAL NOT NULL,
			data_movimento     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY(riga_ordine_id) REFERENCES righe_ordine(id),
			FOREIGN KEY(lotto_id)       REFERENCES lotti_acquisto(id)
		);

		-- Impostazioni globali key-value (branding, soglie, flag operativi).
		CREATE TABLE IF NOT EXISTS impostazioni (
			chiave         TEXT PRIMARY KEY,
			valore         TEXT NOT NULL,
			aggiornata_il  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);

		-- Logo aziendale persistente (singleton).
		CREATE TABLE IF NOT EXISTS branding_logo (
			id        INTEGER PRIMARY KEY CHECK(id=1),
			blob_data BLOB NOT NULL,
			mime      TEXT NOT NULL
		);

		-- Notifiche in-app mostrate al singolo utente nella pagina /notifiche.
		-- tipo: 'ordine_inviato' | 'ordine_approvato' | 'ordine_rifiutato'
		--     | 'ordine_in_preparazione' | 'ordine_pronto' | 'scorta'
		CREATE TABLE IF NOT EXISTS notifiche (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			utente_username TEXT NOT NULL,
			tipo            TEXT NOT NULL,
			messaggio       TEXT NOT NULL,
			ordine_id       INTEGER,
			prodotto_id     INTEGER,
			letta           INTEGER NOT NULL DEFAULT 0,
			creata_il       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY(utente_username) REFERENCES utenti(username),
			FOREIGN KEY(ordine_id)       REFERENCES ordini(id),
			FOREIGN KEY(prodotto_id)     REFERENCES prodotti(id)
		);

		-- Coda durevole degli invii email transazionali. Il worker asincrono
		-- consuma le righe con stato='in_attesa' e prossimo_tentativo <= now.
		-- stato: 'in_attesa' | 'inviata' | 'abbandonata'
		CREATE TABLE IF NOT EXISTS email_outbox (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			destinatario        TEXT NOT NULL,
			soggetto            TEXT NOT NULL,
			corpo_html          TEXT NOT NULL,
			tipo                TEXT NOT NULL,
			notifica_id         INTEGER,
			stato               TEXT NOT NULL DEFAULT 'in_attesa',
			tentativi           INTEGER NOT NULL DEFAULT 0,
			ultimo_errore       TEXT,
			prossimo_tentativo  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			inviata_il          DATETIME,
			creata_il           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY(notifica_id) REFERENCES notifiche(id)
		);

		-- ─── Cassa Economale (Fondo Economale) ──────────────────────────────────
		-- Modulo separato dal magazzino. L'Economo opera come Agente Contabile
		-- ai sensi degli artt. 93 e 233 del D.Lgs. 267/2000 (TUEL). Vedi PLANE.md §8.

		-- Capitoli di spesa (PEG): budget annuale per voce contabile.
		-- Il residuo è SEMPRE calcolato on-demand, mai materializzato.
		CREATE TABLE IF NOT EXISTS capitoli_spesa (
			id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			anno               INTEGER NOT NULL,
			codice_peg         TEXT NOT NULL,
			descrizione        TEXT NOT NULL,
			importo_stanziato  REAL NOT NULL DEFAULT 0,
			attivo             INTEGER NOT NULL DEFAULT 1,
			creato_il          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(anno, codice_peg)
		);

		-- Spese economali (pratiche).
		-- stato: 'in_approvazione' | 'autorizzata' | 'rifiutata_funz'
		--      | 'impegnata' | 'rifiutata_econ' | 'rendicontata' | 'chiusa'
		-- tipo_pagamento: 'contanti' | 'carta'.
		-- I campi fornitore/data_documento/estremi_documento sono NULL fino alla
		-- rendicontazione; il vincolo "non NULL alla chiusura" è normativo
		-- (Corte dei Conti) e va enforced lato repo, non a livello di schema.
		CREATE TABLE IF NOT EXISTS spese_economali (
			id                    INTEGER PRIMARY KEY AUTOINCREMENT,
			utente_username       TEXT NOT NULL,
			settore_id            TEXT NOT NULL,
			capitolo_id           INTEGER,
			motivazione           TEXT NOT NULL,
			importo_presunto      REAL NOT NULL,
			importo_effettivo     REAL,
			tipo_pagamento        TEXT NOT NULL CHECK(tipo_pagamento IN ('contanti','carta')),
			stato                 TEXT NOT NULL CHECK(stato IN (
			                          'in_approvazione','autorizzata','rifiutata_funz',
			                          'impegnata','rifiutata_econ','rendicontata','chiusa'
			                      )),
			fornitore             TEXT,
			data_documento        DATE,
			estremi_documento     TEXT,
			note_funzionario      TEXT NOT NULL DEFAULT '',
			note_economo          TEXT NOT NULL DEFAULT '',
			funzionario_username  TEXT,
			economo_username      TEXT,
			data_creazione        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			data_autorizzazione   DATETIME,
			data_impegno          DATETIME,
			data_rendicontazione  DATETIME,
			data_chiusura         DATETIME,
			FOREIGN KEY(utente_username)      REFERENCES utenti(username),
			FOREIGN KEY(settore_id)           REFERENCES settori(id),
			FOREIGN KEY(capitolo_id)          REFERENCES capitoli_spesa(id),
			FOREIGN KEY(funzionario_username) REFERENCES utenti(username),
			FOREIGN KEY(economo_username)     REFERENCES utenti(username)
		);

		-- Pezze d'appoggio: scontrini/ricevute/fatture allegate alla spesa.
		-- Salvate come BLOB nel DB (coerente con prodotti.immagine_blob).
		CREATE TABLE IF NOT EXISTS allegati_spesa (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			spesa_id     INTEGER NOT NULL,
			filename     TEXT NOT NULL,
			mime_type    TEXT NOT NULL,
			dimensione   INTEGER NOT NULL,
			blob_data    BLOB NOT NULL,
			caricato_da  TEXT NOT NULL,
			caricato_il  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY(spesa_id)    REFERENCES spese_economali(id),
			FOREIGN KEY(caricato_da) REFERENCES utenti(username)
		);

		-- Richieste di Reintegro: numerazione progressiva annuale (UNIQUE(anno,numero)).
		-- stato: 'bozza' | 'inviata' | 'liquidata'.
		CREATE TABLE IF NOT EXISTS reintegri (
			id                      INTEGER PRIMARY KEY AUTOINCREMENT,
			numero                  INTEGER NOT NULL,
			anno                    INTEGER NOT NULL,
			data_richiesta          DATE NOT NULL,
			data_emissione_mandato  DATE,
			importo_totale          REAL NOT NULL DEFAULT 0,
			stato                   TEXT NOT NULL CHECK(stato IN ('bozza','inviata','liquidata')),
			economo_username        TEXT NOT NULL,
			UNIQUE(anno, numero),
			FOREIGN KEY(economo_username) REFERENCES utenti(username)
		);

		-- Giornale di Cassa: registro cronologico dei movimenti del Fondo.
		-- tipo: 'anticipazione' | 'reintegro' | 'uscita' | 'restituzione_tesoreria'.
		CREATE TABLE IF NOT EXISTS movimenti_cassa (
			id                       INTEGER PRIMARY KEY AUTOINCREMENT,
			data                     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			tipo                     TEXT NOT NULL CHECK(tipo IN (
			                             'anticipazione','reintegro','uscita','restituzione_tesoreria'
			                         )),
			descrizione              TEXT NOT NULL,
			importo                  REAL NOT NULL,
			riferimento_spesa_id     INTEGER,
			riferimento_reintegro_id INTEGER,
			creato_da                TEXT NOT NULL,
			FOREIGN KEY(riferimento_spesa_id)     REFERENCES spese_economali(id),
			FOREIGN KEY(riferimento_reintegro_id) REFERENCES reintegri(id),
			FOREIGN KEY(creato_da)                REFERENCES utenti(username)
		);

		-- Junction reintegro ↔ spese chiuse incluse.
		CREATE TABLE IF NOT EXISTS reintegro_spese (
			reintegro_id INTEGER NOT NULL,
			spesa_id     INTEGER NOT NULL,
			PRIMARY KEY(reintegro_id, spesa_id),
			FOREIGN KEY(reintegro_id) REFERENCES reintegri(id),
			FOREIGN KEY(spesa_id)     REFERENCES spese_economali(id)
		);
	`)
	if err != nil {
		return err
	}
	if _, err = conn.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_righe_ordine_uniq ON righe_ordine(ordine_id, prodotto_id)`); err != nil {
		return err
	}
	if _, err = conn.Exec(`CREATE INDEX IF NOT EXISTS idx_notifiche_utente_letta ON notifiche(utente_username, letta, creata_il DESC)`); err != nil {
		return err
	}
	if _, err = conn.Exec(`CREATE INDEX IF NOT EXISTS idx_email_outbox_pending ON email_outbox(stato, prossimo_tentativo)`); err != nil {
		return err
	}
	if _, err = conn.Exec(`CREATE INDEX IF NOT EXISTS idx_spese_economali_stato ON spese_economali(stato)`); err != nil {
		return err
	}
	if _, err = conn.Exec(`CREATE INDEX IF NOT EXISTS idx_spese_economali_settore ON spese_economali(settore_id)`); err != nil {
		return err
	}
	if _, err = conn.Exec(`CREATE INDEX IF NOT EXISTS idx_spese_economali_utente ON spese_economali(utente_username)`); err != nil {
		return err
	}
	if _, err = conn.Exec(`CREATE INDEX IF NOT EXISTS idx_movimenti_cassa_data ON movimenti_cassa(data DESC)`); err != nil {
		return err
	}
	if _, err = conn.Exec(`CREATE INDEX IF NOT EXISTS idx_allegati_spesa_spesa ON allegati_spesa(spesa_id)`); err != nil {
		return err
	}
	if _, err = conn.Exec(`CREATE INDEX IF NOT EXISTS idx_reintegro_spese_spesa ON reintegro_spese(spesa_id)`); err != nil {
		return err
	}
	// Migrazioni additive: colonne nuove su tabelle esistenti.
	// sqlite supporta ADD COLUMN ma non IF NOT EXISTS, quindi probe via PRAGMA.
	if err := ensureColumn(conn, "categorie", "icona", `ALTER TABLE categorie ADD COLUMN icona TEXT NOT NULL DEFAULT 'fa-solid fa-box'`); err != nil {
		return err
	}
	if err := ensureColumn(conn, "prodotti", "icona", `ALTER TABLE prodotti ADD COLUMN icona TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureColumn(conn, "righe_ordine", "prenotazione", `ALTER TABLE righe_ordine ADD COLUMN prenotazione INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := ensureColumn(conn, "righe_ordine", "nota_utente", `ALTER TABLE righe_ordine ADD COLUMN nota_utente TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	// Retro-compat: DB esistenti pre-acquisti hanno lotti_acquisto senza acquisto_id.
	if err := ensureColumn(conn, "lotti_acquisto", "acquisto_id", `ALTER TABLE lotti_acquisto ADD COLUMN acquisto_id INTEGER`); err != nil {
		return err
	}
	// Supporto multi-ruolo magazziniere+economo.
	if err := ensureColumn(conn, "utenti", "ruolo_secondario", `ALTER TABLE utenti ADD COLUMN ruolo_secondario TEXT`); err != nil {
		return err
	}
	return nil
}

// ensureColumn aggiunge una colonna a una tabella solo se non già presente.
func ensureColumn(conn *sql.DB, table, column, alter string) error {
	rows, err := conn.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if _, err := conn.Exec(alter); err != nil {
		return fmt.Errorf("alter %s add %s: %w", table, column, err)
	}
	return nil
}

// ── Utenti ───────────────────────────────────────────────────────────────────

// UpsertUtente creates a new user record or updates email and role on conflict.
// ruoloSecondario è nil per utenti single-role; &"economo" per utenti magazziniere+economo.
func (db *DB) UpsertUtente(username, email, ruoloPrimario string, ruoloSecondario *string) error {
	_, err := db.conn.Exec(
		`INSERT INTO utenti (username, email, ruolo, ruolo_secondario)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(username) DO UPDATE SET
		   email=excluded.email,
		   ruolo=excluded.ruolo,
		   ruolo_secondario=excluded.ruolo_secondario`,
		username, email, ruoloPrimario, ruoloSecondario,
	)
	return err
}

// GetAllUtenti returns all users.
func (db *DB) GetAllUtenti() ([]models.Utente, error) {
	rows, err := db.conn.Query(`SELECT username, COALESCE(email,''), ruolo, COALESCE(ruolo_secondario,''), COALESCE(settore_id,'') FROM utenti ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Utente
	for rows.Next() {
		var u models.Utente
		if err := rows.Scan(&u.Username, &u.Email, &u.Ruolo, &u.RuoloSecondario, &u.SettoreID); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// GetSettoreIDByUsername restituisce il settore_id dell'utente, o "" se non assegnato.
func (db *DB) GetSettoreIDByUsername(username string) (string, error) {
	var s sql.NullString
	err := db.conn.QueryRow(`SELECT settore_id FROM utenti WHERE username = ?`, username).Scan(&s)
	if err != nil {
		return "", err
	}
	return s.String, nil
}

// ── Categorie ────────────────────────────────────────────────────────────────

func (db *DB) GetAllCategorie() ([]models.Categoria, error) {
	rows, err := db.conn.Query(`SELECT id, nome, COALESCE(icona,'fa-solid fa-box') FROM categorie ORDER BY nome`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Categoria
	for rows.Next() {
		var c models.Categoria
		if err := rows.Scan(&c.ID, &c.Nome, &c.Icona); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (db *DB) CreateCategoria(nome, icona string) (models.Categoria, error) {
	if icona == "" {
		icona = "fa-solid fa-box"
	}
	res, err := db.conn.Exec(`INSERT INTO categorie (nome, icona) VALUES (?, ?)`, nome, icona)
	if err != nil {
		return models.Categoria{}, err
	}
	id, _ := res.LastInsertId()
	return models.Categoria{ID: id, Nome: nome, Icona: icona}, nil
}

func (db *DB) UpdateCategoria(id int64, nome, icona string) error {
	if icona == "" {
		icona = "fa-solid fa-box"
	}
	_, err := db.conn.Exec(`UPDATE categorie SET nome=?, icona=? WHERE id=?`, nome, icona, id)
	return err
}

func (db *DB) DeleteCategoria(id int64) error {
	_, err := db.conn.Exec(`DELETE FROM categorie WHERE id=?`, id)
	return err
}

// GetCatalogo restituisce prodotti con disponibilità aggregata dai lotti.
// search filtra per nome o codice (case-insensitive). categoriaID=0 = tutte le categorie.
func (db *DB) GetCatalogo(search string, categoriaID int64) ([]models.ProdottoCatalogo, error) {
	rows, err := db.conn.Query(`
		SELECT p.id, COALESCE(p.codice_articolo,''), p.nome, COALESCE(p.descrizione,''),
		       COALESCE(p.categoria_id,0), COALESCE(c.nome,''),
		       COALESCE(c.icona,'fa-solid fa-box'), COALESCE(p.icona,''),
		       p.scorta_minima,
		       COALESCE(SUM(l.quantita_rimanente),0)
		FROM prodotti p
		LEFT JOIN categorie c ON c.id = p.categoria_id
		LEFT JOIN lotti_acquisto l ON l.prodotto_id = p.id
		WHERE (? = '' OR LOWER(p.nome) LIKE '%'||LOWER(?)||'%'
		              OR LOWER(COALESCE(p.codice_articolo,'')) LIKE '%'||LOWER(?)||'%')
		  AND (? = 0 OR p.categoria_id = ?)
		GROUP BY p.id
		ORDER BY p.nome
	`, search, search, search, categoriaID, categoriaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.ProdottoCatalogo
	for rows.Next() {
		var p models.ProdottoCatalogo
		if err := rows.Scan(&p.ID, &p.CodiceArticolo, &p.Nome, &p.Descrizione,
			&p.CategoriaID, &p.CategoriaNome, &p.CategoriaIcona, &p.Icona,
			&p.ScortaMinima, &p.Disponibile); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ── Prodotti ─────────────────────────────────────────────────────────────────

// ProdottoRow extends Prodotto with the category name for list views (no BLOB).
// HasImmagine is true when the row has a non-empty immagine_blob, computed in
// SQL so the list query never has to ship the BLOB payload itself.
type ProdottoRow struct {
	models.Prodotto
	CategoriaName   string
	ScortaRimanente int
	HasImmagine     bool
}

func (db *DB) GetAllProdotti() ([]ProdottoRow, error) {
	rows, err := db.conn.Query(`
		SELECT p.id, COALESCE(p.codice_articolo,''), p.nome, COALESCE(p.descrizione,''),
		       COALESCE(p.categoria_id,0), p.scorta_minima, COALESCE(p.icona,''),
		       (p.immagine_blob IS NOT NULL AND length(p.immagine_blob) > 0),
		       COALESCE(c.nome,'—'),
		       COALESCE((SELECT SUM(l.quantita_rimanente) FROM lotti_acquisto l WHERE l.prodotto_id=p.id),0)
		FROM prodotti p
		LEFT JOIN categorie c ON c.id=p.categoria_id
		ORDER BY p.nome`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProdottoRow
	for rows.Next() {
		var r ProdottoRow
		if err := rows.Scan(&r.ID, &r.CodiceArticolo, &r.Nome, &r.Descrizione,
			&r.CategoriaID, &r.ScortaMinima, &r.Icona, &r.HasImmagine,
			&r.CategoriaName, &r.ScortaRimanente); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (db *DB) GetProdottoByID(id int64) (models.Prodotto, error) {
	var p models.Prodotto
	err := db.conn.QueryRow(`
		SELECT id, COALESCE(codice_articolo,''), nome, COALESCE(descrizione,''),
		       COALESCE(categoria_id,0), scorta_minima, COALESCE(icona,'')
		FROM prodotti WHERE id=?`, id).
		Scan(&p.ID, &p.CodiceArticolo, &p.Nome, &p.Descrizione, &p.CategoriaID, &p.ScortaMinima, &p.Icona)
	if err == sql.ErrNoRows {
		return p, fmt.Errorf("prodotto %d non trovato", id)
	}
	return p, err
}

func (db *DB) CreateProdotto(p models.Prodotto) (int64, error) {
	var catID interface{}
	if p.CategoriaID != 0 {
		catID = p.CategoriaID
	}
	res, err := db.conn.Exec(`
		INSERT INTO prodotti (codice_articolo, nome, descrizione, categoria_id, scorta_minima, immagine_blob, icona)
		VALUES (?,?,?,?,?,?,?)`,
		nullableStr(p.CodiceArticolo), p.Nome, nullableStr(p.Descrizione), catID, p.ScortaMinima, nullableBlob(p.ImmagineBLOB), p.Icona,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (db *DB) UpdateProdotto(p models.Prodotto) error {
	var catID interface{}
	if p.CategoriaID != 0 {
		catID = p.CategoriaID
	}
	if len(p.ImmagineBLOB) > 0 {
		_, err := db.conn.Exec(`
			UPDATE prodotti SET codice_articolo=?, nome=?, descrizione=?, categoria_id=?, scorta_minima=?, immagine_blob=?, icona=?
			WHERE id=?`,
			nullableStr(p.CodiceArticolo), p.Nome, nullableStr(p.Descrizione), catID, p.ScortaMinima, p.ImmagineBLOB, p.Icona, p.ID,
		)
		return err
	}
	_, err := db.conn.Exec(`
		UPDATE prodotti SET codice_articolo=?, nome=?, descrizione=?, categoria_id=?, scorta_minima=?, icona=?
		WHERE id=?`,
		nullableStr(p.CodiceArticolo), p.Nome, nullableStr(p.Descrizione), catID, p.ScortaMinima, p.Icona, p.ID,
	)
	return err
}

func (db *DB) DeleteProdotto(id int64) error {
	_, err := db.conn.Exec(`DELETE FROM prodotti WHERE id=?`, id)
	return err
}

// GetProdottoImmagine returns only the immagine_blob for serving.
func (db *DB) GetProdottoImmagine(id int64) ([]byte, error) {
	var blob []byte
	err := db.conn.QueryRow(`SELECT immagine_blob FROM prodotti WHERE id=?`, id).Scan(&blob)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return blob, err
}

// ── Lotti ────────────────────────────────────────────────────────────────────

func (db *DB) CreateLotto(l models.LottoAcquisto) (int64, error) {
	res, err := db.conn.Exec(`
		INSERT INTO lotti_acquisto (prodotto_id, data_acquisto, quantita_iniziale, quantita_rimanente, costo_unitario)
		VALUES (?,?,?,?,?)`,
		l.ProdottoID, l.DataAcquisto.UTC().Format(time.RFC3339), l.QuantitaIniziale, l.QuantitaIniziale, l.CostoUnitario,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (db *DB) GetLottiByProdotto(prodottoID int64) ([]models.LottoAcquisto, error) {
	rows, err := db.conn.Query(`
		SELECT id, prodotto_id, data_acquisto, quantita_iniziale, quantita_rimanente, costo_unitario
		FROM lotti_acquisto WHERE prodotto_id=? ORDER BY data_acquisto ASC`, prodottoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.LottoAcquisto
	for rows.Next() {
		var lo models.LottoAcquisto
		var daStr string
		if err := rows.Scan(&lo.ID, &lo.ProdottoID, &daStr, &lo.QuantitaIniziale, &lo.QuantitaRimanente, &lo.CostoUnitario); err != nil {
			return nil, err
		}
		lo.DataAcquisto, _ = time.Parse(time.RFC3339, daStr)
		out = append(out, lo)
	}
	return out, rows.Err()
}

// ── Scorte ───────────────────────────────────────────────────────────────────

// GetProdottiSottoSoglia returns products where current stock < scorta_minima.
func (db *DB) GetProdottiSottoSoglia() ([]ProdottoRow, error) {
	rows, err := db.conn.Query(`
		SELECT p.id, COALESCE(p.codice_articolo,''), p.nome, COALESCE(p.descrizione,''),
		       COALESCE(p.categoria_id,0), p.scorta_minima, COALESCE(p.icona,''),
		       COALESCE(c.nome,'—'),
		       COALESCE((SELECT SUM(l.quantita_rimanente) FROM lotti_acquisto l WHERE l.prodotto_id=p.id),0) AS scorta
		FROM prodotti p
		LEFT JOIN categorie c ON c.id=p.categoria_id
		WHERE scorta < p.scorta_minima
		ORDER BY scorta ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProdottoRow
	for rows.Next() {
		var r ProdottoRow
		if err := rows.Scan(&r.ID, &r.CodiceArticolo, &r.Nome, &r.Descrizione,
			&r.CategoriaID, &r.ScortaMinima, &r.Icona, &r.CategoriaName, &r.ScortaRimanente); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (db *DB) getRigheConProdotto(ordineID int64) ([]models.RigaConProdotto, error) {
	rows, err := db.conn.Query(`
		SELECT r.id, r.ordine_id, r.prodotto_id, r.qta_richiesta, r.qta_approvata,
		       r.qta_evasa, r.stato_riga,
		       COALESCE(r.prenotazione,0), COALESCE(r.nota_utente,''),
		       p.nome, COALESCE(p.codice_articolo,'')
		FROM righe_ordine r
		JOIN prodotti p ON p.id = r.prodotto_id
		WHERE r.ordine_id = ?
		ORDER BY r.id
	`, ordineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.RigaConProdotto
	for rows.Next() {
		var r models.RigaConProdotto
		var prenot int
		if err := rows.Scan(&r.ID, &r.OrdineID, &r.ProdottoID, &r.QtaRichiesta, &r.QtaApprovata,
			&r.QtaEvasa, &r.StatoRiga, &prenot, &r.NotaUtente,
			&r.ProdottoNome, &r.ProdottoCodice); err != nil {
			return nil, err
		}
		r.Prenotazione = prenot != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetOrCreateBozza restituisce l'ID dell'ordine bozza dell'utente, creandolo se non esiste.
// Errore se l'utente non ha settore_id assegnato.
func (db *DB) GetOrCreateBozza(username string) (int64, error) {
	var existing int64
	err := db.conn.QueryRow(
		`SELECT id FROM ordini WHERE utente_username = ? AND stato = 'bozza' LIMIT 1`, username,
	).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	settoreID, err := db.GetSettoreIDByUsername(username)
	if err != nil {
		return 0, err
	}
	if settoreID == "" {
		return 0, fmt.Errorf("utente %s senza settore assegnato", username)
	}
	res, err := db.conn.Exec(
		`INSERT INTO ordini (utente_username, settore_id, stato) VALUES (?, ?, 'bozza')`,
		username, settoreID,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetBozzaConRighe restituisce l'ordine bozza con righe, o nil se non esiste.
func (db *DB) GetBozzaConRighe(username string) (*models.OrdineConRighe, error) {
	var o models.Ordine
	err := db.conn.QueryRow(`
		SELECT id, utente_username, settore_id, data_creazione, stato, COALESCE(note_funzionario,'')
		FROM ordini WHERE utente_username = ? AND stato = 'bozza' LIMIT 1
	`, username).Scan(&o.ID, &o.UtenteUsername, &o.SettoreID, &o.DataCreazione, &o.Stato, &o.NoteFunzionario)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	righe, err := db.getRigheConProdotto(o.ID)
	if err != nil {
		return nil, err
	}
	return &models.OrdineConRighe{Ordine: o, Righe: righe}, nil
}

// UpsertRigaBozza inserisce o aggiorna la quantità di un prodotto nella bozza.
// Se qta <= 0 rimuove la riga. Non tocca i flag prenotazione/nota_utente di una riga esistente.
func (db *DB) UpsertRigaBozza(ordineID, prodottoID int64, qta int) error {
	if qta <= 0 {
		_, err := db.conn.Exec(
			`DELETE FROM righe_ordine WHERE ordine_id = ? AND prodotto_id = ?`,
			ordineID, prodottoID,
		)
		return err
	}
	_, err := db.conn.Exec(`
		INSERT INTO righe_ordine (ordine_id, prodotto_id, qta_richiesta)
		VALUES (?, ?, ?)
		ON CONFLICT(ordine_id, prodotto_id) DO UPDATE SET qta_richiesta = excluded.qta_richiesta
	`, ordineID, prodottoID, qta)
	return err
}

// UpsertPrenotazione crea/aggiorna una riga bozza con flag prenotazione e nota utente.
// Usata dal flusso "Prenota rifornimento" per prodotti esauriti.
func (db *DB) UpsertPrenotazione(ordineID, prodottoID int64, qta int, nota string) error {
	_, err := db.conn.Exec(`
		INSERT INTO righe_ordine (ordine_id, prodotto_id, qta_richiesta, prenotazione, nota_utente)
		VALUES (?, ?, ?, 1, ?)
		ON CONFLICT(ordine_id, prodotto_id) DO UPDATE
		SET qta_richiesta = excluded.qta_richiesta,
		    prenotazione  = 1,
		    nota_utente   = excluded.nota_utente
	`, ordineID, prodottoID, qta, nota)
	return err
}

// GetRigaBozzaCorrente restituisce la quantità di una riga della bozza, o 0 se non esiste.
// Usata dallo stepper +/− per calcolare il nuovo valore lato server.
func (db *DB) GetRigaBozzaCorrente(ordineID, prodottoID int64) (int, error) {
	var qta int
	err := db.conn.QueryRow(
		`SELECT qta_richiesta FROM righe_ordine WHERE ordine_id = ? AND prodotto_id = ?`,
		ordineID, prodottoID,
	).Scan(&qta)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return qta, err
}

// GetDisponibilitaProdotto restituisce la giacenza corrente (somma quantita_rimanente lotti).
func (db *DB) GetDisponibilitaProdotto(prodottoID int64) (int, error) {
	var n int
	err := db.conn.QueryRow(
		`SELECT COALESCE(SUM(quantita_rimanente),0) FROM lotti_acquisto WHERE prodotto_id = ?`,
		prodottoID,
	).Scan(&n)
	return n, err
}

// DeleteRigaBozza rimuove un prodotto dalla bozza.
func (db *DB) DeleteRigaBozza(ordineID, prodottoID int64) error {
	_, err := db.conn.Exec(
		`DELETE FROM righe_ordine WHERE ordine_id = ? AND prodotto_id = ?`,
		ordineID, prodottoID,
	)
	return err
}

// scanOrdini is a helper that scans rows into OrdineConRighe, fetching righe for each ordine.
func (db *DB) scanOrdini(rows *sql.Rows) ([]models.OrdineConRighe, error) {
	defer rows.Close()
	var out []models.OrdineConRighe
	for rows.Next() {
		var o models.Ordine
		if err := rows.Scan(&o.ID, &o.UtenteUsername, &o.SettoreID, &o.DataCreazione,
			&o.Stato, &o.NoteFunzionario); err != nil {
			return nil, err
		}
		righe, err := db.getRigheConProdotto(o.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, models.OrdineConRighe{Ordine: o, Righe: righe})
	}
	return out, rows.Err()
}

// GetOrdiniUtente restituisce tutti gli ordini (escluso bozza) dell'utente, dal più recente.
func (db *DB) GetOrdiniUtente(username string) ([]models.OrdineConRighe, error) {
	rows, err := db.conn.Query(`
		SELECT id, utente_username, settore_id, data_creazione, stato, COALESCE(note_funzionario,'')
		FROM ordini WHERE utente_username = ? AND stato != 'bozza'
		ORDER BY data_creazione DESC
	`, username)
	if err != nil {
		return nil, err
	}
	return db.scanOrdini(rows)
}

// GetOrdiniSettore restituisce ordini in_approvazione del settore, dal più vecchio.
func (db *DB) GetOrdiniSettore(settoreID string) ([]models.OrdineConRighe, error) {
	rows, err := db.conn.Query(`
		SELECT id, utente_username, settore_id, data_creazione, stato, COALESCE(note_funzionario,'')
		FROM ordini WHERE settore_id = ? AND stato = 'in_approvazione'
		ORDER BY data_creazione ASC
	`, settoreID)
	if err != nil {
		return nil, err
	}
	return db.scanOrdini(rows)
}

// VerificaOrdineSettore restituisce un errore se l'ordine non appartiene al settore indicato.
func (db *DB) VerificaOrdineSettore(ordineID int64, settoreID string) error {
	var count int
	err := db.conn.QueryRow(
		`SELECT COUNT(*) FROM ordini WHERE id = ? AND settore_id = ?`, ordineID, settoreID,
	).Scan(&count)
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("ordine %d non appartiene al settore %s", ordineID, settoreID)
	}
	return nil
}

// GetOrdiniAttivi restituisce ordini in lavorazione per il magazzino.
func (db *DB) GetOrdiniAttivi() ([]models.OrdineConRighe, error) {
	rows, err := db.conn.Query(`
		SELECT id, utente_username, settore_id, data_creazione, stato, COALESCE(note_funzionario,'')
		FROM ordini WHERE stato IN ('approvato','in_preparazione','pronto')
		ORDER BY data_creazione ASC
	`)
	if err != nil {
		return nil, err
	}
	return db.scanOrdini(rows)
}

// ── order actions ────────────────────────────────────────────────────────────

// InviaOrdine transita la bozza a in_approvazione o in_preparazione (auto-approvazione).
func (db *DB) InviaOrdine(ordineID int64, username string) error {
	var funzionario sql.NullString
	err := db.conn.QueryRow(`
		SELECT s.funzionario_username FROM settori s
		JOIN ordini o ON o.settore_id = s.id WHERE o.id = ?
	`, ordineID).Scan(&funzionario)
	if err != nil {
		return err
	}
	stato := "in_approvazione"
	if funzionario.Valid && funzionario.String == username {
		stato = "in_preparazione"
	}
	_, err = db.conn.Exec(
		`UPDATE ordini SET stato = ? WHERE id = ? AND stato = 'bozza'`,
		stato, ordineID,
	)
	return err
}

// ApprovaOrdine aggiorna le quantità approvate e transita l'ordine ad 'approvato'.
// qtaPerRiga mappa riga_ordine.id → quantità approvata.
func (db *DB) ApprovaOrdine(ordineID int64, qtaPerRiga map[int64]int, note string) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for rigaID, qta := range qtaPerRiga {
		if _, err = tx.Exec(
			`UPDATE righe_ordine SET qta_approvata = ? WHERE id = ? AND ordine_id = ?`,
			qta, rigaID, ordineID,
		); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(
		`UPDATE ordini SET stato = 'approvato', note_funzionario = ? WHERE id = ?`,
		note, ordineID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// RifiutaOrdine transita l'ordine a 'rifiutato' con nota obbligatoria.
func (db *DB) RifiutaOrdine(ordineID int64, note string) error {
	_, err := db.conn.Exec(
		`UPDATE ordini SET stato = 'rifiutato', note_funzionario = ? WHERE id = ? AND stato = 'in_approvazione'`,
		note, ordineID,
	)
	return err
}

// PreparaOrdineFIFO scarica i lotti FIFO per ogni riga, crea movimenti_magazzino
// e transita l'ordine a 'in_preparazione'. Restituisce l'elenco dei prodotti
// che, per effetto dello scarico, sono scesi sotto la propria scorta_minima
// (utile per emettere notifiche `scorta` ai magazzinieri).
func (db *DB) PreparaOrdineFIFO(ordineID int64) ([]models.ScortaSottoSoglia, error) {
	tx, err := db.conn.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`
		SELECT id, prodotto_id, COALESCE(qta_approvata, qta_richiesta)
		FROM righe_ordine WHERE ordine_id = ?
	`, ordineID)
	if err != nil {
		return nil, err
	}
	type riga struct {
		id           int64
		prodottoID   int64
		qtaDaEvadere int
	}
	var righe []riga
	for rows.Next() {
		var r riga
		if err := rows.Scan(&r.id, &r.prodottoID, &r.qtaDaEvadere); err != nil {
			rows.Close()
			return nil, err
		}
		righe = append(righe, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Snapshot pre-scarico per individuare i prodotti che attraversano la
	// soglia minima durante questa transazione.
	type stockSnap struct {
		nome      string
		soglia    int
		preScarico int
	}
	pre := map[int64]stockSnap{}
	for _, r := range righe {
		if _, ok := pre[r.prodottoID]; ok {
			continue
		}
		var s stockSnap
		err := tx.QueryRow(`
			SELECT p.nome, p.scorta_minima,
			       COALESCE((SELECT SUM(quantita_rimanente) FROM lotti_acquisto WHERE prodotto_id = p.id AND quantita_rimanente > 0), 0)
			FROM prodotti p WHERE p.id = ?
		`, r.prodottoID).Scan(&s.nome, &s.soglia, &s.preScarico)
		if err != nil {
			return nil, err
		}
		pre[r.prodottoID] = s
	}

	for _, r := range righe {
		qtaResidua := r.qtaDaEvadere
		qtaEvasa := 0

		lotti, err := tx.Query(`
			SELECT id, quantita_rimanente, costo_unitario FROM lotti_acquisto
			WHERE prodotto_id = ? AND quantita_rimanente > 0
			ORDER BY data_acquisto ASC
		`, r.prodottoID)
		if err != nil {
			return nil, err
		}
		type lotto struct {
			id        int64
			rimanente int
			costoUnit float64
		}
		var ls []lotto
		for lotti.Next() {
			var l lotto
			if err := lotti.Scan(&l.id, &l.rimanente, &l.costoUnit); err != nil {
				lotti.Close()
				return nil, err
			}
			ls = append(ls, l)
		}
		lotti.Close()

		for _, l := range ls {
			if qtaResidua <= 0 {
				break
			}
			prelevato := l.rimanente
			if prelevato > qtaResidua {
				prelevato = qtaResidua
			}
			costo := float64(prelevato) * l.costoUnit
			if _, err = tx.Exec(
				`INSERT INTO movimenti_magazzino (riga_ordine_id, lotto_id, quantita_prelevata, costo_totale) VALUES (?,?,?,?)`,
				r.id, l.id, prelevato, costo,
			); err != nil {
				return nil, err
			}
			if _, err = tx.Exec(
				`UPDATE lotti_acquisto SET quantita_rimanente = quantita_rimanente - ? WHERE id = ?`,
				prelevato, l.id,
			); err != nil {
				return nil, err
			}
			qtaResidua -= prelevato
			qtaEvasa += prelevato
		}

		statoRiga := "evasa"
		if qtaEvasa == 0 {
			statoRiga = "in_attesa"
		} else if qtaEvasa < r.qtaDaEvadere {
			statoRiga = "evasa_parziale"
		}
		if _, err = tx.Exec(
			`UPDATE righe_ordine SET qta_evasa = ?, stato_riga = ? WHERE id = ?`,
			qtaEvasa, statoRiga, r.id,
		); err != nil {
			return nil, err
		}
	}

	if _, err = tx.Exec(`UPDATE ordini SET stato = 'in_preparazione' WHERE id = ?`, ordineID); err != nil {
		return nil, err
	}

	// Confronta post-scarico per costruire l'elenco di scorte sotto soglia.
	var attraversate []models.ScortaSottoSoglia
	for prodID, snap := range pre {
		var post int
		err := tx.QueryRow(`
			SELECT COALESCE(SUM(quantita_rimanente), 0) FROM lotti_acquisto
			WHERE prodotto_id = ? AND quantita_rimanente > 0
		`, prodID).Scan(&post)
		if err != nil {
			return nil, err
		}
		if snap.preScarico >= snap.soglia && post < snap.soglia {
			attraversate = append(attraversate, models.ScortaSottoSoglia{
				ProdottoID:   prodID,
				ProdottoNome: snap.nome,
				Rimanente:    post,
				SogliaMinima: snap.soglia,
			})
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return attraversate, nil
}

// SimulaOrdineFIFO esegue una simulazione non distruttiva dello scarico
// FIFO per un ordine: legge le righe, scorre i lotti in ORDER BY data_acquisto
// ASC e calcola quali prelievi avverrebbero, senza scrivere su
// movimenti_magazzino o lotti_acquisto. Pensata per l'anteprima dal cruscotto
// magazziniere prima di confermare la preparazione.
func (db *DB) SimulaOrdineFIFO(ordineID int64) (*models.AnteprimaFIFO, error) {
	rows, err := db.conn.Query(`
		SELECT r.id, r.prodotto_id, p.nome,
		       COALESCE(r.qta_approvata, r.qta_richiesta)
		FROM righe_ordine r
		JOIN prodotti p ON p.id = r.prodotto_id
		WHERE r.ordine_id = ?
		ORDER BY r.id
	`, ordineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type rigaIn struct {
		id           int64
		prodottoID   int64
		prodottoNome string
		qtaDaEvadere int
	}
	var righe []rigaIn
	for rows.Next() {
		var r rigaIn
		if err := rows.Scan(&r.id, &r.prodottoID, &r.prodottoNome, &r.qtaDaEvadere); err != nil {
			return nil, err
		}
		righe = append(righe, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := &models.AnteprimaFIFO{OrdineID: ordineID, Righe: make([]models.RigaAnteprima, 0, len(righe))}
	for _, r := range righe {
		lotti, err := db.conn.Query(`
			SELECT id, data_acquisto, quantita_rimanente, costo_unitario
			FROM lotti_acquisto
			WHERE prodotto_id = ? AND quantita_rimanente > 0
			ORDER BY data_acquisto ASC
		`, r.prodottoID)
		if err != nil {
			return nil, err
		}
		ra := models.RigaAnteprima{
			RigaID:       r.id,
			ProdottoID:   r.prodottoID,
			ProdottoNome: r.prodottoNome,
			QtaDaEvadere: r.qtaDaEvadere,
		}
		residua := r.qtaDaEvadere
		for lotti.Next() {
			var lID int64
			var lData time.Time
			var lRim int
			var lCosto float64
			if err := lotti.Scan(&lID, &lData, &lRim, &lCosto); err != nil {
				lotti.Close()
				return nil, err
			}
			if residua <= 0 {
				continue
			}
			prelevato := lRim
			if prelevato > residua {
				prelevato = residua
			}
			costo := float64(prelevato) * lCosto
			ra.Picks = append(ra.Picks, models.PickFIFO{
				LottoID:       lID,
				DataAcquisto:  lData,
				QtaPrelevata:  prelevato,
				CostoUnitario: lCosto,
				CostoTotale:   costo,
			})
			ra.QtaSimulataEvasa += prelevato
			ra.CostoRiga += costo
			residua -= prelevato
		}
		lotti.Close()
		if err := lotti.Err(); err != nil {
			return nil, err
		}
		switch {
		case ra.QtaSimulataEvasa == 0:
			ra.Esito = "in_attesa"
		case ra.QtaSimulataEvasa < ra.QtaDaEvadere:
			ra.Esito = "evasa_parziale"
		default:
			ra.Esito = "evasa"
		}
		out.TotaleCosto += ra.CostoRiga
		out.Righe = append(out.Righe, ra)
	}
	return out, nil
}

// SegnaOrdinePronte transita l'ordine a 'pronto'. Restituisce l'username per notifica email.
func (db *DB) SegnaOrdinePronte(ordineID int64) (string, error) {
	var username string
	if err := db.conn.QueryRow(`SELECT utente_username FROM ordini WHERE id = ?`, ordineID).Scan(&username); err != nil {
		return "", err
	}
	_, err := db.conn.Exec(
		`UPDATE ordini SET stato = 'pronto' WHERE id = ? AND stato = 'in_preparazione'`, ordineID,
	)
	return username, err
}

// ConsegnaOrdine transita l'ordine a 'ritirato'.
func (db *DB) ConsegnaOrdine(ordineID int64) error {
	_, err := db.conn.Exec(
		`UPDATE ordini SET stato = 'ritirato' WHERE id = ? AND stato = 'pronto'`, ordineID,
	)
	return err
}

// ── Impostazioni ─────────────────────────────────────────────────────────────

// GetImpostazione legge un valore dalla tabella impostazioni.
// Ritorna (valore, true) se presente, ("", false) altrimenti.
func (db *DB) GetImpostazione(chiave string) (string, bool) {
	var v string
	err := db.conn.QueryRow(`SELECT valore FROM impostazioni WHERE chiave = ?`, chiave).Scan(&v)
	if err != nil {
		return "", false
	}
	return v, true
}

// SetImpostazione upserta una chiave; valore vuoto è ammesso (per reset esplicito).
func (db *DB) SetImpostazione(chiave, valore string) error {
	_, err := db.conn.Exec(`
		INSERT INTO impostazioni (chiave, valore) VALUES (?, ?)
		ON CONFLICT(chiave) DO UPDATE SET valore=excluded.valore, aggiornata_il=CURRENT_TIMESTAMP
	`, chiave, valore)
	return err
}

// GetAllImpostazioni ritorna tutte le impostazioni come mappa.
func (db *DB) GetAllImpostazioni() (map[string]string, error) {
	rows, err := db.conn.Query(`SELECT chiave, valore FROM impostazioni`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// HasBrandingLogo segnala se un logo brand è già stato caricato in DB.
func (db *DB) HasBrandingLogo() (bool, error) {
	var n int
	err := db.conn.QueryRow(`SELECT COUNT(*) FROM branding_logo WHERE id=1`).Scan(&n)
	return n > 0, err
}

// GetBrandingLogo ritorna (blob, mime). Se non esiste, blob è nil.
func (db *DB) GetBrandingLogo() ([]byte, string, error) {
	var blob []byte
	var mime string
	err := db.conn.QueryRow(`SELECT blob_data, mime FROM branding_logo WHERE id=1`).Scan(&blob, &mime)
	if err == sql.ErrNoRows {
		return nil, "", nil
	}
	return blob, mime, err
}

// SetBrandingLogo sostituisce il logo brand.
func (db *DB) SetBrandingLogo(blob []byte, mime string) error {
	_, err := db.conn.Exec(`
		INSERT INTO branding_logo (id, blob_data, mime) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET blob_data=excluded.blob_data, mime=excluded.mime
	`, blob, mime)
	return err
}

// DeleteBrandingLogo rimuove il logo brand (torna al default env / SVG).
func (db *DB) DeleteBrandingLogo() error {
	_, err := db.conn.Exec(`DELETE FROM branding_logo WHERE id=1`)
	return err
}

// GetSettoreNomeByUsername restituisce il nome leggibile del settore di un utente, "" se non assegnato.
func (db *DB) GetSettoreNomeByUsername(username string) (string, error) {
	var nome sql.NullString
	err := db.conn.QueryRow(`
		SELECT s.nome FROM utenti u
		LEFT JOIN settori s ON s.id = u.settore_id
		WHERE u.username = ?`, username).Scan(&nome)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return nome.String, nil
}

// GetOrdiniSettoreAll restituisce tutti gli ordini (escluso bozza) di un settore,
// dal più recente. Usata dal funzionario per vedere lo storico completo.
func (db *DB) GetOrdiniSettoreAll(settoreID string) ([]models.OrdineConRighe, error) {
	rows, err := db.conn.Query(`
		SELECT id, utente_username, settore_id, data_creazione, stato, COALESCE(note_funzionario,'')
		FROM ordini WHERE settore_id = ? AND stato != 'bozza'
		ORDER BY data_creazione DESC
	`, settoreID)
	if err != nil {
		return nil, err
	}
	return db.scanOrdini(rows)
}

// GetOrdiniStorico restituisce ordini per la vista storico del magazzino.
// stato vuoto = tutti gli stati (escluso bozza). query è una stringa libera
// matchata contro ordine.id, utente_username, settore.nome, prodotto.nome
// e prodotto.codice_articolo. prodottoID > 0 limita agli ordini che
// contengono quel prodotto.
func (db *DB) GetOrdiniStorico(stato, query string, prodottoID int64) ([]models.OrdineConRighe, error) {
	sb := `
		SELECT DISTINCT o.id, o.utente_username, o.settore_id, o.data_creazione,
		       o.stato, COALESCE(o.note_funzionario,'')
		FROM ordini o
		LEFT JOIN settori s ON s.id = o.settore_id
	`
	needRighe := prodottoID > 0 || query != ""
	if needRighe {
		sb += `
		LEFT JOIN righe_ordine r ON r.ordine_id = o.id
		LEFT JOIN prodotti p ON p.id = r.prodotto_id
		`
	}
	sb += ` WHERE o.stato != 'bozza' `
	args := []any{}
	if stato != "" {
		sb += ` AND o.stato = ? `
		args = append(args, stato)
	}
	if prodottoID > 0 {
		sb += ` AND r.prodotto_id = ? `
		args = append(args, prodottoID)
	}
	if query != "" {
		like := "%" + query + "%"
		sb += ` AND (
			CAST(o.id AS TEXT) LIKE ?
			OR o.utente_username LIKE ?
			OR COALESCE(s.nome,'') LIKE ?
			OR COALESCE(p.nome,'') LIKE ?
			OR COALESCE(p.codice_articolo,'') LIKE ?
		) `
		args = append(args, like, like, like, like, like)
	}
	sb += ` ORDER BY o.data_creazione DESC `

	rows, err := db.conn.Query(sb, args...)
	if err != nil {
		return nil, err
	}
	return db.scanOrdini(rows)
}

// ── Fornitori ────────────────────────────────────────────────────────────────

// GetAllFornitori restituisce i fornitori attivi, ordinati per nome.
func (db *DB) GetAllFornitori() ([]models.Fornitore, error) {
	rows, err := db.conn.Query(`
		SELECT id, nome, COALESCE(partita_iva,''), COALESCE(email,''),
		       COALESCE(telefono,''), COALESCE(note,''), attivo
		FROM fornitori WHERE attivo=1 ORDER BY nome`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Fornitore
	for rows.Next() {
		var f models.Fornitore
		var attivo int
		if err := rows.Scan(&f.ID, &f.Nome, &f.PartitaIVA, &f.Email, &f.Telefono, &f.Note, &attivo); err != nil {
			return nil, err
		}
		f.Attivo = attivo != 0
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetFornitoreByID restituisce il fornitore o sql.ErrNoRows.
func (db *DB) GetFornitoreByID(id int64) (models.Fornitore, error) {
	var f models.Fornitore
	var attivo int
	err := db.conn.QueryRow(`
		SELECT id, nome, COALESCE(partita_iva,''), COALESCE(email,''),
		       COALESCE(telefono,''), COALESCE(note,''), attivo
		FROM fornitori WHERE id=?`, id).
		Scan(&f.ID, &f.Nome, &f.PartitaIVA, &f.Email, &f.Telefono, &f.Note, &attivo)
	f.Attivo = attivo != 0
	return f, err
}

// CreateFornitore inserisce un nuovo fornitore. Nome è UNIQUE: ritorna errore
// se collide. attivo=1 di default.
func (db *DB) CreateFornitore(f models.Fornitore) (int64, error) {
	res, err := db.conn.Exec(`
		INSERT INTO fornitori (nome, partita_iva, email, telefono, note, attivo)
		VALUES (?,?,?,?,?,1)`,
		f.Nome, nullableStr(f.PartitaIVA), nullableStr(f.Email),
		nullableStr(f.Telefono), nullableStr(f.Note),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (db *DB) UpdateFornitore(f models.Fornitore) error {
	_, err := db.conn.Exec(`
		UPDATE fornitori SET nome=?, partita_iva=?, email=?, telefono=?, note=?
		WHERE id=?`,
		f.Nome, nullableStr(f.PartitaIVA), nullableStr(f.Email),
		nullableStr(f.Telefono), nullableStr(f.Note), f.ID,
	)
	return err
}

// DeleteFornitore prova un hard delete; se il fornitore è referenziato da
// uno o più acquisti applica soft delete (attivo=0) per preservare lo storico.
func (db *DB) DeleteFornitore(id int64) error {
	var n int
	if err := db.conn.QueryRow(`SELECT COUNT(*) FROM acquisti WHERE fornitore_id=?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		_, err := db.conn.Exec(`UPDATE fornitori SET attivo=0 WHERE id=?`, id)
		return err
	}
	_, err := db.conn.Exec(`DELETE FROM fornitori WHERE id=?`, id)
	return err
}

// ── Acquisti (documento head + lotti righe) ─────────────────────────────────

// CreateAcquisto crea il documento head e le N righe lotti_acquisto in
// un'unica transazione. La data_acquisto del head viene denormalizzata su
// ogni riga lotti_acquisto per non rompere il FIFO esistente.
func (db *DB) CreateAcquisto(a models.Acquisto, righe []models.LottoAcquisto) (int64, error) {
	if len(righe) == 0 {
		return 0, fmt.Errorf("acquisto senza righe")
	}
	tx, err := db.conn.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck
	dataStr := a.DataAcquisto.UTC().Format(time.RFC3339)
	var fornID interface{}
	if a.FornitoreID != nil {
		fornID = *a.FornitoreID
	}
	res, err := tx.Exec(`
		INSERT INTO acquisti (data_acquisto, fornitore_id, numero_doc, note, created_by)
		VALUES (?,?,?,?,?)`,
		dataStr, fornID, nullableStr(a.NumeroDoc), nullableStr(a.Note), nullableStr(a.CreatedBy),
	)
	if err != nil {
		return 0, fmt.Errorf("insert acquisti: %w", err)
	}
	acqID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, r := range righe {
		if r.QuantitaIniziale <= 0 {
			return 0, fmt.Errorf("quantita non valida per prodotto %d", r.ProdottoID)
		}
		if _, err := tx.Exec(`
			INSERT INTO lotti_acquisto
			  (prodotto_id, data_acquisto, quantita_iniziale, quantita_rimanente, costo_unitario, acquisto_id)
			VALUES (?,?,?,?,?,?)`,
			r.ProdottoID, dataStr, r.QuantitaIniziale, r.QuantitaIniziale, r.CostoUnitario, acqID,
		); err != nil {
			return 0, fmt.Errorf("insert lotto prodotto %d: %w", r.ProdottoID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return acqID, nil
}

// GetAcquistiList restituisce gli ultimi `limit` acquisti (head + nome fornitore).
// Limit <= 0 → 100 default.
func (db *DB) GetAcquistiList(limit int) ([]models.Acquisto, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := db.conn.Query(`
		SELECT a.id, a.data_acquisto, a.fornitore_id, COALESCE(f.nome,''),
		       COALESCE(a.numero_doc,''), COALESCE(a.note,''),
		       COALESCE(a.created_by,''), a.created_at
		FROM acquisti a
		LEFT JOIN fornitori f ON f.id = a.fornitore_id
		ORDER BY a.data_acquisto DESC, a.id DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Acquisto
	for rows.Next() {
		var a models.Acquisto
		var fornID sql.NullInt64
		var daStr, caStr string
		if err := rows.Scan(&a.ID, &daStr, &fornID, &a.FornitoreNome,
			&a.NumeroDoc, &a.Note, &a.CreatedBy, &caStr); err != nil {
			return nil, err
		}
		if fornID.Valid {
			id := fornID.Int64
			a.FornitoreID = &id
		}
		a.DataAcquisto, _ = time.Parse(time.RFC3339, daStr)
		a.CreatedAt, _ = time.Parse(time.RFC3339, caStr)
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAcquistoConRighe carica un acquisto con le sue righe (lotti) + nome prodotto.
func (db *DB) GetAcquistoConRighe(id int64) (models.Acquisto, error) {
	var a models.Acquisto
	var fornID sql.NullInt64
	var daStr, caStr string
	err := db.conn.QueryRow(`
		SELECT a.id, a.data_acquisto, a.fornitore_id, COALESCE(f.nome,''),
		       COALESCE(a.numero_doc,''), COALESCE(a.note,''),
		       COALESCE(a.created_by,''), a.created_at
		FROM acquisti a
		LEFT JOIN fornitori f ON f.id = a.fornitore_id
		WHERE a.id=?`, id).
		Scan(&a.ID, &daStr, &fornID, &a.FornitoreNome, &a.NumeroDoc, &a.Note, &a.CreatedBy, &caStr)
	if err != nil {
		return a, err
	}
	if fornID.Valid {
		v := fornID.Int64
		a.FornitoreID = &v
	}
	a.DataAcquisto, _ = time.Parse(time.RFC3339, daStr)
	a.CreatedAt, _ = time.Parse(time.RFC3339, caStr)
	rows, err := db.conn.Query(`
		SELECT l.id, l.prodotto_id, l.data_acquisto, l.quantita_iniziale,
		       l.quantita_rimanente, l.costo_unitario,
		       p.nome, COALESCE(p.codice_articolo,'')
		FROM lotti_acquisto l
		JOIN prodotti p ON p.id = l.prodotto_id
		WHERE l.acquisto_id=?
		ORDER BY l.id`, id)
	if err != nil {
		return a, err
	}
	defer rows.Close()
	for rows.Next() {
		var lo models.LottoAcquisto
		var rDaStr string
		if err := rows.Scan(&lo.ID, &lo.ProdottoID, &rDaStr, &lo.QuantitaIniziale,
			&lo.QuantitaRimanente, &lo.CostoUnitario,
			&lo.ProdottoNome, &lo.ProdottoCodice); err != nil {
			return a, err
		}
		lo.DataAcquisto, _ = time.Parse(time.RFC3339, rDaStr)
		acqID := a.ID
		lo.AcquistoID = &acqID
		a.Righe = append(a.Righe, lo)
	}
	return a, rows.Err()
}

// ── Notifiche & Email outbox ────────────────────────────────────────────────

// GetUtente recupera username, email, ruolo, ruolo_secondario e settore di un utente.
func (db *DB) GetUtente(username string) (models.Utente, error) {
	var u models.Utente
	err := db.conn.QueryRow(`
		SELECT username, COALESCE(email,''), ruolo, COALESCE(ruolo_secondario,''), COALESCE(settore_id,'')
		FROM utenti WHERE username = ?
	`, username).Scan(&u.Username, &u.Email, &u.Ruolo, &u.RuoloSecondario, &u.SettoreID)
	return u, err
}

// GetUtentiByRuolo restituisce gli utenti con il ruolo indicato.
// Usato per broadcast notifiche ai magazzinieri.
func (db *DB) GetUtentiByRuolo(ruolo string) ([]models.Utente, error) {
	rows, err := db.conn.Query(`
		SELECT username, COALESCE(email,''), ruolo, COALESCE(settore_id,'')
		FROM utenti WHERE ruolo = ? ORDER BY username
	`, ruolo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Utente
	for rows.Next() {
		var u models.Utente
		if err := rows.Scan(&u.Username, &u.Email, &u.Ruolo, &u.SettoreID); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// GetFunzionarioSettore restituisce lo username del funzionario responsabile
// del settore, oppure "" se non assegnato.
func (db *DB) GetFunzionarioSettore(settoreID string) (string, error) {
	var s sql.NullString
	err := db.conn.QueryRow(`SELECT funzionario_username FROM settori WHERE id = ?`, settoreID).Scan(&s)
	if err != nil {
		return "", err
	}
	return s.String, nil
}

// GetOrdineMeta restituisce i metadati necessari per costruire una notifica
// (proprietario, settore) per un dato ordine.
func (db *DB) GetOrdineMeta(ordineID int64) (utenteUsername, settoreID, stato string, err error) {
	err = db.conn.QueryRow(`
		SELECT utente_username, settore_id, stato FROM ordini WHERE id = ?
	`, ordineID).Scan(&utenteUsername, &settoreID, &stato)
	return
}

// InsertNotifica crea una nuova riga in notifiche.
func (db *DB) InsertNotifica(n models.Notifica) (int64, error) {
	var ordineID, prodottoID interface{}
	if n.OrdineID != nil {
		ordineID = *n.OrdineID
	}
	if n.ProdottoID != nil {
		prodottoID = *n.ProdottoID
	}
	res, err := db.conn.Exec(`
		INSERT INTO notifiche (utente_username, tipo, messaggio, ordine_id, prodotto_id)
		VALUES (?, ?, ?, ?, ?)
	`, n.UtenteUsername, n.Tipo, n.Messaggio, ordineID, prodottoID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListNotifiche restituisce le notifiche dell'utente filtrate per tab.
// filtro: "" (Tutte) | "non_lette" | "ordini" | "scorte".
func (db *DB) ListNotifiche(username, filtro string, limit int) ([]models.Notifica, error) {
	if limit <= 0 {
		limit = 200
	}
	where := "utente_username = ?"
	args := []interface{}{username}
	switch filtro {
	case "non_lette":
		where += " AND letta = 0"
	case "ordini":
		where += " AND tipo LIKE 'ordine_%'"
	case "scorte":
		where += " AND tipo = 'scorta'"
	}
	rows, err := db.conn.Query(`
		SELECT id, utente_username, tipo, messaggio, ordine_id, prodotto_id, letta, creata_il
		FROM notifiche
		WHERE `+where+`
		ORDER BY creata_il DESC, id DESC
		LIMIT ?
	`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Notifica
	for rows.Next() {
		var n models.Notifica
		var ordineID, prodottoID sql.NullInt64
		var letta int
		if err := rows.Scan(&n.ID, &n.UtenteUsername, &n.Tipo, &n.Messaggio, &ordineID, &prodottoID, &letta, &n.CreataIl); err != nil {
			return nil, err
		}
		if ordineID.Valid {
			v := ordineID.Int64
			n.OrdineID = &v
		}
		if prodottoID.Valid {
			v := prodottoID.Int64
			n.ProdottoID = &v
		}
		n.Letta = letta != 0
		out = append(out, n)
	}
	return out, rows.Err()
}

// CountNotificheNonLette è usato dal badge in topbar.
func (db *DB) CountNotificheNonLette(username string) (int, error) {
	var n int
	err := db.conn.QueryRow(
		`SELECT COUNT(*) FROM notifiche WHERE utente_username = ? AND letta = 0`,
		username,
	).Scan(&n)
	return n, err
}

// MarcaNotificaLetta segna come letta una singola notifica, vincolata
// all'utente proprietario per evitare cross-account writes.
func (db *DB) MarcaNotificaLetta(id int64, username string) error {
	_, err := db.conn.Exec(
		`UPDATE notifiche SET letta = 1 WHERE id = ? AND utente_username = ?`,
		id, username,
	)
	return err
}

// MarcaTutteLette segna come lette tutte le notifiche di un utente.
func (db *DB) MarcaTutteLette(username string) error {
	_, err := db.conn.Exec(
		`UPDATE notifiche SET letta = 1 WHERE utente_username = ? AND letta = 0`,
		username,
	)
	return err
}

// EnqueueEmail aggiunge un job di invio email alla outbox.
func (db *DB) EnqueueEmail(out models.EmailOutbox) (int64, error) {
	var notificaID interface{}
	if out.NotificaID != nil {
		notificaID = *out.NotificaID
	}
	res, err := db.conn.Exec(`
		INSERT INTO email_outbox (destinatario, soggetto, corpo_html, tipo, notifica_id)
		VALUES (?, ?, ?, ?, ?)
	`, out.Destinatario, out.Soggetto, out.CorpoHTML, out.Tipo, notificaID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// LeasePendingEmails restituisce fino a `limit` job pronti per essere inviati.
// Non blocca le righe (SQLite con WAL gestisce serializzazione delle scritture).
func (db *DB) LeasePendingEmails(now time.Time, limit int) ([]models.EmailOutbox, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := db.conn.Query(`
		SELECT id, destinatario, soggetto, corpo_html, tipo,
		       notifica_id, stato, tentativi, COALESCE(ultimo_errore,''),
		       prossimo_tentativo, inviata_il, creata_il
		FROM email_outbox
		WHERE stato = 'in_attesa' AND prossimo_tentativo <= ?
		ORDER BY prossimo_tentativo ASC
		LIMIT ?
	`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.EmailOutbox
	for rows.Next() {
		var e models.EmailOutbox
		var notificaID sql.NullInt64
		var inviataIl sql.NullTime
		if err := rows.Scan(&e.ID, &e.Destinatario, &e.Soggetto, &e.CorpoHTML, &e.Tipo,
			&notificaID, &e.Stato, &e.Tentativi, &e.UltimoErrore,
			&e.ProssimoTentativo, &inviataIl, &e.CreataIl); err != nil {
			return nil, err
		}
		if notificaID.Valid {
			v := notificaID.Int64
			e.NotificaID = &v
		}
		if inviataIl.Valid {
			t := inviataIl.Time
			e.InviataIl = &t
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkEmailSent segna un job come inviato con successo.
func (db *DB) MarkEmailSent(id int64, sentAt time.Time) error {
	_, err := db.conn.Exec(
		`UPDATE email_outbox SET stato='inviata', inviata_il=?, ultimo_errore='' WHERE id = ?`,
		sentAt, id,
	)
	return err
}

// MarkEmailFailed aggiorna il job dopo un tentativo fallito. Se `abbandona`
// è true imposta stato='abbandonata', altrimenti rimette il job in attesa
// con prossimo_tentativo aggiornato.
func (db *DB) MarkEmailFailed(id int64, attempts int, errMsg string, nextAttempt time.Time, abbandona bool) error {
	stato := "in_attesa"
	if abbandona {
		stato = "abbandonata"
	}
	_, err := db.conn.Exec(`
		UPDATE email_outbox
		SET stato=?, tentativi=?, ultimo_errore=?, prossimo_tentativo=?
		WHERE id = ?
	`, stato, attempts, errMsg, nextAttempt, id)
	return err
}

// ── Reportistica magazziniere ────────────────────────────────────────────────

// GetSpesaAnno restituisce la spesa totale (somma costo_totale dei
// movimenti_magazzino) per l'anno indicato.
func (db *DB) GetSpesaAnno(anno int) (float64, error) {
	var v float64
	err := db.conn.QueryRow(`
		SELECT COALESCE(SUM(costo_totale), 0)
		FROM movimenti_magazzino
		WHERE strftime('%Y', data_movimento) = ?
	`, strconv.Itoa(anno)).Scan(&v)
	return v, err
}

// GetOrdiniEvasiAnno conta gli ordini chiusi (stato='ritirato') creati
// nell'anno indicato.
func (db *DB) GetOrdiniEvasiAnno(anno int) (int, error) {
	var n int
	err := db.conn.QueryRow(`
		SELECT COUNT(*) FROM ordini
		WHERE stato = 'ritirato' AND strftime('%Y', data_creazione) = ?
	`, strconv.Itoa(anno)).Scan(&n)
	return n, err
}

// GetTempoMedioEvasioneAnno calcola il tempo medio (in giorni) intercorso
// tra la creazione dell'ordine e lo scarico dei suoi movimenti, per gli
// ordini conclusi nello stato 'ritirato' dell'anno indicato.
func (db *DB) GetTempoMedioEvasioneAnno(anno int) (float64, error) {
	var v sql.NullFloat64
	err := db.conn.QueryRow(`
		SELECT AVG(julianday(m.data_movimento) - julianday(o.data_creazione))
		FROM movimenti_magazzino m
		JOIN righe_ordine r ON r.id = m.riga_ordine_id
		JOIN ordini o ON o.id = r.ordine_id
		WHERE o.stato = 'ritirato'
		  AND strftime('%Y', o.data_creazione) = ?
	`, strconv.Itoa(anno)).Scan(&v)
	if err != nil {
		return 0, err
	}
	if !v.Valid {
		return 0, nil
	}
	return v.Float64, nil
}

// GetSettoriAttiviAnno conta i settori distinti che hanno avuto almeno un
// movimento di magazzino nell'anno indicato.
func (db *DB) GetSettoriAttiviAnno(anno int) (int, error) {
	var n int
	err := db.conn.QueryRow(`
		SELECT COUNT(DISTINCT o.settore_id)
		FROM movimenti_magazzino m
		JOIN righe_ordine r ON r.id = m.riga_ordine_id
		JOIN ordini o ON o.id = r.ordine_id
		WHERE strftime('%Y', m.data_movimento) = ?
	`, strconv.Itoa(anno)).Scan(&n)
	return n, err
}

// GetSpesaMensile aggrega la spesa per mese (1..12) nell'anno indicato.
// Restituisce sempre 12 entries (i mesi senza movimenti hanno Spesa=0).
func (db *DB) GetSpesaMensile(anno int) ([]models.SpesaMese, error) {
	rows, err := db.conn.Query(`
		SELECT CAST(strftime('%m', data_movimento) AS INTEGER) AS mese,
		       COALESCE(SUM(costo_totale), 0) AS spesa
		FROM movimenti_magazzino
		WHERE strftime('%Y', data_movimento) = ?
		GROUP BY mese
	`, strconv.Itoa(anno))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byMese := map[int]float64{}
	for rows.Next() {
		var m int
		var s float64
		if err := rows.Scan(&m, &s); err != nil {
			return nil, err
		}
		byMese[m] = s
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]models.SpesaMese, 12)
	for i := 1; i <= 12; i++ {
		out[i-1] = models.SpesaMese{Mese: i, Spesa: byMese[i]}
	}
	return out, nil
}

// GetSpesaPerSettore aggrega la spesa per settore nell'anno indicato,
// ordinata in modo decrescente per importo.
func (db *DB) GetSpesaPerSettore(anno int) ([]models.SpesaSettore, error) {
	rows, err := db.conn.Query(`
		SELECT o.settore_id,
		       COALESCE(s.nome, o.settore_id) AS settore_nome,
		       SUM(m.costo_totale) AS spesa
		FROM movimenti_magazzino m
		JOIN righe_ordine r ON r.id = m.riga_ordine_id
		JOIN ordini o ON o.id = r.ordine_id
		LEFT JOIN settori s ON s.id = o.settore_id
		WHERE strftime('%Y', m.data_movimento) = ?
		GROUP BY o.settore_id, s.nome
		ORDER BY spesa DESC
	`, strconv.Itoa(anno))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.SpesaSettore
	for rows.Next() {
		var s models.SpesaSettore
		if err := rows.Scan(&s.SettoreID, &s.SettoreNome, &s.Spesa); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// StreamMovimentiCSV scrive su w il CSV dei movimenti dell'anno indicato.
// Formato compatibile con Excel italiano: BOM UTF-8, separatore ';', decimali
// con virgola, date 'gg/mm/aaaa'. Stream riga per riga senza caricare tutto
// in memoria.
func (db *DB) StreamMovimentiCSV(w io.Writer, anno int) error {
	// BOM UTF-8 (necessario perché Excel italiano riconosca la codifica).
	if _, err := w.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "id;data;ordine_id;settore;prodotto_codice;prodotto;quantita;costo_totale;lotto_id\r\n"); err != nil {
		return err
	}
	rows, err := db.conn.Query(`
		SELECT m.id, m.data_movimento, o.id, COALESCE(s.nome, o.settore_id),
		       COALESCE(p.codice_articolo, ''), p.nome,
		       m.quantita_prelevata, m.costo_totale, m.lotto_id
		FROM movimenti_magazzino m
		LEFT JOIN righe_ordine r ON r.id = m.riga_ordine_id
		LEFT JOIN ordini o ON o.id = r.ordine_id
		LEFT JOIN settori s ON s.id = o.settore_id
		LEFT JOIN prodotti p ON p.id = r.prodotto_id
		WHERE strftime('%Y', m.data_movimento) = ?
		ORDER BY m.data_movimento, m.id
	`, strconv.Itoa(anno))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id        int64
			dataMov   time.Time
			ordineID  sql.NullInt64
			settore   sql.NullString
			prodCod   string
			prodNome  string
			qta       int
			costo     float64
			lottoID   int64
		)
		if err := rows.Scan(&id, &dataMov, &ordineID, &settore, &prodCod, &prodNome, &qta, &costo, &lottoID); err != nil {
			return err
		}
		ord := ""
		if ordineID.Valid {
			ord = strconv.FormatInt(ordineID.Int64, 10)
		}
		set := ""
		if settore.Valid {
			set = settore.String
		}
		costoStr := strings.Replace(strconv.FormatFloat(costo, 'f', 2, 64), ".", ",", 1)
		if _, err := fmt.Fprintf(w, "%d;%s;%s;%s;%s;%s;%d;%s;%d\r\n",
			id,
			dataMov.Format("02/01/2006"),
			ord,
			csvEscape(set),
			csvEscape(prodCod),
			csvEscape(prodNome),
			qta,
			costoStr,
			lottoID,
		); err != nil {
			return err
		}
	}
	return rows.Err()
}

// csvEscape mette il campo tra virgolette se contiene caratteri speciali
// (";", virgolette, newline) e raddoppia le virgolette interne.
func csvEscape(s string) string {
	if !strings.ContainsAny(s, ";\"\r\n") {
		return s
	}
	return "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\""
}

// ── helpers ──────────────────────────────────────────────────────────────────

func nullableStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func nullableBlob(b []byte) interface{} {
	if len(b) == 0 {
		return nil
	}
	return b
}

// nullableInt64 converte un puntatore int64 in un valore adatto a INSERT/UPDATE.
func nullableInt64(p *int64) interface{} {
	if p == nil {
		return nil
	}
	return *p
}

// nullableFloat64 converte un puntatore float64 in un valore adatto a INSERT/UPDATE.
func nullableFloat64(p *float64) interface{} {
	if p == nil {
		return nil
	}
	return *p
}

// nullableTime converte un puntatore time.Time in un valore adatto a INSERT/UPDATE.
func nullableTime(p *time.Time) interface{} {
	if p == nil {
		return nil
	}
	return *p
}

func nullableStrPtr(p *string) interface{} {
	if p == nil {
		return nil
	}
	return *p
}

func ptrInt64(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

func ptrFloat64(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	v := n.Float64
	return &v
}

func ptrString(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	v := n.String
	return &v
}

func ptrTime(n sql.NullTime) *time.Time {
	if !n.Valid {
		return nil
	}
	v := n.Time
	return &v
}

// ── Cassa Economale (Fondo Economale) ──────────────────────────────────────
// Capitoli di spesa (PEG) e workflow Spese Economali.
// I saldi (impegnato/speso/residuo) sono SEMPRE calcolati on-demand:
// vedi PLANE.md §8.7 e CLAUDE.md "Capienza Capitolo".

// CreaCapitolo inserisce un nuovo capitolo. UNIQUE(anno, codice_peg) gestita dal DB.
func (db *DB) CreaCapitolo(c models.CapitoloSpesa) (int64, error) {
	res, err := db.conn.Exec(`
		INSERT INTO capitoli_spesa (anno, codice_peg, descrizione, importo_stanziato, attivo)
		VALUES (?, ?, ?, ?, ?)`,
		c.Anno, strings.TrimSpace(c.CodicePEG), strings.TrimSpace(c.Descrizione),
		c.ImportoStanziato, boolToInt(c.Attivo),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// AggiornaCapitolo modifica i campi editabili di un capitolo esistente.
// Anno e codice_peg restano fissi (cambiarli equivarrebbe a creare un capitolo
// nuovo, con conseguenze sui saldi storici).
func (db *DB) AggiornaCapitolo(c models.CapitoloSpesa) error {
	_, err := db.conn.Exec(`
		UPDATE capitoli_spesa
		SET descrizione = ?, importo_stanziato = ?, attivo = ?
		WHERE id = ?`,
		strings.TrimSpace(c.Descrizione), c.ImportoStanziato, boolToInt(c.Attivo), c.ID,
	)
	return err
}

// DisattivaCapitolo marca un capitolo come non più utilizzabile per nuove spese.
// Le spese già impegnate restano intatte.
func (db *DB) DisattivaCapitolo(id int64) error {
	_, err := db.conn.Exec(`UPDATE capitoli_spesa SET attivo = 0 WHERE id = ?`, id)
	return err
}

// GetCapitoloByID restituisce un capitolo singolo (errore se non trovato).
func (db *DB) GetCapitoloByID(id int64) (models.CapitoloSpesa, error) {
	var c models.CapitoloSpesa
	var attivo int
	err := db.conn.QueryRow(`
		SELECT id, anno, codice_peg, descrizione, importo_stanziato, attivo, creato_il
		FROM capitoli_spesa WHERE id = ?`, id,
	).Scan(&c.ID, &c.Anno, &c.CodicePEG, &c.Descrizione, &c.ImportoStanziato, &attivo, &c.CreatoIl)
	if err != nil {
		return c, err
	}
	c.Attivo = (attivo != 0)
	return c, nil
}

// GetCapitoliConSaldi restituisce tutti i capitoli dell'anno richiesto con
// impegnato/speso/residuo calcolati on-demand. Niente materializzazione.
//
//	impegnato = SUM(importo_presunto WHERE stato IN ('impegnata','rendicontata'))
//	speso     = SUM(importo_effettivo WHERE stato='chiusa')
//	residuo   = importo_stanziato − impegnato − speso
func (db *DB) GetCapitoliConSaldi(anno int) ([]models.CapitoloConSaldi, error) {
	rows, err := db.conn.Query(`
		SELECT c.id, c.anno, c.codice_peg, c.descrizione, c.importo_stanziato, c.attivo, c.creato_il,
		       COALESCE((SELECT SUM(s.importo_presunto) FROM spese_economali s
		                 WHERE s.capitolo_id = c.id AND s.stato IN ('impegnata','rendicontata')), 0) AS impegnato,
		       COALESCE((SELECT SUM(s.importo_effettivo) FROM spese_economali s
		                 WHERE s.capitolo_id = c.id AND s.stato = 'chiusa'), 0) AS speso
		FROM capitoli_spesa c
		WHERE c.anno = ?
		ORDER BY c.codice_peg ASC`, anno)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.CapitoloConSaldi
	for rows.Next() {
		var r models.CapitoloConSaldi
		var attivo int
		if err := rows.Scan(&r.ID, &r.Anno, &r.CodicePEG, &r.Descrizione,
			&r.ImportoStanziato, &attivo, &r.CreatoIl,
			&r.Impegnato, &r.Speso); err != nil {
			return nil, err
		}
		r.Attivo = (attivo != 0)
		r.Residuo = r.ImportoStanziato - r.Impegnato - r.Speso
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetCapitoliAttivi è una scorciatoia per le UI di scelta (dropdown nei form spesa).
func (db *DB) GetCapitoliAttivi(anno int) ([]models.CapitoloSpesa, error) {
	rows, err := db.conn.Query(`
		SELECT id, anno, codice_peg, descrizione, importo_stanziato, attivo, creato_il
		FROM capitoli_spesa
		WHERE anno = ? AND attivo = 1
		ORDER BY codice_peg ASC`, anno)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.CapitoloSpesa
	for rows.Next() {
		var c models.CapitoloSpesa
		var attivo int
		if err := rows.Scan(&c.ID, &c.Anno, &c.CodicePEG, &c.Descrizione,
			&c.ImportoStanziato, &attivo, &c.CreatoIl); err != nil {
			return nil, err
		}
		c.Attivo = (attivo != 0)
		out = append(out, c)
	}
	return out, rows.Err()
}

// CreaSpesa inserisce una nuova spesa in stato 'in_approvazione'.
// Tutti i campi di workflow successivi restano NULL: vengono valorizzati dalle
// transizioni `Autorizza/Impegna/Rendiconta/Chiudi` (slice futuri).
func (db *DB) CreaSpesa(s models.SpesaEconomale) (int64, error) {
	res, err := db.conn.Exec(`
		INSERT INTO spese_economali
		    (utente_username, settore_id, motivazione, importo_presunto,
		     tipo_pagamento, stato, note_funzionario, note_economo)
		VALUES (?, ?, ?, ?, ?, 'in_approvazione', '', '')`,
		s.UtenteUsername, s.SettoreID,
		strings.TrimSpace(s.Motivazione), s.ImportoPresunto,
		s.TipoPagamento,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// scanSpesaRow legge una riga dal risultato di una SELECT su spese_economali
// (con LEFT JOIN su utenti/settori/capitoli_spesa per i campi joinati).
// L'ordine delle colonne è fissato dalla SELECT comune in spesaSelectSQL.
func scanSpesaRow(rs interface {
	Scan(dest ...interface{}) error
}) (models.SpesaEconomale, error) {
	var s models.SpesaEconomale
	var (
		capitoloID                     sql.NullInt64
		importoEffettivo               sql.NullFloat64
		fornitore, estremiDocumento    sql.NullString
		funzionarioUsername, economoUs sql.NullString
		dataDocumento                  sql.NullTime
		dataAut, dataImp, dataRen, dataChi sql.NullTime
		utenteEmail                    sql.NullString
		settoreNome                    sql.NullString
		capitoloPEG, capitoloDescr     sql.NullString
	)
	err := rs.Scan(
		&s.ID, &s.UtenteUsername, &s.SettoreID, &capitoloID,
		&s.Motivazione, &s.ImportoPresunto, &importoEffettivo,
		&s.TipoPagamento, &s.Stato,
		&fornitore, &dataDocumento, &estremiDocumento,
		&s.NoteFunzionario, &s.NoteEconomo,
		&funzionarioUsername, &economoUs,
		&s.DataCreazione, &dataAut, &dataImp, &dataRen, &dataChi,
		&utenteEmail, &settoreNome, &capitoloPEG, &capitoloDescr,
	)
	if err != nil {
		return s, err
	}
	s.CapitoloID = ptrInt64(capitoloID)
	s.ImportoEffettivo = ptrFloat64(importoEffettivo)
	s.Fornitore = ptrString(fornitore)
	s.DataDocumento = ptrTime(dataDocumento)
	s.EstremiDocumento = ptrString(estremiDocumento)
	s.FunzionarioUsername = ptrString(funzionarioUsername)
	s.EconomoUsername = ptrString(economoUs)
	s.DataAutorizzazione = ptrTime(dataAut)
	s.DataImpegno = ptrTime(dataImp)
	s.DataRendicontazione = ptrTime(dataRen)
	s.DataChiusura = ptrTime(dataChi)
	if utenteEmail.Valid {
		s.UtenteEmail = utenteEmail.String
	}
	if settoreNome.Valid {
		s.SettoreNome = settoreNome.String
	}
	if capitoloPEG.Valid {
		s.CapitoloPEG = capitoloPEG.String
	}
	if capitoloDescr.Valid {
		s.CapitoloDescr = capitoloDescr.String
	}
	return s, nil
}

const spesaSelectSQL = `
	SELECT s.id, s.utente_username, s.settore_id, s.capitolo_id,
	       s.motivazione, s.importo_presunto, s.importo_effettivo,
	       s.tipo_pagamento, s.stato,
	       s.fornitore, s.data_documento, s.estremi_documento,
	       s.note_funzionario, s.note_economo,
	       s.funzionario_username, s.economo_username,
	       s.data_creazione, s.data_autorizzazione, s.data_impegno,
	       s.data_rendicontazione, s.data_chiusura,
	       u.email, st.nome, c.codice_peg, c.descrizione
	FROM spese_economali s
	LEFT JOIN utenti u         ON u.username = s.utente_username
	LEFT JOIN settori st       ON st.id = s.settore_id
	LEFT JOIN capitoli_spesa c ON c.id = s.capitolo_id`

// GetSpesaByID restituisce una spesa singola con i campi joinati popolati.
func (db *DB) GetSpesaByID(id int64) (models.SpesaEconomale, error) {
	row := db.conn.QueryRow(spesaSelectSQL+` WHERE s.id = ?`, id)
	return scanSpesaRow(row)
}

func (db *DB) querySpese(where string, args ...interface{}) ([]models.SpesaEconomale, error) {
	q := spesaSelectSQL
	if where != "" {
		q += " WHERE " + where
	}
	q += " ORDER BY s.data_creazione DESC, s.id DESC"
	rows, err := db.conn.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.SpesaEconomale
	for rows.Next() {
		s, err := scanSpesaRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetSpeseUtente restituisce le spese create da un utente specifico.
func (db *DB) GetSpeseUtente(username string) ([]models.SpesaEconomale, error) {
	return db.querySpese("s.utente_username = ?", username)
}

// GetSpeseSettore restituisce tutte le spese di un settore (vista funzionario).
func (db *DB) GetSpeseSettore(settoreID string) ([]models.SpesaEconomale, error) {
	return db.querySpese("s.settore_id = ?", settoreID)
}

// GetSpeseAll restituisce tutte le spese (vista economo/admin).
func (db *DB) GetSpeseAll() ([]models.SpesaEconomale, error) {
	return db.querySpese("")
}

// GetSpeseConStato restituisce le spese filtrate per stato (utility per KPI).
func (db *DB) GetSpeseConStato(stato string) ([]models.SpesaEconomale, error) {
	return db.querySpese("s.stato = ?", stato)
}

// AutorizzaSpesa transita da 'in_approvazione' a 'autorizzata'.
func (db *DB) AutorizzaSpesa(id int64, funzionarioUsername string) error {
	res, err := db.conn.Exec(`
		UPDATE spese_economali
		SET stato = 'autorizzata',
		    funzionario_username = ?,
		    data_autorizzazione = datetime('now')
		WHERE id = ? AND stato = 'in_approvazione'`,
		funzionarioUsername, id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("spesa %d non trovata o non in stato 'in_approvazione'", id)
	}
	return nil
}

// RifiutaSpesaFunz transita da 'in_approvazione' a 'rifiutata_funz'. Note obbligatorie.
func (db *DB) RifiutaSpesaFunz(id int64, funzionarioUsername, note string) error {
	note = strings.TrimSpace(note)
	if note == "" {
		return fmt.Errorf("note obbligatorie per il rifiuto")
	}
	res, err := db.conn.Exec(`
		UPDATE spese_economali
		SET stato = 'rifiutata_funz',
		    funzionario_username = ?,
		    note_funzionario = ?,
		    data_autorizzazione = datetime('now')
		WHERE id = ? AND stato = 'in_approvazione'`,
		funzionarioUsername, note, id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("spesa %d non trovata o non in stato 'in_approvazione'", id)
	}
	return nil
}

// ImpegnaSpesa assegna capitolo e transita da 'autorizzata' a 'impegnata'.
// Verifica capienza dentro la stessa transazione (CLAUDE.md "Capienza Capitolo").
func (db *DB) ImpegnaSpesa(id int64, capitoloID int64, economoUsername string) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var importoPresunto float64
	err = tx.QueryRow(
		`SELECT importo_presunto FROM spese_economali WHERE id = ? AND stato = 'autorizzata'`, id,
	).Scan(&importoPresunto)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("spesa %d non trovata o non in stato 'autorizzata'", id)
		}
		return err
	}
	var stanziato, impegnato, speso float64
	err = tx.QueryRow(`
		SELECT c.importo_stanziato,
		       COALESCE((SELECT SUM(s.importo_presunto) FROM spese_economali s
		                 WHERE s.capitolo_id = c.id AND s.stato IN ('impegnata','rendicontata')), 0),
		       COALESCE((SELECT SUM(s.importo_effettivo) FROM spese_economali s
		                 WHERE s.capitolo_id = c.id AND s.stato = 'chiusa'), 0)
		FROM capitoli_spesa c WHERE c.id = ? AND c.attivo = 1`, capitoloID,
	).Scan(&stanziato, &impegnato, &speso)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("capitolo %d non trovato o non attivo", capitoloID)
		}
		return err
	}
	residuo := stanziato - impegnato - speso
	if importoPresunto > residuo {
		return fmt.Errorf("capienza insufficiente: residuo %.2f € < importo presunto %.2f €", residuo, importoPresunto)
	}
	if _, err = tx.Exec(`
		UPDATE spese_economali
		SET stato = 'impegnata',
		    capitolo_id = ?,
		    economo_username = ?,
		    data_impegno = datetime('now')
		WHERE id = ? AND stato = 'autorizzata'`,
		capitoloID, economoUsername, id,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// RifiutaSpesaEcon transita da 'autorizzata' a 'rifiutata_econ'. Note obbligatorie.
func (db *DB) RifiutaSpesaEcon(id int64, economoUsername, note string) error {
	note = strings.TrimSpace(note)
	if note == "" {
		return fmt.Errorf("note obbligatorie per il rifiuto")
	}
	res, err := db.conn.Exec(`
		UPDATE spese_economali
		SET stato = 'rifiutata_econ',
		    economo_username = ?,
		    note_economo = ?
		WHERE id = ? AND stato = 'autorizzata'`,
		economoUsername, note, id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("spesa %d non trovata o non in stato 'autorizzata'", id)
	}
	return nil
}

// RendicontaSpesa registra i dati fiscali e transita da 'impegnata' a 'rendicontata'.
// fornitore, estremiDoc e dataDoc sono obbligatori: vincolo normativo (Corte dei Conti).
func (db *DB) RendicontaSpesa(id int64, fornitore, estremiDoc string, dataDoc time.Time, importoEff float64) error {
	if fornitore == "" || estremiDoc == "" || dataDoc.IsZero() {
		return fmt.Errorf("fornitore, data_documento ed estremi_documento sono obbligatori")
	}
	if importoEff <= 0 {
		return fmt.Errorf("importo effettivo deve essere maggiore di zero")
	}
	res, err := db.conn.Exec(`
		UPDATE spese_economali
		SET stato = 'rendicontata',
		    fornitore = ?,
		    data_documento = ?,
		    estremi_documento = ?,
		    importo_effettivo = ?,
		    data_rendicontazione = datetime('now')
		WHERE id = ? AND stato = 'impegnata'`,
		fornitore, dataDoc, estremiDoc, importoEff, id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("spesa %d non trovata o non in stato 'impegnata'", id)
	}
	return nil
}

// ChiudiSpesa transita da 'rendicontata' a 'chiusa' e inserisce atomicamente
// un movimento_cassa di tipo 'uscita' (CLAUDE.md "Saldo cassa").
func (db *DB) ChiudiSpesa(id int64, economoUsername string) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var importoEff sql.NullFloat64
	var fornitore, estremiDoc sql.NullString
	var dataDoc sql.NullTime
	err = tx.QueryRow(`
		SELECT importo_effettivo, fornitore, data_documento, estremi_documento
		FROM spese_economali WHERE id = ? AND stato = 'rendicontata'`, id,
	).Scan(&importoEff, &fornitore, &dataDoc, &estremiDoc)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("spesa %d non trovata o non in stato 'rendicontata'", id)
		}
		return err
	}
	if !importoEff.Valid || importoEff.Float64 <= 0 ||
		!fornitore.Valid || fornitore.String == "" ||
		!dataDoc.Valid ||
		!estremiDoc.Valid || estremiDoc.String == "" {
		return fmt.Errorf("campi fiscali obbligatori mancanti: impossibile chiudere")
	}
	if _, err = tx.Exec(`
		UPDATE spese_economali
		SET stato = 'chiusa',
		    economo_username = ?,
		    data_chiusura = datetime('now')
		WHERE id = ? AND stato = 'rendicontata'`,
		economoUsername, id,
	); err != nil {
		return err
	}
	if _, err = tx.Exec(`
		INSERT INTO movimenti_cassa (data, tipo, descrizione, importo, riferimento_spesa_id, creato_da)
		VALUES (datetime('now'), 'uscita', ?, ?, ?, ?)`,
		fmt.Sprintf("Spesa economale #%d — %s", id, fornitore.String),
		importoEff.Float64, id, economoUsername,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// CreaAllegato salva una pezza d'appoggio come BLOB in allegati_spesa.
func (db *DB) CreaAllegato(a models.AllegatoSpesa) (int64, error) {
	res, err := db.conn.Exec(`
		INSERT INTO allegati_spesa (spesa_id, filename, mime_type, dimensione, blob_data, caricato_da)
		VALUES (?, ?, ?, ?, ?, ?)`,
		a.SpesaID, a.Filename, a.MimeType, a.Dimensione, nullableBlob(a.BlobData), a.CaricatoDa,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetAllegatiBySpesa restituisce la lista allegati di una spesa SENZA blob_data
// per evitare di caricare tutti i BLOB in memoria.
func (db *DB) GetAllegatiBySpesa(spesaID int64) ([]models.AllegatoSpesa, error) {
	rows, err := db.conn.Query(`
		SELECT id, spesa_id, filename, mime_type, dimensione, caricato_da, caricato_il
		FROM allegati_spesa WHERE spesa_id = ? ORDER BY caricato_il ASC`, spesaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.AllegatoSpesa
	for rows.Next() {
		var a models.AllegatoSpesa
		if err := rows.Scan(&a.ID, &a.SpesaID, &a.Filename, &a.MimeType,
			&a.Dimensione, &a.CaricatoDa, &a.CaricatoIl); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAllegatoBlob restituisce blob_data + mime_type + filename.
// Verifica spesa_id per evitare accesso cross-spesa.
func (db *DB) GetAllegatoBlob(allegatoID, spesaID int64) (blob []byte, mimeType, filename string, err error) {
	err = db.conn.QueryRow(`
		SELECT blob_data, mime_type, filename
		FROM allegati_spesa WHERE id = ? AND spesa_id = ?`, allegatoID, spesaID,
	).Scan(&blob, &mimeType, &filename)
	return
}

// EliminaAllegato cancella un allegato. La policy (uploader/economo/admin) è nel handler.
func (db *DB) EliminaAllegato(allegatoID int64) error {
	_, err := db.conn.Exec(`DELETE FROM allegati_spesa WHERE id = ?`, allegatoID)
	return err
}

// GetSaldoCassa calcola il saldo del fondo per l'anno dato on-demand:
// SUM(anticipazioni + reintegri) − SUM(uscite + restituzioni). Mai materializzato.
func (db *DB) GetSaldoCassa(anno int) (float64, error) {
	var entrate, uscite float64
	err := db.conn.QueryRow(`
		SELECT
		    COALESCE(SUM(CASE WHEN tipo IN ('anticipazione','reintegro') THEN importo ELSE 0 END), 0),
		    COALESCE(SUM(CASE WHEN tipo IN ('uscita','restituzione_tesoreria') THEN importo ELSE 0 END), 0)
		FROM movimenti_cassa
		WHERE strftime('%Y', data) = ?`, fmt.Sprintf("%d", anno),
	).Scan(&entrate, &uscite)
	if err != nil {
		return 0, err
	}
	return entrate - uscite, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
