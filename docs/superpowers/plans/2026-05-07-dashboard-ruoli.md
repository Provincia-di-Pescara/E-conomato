# Dashboard per Ruolo — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implementare dashboard dedicate per utente base, funzionario e magazziniere con workflow completo degli ordini: creazione bozza → approvazione → evasione FIFO.

**Architecture:** Route separate per ruolo (`/dashboard` → user, `/dashboard/funzionario` → funzionario, `/dashboard/magazzino` → magazziniere). Il carrello è un ordine in stato `bozza` persistito in SQLite con salvataggio automatico ad ogni modifica. Redirect post-login instrada per ruolo. HTMX gestisce aggiornamenti parziali carrello e liste.

**Tech Stack:** Go 1.24, SQLite (`database/sql` + `go-sqlite3`), `html/template`, HTMX, Gorilla sessions. Modulo: `github.com/Provincia-di-Pescara/e-conomato`.

---

## File Map

| Azione | File | Responsabilità |
|--------|------|----------------|
| Modifica | `internal/models/models.go` | Nuovi tipi vista: OrdineConRighe, RigaConProdotto, ProdottoCatalogo |
| Modifica | `internal/database/sqlite.go` | Query: catalogo, bozza, ordini per ruolo, azioni, FIFO |
| Modifica | `cmd/server/main.go` | loadTemplates, dashboardURL, redirect, route, handler |
| Crea | `web/templates/dashboard-utente.html` | Catalogo + carrello + storico ordini |
| Crea | `web/templates/dashboard-funzionario.html` | Coda approvazioni + catalogo + storico |
| Crea | `web/templates/dashboard-magazzino.html` | Coda evasione + alert scorte |

---

### Task 1: Nuovi tipi modello

**Files:**
- Modify: `internal/models/models.go`

- [ ] **Step 1: Aggiungi tipi vista alla fine di models.go**

```go
// OrdineConRighe aggrega un ordine con le sue righe per le viste.
type OrdineConRighe struct {
	Ordine
	Righe []RigaConProdotto
}

// RigaConProdotto aggrega una riga ordine con i dati del prodotto associato.
type RigaConProdotto struct {
	RigaOrdine
	ProdottoNome   string
	ProdottoCodice string
}

// ProdottoCatalogo rappresenta un prodotto nel catalogo con disponibilità calcolata.
type ProdottoCatalogo struct {
	ID             int64
	CodiceArticolo string
	Nome           string
	Descrizione    string
	CategoriaID    int64
	CategoriaNome  string
	ScortaMinima   int
	Disponibile    int
}
```

- [ ] **Step 2: Build check**

```bash
cd C:\Users\mirko.daddiego\Documents\magazzino && go build ./...
```

Expected: nessun errore.

- [ ] **Step 3: Commit**

```bash
git add internal/models/models.go
git commit -m "feat(models): add view types OrdineConRighe, RigaConProdotto, ProdottoCatalogo"
```

---

### Task 2: DB — Migration, catalogo e bozza

**Files:**
- Modify: `internal/database/sqlite.go`

- [ ] **Step 1: Aggiungi indice UNIQUE in migrate()**

