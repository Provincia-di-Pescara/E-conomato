# Documento di Analisi, Requisiti e Piano di Progetto Completo
**Progetto:** E-conomato - Sistema Gestionale di Magazzino e Cancelleria per Ente Pubblico
**Stack Tecnologico:** Go, HTMX, SQLite, Docker, Autenticazione LDAP

---

## 1. Fase di Analisi: Intervista e Raccolta dei Requisiti
La prima fase del progetto è consistita in un'intervista mirata per far emergere tutte le necessità, i casi limite e le logiche di business nascoste dell'ente pubblico. Di seguito è riportato il resoconto completo delle domande dell'Analista e delle risposte del Committente, con le relative deduzioni tecniche.

**Domanda 1: Autenticazione, Utenti e Ruoli**
* **Analista:** Oltre a Utente, Funzionario (approvatore) e Magazziniere, esiste anche un ruolo di Amministratore di sistema? L'assegnazione a questi ruoli e l'appartenenza allo specifico settore/ufficio avverrà tramite i gruppi su LDAP (Active Directory)? La gerarchia di approvazione è legata al settore di appartenenza?
* **Committente:** Sì, esiste l'Amministratore. Sì, utilizzeremo i gruppi LDAP per l'assegnazione dei ruoli. L'utente viene instradato automaticamente al Funzionario del suo specifico settore.
* **Deduzione Tecnica:** Il sistema non avrà bisogno di maschere complesse per la registrazione degli utenti o per il recupero delle password. Al momento del login, l'applicazione interrogherà il server LDAP, verificherà le credenziali, leggerà i gruppi di appartenenza e creerà o aggiornerà il profilo dell'utente nel database locale (SQLite), assegnandogli il ruolo corretto e legandolo al suo settore.

**Domanda 2: Gestione Catalogo, Costi e Magazzino**
* **Analista:** Come dobbiamo gestire il tracciamento dei costi per lotto di acquisto al momento dello scarico della merce? Usiamo la logica FIFO (First In, First Out) oppure il costo medio ponderato? Dove salviamo le fotografie dei prodotti? Il magazzino necessita di alert per le scorte minime? I prodotti saranno divisi in categorie?
* **Committente:** Utilizzeremo la logica FIFO. Le foto andrebbero salvate direttamente nel database per facilitare le procedure di backup. Sì agli alert per le scorte minime se impostate per il singolo prodotto. Sì alle categorie, che saranno gestibili in autonomia dal magazzino.
* **Deduzione Tecnica:** La logica FIFO richiederà una struttura a "Lotti". Quando il magazzino ricarica un prodotto, crea un nuovo lotto con la sua quantità e il suo prezzo unitario. Quando si evade un ordine, il sistema dovrà prelevare le quantità partendo dal lotto più vecchio. Salvare le immagini come `BLOB` in SQLite rende l'intero database un unico file facilmente trasportabile, eliminando i problemi di permessi sulle cartelle condivise nei container Docker.

**Domanda 3: Iter dell'Ordine e Flussi di Lavoro**
* **Analista:** Se il Funzionario modifica un ordine abbassando le quantità, l'ordine va direttamente al magazzino o l'utente deve confermare? È consentita l'evasione parziale dell'ordine se in magazzino manca la merce? Il sistema deve inviare notifiche email ai cambi di stato?
* **Committente:** La modifica va direttamente al magazzino (il Funzionario può solo abbassare le quantità richieste, non aumentarle). L'evasione parziale va bene. Sì alle notifiche email automatiche; l'indirizzo email è desumibile dalle proprietà del dominio LDAP.
* **Integrazione del Committente:** Attenzione, il Funzionario può anche creare ordini di materiale per se stesso.
* **Deduzione Tecnica:** Se il Funzionario ordina per se stesso, il sistema deve implementare una "Regola di Auto-Approvazione", saltando lo stato di attesa e inviando l'ordine direttamente al magazzino. L'evasione parziale richiederà che ogni singola riga dell'ordine abbia un proprio stato (es. "In attesa", "Evasa parzialmente", "Evasa totalmente").

