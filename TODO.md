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
- [x] **Tabella notifiche in DB:** `CREATE TABLE notifiche(id, utente_username, tipo, messaggio, ordine_id NULL, prodotto_id NULL, letta BOOL, creata_il)` + indice su `(utente_username, letta, creata_il DESC)`.
- [x] **Emitter centralizzato:** `notify.Emitter.EmitOrdine` / `EmitScorta` invocati dai transition handler `InviaOrdine`, `ApprovaOrdine`, `RifiutaOrdine`, `PreparaOrdineFIFO`, `SegnaOrdinePronte`. `ConsegnaOrdine` non emette (chiusura ciclo).
- [x] **Endpoint UI:** `GET /notifiche` (pagina full + tab Tutte / Non lette / Ordini / Scorte), `POST /notifiche/{id}/letta`, `POST /notifiche/leggi-tutte`, `GET /notifiche/badge` per il poll HTMX.
- [x] **Badge topbar:** partial `_topbar-bell.html` con `hx-trigger="every 60s"` che ricarica il contatore non-lette su tutte le pagine con topbar.
- [x] **Template Email:** `internal/email/orders.go` con `BuildOrdineEmail` (Inviato, Approvato, Rifiutato, In preparazione, Pronto) e `BuildScortaEmail`.
- [x] **Worker Asincrono:** queue durevole `email_outbox` consumata da `internal/notify/worker.go` con backoff esponenziale (60s · 2^n, cap 1h) e abbandono dopo 5 tentativi.
- [x] **Integrazione Trigger:** ogni `EmitOrdine` / `EmitScorta` scrive sempre la riga `notifiche` e accoda un job `email_outbox` solo se `SMTP_SERVER` configurato e l'utente ha un'email.

## 8. Reportistica (Area Magazzino)
*Implementata come tab `/report` magazziniere (vedi §10). Gli aggregati sono renderizzati lato server in HTML; i grafici sono realizzati con il design system `ec-bar` / `ec-legend` invece di Chart.js, sufficiente per i volumi attesi e privo di dipendenze JS aggiuntive.*
- [x] **API Statistiche:** `GetSpesaAnno`, `GetOrdiniEvasiAnno`, `GetTempoMedioEvasioneAnno`, `GetSettoriAttiviAnno`, `GetSpesaMensile`, `GetSpesaPerSettore` in `internal/database/sqlite.go`, consumate dall'handler `handleReportMagazzino`.
- [x] **Grafici dashboard direzionale:** bar chart "Spesa mensile" e legenda "Spesa per settore" in `report-magazzino.html`, riusando le classi `ec-charts` / `ec-bar` / `ec-legend` già presenti nel design system.
- [x] **Esportazione CSV:** endpoint `GET /report.csv?anno=YYYY`, streaming `StreamMovimentiCSV` con BOM UTF-8, separatore `;`, decimali con virgola e date `gg/mm/aaaa` (Excel italiano).

## 9. Deploy e Manutenzione
- [x] **Dockerfile:** Configurare la build multi-stage (builder + runner alpine).
- [x] **Docker Compose:** Definire i volumi per la persistenza del database SQLite.
- [ ] **Script di Backup:** Creare uno script bash per il dump giornaliero del database.
- [ ] **Test di Carico:** Verificare le prestazioni con l'upload di numerose immagini nel DB.

## 10. UI Redesign — Residui dal bundle Claude Design
*Il redesign generale (design system `ec-*`, app-shell sidebar+topbar, login split, dashboard utente/funzionario/magazziniere, anagrafica prodotti) è stato applicato. Restano da implementare le schermate del bundle non ancora cablate al backend.*