Trova le ultime righe di `migrate()`:
```go
	`)
	return err
}
```
Sostituisci con:
```go
	`)
	if err != nil {
		return err
	}
	_, err = conn.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_righe_ordine_uniq ON righe_ordine(ordine_id, prodotto_id)`)
	return err
}
```

- [ ] **Step 2: Aggiungi GetSettoreIDByUsername dopo GetAllUtenti**

```go
// GetSettoreIDByUsername restituisce il settore_id dell'utente, o "" se non assegnato.
func (db *DB) GetSettoreIDByUsername(username string) (string, error) {
	var s sql.NullString
	err := db.conn.QueryRow(`SELECT settore_id FROM utenti WHERE username = ?`, username).Scan(&s)
	if err != nil {
		return "", err
	}
	return s.String, nil
}
```

- [ ] **Step 3: Aggiungi GetCatalogo**

```go
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
```

- [ ] **Step 4: Aggiungi getRigheConProdotto (helper privato)**

```go
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
```

- [ ] **Step 5: Aggiungi GetOrCreateBozza**

```go
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
```

- [ ] **Step 6: Aggiungi GetBozzaConRighe**

```go
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
```

- [ ] **Step 7: Aggiungi UpsertRigaBozza e DeleteRigaBozza**

```go
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
```

- [ ] **Step 8: Build + commit**

```bash
go build ./...
git add internal/database/sqlite.go
git commit -m "feat(db): catalog, bozza CRUD, and unique index migration"
```

---

### Task 3: DB — Query visualizzazione ordini

**Files:**
- Modify: `internal/database/sqlite.go`

- [ ] **Step 1: Aggiungi helper scanOrdini**

```go
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
```

- [ ] **Step 2: Aggiungi GetOrdiniUtente, GetOrdiniSettore, GetOrdiniAttivi**

```go
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
```

- [ ] **Step 3: Build + commit**

```bash
go build ./...
git add internal/database/sqlite.go
git commit -m "feat(db): order listing queries for all roles"
```

---

### Task 4: DB — Azioni ordine e FIFO

**Files:**
- Modify: `internal/database/sqlite.go`

- [ ] **Step 1: Aggiungi InviaOrdine**

```go
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
```

- [ ] **Step 2: Aggiungi ApprovaOrdine**

```go
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
```

- [ ] **Step 3: Aggiungi RifiutaOrdine**

```go
// RifiutaOrdine transita l'ordine a 'rifiutato' con nota obbligatoria.
func (db *DB) RifiutaOrdine(ordineID int64, note string) error {
	_, err := db.conn.Exec(
		`UPDATE ordini SET stato = 'rifiutato', note_funzionario = ? WHERE id = ? AND stato = 'in_approvazione'`,
		note, ordineID,
	)
	return err
}
```

- [ ] **Step 4: Aggiungi PreparaOrdineFIFO**

```go
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
```

- [ ] **Step 5: Aggiungi SegnaOrdinePronte e ConsegnaOrdine**

```go
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
```

- [ ] **Step 6: Build + commit**

```bash
go build ./...
git add internal/database/sqlite.go
git commit -m "feat(db): order action queries and FIFO fulfillment algorithm"
```

---

### Task 5: main.go — Template, route e redirect

**Files:**
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Aggiorna loadTemplates**

Trova:
```go
names := []string{"login", "dashboard", "admin", "magazzino"}
```
Sostituisci con:
```go
names := []string{"login", "dashboard", "admin", "magazzino", "dashboard-utente", "dashboard-funzionario", "dashboard-magazzino"}
```

- [ ] **Step 2: Aggiungi dashboardURL e aggiorna handleRoot**

Aggiungi prima di `handleRoot`:
```go
func (a *App) dashboardURL(role string) string {
	switch role {
	case "funzionario":
		return "/dashboard/funzionario"
	case "magazziniere":
		return "/dashboard/magazzino"
	case "admin":
		return "/admin"
	default:
		return "/dashboard"
	}
}
```

Sostituisci `handleRoot`:
```go
func (a *App) handleRoot(w http.ResponseWriter, r *http.Request) {
	if a.getUsername(r) != "" {
		http.Redirect(w, r, a.dashboardURL(a.getRole(r)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
```

- [ ] **Step 3: Aggiorna HX-Redirect in handleLogin**

Trova:
```go
w.Header().Set("HX-Redirect", "/dashboard")
```
Sostituisci con:
```go
w.Header().Set("HX-Redirect", a.dashboardURL(role))
```

- [ ] **Step 4: Sostituisci route /dashboard e aggiungi nuove route**

Trova:
```go
mux.HandleFunc("/dashboard", app.requireRole("magazziniere", "funzionario", "user")(app.handleDashboard))
mux.HandleFunc("/dashboard/scorte", app.requireRole("magazziniere")(app.handleDashboardScorte))
```
Sostituisci con:
```go
mux.HandleFunc("/dashboard", app.requireRole("user", "funzionario")(app.handleDashboardUtente))
mux.HandleFunc("/dashboard/funzionario", app.requireRole("funzionario")(app.handleDashboardFunzionario))
mux.HandleFunc("/dashboard/magazzino", app.requireRole("magazziniere")(app.handleDashboardMagazzino))
mux.HandleFunc("/dashboard/scorte", app.requireRole("magazziniere")(app.handleDashboardScorte))

// Bozza / carrello
mux.HandleFunc("POST /ordini/righe/{prodotto_id}", app.requireRole("user", "funzionario")(app.handleUpsertRigaBozza))
mux.HandleFunc("DELETE /ordini/righe/{prodotto_id}", app.requireRole("user", "funzionario")(app.handleDeleteRigaBozza))
mux.HandleFunc("POST /ordini/{id}/invia", app.requireRole("user", "funzionario")(app.handleInviaOrdine))

// Azioni funzionario
mux.HandleFunc("POST /ordini/{id}/approva", app.requireRole("funzionario")(app.handleApprovaOrdine))
mux.HandleFunc("POST /ordini/{id}/rifiuta", app.requireRole("funzionario")(app.handleRifiutaOrdine))

// Azioni magazziniere
mux.HandleFunc("POST /ordini/{id}/prepara", app.requireRole("magazziniere")(app.handlePreparaOrdine))
mux.HandleFunc("POST /ordini/{id}/pronto", app.requireRole("magazziniere")(app.handleSegnaPronte))
mux.HandleFunc("POST /ordini/{id}/consegna", app.requireRole("magazziniere")(app.handleConsegnaOrdine))
```

- [ ] **Step 5: Build + commit**

```bash
go build ./...
git add cmd/server/main.go
git commit -m "feat(routing): role-aware redirect and new route registrations"
```

---

### Task 6: main.go — Handler utente e bozza

**Files:**
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Aggiungi handleDashboardUtente**

```go
func (a *App) handleDashboardUtente(w http.ResponseWriter, r *http.Request) {
	if a.getRole(r) == "funzionario" {
		http.Redirect(w, r, "/dashboard/funzionario", http.StatusSeeOther)
		return
	}
	username := a.getUsername(r)
	bozza, _ := a.db.GetBozzaConRighe(username)
	categorie, _ := a.db.GetAllCategorie()
	prodotti, _ := a.db.GetCatalogo(r.URL.Query().Get("q"), 0)
	ordini, _ := a.db.GetOrdiniUtente(username)
	a.render(w, r, "dashboard-utente", map[string]any{
		"Username":  username,
		"Role":      a.getRole(r),
		"IsAdmin":   false,
		"Bozza":     bozza,
		"Categorie": categorie,
		"Prodotti":  prodotti,
		"Ordini":    ordini,
		"Version":   AppVersion,
		"BrandName": a.cfg.BrandName,
		"BrandLogo": a.cfg.BrandLogoPath,
	})
}
```

- [ ] **Step 2: Aggiungi renderCarrello (helper HTMX)**

```go
func (a *App) renderCarrello(w http.ResponseWriter, targetID string, bozza *models.OrdineConRighe) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if bozza == nil || len(bozza.Righe) == 0 {
		fmt.Fprintf(w, `<div id="%s"><p class="empty-state">Carrello vuoto</p></div>`, targetID)
		return
	}
	fmt.Fprintf(w, `<div id="%s">`, targetID)
	for _, r := range bozza.Righe {
		fmt.Fprintf(w,
			`<div class="cart-row">
			  <span style="flex:1">%s</span>
			  <input type="number" min="1" value="%d" name="qta" style="width:60px"
			         hx-post="/ordini/righe/%d" hx-target="#%s" hx-swap="outerHTML" hx-trigger="change">
			  <button hx-delete="/ordini/righe/%d" hx-target="#%s" hx-swap="outerHTML">✕</button>
			</div>`,
			template.HTMLEscapeString(r.ProdottoNome), r.QtaRichiesta,
			r.ProdottoID, targetID, r.ProdottoID, targetID,
		)
	}
	fmt.Fprintf(w,
		`<form hx-post="/ordini/%d/invia" hx-target="body" hx-push-url="true" style="margin-top:1rem">
		  <button type="submit" class="btn-primary" style="width:100%%">Invia Ordine</button>
		</form></div>`,
		bozza.ID,
	)
}
```

- [ ] **Step 3: Aggiungi handleUpsertRigaBozza**

```go
func (a *App) handleUpsertRigaBozza(w http.ResponseWriter, r *http.Request) {
	prodottoID, err := strconv.ParseInt(r.PathValue("prodotto_id"), 10, 64)
	if err != nil {
		http.Error(w, "prodotto_id non valido", 400)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	qta, err := strconv.Atoi(r.FormValue("qta"))
	if err != nil {
		http.Error(w, "quantità non valida", 400)
		return
	}
	username := a.getUsername(r)
	bozzaID, err := a.db.GetOrCreateBozza(username)
	if err != nil {
		logger.Error("get/create bozza: %v", err)
		http.Error(w, "settore non assegnato — contattare l'amministratore", 400)
		return
	}
	if err := a.db.UpsertRigaBozza(bozzaID, prodottoID, qta); err != nil {
		logger.Error("upsert riga bozza: %v", err)
		http.Error(w, "errore interno", 500)
		return
	}
	targetID := r.URL.Query().Get("cart")
	if targetID == "" {
		targetID = "carrello-content"
	}
	bozza, _ := a.db.GetBozzaConRighe(username)
	a.renderCarrello(w, targetID, bozza)
}
```

- [ ] **Step 4: Aggiungi handleDeleteRigaBozza**

```go
func (a *App) handleDeleteRigaBozza(w http.ResponseWriter, r *http.Request) {
	prodottoID, err := strconv.ParseInt(r.PathValue("prodotto_id"), 10, 64)
	if err != nil {
		http.Error(w, "prodotto_id non valido", 400)
		return
	}
	username := a.getUsername(r)
	if bozza, _ := a.db.GetBozzaConRighe(username); bozza != nil {
		if err := a.db.DeleteRigaBozza(bozza.ID, prodottoID); err != nil {
			logger.Error("delete riga bozza: %v", err)
			http.Error(w, "errore interno", 500)
			return
		}
	}
	targetID := r.URL.Query().Get("cart")
	if targetID == "" {
		targetID = "carrello-content"
	}
	bozza, _ := a.db.GetBozzaConRighe(username)
	a.renderCarrello(w, targetID, bozza)
}
```

- [ ] **Step 5: Aggiungi handleInviaOrdine**

```go
func (a *App) handleInviaOrdine(w http.ResponseWriter, r *http.Request) {
	ordineID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", 400)
		return
	}
	username := a.getUsername(r)
	if err := a.db.InviaOrdine(ordineID, username); err != nil {
		logger.Error("invia ordine %d: %v", ordineID, err)
		http.Error(w, "errore interno", 500)
		return
	}
	logger.Info("ordine %d inviato da %s", ordineID, username)
	http.Redirect(w, r, a.dashboardURL(a.getRole(r)), http.StatusSeeOther)
}
```

- [ ] **Step 6: Build + commit**

```bash
go build ./...
git add cmd/server/main.go
git commit -m "feat(handlers): utente dashboard, cart upsert/delete, invia ordine"
```

---

### Task 7: main.go — Handler funzionario

**Files:**
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Aggiungi handleDashboardFunzionario**

```go
func (a *App) handleDashboardFunzionario(w http.ResponseWriter, r *http.Request) {
	username := a.getUsername(r)
	settoreID, _ := a.db.GetSettoreIDByUsername(username)
	daApprovare, _ := a.db.GetOrdiniSettore(settoreID)
	bozza, _ := a.db.GetBozzaConRighe(username)
	categorie, _ := a.db.GetAllCategorie()
	prodotti, _ := a.db.GetCatalogo(r.URL.Query().Get("q"), 0)
	mieiOrdini, _ := a.db.GetOrdiniUtente(username)
	a.render(w, r, "dashboard-funzionario", map[string]any{
		"Username":    username,
		"Role":        a.getRole(r),
		"IsAdmin":     false,
		"DaApprovare": daApprovare,
		"Bozza":       bozza,
		"Categorie":   categorie,
		"Prodotti":    prodotti,
		"MieiOrdini":  mieiOrdini,
		"Version":     AppVersion,
		"BrandName":   a.cfg.BrandName,
		"BrandLogo":   a.cfg.BrandLogoPath,
	})
}
```

- [ ] **Step 2: Aggiungi handleApprovaOrdine**

```go
func (a *App) handleApprovaOrdine(w http.ResponseWriter, r *http.Request) {
	ordineID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", 400)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	qtaPerRiga := map[int64]int{}
	for key, vals := range r.Form {
		if !strings.HasPrefix(key, "riga_") || len(vals) == 0 {
			continue
		}
		rigaID, err := strconv.ParseInt(strings.TrimPrefix(key, "riga_"), 10, 64)
		if err != nil {
			continue
		}
		qta, err := strconv.Atoi(vals[0])
		if err != nil || qta < 0 {
			continue
		}
		qtaPerRiga[rigaID] = qta
	}
	if err := a.db.ApprovaOrdine(ordineID, qtaPerRiga, r.FormValue("note")); err != nil {
		logger.Error("approva ordine %d: %v", ordineID, err)
		http.Error(w, "errore interno", 500)
		return
	}
	logger.Info("ordine %d approvato da %s", ordineID, a.getUsername(r))
	http.Redirect(w, r, "/dashboard/funzionario", http.StatusSeeOther)
}
```

- [ ] **Step 3: Aggiungi handleRifiutaOrdine**

```go
func (a *App) handleRifiutaOrdine(w http.ResponseWriter, r *http.Request) {
	ordineID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", 400)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	note := strings.TrimSpace(r.FormValue("note"))
	if note == "" {
		http.Error(w, "motivazione obbligatoria", 400)
		return
	}
	if err := a.db.RifiutaOrdine(ordineID, note); err != nil {
		logger.Error("rifiuta ordine %d: %v", ordineID, err)
		http.Error(w, "errore interno", 500)
		return
	}
	logger.Info("ordine %d rifiutato da %s", ordineID, a.getUsername(r))
	http.Redirect(w, r, "/dashboard/funzionario", http.StatusSeeOther)
}
```

- [ ] **Step 4: Build + commit**

```bash
go build ./...
git add cmd/server/main.go
git commit -m "feat(handlers): funzionario dashboard, approva, rifiuta"
```

---

### Task 8: main.go — Handler magazzino

**Files:**
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Aggiungi handleDashboardMagazzino**

```go
func (a *App) handleDashboardMagazzino(w http.ResponseWriter, r *http.Request) {
	scorte, _ := a.db.GetProdottiSottoSoglia()
	ordini, _ := a.db.GetOrdiniAttivi()
	a.render(w, r, "dashboard-magazzino", map[string]any{
		"Username":  a.getUsername(r),
		"Role":      a.getRole(r),
		"IsAdmin":   false,
		"Scorte":    scorte,
		"Ordini":    ordini,
		"Version":   AppVersion,
		"BrandName": a.cfg.BrandName,
		"BrandLogo": a.cfg.BrandLogoPath,
	})
}
```

- [ ] **Step 2: Aggiungi handlePreparaOrdine, handleSegnaPronte, handleConsegnaOrdine**

```go
func (a *App) handlePreparaOrdine(w http.ResponseWriter, r *http.Request) {
	ordineID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", 400)
		return
	}
	if err := a.db.PreparaOrdineFIFO(ordineID); err != nil {
		logger.Error("prepara FIFO ordine %d: %v", ordineID, err)
		http.Error(w, "errore interno", 500)
		return
	}
	logger.Info("ordine %d in preparazione (FIFO) da %s", ordineID, a.getUsername(r))
	http.Redirect(w, r, "/dashboard/magazzino", http.StatusSeeOther)
}

func (a *App) handleSegnaPronte(w http.ResponseWriter, r *http.Request) {
	ordineID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", 400)
		return
	}
	utenteUsername, err := a.db.SegnaOrdinePronte(ordineID)
	if err != nil {
		logger.Error("segna pronto ordine %d: %v", ordineID, err)
		http.Error(w, "errore interno", 500)
		return
	}
	logger.Info("ordine %d pronto, notifica a %s (email TODO)", ordineID, utenteUsername)
	http.Redirect(w, r, "/dashboard/magazzino", http.StatusSeeOther)
}

func (a *App) handleConsegnaOrdine(w http.ResponseWriter, r *http.Request) {
	ordineID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", 400)
		return
	}
	if err := a.db.ConsegnaOrdine(ordineID); err != nil {
		logger.Error("consegna ordine %d: %v", ordineID, err)
		http.Error(w, "errore interno", 500)
		return
	}
	logger.Info("ordine %d ritirato, confermato da %s", ordineID, a.getUsername(r))
	http.Redirect(w, r, "/dashboard/magazzino", http.StatusSeeOther)
}
```

- [ ] **Step 3: Build + commit**

```bash
go build ./...
git add cmd/server/main.go
git commit -m "feat(handlers): magazzino dashboard, prepara FIFO, pronto, consegna"
```

---

### Task 9: Template dashboard-utente.html

**Files:**
- Create: `web/templates/dashboard-utente.html`

- [ ] **Step 1: Crea il template**

```html
<!DOCTYPE html>
<html lang="it">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>{{if .BrandName}}{{.BrandName}} — {{end}}I miei ordini</title>
  <link rel="stylesheet" href="/static/css/style.css">
  <script src="/static/js/htmx.min.js" defer></script>
