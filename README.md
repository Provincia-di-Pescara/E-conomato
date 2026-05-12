<div align="center">

<img src="web/static/img/logo-icon.svg" alt="E-conomato Logo" width="120" />

# E-conomato

**Sistema Gestionale di Magazzino e Cancelleria per Ente Pubblico**

[![Go](https://img.shields.io/badge/Go-1.24-00ADD8?logo=go)](https://go.dev)
[![HTMX](https://img.shields.io/badge/HTMX-2.0-3D72D7?logo=html5)](https://htmx.org)
[![SQLite](https://img.shields.io/badge/SQLite-embedded-003B57?logo=sqlite)](https://sqlite.org)
[![Docker](https://img.shields.io/badge/Docker-alpine-2496ED?logo=docker)](https://docker.com)

</div>

---

## 🎯 Cos'è E-conomato?
E-conomato è un gestionale web sviluppato su misura per le pubbliche amministrazioni. È progettato per digitalizzare e tracciare le richieste di cancelleria e materiale di consumo interno. 
Consente agli uffici di effettuare ordini, ai funzionari di approvarli e al magazzino di gestire scorte ed evasioni con scarico automatico dei costi tramite logica FIFO.

---

## 🏗️ Architettura e Scelte Tecnologiche
Il sistema è un monolita moderno, leggero e ultra-veloce:
- **Backend**: Go (Golang) - Eseguibile singolo senza dipendenze runtime pesanti.
- **Frontend**: SSR (Server-Side Rendering) potenziato con **HTMX** (nessuna SPA javascript-heavy) per interazioni asincrone fluide.
- **Database**: SQLite embedded - L'intero schema e perfino i BLOB delle immagini dei prodotti risiedono in un singolo file per massimizzare la portabilità e semplificare i backup.
- **Infrastruttura**: Container Docker singolo con integrazione LDAP nativa.

---

## 👥 Ruoli e Permessi
L'autenticazione è centralizzata tramite Active Directory (LDAP). I ruoli sono associati in base ai gruppi e ai settori di appartenenza:
- **Utente Base**: Naviga il catalogo, inserisce richieste nel carrello e visualizza lo storico dei propri ordini.
- **Funzionario**: Approva, riduce le quantità o rifiuta (motivandolo) le richieste del proprio settore di competenza. Le richieste personali dei funzionari sono invece auto-approvate.
- **Magazziniere**: Gestisce l'anagrafica, carica fatture/DDT (creando "lotti" di acquisto), evade le richieste, monitora le scorte minime, conclude il flusso di consegna e accede alla reportistica finanziaria (export CSV e grafici Chart.js).

---

## 🔄 Il Flusso di Lavoro (Iter dell'Ordine)
1. **Creazione**: L'utente fa richiesta dei materiali. L'ordine entra nello stato `in_approvazione`.
2. **Approvazione**: Il funzionario del settore valida e autorizza la richiesta (Stato: `in_preparazione`).
3. **Evasione e Logica FIFO**: Il magazziniere processa l'ordine. Il sistema Go calcola automaticamente i costi scalando le giacenze dai lotti d'acquisto più vecchi (`ORDER BY data_acquisto ASC`), garantendo la tracciabilità finanziaria.
4. **Pronto al Ritiro**: L'ordine viene confezionato e una notifica email avvisa l'utente (Stato: `pronto`).
5. **Consegna**: Al momento del ritiro fisico, l'ordine viene chiuso (Stato: `ritirato`).

---

## 🚀 Roadmap e Checklist di Sviluppo
*(Tratta dal documento `TODO.md` interno)*
- [x] **Setup Iniziale & Database**: Struttura Go, Modelli SQLite, File env.
- [ ] **Autenticazione e RBAC**: Integrazione Active Directory/LDAP e Middleware ruoli.
- [ ] **Catalogo & Magazzino**: CRUD Prodotti e Categorie, upload BLOB, caricamento merce.
- [ ] **Negozio & Carrello**: UI asincrona via HTMX e checkout.
- [ ] **Workflow Funzionari**: Dashboard di approvazione per settore.
- [ ] **Motore di Evasione FIFO**: Logica per il prelievo lotti ed eventuale evasione parziale.
- [ ] **Notifiche Transazionali**: Email asincrone per i cambi di stato dell'ordine.
- [ ] **Reportistica**: Dashboard direzionale, grafici e export CSV a disposizione del Magazziniere.
- [ ] **Deploy**: Configurazione Docker multi-stage e script di backup.

---

## 🐳 Quick Start (Sviluppo & Deploy)

L'applicazione funziona sia con Podman che con Docker.

```bash
# 1) Clona il repository
git clone https://github.com/Provincia-di-Pescara/E-conomato.git
cd E-conomato

# 2) Configura l'ambiente
cp .env.example .env
# Configura i parametri per LDAP e SMTP (per l'ambiente locale di test puoi lasciare LDAP_HOST=mock)

# 3) Esegui il Container
podman compose up -d
# (Oppure: docker compose up -d)
```

L'applicazione sarà immediatamente disponibile su `http://localhost:8080`.
I dati e il database verranno preservati nativamente nel volume specificato o nella cartella `./data`.

---
*Progetto creato per la Provincia di Pescara.*