- [x] **Schermata Notifiche:** pagina `/notifiche` ruolo-aware (`notifiche-utente`, `notifiche-funzionario`, `notifiche-magazzino`) con tab Tutte / Non lette / Ordini / Scorte, bottone "Segna tutte come lette" e mark-as-read riga per riga via HTMX.
- [x] **Schermata Impostazioni:** endpoint `/impostazioni` con tab Generale/Operatività/Notifiche/Sistema, branding (nome ente + logo) e parametri operativi modificabili dal magazziniere.
- [x] **Modale Anteprima FIFO:** endpoint `GET /ordini/{id}/anteprima-fifo` con `SimulaOrdineFIFO` (non distruttivo, niente scritture su `movimenti_magazzino` o `lotti_acquisto`); partial `fifo-preview-modal` in `dashboard-magazzino.html` aperto dalla card dell'ordine, con tabella prelievi per lotto, costo per riga, totale finale e pulsante "Prepara ora" per confermare il commit reale.
- [x] **Reportistica (Tab Magazziniere):** vedi §8 — pagina `/report` con KPI cards, bar chart mensile, legenda spesa per settore e link `/report.csv?anno=YYYY`.
- [x] **Header carrello — count live:** `renderCarrello` emette uno span OOB (`hx-swap-oob="true"`) su `#cart-badge` ad ogni mutazione (`POST/DELETE /bozza/righe/*`) e l'endpoint `GET /carrello/badge` resta disponibile come fallback per pagine senza il fragment del carrello.
- [x] **Filtri categoria — collegamento backend:** `handleDashboardUtente` ora legge `?cat=<id>` e lo passa a `GetCatalogo`; lo stesso per la variante funzionario.
- [x] **Avatar — colore deterministico per utente:** template func `avatarHue(username)` (FNV-1a → 0..359) applicata come gradiente inline `hsl(h, 65%, 55%) → hsl(h, 70%, 35%)` su tutti gli `.ec-avatar` di sidebar e header notifiche.
- [x] **Vista mobile:** sotto 860px la sidebar diventa un drawer scorrevole (`translateX(-100%) → 0`) attivato da un burger in topbar; partial `_drawer.html` con define `drawer-burger` e `drawer-backdrop` inclusi in tutti i template con app-shell tranne `login`.

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

## 12. Modulo Cassa Economale
*Estensione di E-conomato per la gestione del Fondo Economale (piccole spese in contanti / carta dipartimentale). L'Economo opera come Agente Contabile ai sensi degli artt. 93 e 233 del D.Lgs. 267/2000 (TUEL) e risponde alla Corte dei Conti. Vedi `PLANE.md` § 8 per analisi e requisiti normativi completi.*

### 12.1 Database (Schema)
- [ ] **Tabella `capitoli_spesa`:** `(id, anno, codice_peg, descrizione, importo_stanziato, attivo, creato_il)` con `UNIQUE(anno, codice_peg)`.
- [ ] **Tabella `spese_economali`:** `(id, utente_username, settore_id, capitolo_id NULL, motivazione, importo_presunto, importo_effettivo NULL, tipo_pagamento, stato, fornitore NULL, data_documento NULL, estremi_documento NULL, note_funzionario, note_economo, funzionario_username, economo_username, data_creazione, data_autorizzazione, data_impegno, data_rendicontazione, data_chiusura)`. Stati: `in_approvazione`, `autorizzata`, `rifiutata_funz`, `impegnata`, `rifiutata_econ`, `rendicontata`, `chiusa`. `tipo_pagamento`: `contanti` | `carta`.
- [ ] **Tabella `allegati_spesa`:** `(id, spesa_id, filename, mime_type, dimensione, blob_data, caricato_da, caricato_il)` — pezze d'appoggio salvate come BLOB.
- [ ] **Tabella `movimenti_cassa`:** `(id, data, tipo, descrizione, importo, riferimento_spesa_id NULL, riferimento_reintegro_id NULL, creato_da)`. Tipi: `anticipazione`, `reintegro`, `uscita`, `restituzione_tesoreria`.
- [ ] **Tabelle `reintegri` + `reintegro_spese`:** numerazione progressiva annuale dei reintegri (`numero`, `anno`, `data_richiesta`, `data_emissione_mandato NULL`, `importo_totale`, `stato`, `economo_username`) + join verso le spese chiuse incluse nel reintegro.
- [ ] **Indici:** `(anno, codice_peg)`, `spese_economali(stato)`, `(settore_id)`, `(utente_username)`, `movimenti_cassa(data)`.
- [ ] **Migrazione idempotente:** integrare in `migrate()` (`CREATE TABLE IF NOT EXISTS` + helper `ensureColumn`).

