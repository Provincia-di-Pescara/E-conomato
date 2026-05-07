# Design: Dashboard per Ruolo

**Data:** 2026-05-07
**Progetto:** Gestionale Magazzino
**Stato:** Approvato

---

## Contesto

Il sistema ha 4 ruoli (user, funzionario, magazziniere, admin) ma un'unica dashboard generica che mostra solo i prodotti sotto soglia. L'obiettivo è creare dashboard dedicate per ciascun ruolo, coprendo l'intero workflow degli ordini: creazione → approvazione → evasione.

---

## Approccio: Route separate per ruolo (Opzione B)

Ogni ruolo ha la propria URL e template dedicato. Il redirect post-login instrada automaticamente l'utente alla dashboard corretta.

---

## 1. Routing e Redirect Post-Login

### Redirect per ruolo dopo login

| Ruolo | Redirect |
|---|---|
| user | `/dashboard` |
| funzionario | `/dashboard/funzionario` |
| magazziniere | `/dashboard/magazzino` |
| admin | `/admin` |

Il funzionario che accede direttamente a `/dashboard` viene reindirizzato a `/dashboard/funzionario`.

### Nuove route in `cmd/server/main.go`

| Method | Route | Handler | Ruoli |
|---|---|---|---|
| GET | `/dashboard` | `handleDashboardUtente` | user, funzionario |
| GET | `/dashboard/funzionario` | `handleDashboardFunzionario` | funzionario |
| GET | `/dashboard/magazzino` | `handleDashboardMagazzino` | magazziniere |
| POST | `/ordini/righe/{prodotto_id}` | `handleUpsertRigaBozza` | user, funzionario |
| DELETE | `/ordini/righe/{prodotto_id}` | `handleDeleteRigaBozza` | user, funzionario |
| POST | `/ordini/{id}/invia` | `handleInviaOrdine` | user, funzionario |
| POST | `/ordini/{id}/approva` | `handleApprovaOrdine` | funzionario |
| POST | `/ordini/{id}/rifiuta` | `handleRifiutaOrdine` | funzionario |
| POST | `/ordini/{id}/prepara` | `handlePreparaOrdine` | magazziniere |
| POST | `/ordini/{id}/pronto` | `handleSegnaPronte` | magazziniere |
| POST | `/ordini/{id}/consegna` | `handleConsegnaOrdine` | magazziniere |
| GET | `/ordini` | `handleListOrdini` | user, funzionario, magazziniere |

---

## 2. Modello Dati: Stato `bozza`

Aggiungere `bozza` agli stati validi di `ordini`:

```
bozza → in_approvazione → in_preparazione → pronto → ritirato
                        ↘ rifiutato
```

**Regola auto-approvazione:** se `utente_username == settori.funzionario_username`, l'invio salta `in_approvazione` e va direttamente a `in_preparazione`.

**Regola bozza unica:** un solo ordine in stato `bozza` per utente alla volta. Ogni modifica al carrello (add, update quantità, remove) salva immediatamente nel DB senza azione esplicita dell'utente.

---

## 3. Dashboard Utente Base (`/dashboard`)

### Layout

- **Sinistra (2/3):** Catalogo prodotti con ricerca e filtro per categoria. Card prodotto con immagine, nome, disponibilità, bottone `[+]`.
- **Destra (1/3):** Carrello (ordine bozza). Mostra righe con quantità modificabile, totale prodotti, bottone `[Invia Ordine]`.
- **Sotto:** Lista "I miei ordini" con stati colorati.

### Comportamento HTMX

- `[+]` → `POST /ordini/righe/{prodotto_id}` → aggiorna carrello inline, salva bozza in DB
- Modifica quantità nel carrello → `POST /ordini/righe/{prodotto_id}` con nuova qta
- Rimozione → `DELETE /ordini/righe/{prodotto_id}`
- `[Invia Ordine]` → `POST /ordini/{id}/invia` → redirect o swap stato ordine

### Template

`web/templates/dashboard-utente.html`

