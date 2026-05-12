# Checklist di Sviluppo: Gestionale E-conomato

## 1. Setup Infrastruttura e Base Dati
- [x] **Inizializzazione Repository:** Inizializzare il repository `https://github.com/Provincia-di-Pescara/E-conomato` e rimuovere i file demo non necessari.
- [x] **Configurazione Ambiente:** Creare il file `.env` con i parametri LDAP e SMTP.
- [x] **Modulo SQLite:** Implementare la funzione `InitDB()` in `internal/database/sqlite.go`.
- [x] **Script Schema:** Integrare lo script SQL di creazione tabelle nel processo di avvio dell'app.
- [x] **Modelli Go:** Definire le struct in `internal/models/` (Prodotto, Utente, Ordine, Lotto, Movimento).

## 2. Autenticazione LDAP e Gestione Ruoli
- [x] **Client LDAP:** Configurare la connessione al server in `internal/auth/ldap.go`.
- [x] **Mapping Utente:** Sviluppare la logica che salva l'utente in SQLite dopo il primo login riuscito.
- [x] **Gestione Sessioni:** Implementare il sistema di cookie sicuri per mantenere l'accesso.
- [x] **Middleware RBAC:** Scrivere i filtri per limitare l'accesso alle rotte in base al ruolo (`RequireMagazzino`, `RequireFunzionario`).

## 3. Gestione Catalogo e Magazzino (Area Magazziniere)
- [x] **CRUD Categorie:** Interfaccia HTMX per creare/modificare le categorie.
- [x] **Anagrafica Prodotti:** Form per inserimento prodotti con validazione dei campi.
- [x] **Upload Immagini:** Implementare la conversione del file caricato in `[]byte` (BLOB) per il database.
- [x] **Visualizzazione Immagini:** Creare l'endpoint HTTP per servire i BLOB dal database al browser.
- [x] **Carico Merci (Lotti):** Funzionalità per inserire nuovi lotti di acquisto (Quantità + Costo Unitario).
- [x] **Dashboard Scorte:** Vista HTMX che evidenzia i prodotti sotto la soglia minima.

## 4. Esperienza Utente (Il "Negozio")
- [x] **Interfaccia Catalogo:** Creare la griglia di prodotti con card, immagini e filtri per categoria.
- [x] **Gestione Carrello:** Implementare il carrello temporaneo (lato server o sessione).
- [x] **Componente Badge:** Aggiornamento asincrono del numero articoli nel carrello via HTMX.
- [x] **Pagina Checkout:** Riepilogo ordine e pulsante di invio richiesta.
- [x] **Storico Ordini Utente:** Visualizzazione della lista degli ordini effettuati e dei relativi stati.

## 5. Workflow di Approvazione (Area Funzionario)
- [x] **Dashboard Funzionario:** Elenco ordini filtrati per il settore di competenza dell'utente loggato.
- [x] **Logica Auto-Approvazione:** Implementare il check: `se utente == responsabile settore -> stato = approvato`.
- [x] **Modifica Ordine:** Permettere al funzionario di modificare le quantità (solo al ribasso) prima dell'approvazione.
- [x] **Gestione Rifiuto:** Funzionalità per rifiutare l'ordine con obbligo di inserimento note.

## 6. Motore di Evasione e Algoritmo FIFO
- [x] **Dashboard Magazziniere:** Vista degli ordini approvati in attesa di preparazione.
- [x] **Algoritmo FIFO:** Sviluppare la logica Go che scala le quantità dai lotti più vecchi (`ORDER BY data_acquisto ASC`).
- [x] **Transazione Movimenti:** Garantire l'atomicità dello scarico magazzino e creazione record `movimenti_magazzino`.
- [x] **Evasione Parziale:** Logica per gestire ordini con merce insufficiente (stato `evasa_parziale`).
- [x] **Consegna Finale:** Pulsanti per segnare l'ordine come "Pronto" e "Consegnato".

