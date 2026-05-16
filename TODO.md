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

## 10b. Multi-ruolo `magazziniere+economo`
*Un utente LDAP membro di entrambi i gruppi `LDAP_MAGAZZINIERE_GROUP` e `LDAP_ECONOMO_GROUP` opera su entrambi i domini nella stessa sessione.*

- [x] **Stringa composita in sessione:** `resolveRole` restituisce `"magazziniere+economo"` quando entrambi i flag LDAP sono attivi; la sessione (`sess.Values["role"]`) conserva la stringa intera.
- [x] **Helper `hasRole(roleStr, target string) bool`** in `cmd/server/main.go`: sostituisce ogni check di uguaglianza diretta su `role` con lookup che riconosce la stringa composita.
- [x] **`requireRole` aggiornato:** usa `hasRole` per consentire l'accesso alle rotte sia di magazzino che di economo con un singolo utente composito.
- [x] **`dashboardURL`:** case `"magazziniere+economo"` → `/dashboard/magazzino-economo`.
- [x] **Handler `handleDashboardMagazzinoEconomo`:** carica KPI da entrambi i domini (ordini in coda, scorte sotto soglia, capitoli attivi, spese in approvazione, stanziato anno).
- [x] **Template `dashboard-magazzino-economo.html`** e **`_sidebar-magazzino-economo.html`**: sidebar con sezioni Magazziniere + Cassa Economale, dashboard unificata con sezione ordini e sezione capitoli.
- [x] **Mock dev:** suffisso `.dual` (es. `mario.dual`) attiva entrambi i flag nel blocco mock di `Authenticate()`.
- [x] **Fix UI sidebar composita:** `scrollbar-width: none` su `.ec-side` per eliminare barra grigia da scrollbar Windows sulla sidebar combinata.

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
- [x] **Tabella `capitoli_spesa`:** `(id, anno, codice_peg, descrizione, importo_stanziato, attivo, creato_il)` con `UNIQUE(anno, codice_peg)`.
- [x] **Tabella `spese_economali`:** `(id, utente_username, settore_id, capitolo_id NULL, motivazione, importo_presunto, importo_effettivo NULL, tipo_pagamento, stato, fornitore NULL, data_documento NULL, estremi_documento NULL, note_funzionario, note_economo, funzionario_username, economo_username, data_creazione, data_autorizzazione, data_impegno, data_rendicontazione, data_chiusura)`. Stati: `in_approvazione`, `autorizzata`, `rifiutata_funz`, `impegnata`, `rifiutata_econ`, `rendicontata`, `chiusa`. `tipo_pagamento`: `contanti` | `carta`. CHECK constraints SQL su entrambi gli enum.
- [x] **Tabella `allegati_spesa`:** `(id, spesa_id, filename, mime_type, dimensione, blob_data, caricato_da, caricato_il)` — pezze d'appoggio salvate come BLOB.
- [x] **Tabella `movimenti_cassa`:** `(id, data, tipo, descrizione, importo, riferimento_spesa_id NULL, riferimento_reintegro_id NULL, creato_da)`. Tipi: `anticipazione`, `reintegro`, `uscita`, `restituzione_tesoreria`. CHECK su `tipo`.
- [x] **Tabelle `reintegri` + `reintegro_spese`:** numerazione progressiva annuale dei reintegri (`numero`, `anno`, `data_richiesta`, `data_emissione_mandato NULL`, `importo_totale`, `stato`, `economo_username`) + join verso le spese chiuse incluse nel reintegro. CHECK su `stato` (`bozza`/`inviata`/`liquidata`).
- [x] **Indici:** `idx_spese_economali_stato`, `idx_spese_economali_settore`, `idx_spese_economali_utente`, `idx_movimenti_cassa_data`, `idx_allegati_spesa_spesa`, `idx_reintegro_spese_spesa`. UNIQUE `(anno, codice_peg)` e `(anno, numero)` generano indici impliciti.
- [x] **Migrazione idempotente:** integrata in `migrate()` con `CREATE TABLE IF NOT EXISTS` + `CREATE INDEX IF NOT EXISTS` separati.

