package models

import "time"

// Settore rappresenta un ufficio o settore dell'ente.
type Settore struct {
	ID                  string
	Nome                string
	FunzionarioUsername string
}

// Utente rappresenta un utente del sistema, sincronizzato con LDAP.
// Ruoli possibili: "user", "funzionario", "magazzino", "admin".
type Utente struct {
	Username  string
	Email     string
	Ruolo     string
	SettoreID string
}

// Categoria raggruppa i prodotti del catalogo.
type Categoria struct {
	ID   int64
	Nome string
}

// Prodotto rappresenta un articolo del catalogo.
type Prodotto struct {
	ID             int64
	CodiceArticolo string
	Nome           string
	Descrizione    string
	CategoriaID    int64
	ScortaMinima   int
	ImmagineBLOB   []byte
}

// LottoAcquisto rappresenta un carico di merce acquistata (usato per FIFO).
type LottoAcquisto struct {
	ID                int64
	ProdottoID        int64
	DataAcquisto      time.Time
	QuantitaIniziale  int
	QuantitaRimanente int
	CostoUnitario     float64
}

// Ordine rappresenta una richiesta di materiale effettuata da un utente.
// Stati: "in_approvazione", "approvato", "in_preparazione", "pronto", "ritirato", "rifiutato".
type Ordine struct {
	ID              int64
	UtenteUsername  string
	SettoreID       string
	DataCreazione   time.Time
	Stato           string
	NoteFunzionario string
}

// RigaOrdine rappresenta una singola voce (prodotto) all'interno di un ordine.
// StatoRiga: "in_attesa", "evasa_parziale", "evasa".
type RigaOrdine struct {
	ID           int64
	OrdineID     int64
	ProdottoID   int64
	QtaRichiesta int
	QtaApprovata *int
	QtaEvasa     int
	StatoRiga    string
}

// MovimentoMagazzino registra lo scarico fisico di merce da un lotto per un ordine.
// Congela il costo unitario del lotto al momento del prelievo.
type MovimentoMagazzino struct {
	ID               int64
	RigaOrdineID     *int64
	LottoID          int64
	QuantitaPrelevata int
	CostoTotale      float64
	DataMovimento    time.Time
}

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