</head>
<body>
<nav class="navbar">
  <div class="nav-brand">
    {{if .BrandLogo}}<img src="{{brandLogoSrc .BrandLogo}}" alt="logo" height="32">{{end}}
    {{if .BrandName}}<span>{{.BrandName}}</span>{{end}}
  </div>
  <div class="nav-user">
    <span class="user-chip">{{.Username}} <span class="badge-role badge-{{.Role}}">{{.Role}}</span></span>
    <form method="POST" action="/logout" style="display:inline">
      <button type="submit" class="btn-link">Esci</button>
    </form>
  </div>
</nav>

<main class="container" style="display:grid;grid-template-columns:1fr 300px;gap:1.5rem;align-items:start;padding-top:1.5rem">

  <!-- Catalogo -->
  <section>
    <h2>Catalogo Prodotti</h2>
    <input type="search" placeholder="Cerca prodotto…" name="q"
           hx-get="/dashboard" hx-target="#catalogo-grid" hx-trigger="input changed delay:300ms"
           class="mock-input" style="width:100%;margin-bottom:1rem">
    <div id="catalogo-grid" style="display:grid;grid-template-columns:repeat(auto-fill,minmax(160px,1fr));gap:1rem">
      {{range .Prodotti}}
      <div class="card" style="padding:.75rem">
        <img src="/prodotti/{{.ID}}/immagine" alt="{{.Nome}}"
             style="width:100%;height:90px;object-fit:cover;border-radius:4px;margin-bottom:.5rem"
             onerror="this.style.display='none'">
        <p style="font-weight:600;margin:0 0 .25rem">{{.Nome}}</p>
        <p style="font-size:.8rem;color:#888;margin:0 0 .25rem">{{.CategoriaNome}}</p>
        <p style="font-size:.85rem;margin:0 0 .5rem">Disp.: <strong>{{.Disponibile}}</strong></p>
        <div style="display:flex;gap:.25rem;align-items:center">
          <input type="number" min="1" value="1" id="qta-{{.ID}}" name="qta" style="width:50px" class="mock-input">
          <button class="btn-sm"
                  hx-post="/ordini/righe/{{.ID}}?cart=carrello-content"
                  hx-target="#carrello-content"
                  hx-swap="outerHTML"
                  hx-include="#qta-{{.ID}}">+</button>
        </div>
      </div>
      {{else}}
      <p class="empty-state">Nessun prodotto nel catalogo.</p>
      {{end}}
    </div>
  </section>

  <!-- Carrello -->
  <aside style="position:sticky;top:1rem">
    <h2>Carrello</h2>
    {{if .Bozza}}
    <div id="carrello-content">
      {{range .Bozza.Righe}}
      <div class="cart-row" style="display:flex;gap:.5rem;align-items:center;margin-bottom:.5rem">
        <span style="flex:1;font-size:.9rem">{{.ProdottoNome}}</span>
        <input type="number" min="1" value="{{.QtaRichiesta}}" name="qta" style="width:55px"
               hx-post="/ordini/righe/{{.ProdottoID}}?cart=carrello-content"
               hx-target="#carrello-content" hx-swap="outerHTML" hx-trigger="change">
        <button hx-delete="/ordini/righe/{{.ProdottoID}}?cart=carrello-content"
                hx-target="#carrello-content" hx-swap="outerHTML"
                style="background:none;border:none;cursor:pointer;color:#c00">✕</button>
      </div>
      {{end}}
      <form hx-post="/ordini/{{.Bozza.ID}}/invia" hx-target="body" hx-push-url="true" style="margin-top:1rem">
        <button type="submit" class="btn-primary" style="width:100%">Invia Ordine</button>
      </form>
    </div>
    {{else}}
    <div id="carrello-content">
      <p class="empty-state">Carrello vuoto</p>
    </div>
    {{end}}
  </aside>