### 12.2 Modelli Go
- [x] **Struct:** `CapitoloSpesa`, `SpesaEconomale`, `AllegatoSpesa`, `MovimentoCassa`, `Reintegro`, `ReintegroSpesa` in `internal/models/models.go`. PascalCase + puntatori per nullable + campi joinati appesi (`UtenteEmail`, `SettoreNome`, `CapitoloPEG`, `CapitoloDescr`).
- [x] **Viewmodel:** `CapitoloConSaldi` (stanziato/impegnato/speso/residuo), `RigaGiornaleCassa`, `RigaReintegro`, `SezioneContoGiudiziale`. Shell dichiarate ora; campi popolati dai repo nei prossimi slice.

### 12.3 LDAP & Ruolo Economo
- [x] **Env `LDAP_ECONOMO_GROUP`:** aggiunta in `.env.example`, `internal/config/config.go` e `compose.yml`.
- [x] **`resolveRole()`:** firma estesa a `(isMag, isEco, isFun) string` in `internal/auth/ldap.go` con precedenza `magazziniere > economo > funzionario > user`. `admin` resta gestito da `requireRole`.
- [x] **Mock dev:** suffisso `.economo` mappato al ruolo economo.
- [x] **Aggiornare CLAUDE.md:** sezione *Roles* riscritta con precedenza corretta + env vars + nota sul ruolo economo live.

### 12.4 Repo methods (`internal/database/sqlite.go`)
- [x] **CRUD capitoli:** `CreaCapitolo`, `AggiornaCapitolo`, `DisattivaCapitolo`, `GetCapitoloByID`, `GetCapitoliConSaldi`, `GetCapitoliAttivi`.
- [x] **Workflow spese:** `CreaSpesa`, `AutorizzaSpesa`, `RifiutaSpesaFunz`, `ImpegnaSpesa` (transazione atomica + validazione capienza capitolo), `RifiutaSpesaEcon`, `RendicontaSpesa` (`fornitore` + `data_documento` + `estremi_documento` obbligatori — normativo Corte dei Conti), `ChiudiSpesa` (inserisce riga `movimenti_cassa` tipo `uscita` atomicamente).
- [x] **Liste:** `GetSpeseSettore`, `GetSpeseUtente`, `GetSpeseAll`, `GetSpeseConStato`, `GetAllegatiBySpesa`, `GetAllegatoBlob`, `CreaAllegato`, `EliminaAllegato`.
- [~] **Movimenti cassa:** `GetSaldoCassa` ✓ (saldo on-demand per anno, alimentato da `ChiudiSpesa`); `RegistraAnticipazione`, `RegistraReintegro`, `RegistraRestituzioneTesoreria`, `GetGiornaleCassa(da, a)` — da completare (Fase 4).
- [ ] **Reportistica:** `BuildRichiestaReintegro(periodo)` → `[]RigaReintegro` raggruppate per capitolo; `BuildContoGiudiziale(anno)` → `SezioneContoGiudiziale`.