**Domanda 4: Reportistica e Amministrazione**
* **Analista:** È necessario implementare blocchi stringenti sui budget degli uffici o è sufficiente una reportistica a posteriori?
* **Committente:** È sufficiente il report a posteriori. Lo vorrei in formato grafico e consultabile online, magari utilizzando librerie come Chart.js o simili.
* **Deduzione Tecnica:** Salveremo lo storico dei movimenti di magazzino congelando il costo di ogni singola estrazione. Da questa tabella, costruiremo delle API interne in linguaggio Go che aggregheranno i dati per mese e per settore, fornendo i dati formattati in JSON alla libreria Chart.js per disegnare i grafici nella dashboard dell'Amministratore.

**Domanda 5: Infrastruttura e Deploy**
* **Analista:** Per il rilascio utilizzeremo Docker e Docker Compose. È disponibile un ambiente per fare i test?
* **Committente:** Sì, ho tutto il necessario per fare i test.

---

## 2. Architettura del Sistema e Scelte Tecnologiche

Il sistema è basato su un'architettura monolitica moderna, progettata per essere estremamente veloce, sicura e con un consumo di risorse hardware minimo.

1. **Linguaggio Backend (Go / Golang):** Offre prestazioni eccellenti e viene compilato in un singolo file binario. Questo garantisce che l'applicazione contenga già tutto il necessario per l'esecuzione, senza dipendenze esterne complicate (come macchine virtuali Java o interpreti Python/PHP).
2. **Frontend (HTML + HTMX + CSS):** Invece di creare una Single Page Application pesante in React o Angular, le pagine verranno renderizzate lato server in puro HTML. `HTMX` si occuperà di intercettare i click sui pulsanti (es. "Aggiungi al carrello", "Approva ordine") e di aggiornare solo piccole porzioni della pagina in tempo reale comunicando con il server, garantendo un'esperienza utente fluida (simile a un'applicazione moderna) ma con una frazione del codice JavaScript.
3. **Database (SQLite):** Ideale per gestionali interni con volumi di traffico non esasperati. L'intero database, comprensivo dei dati strutturati e delle immagini (salvate in formato BLOB), risiederà in un singolo file sul disco locale. Il backup consisterà nella semplice copia di questo file a caldo (tramite i comandi integrati di backup di SQLite).
4. **Containerizzazione (Docker):** L'applicazione e il database condivideranno un container Docker. Un volume locale sarà montato all'interno del container per garantire la persistenza del file del database SQLite anche in caso di riavvio o aggiornamento dell'applicazione.

---

## 3. Modello dei Ruoli e dei Permessi

Ogni utente che accede al sistema eredita i propri permessi dalle configurazioni della Active Directory (LDAP) dell'ente pubblico.

* **Utente Base:**
  * Può navigare il catalogo dei prodotti (vedendo foto, descrizioni e disponibilità).
  * Può aggiungere i prodotti a un carrello virtuale.
  * Può inviare la richiesta di materiale, che verrà instradata al Funzionario del suo settore.
  * Può visualizzare lo storico dei propri ordini e il loro stato di avanzamento ("In Approvazione", "In Preparazione", "Pronto per il Ritiro", "Ritirato", "Rifiutato").
* **Funzionario (Responsabile di Settore):**
  * Possiede tutti i permessi dell'Utente Base per effettuare ordini personali.
  * Dispone di una dashboard aggiuntiva denominata "Ordini da Approvare".
  * Visualizza unicamente le richieste provenienti dagli utenti assegnati al proprio settore identificato su LDAP.
  * Può rifiutare un ordine inserendo una motivazione.
  * Può approvare un ordine modificando (esclusivamente diminuendo) le quantità richieste dei singoli prodotti.
* **Magazzino (Magazziniere):**
  * Gestisce l'anagrafica delle Categorie e dei Prodotti (inserimento nome, codice, descrizione, caricamento fotografia, definizione della soglia di scorta minima).
  * Inserisce le fatture o i documenti di trasporto (DDT) dei fornitori, creando nuovi "Lotti di acquisto" e incrementando le giacenze.
  * Visualizza gli ordini approvati e li evade fisicamente, potendo effettuare evasioni parziali.
  * Gestisce gli stati finali di consegna e visualizza i cruscotti di allarme per i prodotti sotto la soglia minima di scorta.
  * Accede alla reportistica finanziaria esportabile in CSV/Excel e ai grafici analitici sui consumi (Chart.js).

