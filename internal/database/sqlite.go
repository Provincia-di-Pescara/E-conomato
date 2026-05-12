package database

import (
	"database/sql"
	"fmt"
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

		-- Lotti di acquisto (base per algoritmo FIFO)
		CREATE TABLE IF NOT EXISTS lotti_acquisto (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			prodotto_id         INTEGER NOT NULL,
			data_acquisto       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			quantita_iniziale   INTEGER NOT NULL,
			quantita_rimanente  INTEGER NOT NULL,
			costo_unitario      REAL NOT NULL,
			FOREIGN KEY(prodotto_id) REFERENCES prodotti(id)
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
	`)
	if err != nil {
		return err
	}
	_, err = conn.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_righe_ordine_uniq ON righe_ordine(ordine_id, prodotto_id)`)
	return err
}

// ── Utenti ───────────────────────────────────────────────────────────────────

// UpsertUtente creates a new user record or updates email and role on conflict.
func (db *DB) UpsertUtente(username, email, ruolo string) error {
	_, err := db.conn.Exec(
		`INSERT INTO utenti (username, email, ruolo)
		 VALUES (?, ?, ?)
		 ON CONFLICT(username) DO UPDATE SET email=excluded.email, ruolo=excluded.ruolo`,
		username, email, ruolo,
	)
	return err
}

// GetAllUtenti returns all users.
func (db *DB) GetAllUtenti() ([]models.Utente, error) {
	rows, err := db.conn.Query(`SELECT username, COALESCE(email,''), ruolo, COALESCE(settore_id,'') FROM utenti ORDER BY username`)
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
	rows, err := db.conn.Query(`SELECT id, nome FROM categorie ORDER BY nome`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Categoria
	for rows.Next() {
		var c models.Categoria
		if err := rows.Scan(&c.ID, &c.Nome); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (db *DB) CreateCategoria(nome string) (models.Categoria, error) {
	res, err := db.conn.Exec(`INSERT INTO categorie (nome) VALUES (?)`, nome)
	if err != nil {
		return models.Categoria{}, err
	}
	id, _ := res.LastInsertId()
	return models.Categoria{ID: id, Nome: nome}, nil
}

func (db *DB) UpdateCategoria(id int64, nome string) error {
	_, err := db.conn.Exec(`UPDATE categorie SET nome=? WHERE id=?`, nome, id)
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
		       COALESCE(p.categoria_id,0), COALESCE(c.nome,''), p.scorta_minima,
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
			&p.CategoriaID, &p.CategoriaNome, &p.ScortaMinima, &p.Disponibile); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ── Prodotti ─────────────────────────────────────────────────────────────────

// ProdottoRow extends Prodotto with the category name for list views (no BLOB).
type ProdottoRow struct {
	models.Prodotto
	CategoriaName    string
	ScortaRimanente  int
}

func (db *DB) GetAllProdotti() ([]ProdottoRow, error) {
	rows, err := db.conn.Query(`
		SELECT p.id, COALESCE(p.codice_articolo,''), p.nome, COALESCE(p.descrizione,''),
		       COALESCE(p.categoria_id,0), p.scorta_minima,
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
			&r.CategoriaID, &r.ScortaMinima, &r.CategoriaName, &r.ScortaRimanente); err != nil {
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
		       COALESCE(categoria_id,0), scorta_minima
		FROM prodotti WHERE id=?`, id).
		Scan(&p.ID, &p.CodiceArticolo, &p.Nome, &p.Descrizione, &p.CategoriaID, &p.ScortaMinima)
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
		INSERT INTO prodotti (codice_articolo, nome, descrizione, categoria_id, scorta_minima, immagine_blob)
		VALUES (?,?,?,?,?,?)`,
		nullableStr(p.CodiceArticolo), p.Nome, nullableStr(p.Descrizione), catID, p.ScortaMinima, nullableBlob(p.ImmagineBLOB),
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
			UPDATE prodotti SET codice_articolo=?, nome=?, descrizione=?, categoria_id=?, scorta_minima=?, immagine_blob=?
			WHERE id=?`,
			nullableStr(p.CodiceArticolo), p.Nome, nullableStr(p.Descrizione), catID, p.ScortaMinima, p.ImmagineBLOB, p.ID,
		)
		return err
	}
	_, err := db.conn.Exec(`
		UPDATE prodotti SET codice_articolo=?, nome=?, descrizione=?, categoria_id=?, scorta_minima=?
		WHERE id=?`,
		nullableStr(p.CodiceArticolo), p.Nome, nullableStr(p.Descrizione), catID, p.ScortaMinima, p.ID,
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
		       COALESCE(p.categoria_id,0), p.scorta_minima,
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
			&r.CategoriaID, &r.ScortaMinima, &r.CategoriaName, &r.ScortaRimanente); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (db *DB) getRigheConProdotto(ordineID int64) ([]models.RigaConProdotto, error) {
	rows, err := db.conn.Query(`
		SELECT r.id, r.ordine_id, r.prodotto_id, r.qta_richiesta, r.qta_approvata,
		       r.qta_evasa, r.stato_riga, p.nome, COALESCE(p.codice_articolo,'')
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
		if err := rows.Scan(&r.ID, &r.OrdineID, &r.ProdottoID, &r.QtaRichiesta, &r.QtaApprovata,
			&r.QtaEvasa, &r.StatoRiga, &r.ProdottoNome, &r.ProdottoCodice); err != nil {
			return nil, err
		}
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
// Se qta <= 0 rimuove la riga.
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
// e transita l'ordine a 'in_preparazione'.
func (db *DB) PreparaOrdineFIFO(ordineID int64) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`
		SELECT id, prodotto_id, COALESCE(qta_approvata, qta_richiesta)
		FROM righe_ordine WHERE ordine_id = ?
	`, ordineID)
	if err != nil {
		return err
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
			return err
		}
		righe = append(righe, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
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
			return err
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
				return err
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
				return err
			}
			if _, err = tx.Exec(
				`UPDATE lotti_acquisto SET quantita_rimanente = quantita_rimanente - ? WHERE id = ?`,
				prelevato, l.id,
			); err != nil {
				return err
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
			return err
		}
	}

	if _, err = tx.Exec(`UPDATE ordini SET stato = 'in_preparazione' WHERE id = ?`, ordineID); err != nil {
		return err
	}
	return tx.Commit()
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