### 12.5 Handler & Routing (`cmd/server/main.go`)
- [x] **Rotte utente/funzionario:** `GET /spese`, `GET /spese/nuova`, `POST /spese`, `GET /spese/{id}` (scoping ruolo-aware nei handler; economo vede tutte, funzionario vede settore, utente vede proprie).
- [x] **Transizioni:** `POST /spese/{id}/autorizza`, `rifiuta-funz`, `impegna`, `rifiuta-econ`, `rendiconta`, `chiudi` — tutti implementati con access-check, HX-Redirect e validazioni normative.
- [x] **Allegati:** `POST /spese/{id}/allegati`, `GET /spese/{id}/allegati/{aid}`, `POST /spese/{id}/allegati/{aid}/elimina` — MIME whitelist server-side (PDF/JPEG/PNG via `http.DetectContentType`), cap 10 MB, access-check ruolo-aware, `Content-Disposition: attachment`.
- [x] **CRUD capitoli:** `GET /capitoli`, `GET /capitoli/nuovo`, `POST /capitoli`, `GET /capitoli/{id}/edit`, `POST /capitoli/{id}`, `POST /capitoli/{id}/disattiva` (solo economo).
- [x] **Dashboard:** `GET /dashboard-economo` con KPI capitoli attivi, spese in approvazione, totale stanziato + lista capitoli con saldi + ultime spese pending. `GET /dashboard/magazzino-economo` per utenti con ruolo composito.
- [ ] **Reportistica:** `GET /economo/giornale-cassa` (filtro periodo, output HTML/CSV/PDF); `GET /economo/reintegro/nuovo`, `POST /economo/reintegro`, `GET /economo/reintegro/{id}`, `GET /economo/reintegro/{id}/pdf|csv|allegati.zip`; `GET /economo/conto-giudiziale?anno=YYYY` (HTML/PDF/CSV).
- [ ] **Cassa:** `POST /economo/anticipazione`, `POST /economo/restituzione-tesoreria`.
- [x] **Middleware:** `requireRole("economo")` + `hasRole()` sulle rotte capitoli + dashboard; `requireAuth` su `/spese*` con check ownership/settore nei handler. Post-login redirect: `economo` → `/dashboard-economo`, `magazziniere+economo` → `/dashboard/magazzino-economo`.

### 12.6 Notifiche
- [ ] **`EventoSpesa` enum:** nuovo `internal/email/spese.go` con stati `inviata`, `autorizzata`, `rifiutata_funz`, `impegnata`, `rifiutata_econ`, `rendicontata`, `chiusa`.
- [ ] **`BuildSpesaEmail`:** template email coerente con `BuildOrdineEmail` (renderEmailShell + meta righe).
- [ ] **`EmitSpesa`** in `internal/notify`: scrive riga `notifiche` + accoda `email_outbox`, chiama `e.wake()`.
- [ ] **Routing eventi:** nuova → funzionario settore; autorizzata → economi (broadcast); impegno/rifiuto/chiusura → utente richiedente; rendicontata → economi.

### 12.7 Templates (`web/templates/`)
- [x] **`dashboard-economo.html`:** KPI capitoli attivi, spese in approvazione, totale stanziato anno corrente + tabella capitoli con saldi (impegnato/speso/residuo) + lista ultime spese pending. Mancano progress bar capienza, saldo cassa e ultimi reintegri (slice futuro: dipendono da `GetSaldoCassa` / reintegri).
- [x] **`_sidebar-economo.html`**: Dashboard, Capitoli, Spese cliccabili; Giornale cassa, Reintegri, Conto giudiziale come placeholder disabled. **`notifiche-economo.html`** mancante.
- [x] **`_sidebar-magazzino-economo.html`** e **`dashboard-magazzino-economo.html`**: sidebar combinata con sezioni Magazziniere + Cassa Economale, dashboard unificata con KPI da entrambi i domini.
- [x] **Liste:** `spese-utente.html` — template unico ruolo-aware: utente vede proprie, funzionario vede settore, economo/magazziniere+economo vedono tutte. Adeguato anche per il ruolo composito con `or (eq .Role "economo") (eq .Role "magazziniere+economo")`.
- [x] **Dettaglio:** `spesa-detail.html` con sezioni role-aware (campi lifecycle mostrati solo se valorizzati). Ruolo composito supportato.
- [x] **Form:** `spesa-form.html` (creazione: motivazione, importo presunto, tipo pagamento). `spesa-rendiconta-form.html` mancante (slice rendicontazione).
- [x] **Capitoli:** `capitoli.html` e `capitolo-form.html` (anno, codice PEG, descrizione, importo stanziato, toggle attivo in edit).
- [ ] **Report:** `report-giornale-cassa.html`, `report-reintegro.html`, `report-conto-giudiziale.html` (Modello 21).
- [x] **Coerenza UI:** design system `ec-*` riusato, sidebar partial `_sidebar-economo` / `_sidebar-magazzino-economo` parsati per i template economo, topbar bell e drawer mobile inclusi.

