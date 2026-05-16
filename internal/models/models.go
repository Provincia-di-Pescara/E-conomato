package models

import "time"

// Settore rappresenta un ufficio o settore dell'ente.
type Settore struct {
	ID                  string
	Nome                string
	FunzionarioUsername string
}

// Utente rappresenta un utente del sistema, sincronizzato con LDAP.
// Ruoli possibili: "user", "funzionario", "magazziniere", "economo", "admin".
// RuoloSecondario è "" per utenti single-role; "economo" quando Ruolo="magazziniere" e l'utente ha entrambi i ruoli.
type Utente struct {
	Username        string
	Email           string
	Ruolo           string
	RuoloSecondario string
	SettoreID       string
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
	SpesaID        *int64
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

// PickFIFO rappresenta un singolo prelievo simulato da un lotto durante
// l'anteprima FIFO di un ordine. Non viene mai persistito: serve solo a
// mostrare al magazziniere quali lotti verrebbero scaricati e a quale costo.
type PickFIFO struct {
	LottoID       int64
	DataAcquisto  time.Time
	QtaPrelevata  int
	CostoUnitario float64
	CostoTotale   float64
}

// RigaAnteprima è la simulazione FIFO di una singola riga ordine.
// Esito: "evasa" | "evasa_parziale" | "in_attesa".
type RigaAnteprima struct {
	RigaID           int64
	ProdottoID       int64
	ProdottoNome     string
	QtaDaEvadere     int
	QtaSimulataEvasa int
	Esito            string
	Picks            []PickFIFO
	CostoRiga        float64
}

// AnteprimaFIFO è il risultato di SimulaOrdineFIFO: snapshot non distruttivo
// dei prelievi che verrebbero eseguiti su un ordine se preparato adesso.
type AnteprimaFIFO struct {
	OrdineID    int64
	Righe       []RigaAnteprima
	TotaleCosto float64
}

// SpesaMese aggrega la spesa (somma costo_totale dei movimenti_magazzino)
// per un singolo mese dell'anno. Mese in 1..12.
type SpesaMese struct {
	Mese  int
	Spesa float64
}

// SpesaSettore aggrega la spesa per un settore in un dato periodo.
type SpesaSettore struct {
	SettoreID   string
	SettoreNome string
	Spesa       float64
}

// ─── Cassa Economale (Fondo Economale) ───────────────────────────────────
// Modulo separato dal magazzino. L'Economo opera come Agente Contabile
// ai sensi degli artt. 93 e 233 del D.Lgs. 267/2000 (TUEL). Vedi PLANE.md §8.

// CapitoloSpesa è un capitolo PEG (Piano Esecutivo di Gestione) annuale.
// Il residuo NON è memorizzato qui: si calcola on-demand come
// importo_stanziato − impegnato − speso (vedi CapitoloConSaldi).
type CapitoloSpesa struct {
	ID               int64
	Anno             int
	CodicePEG        string
	Descrizione      string
	ImportoStanziato float64
	Attivo           bool
	CreatoIl         time.Time
}

// SpesaEconomale è una pratica di spesa del Fondo Economale.
// Stati: 'in_approvazione' | 'autorizzata' | 'rifiutata_funz'
//      | 'impegnata' | 'rifiutata_econ' | 'rendicontata' | 'chiusa'
// TipoPagamento: 'contanti' | 'carta'.
// Fornitore/DataDocumento/EstremiDocumento sono NULL fino alla rendicontazione
// e diventano obbligatori per la chiusura (enforcement lato repo).
type SpesaEconomale struct {
	ID                  int64
	UtenteUsername      string
	SettoreID           string
	CapitoloID          *int64
	Motivazione         string
	ImportoPresunto     float64
	ImportoEffettivo    *float64
	TipoPagamento       string
	Stato               string
	Fornitore           *string
	DataDocumento       *time.Time
	EstremiDocumento    *string
	NoteFunzionario     string
	NoteEconomo         string
	FunzionarioUsername *string
	EconomoUsername     *string
	DataCreazione       time.Time
	DataAutorizzazione  *time.Time
	DataImpegno         *time.Time
	DataRendicontazione *time.Time
	DataChiusura        *time.Time
	// Campi joinati (popolati da GetSpese*).
	UtenteEmail   string
	SettoreNome   string
	CapitoloPEG   string
	CapitoloDescr string
}

// AllegatoSpesa è una pezza d'appoggio (scontrino/ricevuta/fattura) allegata
// a una spesa. Salvata come BLOB nel DB, coerente con prodotti.immagine_blob.
type AllegatoSpesa struct {
	ID         int64
	SpesaID    int64
	Filename   string
	MimeType   string
	Dimensione int64
	BlobData   []byte
	CaricatoDa string
	CaricatoIl time.Time
}

// MovimentoCassa è una riga del Giornale di Cassa.
// Tipi: 'anticipazione' | 'reintegro' | 'uscita' | 'restituzione_tesoreria'.
type MovimentoCassa struct {
	ID                     int64
	Data                   time.Time
	Tipo                   string
	Descrizione            string
	Importo                float64
	RiferimentoSpesaID     *int64
	RiferimentoReintegroID *int64
	CreatoDa               string
}

// Reintegro è una richiesta di reintegro del Fondo, numerata progressivamente
// per anno (UNIQUE(anno, numero)). Stati: 'bozza' | 'inviata' | 'liquidata'.
type Reintegro struct {
	ID                   int64
	Numero               int
	Anno                 int
	DataRichiesta        time.Time
	DataEmissioneMandato *time.Time
	ImportoTotale        float64
	Stato                string
	EconomoUsername      string
}

// ReintegroSpesa è la junction tra reintegri e spese chiuse incluse nel reintegro.
type ReintegroSpesa struct {
	ReintegroID int64
	SpesaID     int64
}

// ─── Viewmodels Cassa Economale ──────────────────────────────────────────

// CapitoloConSaldi è la vista capitolo + saldi calcolati on-demand.
// I tre saldi NON vengono persistiti: ricalcolati ad ogni query.
type CapitoloConSaldi struct {
	CapitoloSpesa
	Impegnato float64
	Speso     float64
	Residuo   float64
}

// RigaGiornaleCassa è una riga del registro cronologico della cassa,
// con saldo progressivo calcolato dal repo durante la SELECT.
type RigaGiornaleCassa struct {
	MovimentoCassa
	NumeroPratica    string
	NumeroReintegro  string
	ImportoEntrata   float64
	ImportoUscita    float64
	SaldoProgressivo float64
}

// RigaReintegro è una riga del report "Richiesta di Reintegro" (raggruppata per PEG).
type RigaReintegro struct {
	CapitoloID       int64
	CodicePEG        string
	DescrizionePEG   string
	SpesaID          int64
	NumeroPratica    string
	Fornitore        string
	Oggetto          string
	DataDocumento    time.Time
	EstremiDocumento string
	Importo          float64
}

// SezioneContoGiudiziale è il riepilogo annuale conforme a Modello 21
// (D.P.R. 194/1996). Vedi PLANE.md §8.8 lettera C.
type SezioneContoGiudiziale struct {
	Anno                  int
	FondoIniziale         float64
	TotaleReintegri       float64
	TotaleSpese           float64
	SaldoFinale           float64
	RestituitoInTesoreria float64
}