</main>

<!-- Storico -->
<section class="container" style="margin-top:2rem;padding-bottom:2rem">
  <h2>I miei ordini</h2>
  {{if .Ordini}}
  <table>
    <thead><tr><th>#</th><th>Data</th><th>Prodotti</th><th>Stato</th><th>Note</th></tr></thead>
    <tbody>
    {{range .Ordini}}
    <tr>
      <td>#{{.ID}}</td>
      <td>{{fmtDate .DataCreazione}}</td>
      <td>{{len .Righe}}</td>
      <td><span class="badge-stato badge-{{.Stato}}">{{.Stato}}</span></td>
      <td style="font-size:.85rem;color:#666">{{.NoteFunzionario}}</td>
    </tr>
    {{end}}
    </tbody>
  </table>
  {{else}}
  <p class="empty-state">Nessun ordine inviato.</p>
  {{end}}
</section>
</body>
</html>
```

- [ ] **Step 2: Avvia e verifica**

```bash
go run ./cmd/server
```

Login come utente base (mock mode). Verifica:
- Catalogo prodotti visibile
- Click `+` aggiorna carrello via HTMX (no reload)
- Modifica quantità nel carrello salva immediatamente
- `✕` rimuove prodotto dal carrello
- `Invia Ordine` → redirect alla dashboard con ordine nello storico

- [ ] **Step 3: Commit**

```bash
git add web/templates/dashboard-utente.html
git commit -m "feat(ui): utente dashboard — catalog, cart, order history"
```

---

### Task 10: Template dashboard-funzionario.html

**Files:**
- Create: `web/templates/dashboard-funzionario.html`

- [ ] **Step 1: Crea il template**

```html
<!DOCTYPE html>
<html lang="it">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>{{if .BrandName}}{{.BrandName}} — {{end}}Funzionario</title>
  <link rel="stylesheet" href="/static/css/style.css">
  <script src="/static/js/htmx.min.js" defer></script>
