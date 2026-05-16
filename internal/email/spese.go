package email

import (
	"fmt"
	"html"
	"strings"
)

// EventoSpesa identifica i tipi di transizione che generano un'email per le spese economali.
type EventoSpesa string

const (
	EventoSpesaInviata       EventoSpesa = "spesa_inviata"
	EventoSpesaAutorizzata   EventoSpesa = "spesa_autorizzata"
	EventoSpesaRifiutataFunz EventoSpesa = "spesa_rifiutata_funz"
	EventoSpesaImpegnata     EventoSpesa = "spesa_impegnata"
	EventoSpesaRifiutataEcon EventoSpesa = "spesa_rifiutata_econ"
	EventoSpesaRendicontata  EventoSpesa = "spesa_rendicontata"
	EventoSpesaChiusa        EventoSpesa = "spesa_chiusa"
)

// SpesaEmailParams aggrega i dati necessari per renderizzare un'email evento spesa.
type SpesaEmailParams struct {
	Evento        EventoSpesa
	SpesaID       int64
	SpesaSettore  string
	Motivazione   string
	Destinatario  string
	Mittente      string
	NoteExtra     string
	BrandName     string
	BrandLogoURL  string
	AppBaseURL    string
}

// BuildSpesaEmail produce (subject, htmlBody) brandizzati per ogni evento spesa economale.
func BuildSpesaEmail(p SpesaEmailParams) (string, string) {
	titolo, sottotitolo, ctaLabel, ctaPath := descrizioneEventoSpesa(p.Evento, p.SpesaID)
	subject := fmt.Sprintf("[%s] %s", brandNameOrDefault(p.BrandName), titolo)
	cta := absLink(p.AppBaseURL, ctaPath)
	notice := ""
	if strings.TrimSpace(p.NoteExtra) != "" {
		notice = fmt.Sprintf(`<tr><td style="padding:14px 24px 0 24px;">
<table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="background:rgba(239,68,68,0.10);border:1px solid rgba(239,68,68,0.32);border-radius:12px;">
<tr><td style="padding:12px 14px;color:#fecaca;font-size:13px;line-height:1.55;">
<strong style="color:#fda4af;">Motivazione:</strong> %s
</td></tr></table></td></tr>`, html.EscapeString(p.NoteExtra))
	}
	body := renderEmailShell(emailShell{
		Subject:      subject,
		Brand:        p.BrandName,
		BrandLogoURL: p.BrandLogoURL,
		AppBaseURL:   p.AppBaseURL,
		Header:       titolo,
		Subtitle:     sottotitolo,
		MetaLines: []emailMeta{
			{"Pratica", fmt.Sprintf("#%d", p.SpesaID)},
			{"Settore", strOrFallback(p.SpesaSettore, "—")},
			{"Oggetto", strOrFallback(p.Motivazione, "—")},
		},
		ExtraNotice: notice,
		CTALabel:    ctaLabel,
		CTAURL:      cta,
	})
	return subject, body
}

func descrizioneEventoSpesa(ev EventoSpesa, spesaID int64) (titolo, sottotitolo, cta, path string) {
	dettaglio := fmt.Sprintf("/spese/%d", spesaID)
	switch ev {
	case EventoSpesaInviata:
		return "Nuova spesa da autorizzare",
			"È stata inviata una nuova richiesta di spesa economale. Apri la dashboard per autorizzarla o rifiutarla.",
			"Apri spesa",
			dettaglio
	case EventoSpesaAutorizzata:
		return "Spesa autorizzata",
			"La spesa economale è stata autorizzata dal funzionario e attende impegno sul capitolo.",
			"Apri spesa",
			dettaglio
	case EventoSpesaRifiutataFunz:
		return "Spesa rifiutata dal funzionario",
			"La tua richiesta di spesa economale è stata rifiutata dal funzionario di settore.",
			"Vedi dettagli",
			dettaglio
	case EventoSpesaImpegnata:
		return "Spesa impegnata",
			"La tua spesa è stata impegnata sul capitolo di bilancio. Procedi con la rendicontazione.",
			"Vai alla spesa",
			dettaglio
	case EventoSpesaRifiutataEcon:
		return "Spesa rifiutata dall'economo",
			"La tua spesa non è stata impegnata dall'economo.",
			"Vedi dettagli",
			dettaglio
	case EventoSpesaRendicontata:
		return "Spesa rendicontata",
			"Una spesa economale è stata rendicontata e attende chiusura.",
			"Apri spesa",
			dettaglio
	case EventoSpesaChiusa:
		return "Spesa chiusa",
			"La tua spesa economale è stata chiusa e l'uscita è stata registrata nel giornale di cassa.",
			"Vedi spesa",
			dettaglio
	}
	return fmt.Sprintf("Aggiornamento spesa #%d", spesaID), "", "Apri E-conomato", "/"
}