---

## 4. Logiche di Business e Flussi di Lavoro (Workflow)

Di seguito sono descritte le logiche operative principali che il software dovrà implementare rigidamente nel codice backend.

### A. L'Iter dell'Ordine
1. **Creazione:** L'Utente seleziona i prodotti e clicca su "Invia Ordine". Il sistema legge il `settore_id` dell'utente loggato e associa l'ordine a quel settore.
2. **Auto-Approvazione:** Se l'identificativo LDAP dell'utente corrisponde al responsabile (Funzionario) di quel settore, l'ordine assume immediatamente lo stato `in_preparazione`. Altrimenti, entra in stato `in_approvazione`.
3. **Approvazione Funzionario:** Il Funzionario visualizza l'ordine. Se riduce una quantità da 10 a 5, il sistema salva nel database la richiesta originale (10) e la quantità approvata (5). Al click su "Approva", l'ordine passa a `in_preparazione`.
4. **Evasione Magazzino:** Il Magazziniere visualizza l'ordine. Inizia la preparazione fisica del materiale. Cliccando su "Prepara Merce", si innesca l'algoritmo FIFO. Il Magazziniere clicca su "Segna come Pronto". L'ordine passa allo stato `pronto`.
5. **Notifica e Ritiro:** Il passaggio allo stato `pronto` innesca una funzione asincrona in Go che compone un'email e la invia all'utente. Quando l'utente si reca fisicamente in magazzino per ritirare i beni, il Magazziniere clicca su "Consegna Effettuata". L'ordine passa allo stato definitivo `ritirato`.

### B. Algoritmo Logico FIFO (Gestione Valore Magazzino)
Per garantire una contabilità perfetta, il sistema non ragiona semplicemente sommando i prodotti, ma tenendo traccia dei "Lotti".
Esempio pratico:
* Acquistiamo il Lotto A: 100 Penne a 0,50 Euro l'una.
* Acquistiamo il Lotto B: 100 Penne a 0,60 Euro l'una (i prezzi sono aumentati).
* Se il magazziniere deve evadere un ordine di 150 Penne per il Settore "Affari Legali", il codice Go eseguirà un'iterazione:
  1. Individua il Lotto A (più vecchio). Preleva tutte le 100 Penne disponibili. Calcola il costo: 100 * 0,50 = 50,00 Euro. Azzera la quantità rimanente del Lotto A.
  2. Poiché mancano ancora 50 Penne all'appello, passa al Lotto B. Preleva 50 Penne. Calcola il costo: 50 * 0,60 = 30,00 Euro. Aggiorna la quantità rimanente del Lotto B a 50.
  3. Il sistema salva questi due "Movimenti di Magazzino" separati, associandoli all'ordine e al Settore "Affari Legali", imputando correttamente la spesa esatta di 80,00 Euro.

---

## 5. Schema Strutturale del Database (SQLite)

Il seguente codice SQL rappresenta l'esatta struttura delle tabelle che verranno create automaticamente dall'applicazione Go al suo primo avvio.

