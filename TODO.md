# Checklist di Sviluppo: Gestionale E-conomato

## 1. Setup Infrastruttura e Base Dati
- [x] **Inizializzazione Repository:** Clonare `gopulley` https://github.com/Provincia-di-Pescara/E-conomato e rimuovere i file demo non necessari.
- [x] **Configurazione Ambiente:** Creare il file `.env` con i parametri LDAP e SMTP.
- [x] **Modulo SQLite:** Implementare la funzione `InitDB()` in `internal/database/sqlite.go`.
- [x] **Script Schema:** Integrare lo script SQL di creazione tabelle nel processo di avvio dell'app.
- [x] **Modelli Go:** Definire le struct in `internal/models/` (Prodotto, Utente, Ordine, Lotto, Movimento).

## 2. Autenticazione LDAP e Gestione Ruoli
- [ ] **Client LDAP:** Configurare la connessione al server in `internal/auth/ldap.go`.
- [ ] **Mapping Utente:** Sviluppare la logica che salva l'utente in SQLite dopo il primo login riuscito.
- [ ] **Gestione Sessioni:** Implementare il sistema di cookie sicuri per mantenere l'accesso.
- [ ] **Middleware RBAC:** Scrivere i filtri per limitare l'accesso alle rotte in base al ruolo (`RequireMagazzino`, `RequireFunzionario`).

## 3. Gestione Catalogo e Magazzino (Area Magazziniere)
- [ ] **CRUD Categorie:** Interfaccia HTMX per creare/modificare le categorie.
- [ ] **Anagrafica Prodotti:** Form per inserimento prodotti con validazione dei campi.
- [ ] **Upload Immagini:** Implementare la conversione del file caricato in `[]byte` (BLOB) per il database.
- [ ] **Visualizzazione Immagini:** Creare l'endpoint HTTP per servire i BLOB dal database al browser.
- [ ] **Carico Merci (Lotti):** Funzionalità per inserire nuovi lotti di acquisto (Quantità + Costo Unitario).
- [ ] **Dashboard Scorte:** Vista HTMX che evidenzia i prodotti sotto la soglia minima.

## 4. Esperienza Utente (Il "Negozio")
- [ ] **Interfaccia Catalogo:** Creare la griglia di prodotti con card, immagini e filtri per categoria.
- [ ] **Gestione Carrello:** Implementare il carrello temporaneo (lato server o sessione).
- [ ] **Componente Badge:** Aggiornamento asincrono del numero articoli nel carrello via HTMX.
- [ ] **Pagina Checkout:** Riepilogo ordine e pulsante di invio richiesta.
- [ ] **Storico Ordini Utente:** Visualizzazione della lista degli ordini effettuati e dei relativi stati.

## 5. Workflow di Approvazione (Area Funzionario)
- [ ] **Dashboard Funzionario:** Elenco ordini filtrati per il settore di competenza dell'utente loggato.
- [ ] **Logica Auto-Approvazione:** Implementare il check: `se utente == responsabile settore -> stato = approvato`.
- [ ] **Modifica Ordine:** Permettere al funzionario di modificare le quantità (solo al ribasso) prima dell'approvazione.
- [ ] **Gestione Rifiuto:** Funzionalità per rifiutare l'ordine con obbligo di inserimento note.

## 6. Motore di Evasione e Algoritmo FIFO
- [ ] **Dashboard Magazziniere:** Vista degli ordini approvati in attesa di preparazione.
- [ ] **Algoritmo FIFO:** Sviluppare la logica Go che scala le quantità dai lotti più vecchi (`ORDER BY data_acquisto ASC`).
- [ ] **Transazione Movimenti:** Garantire l'atomicità dello scarico magazzino e creazione record `movimenti_magazzino`.
- [ ] **Evasione Parziale:** Logica per gestire ordini con merce insufficiente (stato `evasa_parziale`).
- [ ] **Consegna Finale:** Pulsanti per segnare l'ordine come "Pronto" e "Consegnato".

## 7. Notifiche ed Email
- [ ] **Template Email:** Creare i modelli HTML per le notifiche (Ordine Ricevuto, Approvato, Pronto al Ritiro).
- [ ] **Worker Asincrono:** Implementare l'invio delle mail tramite goroutine per non rallentare l'interfaccia utente.
- [ ] **Integrazione Trigger:** Inserire le chiamate di invio mail nei passaggi di stato dell'ordine.

## 8. Reportistica (Area Magazzino)
- [ ] **API Statistiche:** Creare endpoint che restituiscono dati aggregati in JSON (spesa per settore, consumi mensili).
- [ ] **Integrazione Chart.js:** Implementare grafici a torta e a barre nella dashboard direzionale (Magazzino).
- [ ] **Esportazione CSV:** Generazione dinamica di file CSV con lo storico di tutti i movimenti valorizzati.

## 9. Deploy e Manutenzione
- [ ] **Dockerfile:** Configurare la build multi-stage (builder + runner alpine).
- [ ] **Docker Compose:** Definire i volumi per la persistenza del database SQLite.
- [ ] **Script di Backup:** Creare uno script bash per il dump giornaliero del database.
- [ ] **Test di Carico:** Verificare le prestazioni con l'upload di numerose immagini nel DB.