</head>
<body>
<nav class="navbar">
  <div class="nav-brand">
    {{if .BrandLogo}}<img src="{{brandLogoSrc .BrandLogo}}" alt="logo" height="32">{{end}}
    {{if .BrandName}}<span>{{.BrandName}}</span>{{end}}
  </div>
  <div class="nav-links" style="display:flex;gap:.5rem">
    <button class="tab-btn active" onclick="showTab('approvazioni')">
      Da approvare{{if .DaApprovare}} <span class="badge-count">{{len .DaApprovare}}</span>{{end}}
    </button>
    <button class="tab-btn" onclick="showTab('miei')">I miei ordini</button>
    <button class="tab-btn" onclick="showTab('nuovo')">Nuovo ordine</button>
  </div>
  <div class="nav-user">
    <span class="user-chip">{{.Username}} <span class="badge-role badge-funzionario">funzionario</span></span>
    <form method="POST" action="/logout" style="display:inline">
      <button type="submit" class="btn-link">Esci</button>
    </form>
  </div>
</nav>

<main class="container" style="padding-top:1.5rem">

  <!-- Tab: Approvazioni -->
  <section id="tab-approvazioni">
    <h2>Ordini da approvare</h2>
    {{if .DaApprovare}}
    {{range .DaApprovare}}
    <div class="card" style="margin-bottom:1rem;padding:1rem">
      <div style="display:flex;gap:1rem;align-items:center;flex-wrap:wrap">
        <strong>#{{.ID}}</strong>
        <span>{{.UtenteUsername}}</span>
        <span style="color:#888">{{fmtDate .DataCreazione}}</span>
        <span class="badge-stato badge-in_approvazione">In approvazione</span>
      </div>
      <details style="margin-top:.75rem">
        <summary style="cursor:pointer">{{len .Righe}} prodotto{{if gt (len .Righe) 1}}i{{end}} — espandi per approvare</summary>
        <form method="POST" action="/ordini/{{.ID}}/approva" style="margin-top:.75rem">
          <table>
            <thead><tr><th>Prodotto</th><th>Richiesto</th><th>Approva (max = richiesto)</th></tr></thead>
            <tbody>
            {{range .Righe}}
            <tr>
              <td>{{.ProdottoNome}}</td>
              <td>{{.QtaRichiesta}}</td>
              <td><input type="number" name="riga_{{.ID}}" value="{{.QtaRichiesta}}"
                         min="0" max="{{.QtaRichiesta}}" style="width:70px"></td>
            </tr>
            {{end}}
            </tbody>
          </table>
          <div style="margin-top:.75rem;display:flex;gap:.75rem;align-items:center;flex-wrap:wrap">
            <label>Note: <input type="text" name="note" placeholder="Opzionale" style="width:260px"></label>
            <button type="submit" class="btn-primary">Approva ordine</button>
          </div>
        </form>
        <form method="POST" action="/ordini/{{.ID}}/rifiuta" style="margin-top:.75rem;display:flex;gap:.5rem;align-items:center">
          <input type="text" name="note" placeholder="Motivazione rifiuto (obbligatoria)" required style="width:280px">
          <button type="submit" class="btn-danger">Rifiuta</button>
        </form>
      </details>
    </div>
    {{end}}
    {{else}}
    <p class="empty-state">Nessun ordine in attesa di approvazione.</p>
    {{end}}
  </section>

  <!-- Tab: I miei ordini -->
  <section id="tab-miei" style="display:none">
    <h2>I miei ordini</h2>
    {{if .MieiOrdini}}
    <table>
      <thead><tr><th>#</th><th>Data</th><th>Prodotti</th><th>Stato</th><th>Note</th></tr></thead>
      <tbody>
      {{range .MieiOrdini}}
      <tr>
        <td>#{{.ID}}</td>
        <td>{{fmtDate .DataCreazione}}</td>
        <td>{{len .Righe}}</td>
        <td><span class="badge-stato badge-{{.Stato}}">{{.Stato}}</span></td>
        <td style="font-size:.85rem;color:#666">{{.NoteFunzionario}}</td>
      </tr>
      {{end}}
      </tbody>
    </table>
    {{else}}
    <p class="empty-state">Nessun ordine inviato.</p>
    {{end}}
  </section>

  <!-- Tab: Nuovo ordine -->
  <section id="tab-nuovo" style="display:none">
    <div style="display:grid;grid-template-columns:1fr 300px;gap:1.5rem;align-items:start">
      <div>
        <h2>Catalogo</h2>
        <div style="display:grid;grid-template-columns:repeat(auto-fill,minmax(160px,1fr));gap:1rem">
          {{range .Prodotti}}
          <div class="card" style="padding:.75rem">
            <img src="/prodotti/{{.ID}}/immagine" alt="{{.Nome}}"
                 style="width:100%;height:90px;object-fit:cover;border-radius:4px;margin-bottom:.5rem"
                 onerror="this.style.display='none'">
            <p style="font-weight:600;margin:0 0 .25rem">{{.Nome}}</p>
            <p style="font-size:.85rem;margin:0 0 .5rem">Disp.: <strong>{{.Disponibile}}</strong></p>
            <div style="display:flex;gap:.25rem;align-items:center">
              <input type="number" min="1" value="1" id="fn-qta-{{.ID}}" name="qta" style="width:50px" class="mock-input">
              <button class="btn-sm"
                      hx-post="/ordini/righe/{{.ID}}?cart=fn-carrello-content"
                      hx-target="#fn-carrello-content"
                      hx-swap="outerHTML"
                      hx-include="#fn-qta-{{.ID}}">+</button>
            </div>
          </div>
          {{end}}
        </div>
      </div>
      <aside style="position:sticky;top:1rem">
        <h2>Carrello</h2>
        {{if .Bozza}}
        <div id="fn-carrello-content">
          {{range .Bozza.Righe}}
          <div class="cart-row" style="display:flex;gap:.5rem;align-items:center;margin-bottom:.5rem">
            <span style="flex:1;font-size:.9rem">{{.ProdottoNome}}</span>
            <input type="number" min="1" value="{{.QtaRichiesta}}" name="qta" style="width:55px"
                   hx-post="/ordini/righe/{{.ProdottoID}}?cart=fn-carrello-content"
                   hx-target="#fn-carrello-content" hx-swap="outerHTML" hx-trigger="change">
            <button hx-delete="/ordini/righe/{{.ProdottoID}}?cart=fn-carrello-content"
                    hx-target="#fn-carrello-content" hx-swap="outerHTML"
                    style="background:none;border:none;cursor:pointer;color:#c00">✕</button>
          </div>
          {{end}}
          <form hx-post="/ordini/{{.Bozza.ID}}/invia" hx-target="body" hx-push-url="true" style="margin-top:1rem">
            <button type="submit" class="btn-primary" style="width:100%">Invia Ordine</button>
          </form>
        </div>
        {{else}}
        <div id="fn-carrello-content">
          <p class="empty-state">Carrello vuoto</p>
        </div>
        {{end}}
      </aside>
    </div>
  </section>

