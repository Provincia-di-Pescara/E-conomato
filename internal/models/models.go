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
	ID    int64
	Nome  string
	Icona string // classe Font Awesome (es. "fa-solid fa-box")
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
	Icona          string // classe Font Awesome (es. "fa-solid fa-pen"); vuoto = usa immagine BLOB
}

// LottoAcquisto rappresenta una riga di acquisto per un singolo prodotto
// (base FIFO). AcquistoID raggruppa più righe nello stesso documento (head).
type LottoAcquisto struct {
	ID                int64
	ProdottoID        int64
	DataAcquisto      time.Time
	QuantitaIniziale  int
	QuantitaRimanente int
	CostoUnitario     float64
	AcquistoID        *int64
	// Campi joinati in lettura (popolati da GetAcquistoConRighe).
	ProdottoNome   string
	ProdottoCodice string
}

// Fornitore rappresenta un fornitore opzionale per i documenti di acquisto.
type Fornitore struct {
	ID         int64
	Nome       string
	PartitaIVA string
	Email      string
	Telefono   string
	Note       string
	Attivo     bool
}

// Acquisto è il documento head di un carico merce: una bolla/fattura del
// fornitore che raggruppa N righe (lotti) di prodotti diversi.
type Acquisto struct {
	ID            int64
	DataAcquisto  time.Time
	FornitoreID   *int64
	FornitoreNome string
	NumeroDoc     string
	Note          string
	CreatedBy     string
	CreatedAt     time.Time
	Righe         []LottoAcquisto
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
// Prenotazione=true se la riga è stata richiesta su prodotto esaurito: il FIFO
// la lascia in_attesa finché non entrano nuovi lotti.
type RigaOrdine struct {
	ID           int64
	OrdineID     int64
	ProdottoID   int64
	QtaRichiesta int
	QtaApprovata *int
	QtaEvasa     int
	StatoRiga    string
	Prenotazione bool
	NotaUtente   string
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
	CategoriaIcona string
	Icona          string
	ScortaMinima   int
	Disponibile    int
}

// Notifica è un evento mostrato all'utente nella pagina /notifiche e nel
// contatore in topbar. Tipi ammessi: "ordine_inviato", "ordine_approvato",
// "ordine_rifiutato", "ordine_in_preparazione", "ordine_pronto", "scorta".
type Notifica struct {
	ID             int64
	UtenteUsername string
	Tipo           string
	Messaggio      string
	OrdineID       *int64
	ProdottoID     *int64
	Letta          bool
	CreataIl       time.Time
}

// EmailOutbox è un job di invio email gestito dal worker asincrono.
// Stati: "in_attesa", "inviata", "abbandonata".
type EmailOutbox struct {
	ID                int64
	Destinatario      string
	Soggetto          string
	CorpoHTML         string
	Tipo              string
	NotificaID        *int64
	Stato             string
	Tentativi         int
	UltimoErrore      string
	ProssimoTentativo time.Time
	InviataIl         *time.Time
	CreataIl          time.Time
}

// ScortaSottoSoglia segnala un prodotto che, post-scarico FIFO, è sceso
// sotto la soglia minima. Usato per emettere notifiche ai magazzinieri.
type ScortaSottoSoglia struct {
	ProdottoID    int64
	ProdottoNome  string
	Rimanente     int
	SogliaMinima  int
}
