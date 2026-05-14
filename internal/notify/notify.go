// Package notify emette notifiche in-app (tabella `notifiche`) e accoda
// l'eventuale invio email nella outbox durevole (tabella `email_outbox`).
//
// L'Emitter è pensato per essere condiviso da tutti i transition handler.
// Il Worker (worker.go) consuma la outbox in background.
package notify

import (
	"fmt"
	"strings"

	"github.com/Provincia-di-Pescara/e-conomato/internal/config"
	"github.com/Provincia-di-Pescara/e-conomato/internal/database"
	"github.com/Provincia-di-Pescara/e-conomato/internal/email"
	"github.com/Provincia-di-Pescara/e-conomato/internal/logger"
	"github.com/Provincia-di-Pescara/e-conomato/internal/models"
)

// BrandInfo restituisce nome/logoURL del brand corrente. Iniettato dall'app
// così che l'emitter possa rispondere ai cambi runtime (Impostazioni →
// brand_name / branding_logo).
type BrandInfo func() (name, logoURL string)

// Emitter scrive notifiche in-app e accoda email.
type Emitter struct {
	db      *database.DB
	cfg     *config.Config
	brand   BrandInfo
	appBase string
	signal  chan struct{}
}

// NewEmitter costruisce un Emitter. brand può essere nil (default fallback).
func NewEmitter(db *database.DB, cfg *config.Config, brand BrandInfo) *Emitter {
	if brand == nil {
		brand = func() (string, string) { return cfg.BrandName, "" }
	}
	return &Emitter{
		db:      db,
		cfg:     cfg,
		brand:   brand,
		appBase: cfg.AppBaseURL,
		signal:  make(chan struct{}, 1),
	}
}

// Wake è il canale che il worker monitora per sapere che ci sono nuovi job in outbox.
func (e *Emitter) Wake() <-chan struct{} { return e.signal }

func (e *Emitter) wake() {
	select {
	case e.signal <- struct{}{}:
	default:
	}
}

// EventoOrdineParams aggrega ciò che serve a comporre notifica in-app + email.
type EventoOrdineParams struct {
	Tipo           string // valore canonico (ordine_inviato, ordine_approvato, ...)
	OrdineID       int64
	OrdineSettore  string
	Destinatari    []string // username dei destinatari
	Mittente       string   // chi ha innescato la transizione (informativo)
	Messaggio      string   // testo già localizzato per la notifica in-app
	NoteExtra      string   // es. motivazione rifiuto
}

// EmitOrdine genera una notifica in-app per ciascun destinatario e, se SMTP
// è configurato e l'utente ha un'email, accoda l'invio email.
func (e *Emitter) EmitOrdine(p EventoOrdineParams) {
	if p.OrdineID <= 0 || len(p.Destinatari) == 0 {
		return
	}
	brandName, brandLogo := e.brand()
	for _, dest := range dedup(p.Destinatari) {
		if dest == "" {
			continue
		}
		ordineID := p.OrdineID
		notifID, err := e.db.InsertNotifica(models.Notifica{
			UtenteUsername: dest,
			Tipo:           p.Tipo,
			Messaggio:      p.Messaggio,
			OrdineID:       &ordineID,
		})
		if err != nil {
			logger.Error("notify: InsertNotifica utente=%s tipo=%s: %v", dest, p.Tipo, err)
			continue
		}
		if e.cfg.SMTPServer == "" {
			continue
		}
		u, err := e.db.GetUtente(dest)
		if err != nil || strings.TrimSpace(u.Email) == "" {
			continue
		}
		subject, body := email.BuildOrdineEmail(email.OrdineEmailParams{
			Evento:        email.EventoOrdine(p.Tipo),
			OrdineID:      p.OrdineID,
			OrdineSettore: p.OrdineSettore,
			Destinatario:  dest,
			Mittente:      p.Mittente,
			NoteExtra:     p.NoteExtra,
			BrandName:     brandName,
			BrandLogoURL:  brandLogo,
			AppBaseURL:    e.appBase,
		})
		nid := notifID
		if _, err := e.db.EnqueueEmail(models.EmailOutbox{
			Destinatario: u.Email,
			Soggetto:     subject,
			CorpoHTML:    body,
			Tipo:         p.Tipo,
			NotificaID:   &nid,
		}); err != nil {
			logger.Error("notify: EnqueueEmail utente=%s: %v", dest, err)
		}
	}
	e.wake()
}

// EmitScorta notifica i magazzinieri quando un prodotto attraversa scorta_minima.
func (e *Emitter) EmitScorta(s models.ScortaSottoSoglia) {
	utenti, err := e.db.GetUtentiByRuolo("magazziniere")
	if err != nil {
		logger.Error("notify: GetUtentiByRuolo magazziniere: %v", err)
		return
	}
	if len(utenti) == 0 {
		return
	}
	brandName, brandLogo := e.brand()
	messaggio := fmt.Sprintf("Scorta sotto soglia: %s (rimanenti %d, soglia %d)", s.ProdottoNome, s.Rimanente, s.SogliaMinima)
	for _, u := range utenti {
		prodID := s.ProdottoID
		nid, err := e.db.InsertNotifica(models.Notifica{
			UtenteUsername: u.Username,
			Tipo:           "scorta",
			Messaggio:      messaggio,
			ProdottoID:     &prodID,
		})
		if err != nil {
			logger.Error("notify: InsertNotifica scorta utente=%s: %v", u.Username, err)
			continue
		}
		if e.cfg.SMTPServer == "" || strings.TrimSpace(u.Email) == "" {
			continue
		}
		subject, body := email.BuildScortaEmail(s.ProdottoNome, s.Rimanente, s.SogliaMinima, brandName, brandLogo, e.appBase)
		notifID := nid
		if _, err := e.db.EnqueueEmail(models.EmailOutbox{
			Destinatario: u.Email,
			Soggetto:     subject,
			CorpoHTML:    body,
			Tipo:         "scorta",
			NotificaID:   &notifID,
		}); err != nil {
			logger.Error("notify: EnqueueEmail scorta utente=%s: %v", u.Username, err)
		}
	}
	e.wake()
}

// MagazzinieriUsernames restituisce gli username di tutti gli utenti con
// ruolo magazziniere — comodo helper per i transition handler che devono
// notificare a più destinatari simultaneamente.
func (e *Emitter) MagazzinieriUsernames() []string {
	utenti, err := e.db.GetUtentiByRuolo("magazziniere")
	if err != nil {
		logger.Error("notify: lookup magazzinieri: %v", err)
		return nil
	}
	out := make([]string, 0, len(utenti))
	for _, u := range utenti {
		out = append(out, u.Username)
	}
	return out
}

func dedup(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