</main>

<script>
function showTab(name) {
  ['approvazioni','miei','nuovo'].forEach(function(t) {
    document.getElementById('tab-'+t).style.display = t === name ? '' : 'none';
  });
  document.querySelectorAll('.tab-btn').forEach(function(b, i) {
    b.classList.toggle('active', ['approvazioni','miei','nuovo'][i] === name);
  });
}
</script>
</body>
</html>
```

- [ ] **Step 2: Verifica**

Login come funzionario. Verifica:
- Tab "Da approvare" mostra ordini `in_approvazione` del settore del funzionario
- `<details>` espandibile per ogni ordine
- Approvazione con quantità ridotta funziona (max=qta_richiesta enforced dal browser)
- Rifiuto richiede nota (campo `required`)
- Tab "Nuovo ordine": funzionario crea ordine → se è il responsabile del proprio settore, va diretto in `in_preparazione` (auto-approvazione)

- [ ] **Step 3: Commit**

```bash
git add web/templates/dashboard-funzionario.html
git commit -m "feat(ui): funzionario dashboard — approvals, catalog, order history"
```

---

### Task 11: Template dashboard-magazzino.html

**Files:**
- Create: `web/templates/dashboard-magazzino.html`

- [ ] **Step 1: Crea il template**

```html
<!DOCTYPE html>
<html lang="it">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>{{if .BrandName}}{{.BrandName}} — {{end}}Magazzino</title>
  <link rel="stylesheet" href="/static/css/style.css">
  <script src="/static/js/htmx.min.js" defer></script>