### 12.8 Upload allegati
- [x] **Multipart:** `r.ParseMultipartForm(10 << 20)` (limite 10 MB).
- [x] **Whitelist MIME:** `application/pdf`, `image/jpeg`, `image/png` via `http.DetectContentType` server-side.
- [x] **Salvataggio:** BLOB in `allegati_spesa` con `mime_type`, `dimensione`, `filename`, `caricato_da`.
- [x] **Serving:** `Content-Disposition: attachment; filename=...` con verifica accesso (richiedente, funzionario settore, economo, admin). Eliminazione allegato: solo uploader o economo, non ammessa dopo `chiusa`.

### 12.9 Calcolo saldi (real-time)
- [x] **Per capitolo:** `residuo = importo_stanziato − impegnato − speso` calcolato on-demand in `GetCapitoliConSaldi` con subquery correlate (`impegnato = SUM(importo_presunto WHERE stato IN ('impegnata','rendicontata'))`, `speso = SUM(importo_effettivo WHERE stato='chiusa')`).
- [x] **Saldo cassa:** `GetSaldoCassa(anno)` on-demand (SUM entrate − SUM uscite da `movimenti_cassa`). Widget saldo in `dashboard-economo.html`, evidenziato in rosso se negativo.
- [x] **Nessuna materializzazione:** query on-demand confermata, nessun campo cache aggiunto allo schema.

### 12.10 i18n
- [~] **Chiavi `economale.*`** per `it` e `en`: menu sidebar, dashboard, capitoli (lista + form), spese (lista + form + dettaglio + stati), azioni workflow (`economale.azione.*`), errori (`economale.errore.*`), saldo cassa. Mancano: chiavi per intestazioni report giudiziale (Fase 4).

### 12.11 Reportistica giudiziale (Fase 4)
- [ ] **CSV writer** `internal/report/csv.go`: BOM UTF-8 per Excel italiano, separatore `;`, formato numerico `1.234,56`, date `gg/mm/aaaa`.
- [ ] **PDF writer** `internal/report/pdf.go`: libreria Go puro (`gofpdf` o `gopdf`), template Modello 21 per il Conto Giudiziale, intestazione Ente da tabella `impostazioni` (`brand_name`, `branding_logo`).
- [ ] **ZIP allegati** `internal/report/zip.go`: streaming `archive/zip`, naming `pratica-{id}__{filename}`, un ZIP per Richiesta di Reintegro.
- [ ] **Endpoint export:** corretti `Content-Type` e `Content-Disposition: attachment; filename=...`.
- [ ] **Numerazione progressiva annuale** per pratiche e reintegri (resetta al cambio anno).

### 12.12 Test smoke
- [ ] **End-to-end mock LDAP:** utente crea → funzionario autorizza → economo impegna (su capitolo) → utente allega scontrino → utente rendiconta (con fornitore + data + estremi) → economo chiude (con `importo_effettivo`) → economo genera reintegro → export CSV/PDF/ZIP → economo chiude anno con Conto Giudiziale. *Bloccato finché transizioni/allegati/report non sono in piedi; happy-path foundation testato manualmente via curl: login economo → POST /capitoli → POST /spese (utente) → /spese visibile a economo.*
- [ ] **Validazione capienza:** spesa che eccede il residuo capitolo → errore + rollback transazione. *Richiede `ImpegnaSpesa` (slice futuro).*
- [x] **Accessi cross-ruolo:** verifica 403 su rotte economo da utente/funzionario/magazziniere (testato manualmente: `mock.utente` → `/dashboard-economo` = 403, `mario.economo` → `/dashboard/magazzino` = 403).
- [ ] **Coerenza saldo cassa:** somma movimenti `entrate − uscite` deve coincidere con saldo reale dopo ogni transizione.