```sql
-- TABELLE ANAGRAFICHE BASE (I dati sono sincronizzati con le interrogazioni LDAP)
CREATE TABLE IF NOT EXISTS settori (
    id TEXT PRIMARY KEY, -- Identificativo univoco, es. 'A.1', 'E.4'
    nome TEXT NOT NULL,  -- Nome completo, es. 'Gestione giuridica del Personale'
    funzionario_username TEXT -- Username LDAP del responsabile che ha potere di firma e approvazione
);

CREATE TABLE IF NOT EXISTS utenti (
    username TEXT PRIMARY KEY, -- Identificativo di login da LDAP
    email TEXT, -- Estratta dalle proprietà di dominio per l'invio delle notifiche
    ruolo TEXT NOT NULL DEFAULT 'user', -- Può assumere i valori: 'user', 'funzionario', 'magazzino'
    settore_id TEXT, -- Il settore a cui l'utente appartiene
    FOREIGN KEY(settore_id) REFERENCES settori(id)
);

-- TABELLE CATALOGO PRODOTTI
CREATE TABLE IF NOT EXISTS categorie (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    nome TEXT NOT NULL UNIQUE -- es. 'Cancelleria', 'Toner e Cartucce', 'Informatica'
);

CREATE TABLE IF NOT EXISTS prodotti (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    codice_articolo TEXT UNIQUE, -- Codice interno di riferimento, es. 'C0560'
    nome TEXT NOT NULL,
    descrizione TEXT,
    categoria_id INTEGER,
    scorta_minima INTEGER DEFAULT 0, -- Soglia sotto la quale il sistema evidenzierà il prodotto in rosso
    immagine_blob BLOB, -- Fotografia del prodotto convertita in array di byte (salvataggio diretto nel database)
    FOREIGN KEY(categoria_id) REFERENCES categorie(id)
);

-- TABELLE MAGAZZINO E GESTIONE LOTTI (Per il calcolo FIFO)
CREATE TABLE IF NOT EXISTS lotti_acquisto (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    prodotto_id INTEGER NOT NULL,
    data_acquisto DATETIME DEFAULT CURRENT_TIMESTAMP, -- Data in cui il fornitore ha consegnato la merce
    quantita_iniziale INTEGER NOT NULL, -- Quanti pezzi sono stati acquistati
    quantita_rimanente INTEGER NOT NULL, -- Quanti pezzi sono ancora disponibili per l'evasione in questo specifico lotto
    costo_unitario REAL NOT NULL, -- Il prezzo pagato per il singolo pezzo in questo lotto
    FOREIGN KEY(prodotto_id) REFERENCES prodotti(id)
);

-- TABELLE DEGLI ORDINI E RELATIVE RIGHE
CREATE TABLE IF NOT EXISTS ordini (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    utente_username TEXT NOT NULL, -- Chi ha effettuato la richiesta materiale
    settore_id TEXT NOT NULL, -- Congelamento del settore al momento dell'ordine (utile se l'utente cambia ufficio in futuro)
    data_creazione DATETIME DEFAULT CURRENT_TIMESTAMP,
    stato TEXT NOT NULL, -- Enum: 'in_approvazione', 'approvato', 'in_preparazione', 'pronto', 'ritirato', 'rifiutato'
    note_funzionario TEXT, -- Campo testuale per motivare un eventuale rifiuto o modifica delle quantità
    FOREIGN KEY(utente_username) REFERENCES utenti(username),
    FOREIGN KEY(settore_id) REFERENCES settori(id)
);

CREATE TABLE IF NOT EXISTS righe_ordine (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ordine_id INTEGER NOT NULL,
    prodotto_id INTEGER NOT NULL,
    qta_richiesta INTEGER NOT NULL, -- Quantità inserita nel carrello dall'utente
    qta_approvata INTEGER, -- Quantità confermata dal Funzionario (deve essere sempre minore o uguale alla qta_richiesta)
    qta_evasa INTEGER DEFAULT 0, -- Quantità materialmente consegnata dal magazziniere
    stato_riga TEXT DEFAULT 'in_attesa', -- Enum: 'in_attesa', 'evasa_parziale', 'evasa'
    FOREIGN KEY(ordine_id) REFERENCES ordini(id),
    FOREIGN KEY(prodotto_id) REFERENCES prodotti(id)
);

-- TABELLA STORICO MOVIMENTI E FINANZA (Cruciale per la generazione dei report dell'Amministratore)
CREATE TABLE IF NOT EXISTS movimenti_magazzino (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    riga_ordine_id INTEGER, -- Riferimento alla riga di richiesta che ha scatenato il movimento in uscita
    lotto_id INTEGER NOT NULL, -- Da quale specifico lotto è stata prelevata la merce
    quantita_prelevata INTEGER NOT NULL,
    costo_totale REAL NOT NULL, -- Valore calcolato al momento del prelievo (quantita_prelevata * costo_unitario del lotto_id)
    data_movimento DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(riga_ordine_id) REFERENCES righe_ordine(id),
    FOREIGN KEY(lotto_id) REFERENCES lotti_acquisto(id)
);
```

