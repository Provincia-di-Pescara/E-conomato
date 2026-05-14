package email

import (
	"fmt"
	"html"
	"strings"
)

// EventoOrdine identifica i tipi di transizione che generano un'email.
type EventoOrdine string

const (
	EventoOrdineInviato       EventoOrdine = "ordine_inviato"
	EventoOrdineApprovato     EventoOrdine = "ordine_approvato"
	EventoOrdineRifiutato     EventoOrdine = "ordine_rifiutato"
	EventoOrdineInPreparazione EventoOrdine = "ordine_in_preparazione"
	EventoOrdinePronto        EventoOrdine = "ordine_pronto"
)

// OrdineEmailParams aggrega i dati necessari per renderizzare un'email evento ordine.
type OrdineEmailParams struct {
	Evento        EventoOrdine
	OrdineID      int64
	OrdineSettore string
	Destinatario  string
	Mittente      string // chi ha innescato l'evento (es. funzionario approvante)
	NoteExtra     string // es. motivazione rifiuto
	BrandName     string
	BrandLogoURL  string
	AppBaseURL    string
}

// BuildOrdineEmail produce (subject, htmlBody) brandizzati per ogni evento ordine.
func BuildOrdineEmail(p OrdineEmailParams) (string, string) {
	titolo, sottotitolo, ctaLabel, ctaPath := descrizioneEvento(p.Evento, p.OrdineID)
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
			{"Ordine", fmt.Sprintf("#%d", p.OrdineID)},
			{"Settore", strOrFallback(p.OrdineSettore, "—")},
			{"Destinatario", strOrFallback(p.Destinatario, "—")},
		},
		ExtraNotice: notice,
		CTALabel:    ctaLabel,
		CTAURL:      cta,
	})
	return subject, body
}

// BuildScortaEmail produce (subject, htmlBody) per le notifiche scorta.
func BuildScortaEmail(prodottoNome string, rimanente, soglia int, brandName, brandLogoURL, appBaseURL string) (string, string) {
	subject := fmt.Sprintf("[%s] Scorta sotto soglia: %s", brandNameOrDefault(brandName), prodottoNome)
	body := renderEmailShell(emailShell{
		Subject:      subject,
		Brand:        brandName,
		BrandLogoURL: brandLogoURL,
		AppBaseURL:   appBaseURL,
		Header:       "Scorta sotto la soglia minima",
		Subtitle:     fmt.Sprintf("Il prodotto <strong>%s</strong> è sceso sotto la scorta minima impostata.", html.EscapeString(prodottoNome)),
		MetaLines: []emailMeta{
			{"Prodotto", prodottoNome},
			{"Rimanenti", fmt.Sprintf("%d", rimanente)},
			{"Soglia minima", fmt.Sprintf("%d", soglia)},
		},
		CTALabel: "Apri magazzino",
		CTAURL:   absLink(appBaseURL, "/dashboard/magazzino"),
	})
	return subject, body
}

func descrizioneEvento(ev EventoOrdine, ordineID int64) (titolo, sottotitolo, cta, path string) {
	switch ev {
	case EventoOrdineInviato:
		return "Nuovo ordine da approvare",
			"È stato inviato un ordine per il tuo settore. Apri la dashboard per approvarlo o rifiutarlo.",
			"Apri ordine",
			"/dashboard/funzionario"
	case EventoOrdineApprovato:
		return "Ordine approvato",
			"Il tuo ordine è stato approvato ed è in coda al magazzino per la preparazione.",
			"Vedi ordine",
			"/dashboard"
	case EventoOrdineRifiutato:
		return "Ordine rifiutato",
			"Il tuo ordine è stato rifiutato dal funzionario di settore.",
			"Vedi dettagli",
			"/dashboard"
	case EventoOrdineInPreparazione:
		return "Ordine in preparazione",
			"Il magazzino sta preparando il tuo ordine. Riceverai una nuova email quando sarà pronto al ritiro.",
			"Vedi ordine",
			"/dashboard"
	case EventoOrdinePronto:
		return "Ordine pronto al ritiro",
			"Il tuo ordine è pronto. Passa in magazzino per il ritiro.",
			"Vedi ordine",
			"/dashboard"
	}
	return fmt.Sprintf("Aggiornamento ordine #%d", ordineID), "", "Apri E-conomato", "/"
}

// ── email shell condivisa ────────────────────────────────────────────────────

type emailMeta struct {
	Label string
	Value string
}