</head>
<body>
<nav class="navbar">
  <div class="nav-brand">
    {{if .BrandLogo}}<img src="{{brandLogoSrc .BrandLogo}}" alt="logo" height="32">{{end}}
    {{if .BrandName}}<span>{{.BrandName}}</span>{{end}}
  </div>
  <div class="nav-links">
    <span style="font-weight:600">
      Ordini da evadere{{if .Ordini}} <span class="badge-count">{{len .Ordini}}</span>{{end}}
    </span>
    <a href="/prodotti">Prodotti</a>
    <a href="/categorie">Categorie</a>
    <a href="/lotti/new">Carico merce</a>
  </div>
  <div class="nav-user">
    <span class="user-chip">{{.Username}} <span class="badge-role badge-magazziniere">magazziniere</span></span>
    <form method="POST" action="/logout" style="display:inline">
      <button type="submit" class="btn-link">Esci</button>
    </form>
  </div>
</nav>

<main class="container" style="padding-top:1.5rem">

  {{if .Scorte}}
  <div class="alert" style="background:#fff3cd;border:1px solid #ffc107;border-radius:6px;padding:.75rem 1rem;margin-bottom:1.5rem">
    ⚠ <strong>{{len .Scorte}} prodotto{{if gt (len .Scorte) 1}}i{{end}} sotto scorta minima</strong>
    — <a href="/prodotti">Gestisci prodotti</a>
  </div>
  {{end}}

  <h2>Ordini da evadere</h2>

  {{if .Ordini}}
  {{range .Ordini}}
  <div class="card" style="margin-bottom:1rem;padding:1rem">
    <div style="display:flex;gap:1rem;align-items:center;flex-wrap:wrap;margin-bottom:.75rem">
      <strong>#{{.ID}}</strong>
      <span>{{.UtenteUsername}}</span>
      <span style="color:#888">Settore: {{.SettoreID}}</span>
      <span style="color:#888">{{fmtDate .DataCreazione}}</span>
      <span class="badge-stato badge-{{.Stato}}">{{.Stato}}</span>
    </div>
    <table style="margin-bottom:.75rem">
      <thead><tr><th>Prodotto</th><th>Approvato</th><th>Evaso</th><th>Stato riga</th></tr></thead>
      <tbody>
      {{range .Righe}}
      <tr>
        <td>{{.ProdottoNome}}</td>
        <td>{{if .QtaApprovata}}{{.QtaApprovata}}{{else}}{{.QtaRichiesta}}{{end}}</td>
        <td>{{.QtaEvasa}}</td>
        <td><span class="badge-stato badge-{{.StatoRiga}}">{{.StatoRiga}}</span></td>
      </tr>
      {{end}}
      </tbody>
    </table>
    {{if eq .Stato "approvato"}}
    <form method="POST" action="/ordini/{{.ID}}/prepara">
      <button type="submit" class="btn-primary">Inizia preparazione</button>
    </form>
    {{else if eq .Stato "in_preparazione"}}
    <form method="POST" action="/ordini/{{.ID}}/pronto">
      <button type="submit" class="btn-success">Segna come Pronto</button>
    </form>
    {{else if eq .Stato "pronto"}}
    <form method="POST" action="/ordini/{{.ID}}/consegna">
      <button type="submit" class="btn-secondary">Consegna effettuata</button>
    </form>
    {{end}}
  </div>
  {{end}}
  {{else}}
  <p class="empty-state">Nessun ordine da evadere.</p>
  {{end}}