---

## 8. Estensione Piattaforma — Modulo "Cassa Economale"

### 8.1 Obiettivo del Progetto

Estensione dell'applicativo E-conomato con un nuovo modulo destinato alla gestione del **Fondo Economale**, ovvero delle piccole spese urgenti sostenute in contanti o con carta dipartimentale.

L'obiettivo è dematerializzare e tracciare l'intero ciclo di vita della spesa economale — dalla richiesta alla rendicontazione — estendendo le funzionalità della piattaforma dall'attuale gestione fisica del magazzino alla gestione dei flussi finanziari di modesta entità, in piena conformità con i regolamenti di contabilità dell'Ente e con la normativa nazionale sugli Agenti Contabili.

### 8.2 Inquadramento Normativo

Dal punto di vista normativo, la gestione del Fondo Economale negli Enti Locali è materia particolarmente delicata. L'Economo non è un semplice dipendente che gestisce una "cassa": assume la veste di **Agente Contabile** ai sensi degli artt. **93 e 233 del D.Lgs. 18 agosto 2000 n. 267 (TUEL)** e, in quanto tale, **risponde personalmente alla Corte dei Conti** dell'integrità e della corretta gestione delle somme affidategli.

Il suo rendiconto (la cosiddetta **"Resa del Conto"**) non è una semplice lista di spese, bensì un documento ufficiale che deve seguire schemi precisi (in particolare il **Modello 21 del D.P.R. 31 gennaio 1996 n. 194**), atti a consentire alla Ragioneria dell'Ente l'emissione dei mandati di pagamento a copertura delle anticipazioni erogate.

Conseguentemente, l'applicativo deve:

1. **Raccogliere** tutti i dati formalmente richiesti per la rendicontazione (anche oltre quelli operativi).
2. **Generare** i report giudiziali nei formati attesi dai gestionali della Ragioneria.
3. **Conservare** in forma immodificabile le pezze d'appoggio (scontrini, fatture di cortesia, ricevute fiscali).

Ulteriori requisiti formali sugli originali cartacei (non automatizzabili dal software ma da prevedere in procedura):

- **Scontrini parlanti**: devono riportare il Codice Fiscale o la Partita IVA dell'Ente.
- **Timbro di annullo**: sull'originale cartaceo va apposto il timbro *"Pagato a valere sul fondo economale"* per evitare doppi rimborsi.

### 8.3 Contesto e Vantaggi Attesi

Attualmente, l'applicativo gestisce con successo il flusso logistico (richiesta e scarico di beni fisici). L'introduzione della Cassa Economale permetterà di:

- **Visibilità in tempo reale sui Capitoli di Bilancio (P.E.G.)**: calcolo immediato delle quote stanziate, impegnate e spese.
- **Tracciabilità e Trasparenza**: ogni spesa in contanti o con carta dipartimentale sarà legata al dipendente richiedente, al funzionario autorizzatore e all'economo.
- **Dematerializzazione delle pezze d'appoggio**: archiviazione digitale di scontrini e fatture di cortesia, fondamentale per le verifiche dei Revisori dei Conti.
- **Semplificazione procedurale**: un'unica interfaccia per i dipendenti, sia per la richiesta di cancelleria che per l'anticipo di cassa.
- **Conformità immediata**: produzione automatica della modulistica giudiziale prescritta.

### 8.4 Flusso Operativo (Workflow a 4 fasi)

Il modulo introduce un workflow rigorosamente separato dall'algoritmo FIFO del magazzino fisico:

