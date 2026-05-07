# Documento di Analisi, Requisiti e Piano di Progetto Completo
**Progetto:** Sistema Gestionale di Magazzino e Cancelleria per Ente Pubblico
**Stack Tecnologico:** Go, HTMX, SQLite, Docker, Autenticazione LDAP (basato sul repository *gopulley*)

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
* **Analista:** Per il rilascio utilizzeremo Docker e Docker Compose, basandoci sul repository `gopulley` indicato. È disponibile un ambiente per fare i test?
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
* **Amministratore (Admin):**
  * Ha accesso a una dashboard esclusiva per la reportistica finanziaria.
  * Visualizza grafici interattivi sui consumi (suddivisi per centro di costo/settore, andamento temporale mensile e annuale).
  * Può esportare i dati grezzi in formato CSV/Excel per elaborazioni contabili esterne.

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
    ruolo TEXT NOT NULL DEFAULT 'user', -- Può assumere i valori: 'user', 'funzionario', 'magazzino', 'admin'
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