## 7. Notifiche ed Email
- [ ] **Tabella notifiche in DB:** `CREATE TABLE notifiche(id, utente_username, tipo, messaggio, ordine_id NULL, letta BOOL, creata_il)` + indice su `(utente_username, letta)`.
- [ ] **Emitter centralizzato:** funzione `db.EmitNotifica(utente, tipo, ordineID, msg)` chiamata dai transition handler `InviaOrdine`, `ApprovaOrdine`, `RifiutaOrdine`, `PreparaOrdineFIFO`, `SegnaOrdinePronte`, `ConsegnaOrdine`.
- [ ] **Endpoint UI:** `GET /notifiche` (pagina full + tab Tutte / Non lette / Ordini / Scorte), `POST /notifiche/{id}/letta`, `POST /notifiche/leggi-tutte`.
- [ ] **Badge topbar:** componente con `hx-trigger="every 60s"` che ricarica contatore non-lette.
- [ ] **Template Email:** Modelli HTML per Ordine Ricevuto, Approvato, Pronto al Ritiro, Rifiutato.
- [ ] **Worker Asincrono:** invio mail in goroutine (cablata sull'`internal/email` esistente).
- [ ] **Integrazione Trigger:** ogni emitter produce sia riga DB sia job email se `SMTP_SERVER` configurato.

## 8. Reportistica (Area Magazzino)
- [ ] **API Statistiche:** Creare endpoint che restituiscono dati aggregati in JSON (spesa per settore, consumi mensili).
- [ ] **Integrazione Chart.js:** Implementare grafici a torta e a barre nella dashboard direzionale (Magazzino).
- [ ] **Esportazione CSV:** Generazione dinamica di file CSV con lo storico di tutti i movimenti valorizzati.

## 9. Deploy e Manutenzione
- [x] **Dockerfile:** Configurare la build multi-stage (builder + runner alpine).
- [x] **Docker Compose:** Definire i volumi per la persistenza del database SQLite.
- [ ] **Script di Backup:** Creare uno script bash per il dump giornaliero del database.
- [ ] **Test di Carico:** Verificare le prestazioni con l'upload di numerose immagini nel DB.

## 10. UI Redesign — Residui dal bundle Claude Design
*Il redesign generale (design system `ec-*`, app-shell sidebar+topbar, login split, dashboard utente/funzionario/magazziniere, anagrafica prodotti) è stato applicato. Restano da implementare le schermate del bundle non ancora cablate al backend.*

- [ ] **Schermata Notifiche:** vedi sezione 7 sopra.
- [x] **Schermata Impostazioni:** endpoint `/impostazioni` con tab Generale/Operatività/Notifiche/Sistema, branding (nome ente + logo) e parametri operativi modificabili dal magazziniere.
- [ ] **Modale Anteprima FIFO:** Endpoint che, dato un `ordine_id`, restituisce la simulazione dei prelievi per lotto (senza commit) e il costo totale per settore. Da renderizzare in `ec-modal--wide` aperto dalla card del magazziniere — vedi `magazzino.jsx → FifoModal`.
- [ ] **Reportistica (Tab Magazziniere):** Tab `/report` con KPI (spesa anno, ordini evasi, tempo medio evasione, settori attivi), bar chart "Spesa mensile" e legenda "Spesa per settore" — richiede aggregazioni SQL su `movimenti_magazzino` per mese e per `settore_id`, più export CSV. CSS già pronto (`ec-charts`, `ec-bar`, `ec-legend`).
- [ ] **Header carrello — count live:** Il badge `ec-cart__count` nell'header del carrello è statico (riflette solo il render iniziale). Spostarlo dentro il fragment `#carrello-content` emesso da `renderCarrello` oppure usare un secondo target HTMX (`hx-swap-oob`) per aggiornarlo a ogni mutazione.
- [x] **Filtri categoria — collegamento backend:** `handleDashboardUtente` ora legge `?cat=<id>` e lo passa a `GetCatalogo`; lo stesso per la variante funzionario.
- [ ] **Avatar — colore deterministico per utente:** L'avatar `ec-avatar` usa un gradiente fisso. Generare hue stabile dall'hash dello username per distinguere visivamente gli utenti.
- [ ] **Vista mobile:** La sidebar collassa solo sotto 860px nascondendola. Aggiungere drawer/burger toggle per mobile.

## 11. Quality-of-life — dopo test utente
*Bug e gap di feature emersi durante i test sul campo. La sezione raccoglie sia le fix che sono già state implementate, sia quelle ancora aperte.*

- [x] **Sidebar magazziniere coerente:** estrarre la sidebar in un partial condiviso tra `dashboard-magazzino.html`, `magazzino.html`, `impostazioni.html` e le pagine `lotti/new` / `prodotti/new`, in modo che le voci siano sempre le stesse.
- [x] **Voce attiva sidebar:** la classe `is-active` deve riflettere la sezione realmente aperta, senza incoerenze tra navigazione full-page e parziali HTMX.
- [x] **Tab Categorie senza nidificazione:** il partial di lista categorie ritorna solo il contenuto interno di `#cat-list`, evitando il wrapper duplicato dopo salva/annulla.
- [x] **Carico merce / Nuovo prodotto full-page:** entrambi sono ora pagine vere con `ec-app` e sidebar, accessibili coerentemente da qualunque dashboard magazziniere.
- [x] **Pulsanti +/− nel carrello:** stepper accanto all'input quantità per UX più chiara (delta `±1` gestito server-side, clamp `>=1` e `<=disponibile`).
- [x] **Filtro categoria nel catalogo utente:** vedi sezione 10.
- [x] **Storico ordini settore per il funzionario:** nuova tab "Storico settore" che mostra TUTTI gli ordini del settore (non solo quelli del funzionario).
- [x] **Ufficio attivo visibile in topbar:** pill `ec-topbar__settore` accanto al titolo nelle dashboard utente e funzionario.
- [x] **Logo + "-conomato" allineati:** `.ec-brandmark` ricalibrato (icona e wordmark sulla stessa linea visiva).
- [x] **Nome ente personalizzabile dal magazziniere:** rimosso ogni `"Provincia di Pescara"` hardcoded e introdotta la pagina Impostazioni → tab Generale con `brand_name`, `brand_sub` e upload logo persistenti nella tabella `impostazioni` / `branding_logo`.
- [x] **Prenotazione prodotto esaurito:** se `Disponibile == 0` il prodotto mostra un bottone "Prenota rifornimento" che apre una modale (`/prodotti/{id}/prenota`), aggiunge la riga al carrello con flag `prenotazione`, e il magazziniere la vede taggata nel FIFO (resta `in_attesa` finché non entra un lotto).
- [x] **Icone Font Awesome per categorie e prodotti:** Font Awesome Free 6 self-hosted in `web/static/fa/`, colonne `icona` su `categorie` e `prodotti`, picker con whitelist nei form CRUD.
- [ ] **Utenti multi-ufficio (sviluppo futuro):** introdurre tabella junction `utenti_settori(username, settore_id)`, migrare i record `utenti.settore_id` esistenti, selettore di settore attivo nella topbar e scoping degli ordini sul settore attivo della sessione.
