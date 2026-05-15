# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

**E-conomato** — internal stationery / warehouse management system for an Italian public entity (Provincia di Pescara). Go monolith with server-side rendered HTML + HTMX, SQLite embedded, LDAP/AD authentication. Module path: `github.com/Provincia-di-Pescara/e-conomato`.

A second module, **Cassa Economale** (Fondo Economale — petty cash for small urgent expenses paid in cash or with a departmental card), is on the roadmap and documented in `PLANE.md` § 8 and `TODO.md` § 12. It introduces a fourth domain role (Economo) acting as **Agente Contabile** under arts. 93 and 233 of the TUEL (D.Lgs. 267/2000), with mandatory reporting toward the Corte dei Conti following Modello 21 (D.P.R. 194/1996). The constraints in that module (mandatory `fornitore` / `data_documento` / `estremi_documento` on rendicontazione, BLOB-stored attachments, real-time capienza on Capitoli di Spesa) are normative and not negotiable — consult `PLANE.md` § 8 before changing them.

Domain language is Italian (entities: `ordini`, `righe_ordine`, `lotti_acquisto`, `movimenti_magazzino`, `settori`, `prodotti`, `categorie`, `utenti`; Cassa Economale adds `capitoli_spesa`, `spese_economali`, `allegati_spesa`, `movimenti_cassa`, `reintegri`, `reintegro_spese`). Keep that vocabulary in code, comments, and UI text; do not translate.

## Build / Run

**The Go toolchain is not available on the host. All Go commands (build, run, `go mod`, `go test`, `go vet`) must run inside the Docker builder image — never invoke `go` directly on the host.**

Local development uses Compose; `compose.override.yml` is picked up automatically and replaces the published image with a local build:

```bash
podman compose up -d --build      # or: docker compose up -d --build
podman compose logs -f e-conomato
podman compose down
```

App listens on `http://localhost:8080`. State (SQLite DB + uploads) is persisted in `./data` (mounted to `/data` in the container).

To run a single Go command inside the builder without bringing up the stack:

```bash
docker run --rm -v "$PWD":/build -w /build golang:1.26-alpine sh -c "apk add --no-cache gcc musl-dev && CGO_ENABLED=1 go <command>"
```

CGO is required (`mattn/go-sqlite3`), so any in-container `go build` needs `gcc` + `musl-dev`.

The version string baked into the binary comes from `-ldflags "-X main.AppVersion=..."` (see Dockerfile). It can also be overridden at runtime via the `APP_VERSION` env var.

## Configuration

All runtime config flows through `internal/config/config.go` from environment variables (loaded via `.env` at startup with `godotenv`). Copy `.env.example` to `.env` before first run. `LDAP_HOST=mock` short-circuits authentication and accepts any credentials — use it for local dev.

Note that `.env.example` documents some upload-related variables (chunked upload, quotas, SHA-256, SMTP) that are scaffolded but not yet exercised by the E-conomato feature set described in `PLANE.md` / `TODO.md`.

## Architecture

### Layout
- `cmd/server/main.go` — entry point, HTTP routing, session middleware, all handlers. Currently a single large file; new domain handlers are still being added here.
- `internal/config` — env-driven `Config` struct.
- `internal/auth/ldap.go` — `Authenticate(username, password, cfg)` returns `(ok, role, err)`. Supports plain LDAP, StartTLS, and LDAPS; falls back to nested-group search (`LDAP_MATCHING_RULE_IN_CHAIN`, OID `1.2.840.113556.1.4.1941`) when `memberOf` is not populated.
- `internal/database/sqlite.go` — opens SQLite with `_journal_mode=WAL&_foreign_keys=on`, runs idempotent `CREATE TABLE IF NOT EXISTS` migrations at startup, exposes all domain queries as methods on `*DB`.
- `internal/models/models.go` — plain structs mirroring the SQL schema.
- `internal/i18n` — `T(locale, key, args...)` + `ResolveLocale(acceptLanguageHeader)`. Default locale is Italian.
- `internal/logger` — leveled logger (`debug`/`info`/`warn`/`error`) controlled by `LOG_LEVEL`.
- `internal/email` — transactional email worker (stub for status-change notifications).
- `web/templates/*.html` — one file per role-specific dashboard plus shared pages (`login`, `dashboard`, `magazzino`, `download`).
- `web/static/{css,js,img}` — assets served under `/static/`.

### Request flow
1. `main.go` registers routes on `http.NewServeMux` using Go 1.22+ method-prefixed patterns (e.g. `"POST /ordini/{id}/approva"`). Path variables come from `r.PathValue("id")`.
2. `requireAuth` / `requireRole(roles...)` are higher-order middlewares. `admin` is implicitly allowed by every `requireRole` check — do not pass `"admin"` explicitly.
3. Templates are parsed once at boot in `App.loadTemplates`. Each template lives in its own `*template.Template`. To render localized output, `App.render` clones the template and re-binds the `t` / `fmtDate` funcs for the request locale, then calls `ExecuteTemplate`. HTMX partials use `App.renderPartial(baseName, partialName, data)`, which executes a named `{{define}}` block from one of the loaded base templates.
4. Sessions are gorilla/sessions cookies (`magazzino-session`). The auth key is derived from `SESSION_SECRET` (zero-padded to 32 bytes) and the encryption key is `sha256(SESSION_SECRET + "-encryption")`. The `Secure` flag honors `X-Forwarded-Proto` so the app works behind a TLS-terminating reverse proxy.
5. HTMX responses commonly use response headers `HX-Redirect`, `HX-Reswap`, etc., instead of full page reloads. Look at `handleLogin` and `renderCarrello` for the canonical patterns.