</main>
</body>
</html>
```

- [ ] **Step 2: Verifica end-to-end completo**

```bash
go run ./cmd/server
```

Flusso completo in mock mode:
1. Login utente base → `/dashboard` → aggiungi prodotti → invia ordine → verifica stato `in_approvazione`
2. Login funzionario → `/dashboard/funzionario` → approva ordine con quantità ridotta → verifica stato `approvato`
3. Login magazziniere → `/dashboard/magazzino` → "Inizia preparazione" → verifica `movimenti_magazzino` nel DB → "Segna come Pronto" → "Consegna effettuata" → ordine scompare dalla lista
4. Verifica auto-approvazione: funzionario crea ordine per se stesso → salta `in_approvazione`, va a `in_preparazione`

- [ ] **Step 3: Commit finale**

```bash
git add web/templates/dashboard-magazzino.html
git commit -m "feat(ui): magazzino dashboard — fulfillment queue and FIFO actions"
```

---

## Note CSS

Le seguenti classi CSS usate nei template devono essere aggiunte a `web/static/css/style.css`:
- `.badge-count` — badge numerico rosso per conteggi
- `.badge-stato.badge-bozza`, `.badge-in_approvazione`, `.badge-approvato`, `.badge-in_preparazione`, `.badge-pronto`, `.badge-ritirato`, `.badge-rifiutato` — colori per stati ordine
- `.badge-role.badge-funzionario`, `.badge-magazziniere`, `.badge-user` — colori per ruoli
- `.tab-btn`, `.tab-btn.active` — stile bottoni tab
- `.btn-success`, `.btn-danger`, `.btn-secondary` — varianti bottoni (`.btn-primary` già esiste)
- `.card` — card generica con bordo e shadow

Aggiungere in un task separato o inline durante la verifica.