type emailShell struct {
	Subject      string
	Brand        string
	BrandLogoURL string
	AppBaseURL   string
	Header       string
	Subtitle     string // accetta HTML già escapato
	MetaLines    []emailMeta
	ExtraNotice  string // HTML pronto
	CTALabel     string
	CTAURL       string
}

func renderEmailShell(s emailShell) string {
	brandLabel := brandNameOrDefault(s.Brand)
	logoBlock := ""
	if u := strings.TrimSpace(s.BrandLogoURL); u != "" {
		logoBlock = fmt.Sprintf(`<img src="%s" alt="%s" width="40" height="40" style="display:block;width:40px;height:40px;border-radius:10px;border:1px solid rgba(255,255,255,0.18);object-fit:cover;background:#111827;">`, html.EscapeString(u), html.EscapeString(brandLabel))
	}

	metaRows := strings.Builder{}
	for _, m := range s.MetaLines {
		metaRows.WriteString(fmt.Sprintf(`<tr><td style="padding:8px 0;color:#9ca3b0;font-size:12px;text-transform:uppercase;letter-spacing:.04em;width:120px;">%s</td><td style="padding:8px 0;color:#e8eaf0;font-size:14px;">%s</td></tr>`,
			html.EscapeString(m.Label), html.EscapeString(m.Value)))
	}

	cta := ""
	if s.CTAURL != "" {
		cta = fmt.Sprintf(`<tr><td style="padding:18px 24px 0 24px;">
<a href="%s" style="display:inline-block;padding:12px 20px;border-radius:10px;background:linear-gradient(135deg,#6366f1 0%%,#8b5cf6 100%%);color:#ffffff;font-size:14px;font-weight:700;text-decoration:none;">%s</a>
</td></tr>`, html.EscapeString(s.CTAURL), html.EscapeString(s.CTALabel))
	}

	footerLabel := brandLabel
	if footerLabel != "E-conomato" {
		footerLabel = brandLabel + " - E-conomato"
	}

	return fmt.Sprintf(`<!doctype html>
<html lang="it"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1.0"><title>%s</title></head>
<body style="margin:0;padding:0;background:#0d0f14;color:#e8eaf0;font-family:Inter,Segoe UI,Roboto,Arial,sans-serif;">
<table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="background:#0d0f14;padding:28px 12px;"><tr><td align="center">
<table role="presentation" width="620" cellspacing="0" cellpadding="0" style="max-width:620px;width:100%%;background:#15181f;border:1px solid rgba(255,255,255,0.1);border-radius:18px;overflow:hidden;box-shadow:0 16px 40px rgba(0,0,0,0.45);">
<tr><td style="padding:22px 24px;background:linear-gradient(135deg,#6366f1 0%%,#8b5cf6 100%%);">
<table role="presentation" width="100%%" cellspacing="0" cellpadding="0"><tr>
<td valign="middle" style="font-size:20px;font-weight:700;line-height:1.2;color:#ffffff;">%s</td>
<td align="right" valign="middle" style="width:56px;">%s</td>
</tr></table>
</td></tr>

<tr><td style="padding:26px 24px 12px 24px;">
<p style="margin:0 0 10px 0;font-size:20px;line-height:1.35;font-weight:700;color:#e8eaf0;">%s</p>
<p style="margin:0;color:#9ca3b0;font-size:14px;line-height:1.6;">%s</p>
</td></tr>

<tr><td style="padding:6px 24px 0 24px;">
<table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="background:#1c2030;border:1px solid rgba(255,255,255,0.08);border-radius:12px;">
<tr><td style="padding:14px 16px;"><table role="presentation" width="100%%">%s</table></td></tr>
</table>
</td></tr>

%s

%s

<tr><td style="padding:18px 24px 24px 24px;">
<p style="margin:0;color:#5c6070;font-size:12px;line-height:1.5;">Messaggio automatico inviato da %s. Non rispondere a questa email.</p>
</td></tr>
</table></td></tr></table>
</body></html>`,
		html.EscapeString(s.Subject),
		html.EscapeString(brandLabel),
		logoBlock,
		html.EscapeString(s.Header),
		s.Subtitle,
		metaRows.String(),
		s.ExtraNotice,
		cta,
		html.EscapeString(footerLabel),
	)
}

func brandNameOrDefault(b string) string {
	if s := strings.TrimSpace(b); s != "" {
		return s
	}
	return "E-conomato"
}

func strOrFallback(s, fallback string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return fallback
}

func absLink(base, path string) string {
	if path == "" {
		return ""
	}
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}