---

## 4. Dashboard Funzionario (`/dashboard/funzionario`)

### Layout — 3 tab

1. **Ordini da Approvare** (default, badge con conteggio)
2. **I miei Ordini**
3. **Nuovo Ordine** (stesso catalogo + carrello dell'utente base)

### Tab "Ordini da Approvare"

- Mostra solo ordini `in_approvazione` del `settore_id` del funzionario loggato
- Per ogni ordine: username richiedente, data, lista prodotti con quantità richieste
- Azioni per ordine:
  - `[Approva]` → `POST /ordini/{id}/approva` (approvazione diretta senza modifiche)
  - `[Modifica quantità]` → espansione inline HTMX con input numerici (max = qta_richiesta, non aumentabile), campo note, bottone `[Conferma approvazione]`
  - `[Rifiuta]` → modal con campo note obbligatorio → `POST /ordini/{id}/rifiuta`

### Template

`web/templates/dashboard-funzionario.html`

---

## 5. Dashboard Magazzino (`/dashboard/magazzino`)

### Layout — 4 tab

1. **Ordini da Evadere** (default, badge con conteggio)
2. **Prodotti** → link a `/prodotti` (già esistente)
3. **Categorie** → link a `/categorie` (già esistente)
4. **Carico Merce** → link a `/lotti` (già esistente)

### Tab "Ordini da Evadere"

- Banner in cima: alert scorte minime (HTMX partial, già esiste `/dashboard/scorte`)
- Mostra ordini in stati `approvato`, `in_preparazione`, `pronto` (tutti i settori)
- Per ogni ordine: richiedente, settore, data, righe prodotto con quantità approvate
- Azioni contestuali per stato:
  - `approvato` → `[Inizia preparazione]` → `POST /ordini/{id}/prepara` → esegue FIFO, crea `movimenti_magazzino`, stato → `in_preparazione`
  - `in_preparazione` → `[Segna come Pronto]` → `POST /ordini/{id}/pronto` → stato → `pronto` + email asincrona all'utente
  - `pronto` → `[Consegna effettuata]` → `POST /ordini/{id}/consegna` → stato → `ritirato`

### Algoritmo FIFO (`handlePreparaOrdine`)

Per ogni `riga_ordine` dell'ordine:
1. Query `lotti_acquisto` WHERE `prodotto_id = X` AND `quantita_rimanente > 0` ORDER BY `data_acquisto ASC`
2. Itera lotti finché `qta_approvata` soddisfatta
3. Per ogni lotto usato: inserisce `movimenti_magazzino`, aggiorna `quantita_rimanente` del lotto
4. Aggiorna `qta_evasa` e `stato_riga` sulla `riga_ordine`

### Template

`web/templates/dashboard-magazzino.html`

---

## 6. File da Creare/Modificare

### Nuovi file

- `web/templates/dashboard-utente.html`
- `web/templates/dashboard-funzionario.html`
- `web/templates/dashboard-magazzino.html`

### File modificati

- `cmd/server/main.go` — nuovi handler, nuove route, redirect post-login per ruolo
- `internal/database/sqlite.go` — query per bozza, ordini per settore, FIFO
- `internal/models/models.go` — aggiungere stato `bozza` alla documentazione

---

## 7. Verifica End-to-End

1. Login come `user` → redirect a `/dashboard`, aggiungere prodotti al carrello, verificare salvataggio bozza in DB, inviare ordine
2. Login come `funzionario` → redirect a `/dashboard/funzionario`, vedere ordine `in_approvazione`, approvare con modifica quantità
3. Login come `magazziniere` → redirect a `/dashboard/magazzino`, vedere ordine `approvato`, avviare preparazione, verificare movimenti FIFO nel DB, segnare pronto
4. Verificare auto-approvazione: funzionario crea ordine per se stesso → salta `in_approvazione`, va diretto a `in_preparazione`
5. Verificare rifiuto: funzionario rifiuta con note → ordine passa a `rifiutato`, note salvate
