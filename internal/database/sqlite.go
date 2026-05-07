package database

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/mirkochipdotcom/magazzino/internal/models"
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

// migrate crea le tabelle del gestionale magazzino se non esistono già.
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