### Domain model & invariants
- **Roles**: `user`, `funzionario`, `magazziniere`, `economo`, `admin`. Stored in `utenti.ruolo`; assigned from LDAP groups at login (`LDAP_MAGAZZINIERE_GROUP`, `LDAP_ECONOMO_GROUP`, `LDAP_FUNZIONARIO_GROUP`). Precedence in `resolveRole()`: `magazziniere > economo > funzionario > user`. Admin bypass is enforced in `requireRole`, not in the DB and not in `resolveRole`. The `economo` role is the foundation of the Cassa Economale module: schema, repo CRUD minimo e dashboard sono live; transizioni di workflow, allegati, notifiche e report sono in slice successivi.
- **Order state machine** (`ordini.stato`): `in_approvazione` → `approvato` / `in_preparazione` → `pronto` → `ritirato`, plus terminal `rifiutato`. Each `righe_ordine.stato_riga` independently tracks `in_attesa` / `evasa_parziale` / `evasa`.
- **Spesa economale state machine** (`spese_economali.stato`, in roadmap): `in_approvazione` → `autorizzata` → `impegnata` → `rendicontata` → `chiusa`, plus terminal `rifiutata_funz` (funzionario) and `rifiutata_econ` (economo). Closure requires `importo_effettivo` plus the three mandatory fiscal fields (`fornitore`, `data_documento`, `estremi_documento`) — without them the report is rejected by the auditors. This rule is normative (Corte dei Conti) and must be enforced server-side.
- **Capienza Capitolo (Cassa Economale)**: per-capitolo budget is computed real-time as `importo_stanziato − impegnato − speso`, where `impegnato = SUM(importo_presunto WHERE stato IN ('impegnata','rendicontata'))` and `speso = SUM(importo_effettivo WHERE stato='chiusa')`. Do not materialize these aggregates — query on demand to avoid drift. `ImpegnaSpesa` must validate capienza inside the same transaction that assigns the capitolo.
- **Auto-approval**: when a `funzionario` places an order for their own sector, it skips `in_approvazione` and lands directly in `in_preparazione`. This rule must stay in the DB / handler layer — never trust the client.
- **FIFO costing**: stock is decremented from `lotti_acquisto` in `ORDER BY data_acquisto ASC`, and every withdrawal writes one row per lot consumed into `movimenti_magazzino` with `costo_totale = quantita_prelevata * lotto.costo_unitario`. This table is the source of truth for cost reporting — never recompute historic costs from current `lotti_acquisto.costo_unitario`. The whole evasion must run inside a single transaction so the lot decrement and movement insert stay atomic.
- **Funzionario approval rule**: a funzionario can only *decrease* `qta_approvata` relative to `qta_richiesta`. Rejections require a non-empty `note_funzionario`. The same `note_funzionario` / `note_economo` non-empty requirement applies to refusals of spese economali.
- **Saldo cassa**: the economo's running cash balance is `SUM(entrate movimenti_cassa) − SUM(uscite movimenti_cassa)`. Movement types are `anticipazione`, `reintegro`, `uscita`, `restituzione_tesoreria`. Every `ChiudiSpesa` must atomically insert a `movimenti_cassa` row of type `uscita` so the Giornale di Cassa stays consistent with the closed pratiche.
- **Images & attachments**: product photos live as BLOBs in `prodotti.immagine_blob` and are served via `GET /prodotti/{id}/immagine`. Pezze d'appoggio of spese economali follow the same pattern, stored as BLOBs in `allegati_spesa.blob_data` and served by an access-checked endpoint. Multipart uploads are capped at 10 MB in `parseProdottoForm` and the equivalent helper for spese. Do not write images or attachments to disk — keeping them inside the DB is a deliberate choice so the single SQLite file remains the only backup target.

### Translations / UI text
User-visible strings go through `i18n.T`. Italian is authoritative; add new keys to `internal/i18n/messages.go` rather than hard-coding strings in handlers or templates.

## Conventions

- Some scaffolding (config struct, upload session vars, email branding helpers, the `download.html` template) carries fields/code paths that are not yet wired into the E-conomato flows. Prefer extending what is already there over introducing parallel abstractions.
- `internal/database/sqlite.go` follows a "fat repo" style — domain methods on `*DB` like `PreparaOrdineFIFO`, `ApprovaOrdine`, `GetOrdiniSettore`. Put new SQL there, not inline in handlers.
- The current source has no test suite. When adding tests, run them through the Docker builder image (see Build / Run).
- The roadmap and feature backlog live in `TODO.md`; the requirements interview and rationale are in `PLANE.md`. Consult both before changing business logic — many constraints (FIFO, auto-approval, partial fulfillment, BLOB storage, Cassa Economale workflow, mandatory fiscal fields, real-time capienza) come from the customer interview and from public-accounting normative sources, not from the code.