1. **Richiesta (Utente)** — Il dipendente compila una richiesta a testo libero indicando la motivazione dell'acquisto e un "importo presunto". Stato iniziale: `in_approvazione`.
2. **Autorizzazione (Funzionario)** — Il Responsabile del Settore valuta la congruità della richiesta per il proprio ufficio e la approva (`autorizzata`) o la rifiuta con motivazione (`rifiutata_funz`).
3. **Impegno e Avvallo (Economo)** — L'Economo riceve la pratica, verifica la capienza del relativo Capitolo di Spesa, assegna il codice P.E.G. e autorizza l'anticipo dei contanti (o l'uso della carta dipartimentale). L'importo presunto viene provvisoriamente scalato come "budget impegnato". Stato: `impegnata` (o `rifiutata_econ`).
4. **Rendicontazione e Chiusura (Utente ed Economo)** — A spesa effettuata, l'utente carica lo/gli scontrino/i nel sistema (stato `rendicontata`). L'Economo verifica la pezza d'appoggio, inserisce l'"importo effettivo" e i dati fiscali obbligatori, quindi chiude la pratica (stato `chiusa`), liberando l'eventuale budget residuo sul capitolo.

### 8.5 Dati Obbligatori per la Rendicontazione

Affinché il report sia valido davanti ai revisori dei conti, in fase di rendicontazione la pratica **deve** contenere, oltre all'`importo_effettivo` e all'allegato:

- **Fornitore (Creditore)**: ragione sociale del soggetto che ha venduto il bene/servizio (es. "Ferramenta Rossi Srl").
- **Data del documento di spesa**: data stampata sullo scontrino/fattura (che può differire dalla data in cui l'Economo chiude la pratica a sistema).
- **Estremi del documento**: numero dello scontrino, della ricevuta fiscale o della fattura di cortesia.

L'assenza anche di uno solo di questi tre elementi comporta il **rifiuto del report in sede di controllo**.

### 8.6 Modello dei Ruoli e Sicurezza

L'architettura si appoggia sull'integrazione nativa con Active Directory (LDAP):

- **Utente Base**: può inserire richieste e allegare file solo per le proprie pratiche.
- **Funzionario (Responsabile)**: ha visibilità sulle pratiche del proprio settore per l'approvazione formale.
- **Economo (nuovo ruolo)**: accesso a una dashboard privilegiata e protetta. Ha poteri di movimentazione finanziaria sui capitoli, assegnazione P.E.G., validazione finale, gestione reintegri e produzione della Resa del Conto. Il ruolo viene assegnato automaticamente tramite appartenenza a uno specifico gruppo di sicurezza Active Directory.
- **Amministratore**: implicitamente autorizzato a tutte le rotte del modulo.

Precedenza dei ruoli in `resolveRole()`: `admin > magazziniere > economo > funzionario > user`.

### 8.7 Architettura Tecnica

Il modulo è sviluppato in continuità con lo stack tecnologico esistente, garantendo alte prestazioni e bassi consumi di risorse:

- **Database (SQLite)** — Creazione di entità dedicate (`capitoli_spesa`, `spese_economali`, `allegati_spesa`, `movimenti_cassa`, `reintegri`, `reintegro_spese`) per non inquinare il catalogo prodotti. Il calcolo dei residui di budget avviene in tempo reale tramite query SQL aggregata per evitare disallineamenti del dato. Gli allegati sono salvati come BLOB nel database (coerentemente con le foto prodotti) per mantenere il file SQLite come unico target di backup.
- **Backend (Go)** — Sviluppo delle logiche di validazione, controllo capienza fondi e gestione sicura dell'upload multipart per gli allegati. Le route dell'Economo sono isolate tramite il middleware `requireRole("economo")`.
- **Frontend (HTMX)** — Interfaccia reattiva priva di framework JavaScript pesanti. La dashboard dell'Economo include indicatori visivi (progress bar) per il monitoraggio istantaneo della capienza dei Capitoli di Spesa, il saldo di cassa progressivo e gli stati delle pratiche.
- **Storage Allegati** — Le pezze giustificative sono salvate in tabella `allegati_spesa` (BLOB), servite unicamente tramite endpoint protetto con verifica dei diritti d'accesso (richiedente, funzionario del settore, economo, admin), garantendo privacy e sicurezza dei documenti contabili.
- **Reportistica** — Generazione lato server di **CSV** (BOM UTF-8, separatore `;`, formato italiano per numeri/date) per l'import nei gestionali della Ragioneria (Maggioli, Halley, Sicraweb e simili) e di **PDF** impaginati per la stampa ufficiale e l'archivio cartaceo. Le pratiche allegate a una Richiesta di Reintegro vengono distribuite anche in un singolo **archivio ZIP** generato in streaming.

### 8.8 Reportistica Obbligatoria

Il modulo produce tre tipologie di documenti ufficiali, ciascuno esportabile sia in **CSV** sia in **PDF**:

**A. Giornale di Cassa (Registro Cronologico)**

Serve all'Economo per dimostrare, in ogni momento, quanti soldi fisici ha nel cassetto o sulla carta dipartimentale.

- *Formato*: tabella in ordine cronologico.
- *Colonne*: `Data`, `N. Pratica`, `Tipologia` (Entrata/Uscita), `Descrizione`, `Importo Entrata`, `Importo Uscita`, `Saldo di Cassa Progressivo`.
- *Entrate*: anticipazioni iniziali e reintegri versati dalla Ragioneria.
- *Uscite*: spese chiuse e restituzione finale in Tesoreria.

**B. Richiesta di Reintegro (Raggruppata per PEG)**

Il Fondo Economale non è infinito: periodicamente (a fine mese o quando i contanti scarseggiano), l'Economo chiede alla Ragioneria di "ricaricare" la cassa, presentando il conto di ciò che ha speso.

- *Formato*: tabella raggruppata e totalizzata per `capitoli_spesa`.
- *Struttura*: per ciascun capitolo `Codice PEG / Descrizione`, elenco delle spese chiuse del periodo (`N. Pratica`, `Fornitore`, `Oggetto`, `Documento` con data ed estremi, `Importo`). Totale del singolo capitolo. Totale complessivo del reintegro richiesto.
- *Output*: PDF firmabile + CSV per la creazione massiva dei mandati + ZIP contenente tutti gli allegati delle pratiche incluse (una funzione *"Scarica allegati del Reintegro N. 3"* utile sia all'Economo sia ai revisori).
- *Utilità*: la Ragioneria deve emettere i mandati di pagamento esattamente su quei capitoli per ripristinare le somme inizialmente anticipate.

**C. Conto Giudiziale (Resa del Conto Annuale)**

A fine anno finanziario (31/12), il Fondo va chiuso e i contanti non spesi vanno restituiti alla Tesoreria dell'Ente.

- *Formato*: riepilogo definitivo conforme al **Modello 21 D.P.R. 194/1996**.
- *Struttura*: `Fondo Iniziale (Anticipazione)` + `Totale Reintegri ricevuti durante l'anno` − `Totale Spese sostenute` = `Saldo Finale da versare in Tesoreria`.
- *Output*: PDF impaginato in formato Modello 21 (firma esterna a cura dell'Economo) + CSV di dettaglio.

### 8.9 Fasi di Rilascio (Roadmap)

Per minimizzare l'impatto sugli utenti e garantire un'adozione fluida, l'implementazione segue questo schema:

- **Fase 1** ✅ — Consolidamento della gestione del magazzino fisico (ordini a catalogo). Completata e in produzione a partire da v0.5.
- **Fase 2** ✅ — Schema DB Cassa Economale (`capitoli_spesa`, `spese_economali`, `allegati_spesa`, `movimenti_cassa`, `reintegri`, `reintegro_spese`), ruolo Economo via LDAP, modelli Go, repo methods CRUD capitoli, endpoint dashboard/capitoli protetti da `requireRole("economo")`. Completata in v0.6.0.
- **Fase 3** 🔄 *(in corso — v0.6.x)* — UI operativa: flusso richiesta spesa utente/funzionario, dashboard Economo con KPI e capienza capitoli in tempo reale, dettaglio pratica role-aware, supporto multi-ruolo `magazziniere+economo`. Da completare: transizioni workflow (autorizza/impegna/rendiconta/chiudi), allegati BLOB, notifiche email spese.
- **Fase 4** — Generazione dei tre report ufficiali (Giornale di Cassa, Richiesta di Reintegro, Conto Giudiziale) con export CSV/PDF/ZIP, in modo da rendere la piattaforma immediatamente conforme ai controlli della Corte dei Conti.