### 12.2 Modelli Go
- [ ] **Struct:** `CapitoloSpesa`, `SpesaEconomale`, `AllegatoSpesa`, `MovimentoCassa`, `Reintegro` in `internal/models/`.
- [ ] **Viewmodel:** `CapitoloConSaldi` (stanziato/impegnato/speso/residuo), `RigaGiornaleCassa`, `RigaReintegro`, `SezioneContoGiudiziale`.

### 12.3 LDAP & Ruolo Economo
- [ ] **Env `LDAP_ECONOMO_GROUP`:** aggiungere in `.env.example` e `internal/config/config.go`.
- [ ] **`resolveRole()`:** estendere in `internal/auth/ldap.go` con precedenza `admin > magazziniere > economo > funzionario > user`.
- [ ] **Mock dev:** username con suffisso `.economo` instrada al ruolo economo.
- [ ] **Aggiornare CLAUDE.md:** documentare il nuovo ruolo nella sezione *Roles* e la precedenza.

### 12.4 Repo methods (`internal/database/sqlite.go`)
- [ ] **CRUD capitoli:** `CreaCapitolo`, `AggiornaCapitolo`, `DisattivaCapitolo`, `GetCapitoliConSaldi`.
- [ ] **Workflow spese:** `CreaSpesa`, `AutorizzaSpesa`, `RifiutaSpesaFunzionario`, `ImpegnaSpesa` (transazione + validazione capienza capitolo), `RifiutaSpesaEconomo`, `AllegaPezzaAppoggio`, `RendicontaSpesa` (richiede `fornitore` + `data_documento` + `estremi_documento` obbligatori), `ChiudiSpesa` (scrive `importo_effettivo`, libera residuo, inserisce riga `movimenti_cassa` di tipo `uscita`).
- [ ] **Liste:** `GetSpeseSettore`, `GetSpeseUtente`, `GetSpeseDaImpegnare`, `GetSpeseDaChiudere`, `GetAllegato`.
- [ ] **Movimenti cassa:** `RegistraAnticipazione`, `RegistraReintegro` (join con spese chiuse del periodo selezionato), `RegistraRestituzioneTesoreria`, `GetSaldoCassa`, `GetGiornaleCassa(da, a)`.
- [ ] **Reportistica:** `BuildRichiestaReintegro(periodo)` → `[]RigaReintegro` raggruppate per capitolo; `BuildContoGiudiziale(anno)` → `SezioneContoGiudiziale`.

### 12.5 Handler & Routing (`cmd/server/main.go`)
- [ ] **Rotte utente/funzionario:** `GET /spese`, `GET /spese/nuova`, `POST /spese`, `GET /spese/{id}`.
- [ ] **Transizioni:** `POST /spese/{id}/autorizza|rifiuta-funz|impegna|rifiuta-econ|rendiconta|chiudi`.
- [ ] **Allegati:** `POST /spese/{id}/allegato`, `GET /spese/{id}/allegato/{aid}` (check accesso ruolo-aware).
- [ ] **CRUD capitoli:** rotte sotto `/capitoli` (solo economo).
- [ ] **Dashboard:** `GET /dashboard-economo`.
- [ ] **Reportistica:** `GET /economo/giornale-cassa` (filtro periodo, output HTML/CSV/PDF); `GET /economo/reintegro/nuovo`, `POST /economo/reintegro`, `GET /economo/reintegro/{id}`, `GET /economo/reintegro/{id}/pdf|csv|allegati.zip`; `GET /economo/conto-giudiziale?anno=YYYY` (HTML/PDF/CSV).
- [ ] **Cassa:** `POST /economo/anticipazione`, `POST /economo/restituzione-tesoreria`.
- [ ] **Middleware:** `requireRole("economo")` su tutte le rotte privilegiate; check ownership/settore nei handler utente/funzionario.

### 12.6 Notifiche
- [ ] **`EventoSpesa` enum:** nuovo `internal/email/spese.go` con stati `inviata`, `autorizzata`, `rifiutata_funz`, `impegnata`, `rifiutata_econ`, `rendicontata`, `chiusa`.
- [ ] **`BuildSpesaEmail`:** template email coerente con `BuildOrdineEmail` (renderEmailShell + meta righe).
- [ ] **`EmitSpesa`** in `internal/notify`: scrive riga `notifiche` + accoda `email_outbox`, chiama `e.wake()`.
- [ ] **Routing eventi:** nuova → funzionario settore; autorizzata → economi (broadcast); impegno/rifiuto/chiusura → utente richiedente; rendicontata → economi.

### 12.7 Templates (`web/templates/`)
- [ ] **`dashboard-economo.html`:** KPI (saldo cassa, capitoli con progress bar capienza, spese da impegnare, spese da chiudere, ultimi reintegri).
- [ ] **`_sidebar-economo.html`** e **`notifiche-economo.html`**.
- [ ] **Liste:** `spese-utente.html`, `spese-funzionario.html`, `spese-economo.html`.
- [ ] **Dettaglio:** `spesa-detail.html` con sezioni role-aware.
- [ ] **Form:** `spesa-form.html` (creazione), `spesa-rendiconta-form.html` (campi obbligatori fornitore / data documento / estremi).
- [ ] **Capitoli:** `capitoli.html` e `capitolo-form.html`.
- [ ] **Report:** `report-giornale-cassa.html`, `report-reintegro.html`, `report-conto-giudiziale.html` (Modello 21).
- [ ] **Coerenza UI:** riusare il design system `ec-*` esistente; sidebar e topbar bell condivisi tramite partial come per il magazziniere.

### 12.8 Upload allegati
- [ ] **Multipart:** `r.ParseMultipartForm(10 << 20)` (limite 10 MB).
- [ ] **Whitelist MIME:** `application/pdf`, `image/jpeg`, `image/png`.
- [ ] **Salvataggio:** BLOB in `allegati_spesa` con `mime_type` e `dimensione`.
- [ ] **Serving:** `Content-Disposition: inline; filename=...` con verifica accesso (richiedente, funzionario settore, economo, admin).

### 12.9 Calcolo saldi (real-time)
- [ ] **Per capitolo:** `residuo = importo_stanziato − impegnato − speso`, dove `impegnato = SUM(importo_presunto WHERE stato IN ('impegnata','rendicontata'))` e `speso = SUM(importo_effettivo WHERE stato='chiusa')`.
- [ ] **Saldo cassa:** `SUM(entrate movimenti_cassa) − SUM(uscite movimenti_cassa)`.
- [ ] **Nessuna materializzazione:** query on-demand, evitare campi cache per non avere disallineamenti.

### 12.10 i18n
- [ ] **Chiavi `economale.*`** in `internal/i18n/messages.go` per tutte le locale (it autoritativa). Coprire: stati, azioni, etichette form, intestazioni report.

### 12.11 Reportistica giudiziale (Fase 4)
- [ ] **CSV writer** `internal/report/csv.go`: BOM UTF-8 per Excel italiano, separatore `;`, formato numerico `1.234,56`, date `gg/mm/aaaa`.
- [ ] **PDF writer** `internal/report/pdf.go`: libreria Go puro (`gofpdf` o `gopdf`), template Modello 21 per il Conto Giudiziale, intestazione Ente da tabella `impostazioni` (`brand_name`, `branding_logo`).
- [ ] **ZIP allegati** `internal/report/zip.go`: streaming `archive/zip`, naming `pratica-{id}__{filename}`, un ZIP per Richiesta di Reintegro.
- [ ] **Endpoint export:** corretti `Content-Type` e `Content-Disposition: attachment; filename=...`.
- [ ] **Numerazione progressiva annuale** per pratiche e reintegri (resetta al cambio anno).

### 12.12 Test smoke
- [ ] **End-to-end mock LDAP:** utente crea → funzionario autorizza → economo impegna (su capitolo) → utente allega scontrino → utente rendiconta (con fornitore + data + estremi) → economo chiude (con `importo_effettivo`) → economo genera reintegro → export CSV/PDF/ZIP → economo chiude anno con Conto Giudiziale.
- [ ] **Validazione capienza:** spesa che eccede il residuo capitolo → errore + rollback transazione.
- [ ] **Accessi cross-ruolo:** verifica 403 su rotte economo da utente/funzionario/magazziniere.
- [ ] **Coerenza saldo cassa:** somma movimenti `entrate − uscite` deve coincidere con saldo reale dopo ogni transizione.
