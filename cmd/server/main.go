package main

import (
"bytes"
"context"
"crypto/sha256"
"encoding/json"
"fmt"
"html/template"
"image"
_ "image/gif"
_ "image/jpeg"
_ "image/png"
"io"
"net/http"
"os"
"path/filepath"
"regexp"
"sort"
"strconv"
"strings"
"time"

"github.com/HugoSmits86/nativewebp"
"github.com/gorilla/sessions"
"github.com/joho/godotenv"
"golang.org/x/image/draw"
_ "golang.org/x/image/webp"

"github.com/Provincia-di-Pescara/e-conomato/internal/auth"
"github.com/Provincia-di-Pescara/e-conomato/internal/config"
"github.com/Provincia-di-Pescara/e-conomato/internal/database"
"github.com/Provincia-di-Pescara/e-conomato/internal/i18n"
"github.com/Provincia-di-Pescara/e-conomato/internal/logger"
"github.com/Provincia-di-Pescara/e-conomato/internal/models"
"github.com/Provincia-di-Pescara/e-conomato/internal/notify"
)

// normalizeProductImage decodes raw image bytes (jpeg/png/gif/webp), resizes
// so the longest side is at most maxDim pixels, and re-encodes as WebP.
// Returns nil bytes when raw is empty (caller treats as "no image change").
func normalizeProductImage(raw []byte, maxDim int) ([]byte, error) {
if len(raw) == 0 {
return nil, nil
}
src, _, err := image.Decode(bytes.NewReader(raw))
if err != nil {
return nil, fmt.Errorf("formato immagine non supportato: %w", err)
}
b := src.Bounds()
w, h := b.Dx(), b.Dy()
nw, nh := w, h
if w > maxDim || h > maxDim {
if w >= h {
nw = maxDim
nh = h * maxDim / w
} else {
nh = maxDim
nw = w * maxDim / h
}
}
var out image.Image
if nw != w || nh != h {
rgba := image.NewRGBA(image.Rect(0, 0, nw, nh))
draw.CatmullRom.Scale(rgba, rgba.Bounds(), src, b, draw.Over, nil)
out = rgba
} else {
out = src
}
var buf bytes.Buffer
if err := nativewebp.Encode(&buf, out, nil); err != nil {
return nil, fmt.Errorf("encoding webp fallito: %w", err)
}
return buf.Bytes(), nil
}

// AppVersion is injected at build time via -ldflags "-X main.AppVersion=<ver>"
var AppVersion = "unknown"

type App struct {
cfg       *config.Config
db        *database.DB
store     *sessions.CookieStore
templates map[string]*template.Template
notifier  *notify.Emitter
}

const sessionName = "magazzino-session"

// brandLogoSrc risolve il logo aziendale in un URL pronto per src=.
func brandLogoSrc(logo string) string {
if strings.HasPrefix(logo, "http://") || strings.HasPrefix(logo, "https://") {
return logo
}
return "/brand-logo"
}

func (a *App) handleBrandLogo(w http.ResponseWriter, r *http.Request) {
if blob, mime, err := a.db.GetBrandingLogo(); err == nil && len(blob) > 0 {
if mime == "" {
mime = http.DetectContentType(blob)
}
w.Header().Set("Content-Type", mime)
w.Header().Set("Cache-Control", "public, max-age=300")
w.Write(blob) //nolint:errcheck
return
}
if a.cfg.BrandLogoPath == "" {
http.Error(w, "not found", 404)
return
}
logoPath := filepath.Join(filepath.Dir(a.cfg.DBPath), a.cfg.BrandLogoPath)
http.ServeFile(w, r, logoPath)
}

// shouldUseSecureCookie determina se il flag Secure del cookie deve essere true.
func shouldUseSecureCookie(r *http.Request, cfg *config.Config) bool {
if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
return proto == "https"
}
return cfg.SecureCookies
}

// -- Template -----------------------------------------------------------------

func (a *App) loadTemplates(baseDir string) error {
funcs := template.FuncMap{
"brandLogoSrc": brandLogoSrc,
"t":            func(key string, args ...any) string { return i18n.T(i18n.DefaultLocale, key, args...) },
"fmtDate": func(t time.Time) string {
return t.Format("02 Jan 2006 15:04")
},
"initials": initials,
"derefInt": derefInt,
"statoLabel": statoLabel,
"statoTone":  statoTone,
"add":        func(a, b int) int { return a + b },
"sub":        func(a, b int) int { return a - b },
"mul":        func(a, b int) int { return a * b },
"mulFloat":   func(c float64, q int) float64 { return c * float64(q) },
"fmtEUR":     fmtEUR,
"monthLabel": monthLabel,
"percent":    percent,
"avatarHue":  avatarHue,
}

// I template magazziniere riusano la sidebar tramite il partial _sidebar-magazzino.
// Lo associamo a parse-time così che {{template "sidebar-magazzino" .}} risolva.
magazzinoTemplates := map[string]bool{
"magazzino":             true,
"dashboard-magazzino":   true,
"prodotto-form":         true,
"lotto-form":            true,
"impostazioni":          true,
"fornitori":             true,
"fornitore-form":        true,
"acquisti":              true,
"acquisto-detail":       true,
"storico-ordini":        true,
"notifiche-magazzino":   true,
"report-magazzino":      true,
}
sidebarMagazzino := filepath.Join(baseDir, "_sidebar-magazzino.html")
prenotPartial := filepath.Join(baseDir, "_prenotazione-form.html")
topbarBell := filepath.Join(baseDir, "_topbar-bell.html")
drawerPartial := filepath.Join(baseDir, "_drawer.html")
notifBody := filepath.Join(baseDir, "_notifiche-body.html")

names := []string{"login", "dashboard", "magazzino", "dashboard-utente", "dashboard-funzionario", "dashboard-magazzino", "prodotto-form", "lotto-form", "impostazioni", "fornitori", "fornitore-form", "acquisti", "acquisto-detail", "storico-ordini", "notifiche-utente", "notifiche-funzionario", "notifiche-magazzino", "report-magazzino"}
// template senza topbar (e quindi senza bell)
noTopbar := map[string]bool{"login": true, "dashboard": true}
notifiche := map[string]bool{
"notifiche-utente":      true,
"notifiche-funzionario": true,
"notifiche-magazzino":   true,
}
a.templates = make(map[string]*template.Template, len(names))
for _, name := range names {
files := []string{filepath.Join(baseDir, name+".html")}
if magazzinoTemplates[name] {
files = append(files, sidebarMagazzino)
}
// dashboard-utente e dashboard-funzionario possono includere il form di prenotazione
if name == "dashboard-utente" || name == "dashboard-funzionario" {
files = append(files, prenotPartial)
}
if !noTopbar[name] {
files = append(files, topbarBell)
}
// Tutti i template con app-shell (escluso login) caricano il drawer/burger mobile.
if name != "login" {
files = append(files, drawerPartial)
}
if notifiche[name] {
files = append(files, notifBody)
}
tmpl, err := template.New(name + ".html").Funcs(funcs).ParseFiles(files...)
if err != nil {
return fmt.Errorf("parse template %s: %w", name, err)
}
a.templates[name] = tmpl
}
return nil
}

func (a *App) requestLocale(r *http.Request) string {
if r == nil {
return i18n.DefaultLocale
}
return i18n.ResolveLocale(r.Header.Get("Accept-Language"))
}

func (a *App) localizedTemplateFuncs(locale string) template.FuncMap {
return template.FuncMap{
"t":            func(key string, args ...any) string { return i18n.T(locale, key, args...) },
"fmtDate":      func(t time.Time) string { return t.Format("02 Jan 2006 15:04") },
"brandLogoSrc": brandLogoSrc,
"initials":     initials,
"derefInt":     derefInt,
"statoLabel":   statoLabel,
"statoTone":    statoTone,
"add":          func(a, b int) int { return a + b },
"sub":          func(a, b int) int { return a - b },
"mul":          func(a, b int) int { return a * b },
}
}

// initials returns up to 2 uppercase characters from the username,
// for the sidebar avatar. Falls back to "·" if the username is empty.
func initials(s string) string {
s = strings.TrimSpace(s)
if s == "" {
return "·"
}
parts := strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == ' ' || r == '-' || r == '_' })
var out []rune
for _, p := range parts {
if len(out) >= 2 {
break
}
if p == "" {
continue
}
out = append(out, []rune(strings.ToUpper(p))[0])
}
if len(out) == 0 {
out = []rune(strings.ToUpper(s))
if len(out) > 2 {
out = out[:2]
}
}
return string(out)
}

// avatarHue produce un valore Hue (0-359) deterministico dall'username,
// usato come `style="background: hsl(N, ...)"` sugli `ec-avatar` perché ogni
// utente abbia una tinta riconoscibile e stabile tra le pagine. FNV-1a.
func avatarHue(username string) int {
	var h uint32 = 2166136261
	for _, c := range username {
		h ^= uint32(c)
		h *= 16777619
	}
	return int(h % 360)
}

// fmtEUR formatta un importo in EUR con separatore migliaia '.' e decimali ','.
func fmtEUR(v float64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	s := strconv.FormatFloat(v, 'f', 2, 64)
	parts := strings.SplitN(s, ".", 2)
	intPart := parts[0]
	dec := "00"
	if len(parts) == 2 {
		dec = parts[1]
	}
	// inserisci punti di migliaia da destra
	n := len(intPart)
	var b strings.Builder
	for i, ch := range intPart {
		if i > 0 && (n-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(ch)
	}
	sign := ""
	if neg {
		sign = "-"
	}
	return sign + b.String() + "," + dec + " €"
}

// monthLabel restituisce l'etichetta abbreviata italiana del mese (1..12).
func monthLabel(m int) string {
	labels := [...]string{"Gen", "Feb", "Mar", "Apr", "Mag", "Giu", "Lug", "Ago", "Set", "Ott", "Nov", "Dic"}
	if m < 1 || m > 12 {
		return "?"
	}
	return labels[m-1]
}

// percent ritorna v/max*100 arrotondato (clamp 0..100). Se max <= 0 → 0.
func percent(v, max float64) int {
	if max <= 0 {
		return 0
	}
	p := v / max * 100
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return int(p + 0.5)
}

// derefInt dereferences a *int for template comparisons; nil → 0.
func derefInt(p *int) int {
if p == nil {
return 0
}
return *p
}

// statoLabel maps order status codes to human-readable Italian labels.
func statoLabel(stato string) string {
switch stato {
case "in_approvazione":
return "In approvazione"
case "approvato":
return "Approvato"
case "in_preparazione":
return "In preparazione"
case "pronto":
return "Pronto al ritiro"
case "ritirato":
return "Ritirato"
case "rifiutato":
return "Rifiutato"
case "bozza":
return "Bozza"
}
return stato
}

// statoTone maps order status codes to ec-pill tones.
func statoTone(stato string) string {
switch stato {
case "in_approvazione":
return "amber"
case "approvato", "in_preparazione":
return "sky"
case "pronto":
return "teal"
case "ritirato":
return "muted"
case "rifiutato":
return "red"
}
return "neutral"
}

func (a *App) render(w http.ResponseWriter, r *http.Request, name string, data any) {
tmpl, ok := a.templates[name]
if !ok {
http.Error(w, "template not found", 500)
return
}
locale := a.requestLocale(r)
localized, err := tmpl.Clone()
if err != nil {
http.Error(w, "template clone error", 500)
return
}
localized = localized.Funcs(a.localizedTemplateFuncs(locale))
w.Header().Set("Content-Type", "text/html; charset=utf-8")
if err := localized.ExecuteTemplate(w, name+".html", data); err != nil {
logger.Error("render %s: %v", name, err)
}
}

// -- Session helpers -----------------------------------------------------------

func (a *App) getUsername(r *http.Request) string {
sess, _ := a.store.Get(r, sessionName)
u, _ := sess.Values["username"].(string)
return u
}

func (a *App) getRole(r *http.Request) string {
sess, _ := a.store.Get(r, sessionName)
role, _ := sess.Values["role"].(string)
return role
}

// brandInfo aggrega i campi di branding correnti (DB > env > fallback).
type brandInfo struct {
	Name    string
	Sub     string
	LogoSrc string
}

// brand restituisce le info di branding correnti. Le impostazioni in DB vincono
// sull'env; se nulla è valorizzato i template applicano i propri fallback.
func (a *App) brand() brandInfo {
	bi := brandInfo{
		Name:    a.cfg.BrandName,
		LogoSrc: a.cfg.BrandLogoPath,
	}
	if v, ok := a.db.GetImpostazione("brand_name"); ok && v != "" {
		bi.Name = v
	}
	if v, ok := a.db.GetImpostazione("brand_sub"); ok {
		bi.Sub = v
	}
	if has, _ := a.db.HasBrandingLogo(); has {
		bi.LogoSrc = "/brand-logo"
	}
	return bi
}

// viewData arricchisce un payload handler con campi comuni a tutte le viste.
// Aggiunge BrandName / BrandLogo / BrandSub / Version preservando le chiavi
// già impostate dal chiamante.
func (a *App) viewData(r *http.Request, data map[string]any) map[string]any {
	if data == nil {
		data = map[string]any{}
	}
	b := a.brand()
	if _, ok := data["BrandName"]; !ok {
		data["BrandName"] = b.Name
	}
	if _, ok := data["BrandLogo"]; !ok {
		data["BrandLogo"] = b.LogoSrc
	}
	if _, ok := data["BrandSub"]; !ok {
		data["BrandSub"] = b.Sub
	}
	if _, ok := data["Version"]; !ok {
		data["Version"] = AppVersion
	}
	if _, ok := data["NotifNonLette"]; !ok {
		if u := a.getUsername(r); u != "" {
			if n, err := a.db.CountNotificheNonLette(u); err == nil {
				data["NotifNonLette"] = n
			}
		}
	}
	return data
}

// settoreNomeFor restituisce il nome del settore di un utente, "" se non assegnato.
func (a *App) settoreNomeFor(username string) string {
	if username == "" {
		return ""
	}
	nome, _ := a.db.GetSettoreNomeByUsername(username)
	return nome
}

// redirectToLogin emette un redirect verso /login compatibile sia con navigazioni
// normali sia con richieste HTMX (che non seguono 3xx come full-page navigation).
func redirectToLogin(w http.ResponseWriter, r *http.Request) {
if r.Header.Get("HX-Request") == "true" {
w.Header().Set("HX-Redirect", "/login")
w.WriteHeader(http.StatusNoContent)
return
}
http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *App) requireAuth(next http.HandlerFunc) http.HandlerFunc {
return func(w http.ResponseWriter, r *http.Request) {
if a.getUsername(r) == "" {
redirectToLogin(w, r)
return
}
next(w, r)
}
}

// requireRole returns a middleware that allows access only to users with one of the specified roles.
// Admin always has access regardless of the required role list.
func (a *App) requireRole(roles ...string) func(http.HandlerFunc) http.HandlerFunc {
return func(next http.HandlerFunc) http.HandlerFunc {
return func(w http.ResponseWriter, r *http.Request) {
if a.getUsername(r) == "" {
redirectToLogin(w, r)
return
}
role := a.getRole(r)
for _, allowed := range roles {
if role == allowed {
next(w, r)
return
}
}
http.Error(w, "Accesso negato", http.StatusForbidden)
}
}
}

// -- Handlers -----------------------------------------------------------------

func (a *App) dashboardURL(role string) string {
switch role {
case "funzionario":
return "/dashboard/funzionario"
case "magazziniere":
return "/dashboard/magazzino"
default:
return "/dashboard"
}
}

// dashboardURLWithView restituisce l'URL della dashboard del ruolo aggiungendo
// un fragment "#view=<view>" se utile (le dashboard utente/funzionario sono SPA
// con tab data-view che leggono location.hash).
//
// I nomi delle viste differiscono per ruolo:
//   - funzionario: "approva", "storico", "nuovo", "miei"
//   - user:        "catalogo", "ordini"
// `intent` astrae l'intenzione (es. "ordini-personali") e mappa al nome corretto.
func (a *App) dashboardURLWithView(role, intent string) string {
	base := a.dashboardURL(role)
	if intent == "" {
		return base
	}
	var view string
	switch role {
	case "funzionario":
		switch intent {
		case "ordini-personali", "miei":
			view = "miei"
		case "nuovo":
			view = "nuovo"
		case "storico":
			view = "storico"
		case "approva":
			view = "approva"
		}
	case "user":
		switch intent {
		case "ordini-personali", "ordini", "miei":
			view = "ordini"
		case "catalogo":
			view = "catalogo"
		}
	}
	if view == "" {
		return base
	}
	return base + "#view=" + view
}

// hxRedirect emette un redirect coerente con HTMX (HX-Redirect) o con la
// navigazione classica (303). Preserva i fragment "#..." anche per le
// richieste HTMX.
func hxRedirect(w http.ResponseWriter, r *http.Request, url string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", url)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

// GET /
func (a *App) handleRoot(w http.ResponseWriter, r *http.Request) {
if a.getUsername(r) != "" {
http.Redirect(w, r, a.dashboardURL(a.getRole(r)), http.StatusSeeOther)
return
}
http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// GET /login
func (a *App) handleLoginPage(w http.ResponseWriter, r *http.Request) {
a.render(w, r, "login", a.viewData(r, map[string]any{
"MockMode": a.cfg.LDAPHost == "mock",
}))
}

// POST /login
func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
if err := r.ParseForm(); err != nil {
http.Error(w, "bad request", 400)
return
}
username := strings.TrimSpace(r.FormValue("username"))
password := r.FormValue("password")

logger.Debug("login request for: %s", username)

ok, role, err := auth.Authenticate(username, password, a.cfg)
if err != nil {
w.Header().Set("HX-Reswap", "outerHTML")
if strings.Contains(err.Error(), "not in required group") || strings.Contains(err.Error(), "not found under base DN") {
logger.Warn("login failed for %s: unauthorized", username)
a.renderError(w, r, "login.err_invalid")
return
}
logger.Error("ldap error for %s: %v", username, err)
a.renderError(w, r, "login.err_ldap")
return
}
if !ok {
logger.Warn("login failed for %s: invalid credentials", username)
w.Header().Set("HX-Reswap", "outerHTML")
a.renderError(w, r, "login.err_invalid")
return
}

// Sync user to SQLite (email not available from LDAP bind-only flow; left empty on first login)
if err := a.db.UpsertUtente(username, "", role); err != nil {
logger.Error("upsert utente %s: %v", username, err)
}

logger.Info("login successful: %s (role: %s)", username, role)

sess, _ := a.store.Get(r, sessionName)
sess.Values["username"] = username
sess.Values["role"] = role
sess.Options = &sessions.Options{
Path:     "/",
MaxAge:   86400 * a.cfg.LoginSessionTTLHours,
HttpOnly: true,
SameSite: http.SameSiteLaxMode,
Secure:   shouldUseSecureCookie(r, a.cfg),
}
if err := sess.Save(r, w); err != nil {
logger.Error("session save: %v", err)
}

w.Header().Set("HX-Redirect", a.dashboardURL(role))
w.WriteHeader(http.StatusOK)
}

func (a *App) renderError(w http.ResponseWriter, r *http.Request, msgKey string, args ...any) {
msg := i18n.T(a.requestLocale(r), msgKey, args...)
fmt.Fprintf(w, `<p id="login-error" class="ec-auth__error">%s</p>`, template.HTMLEscapeString(msg))
}

// POST /logout
// Pubblico: chiunque può chiamarlo per liberarsi della sessione. Idempotente:
// se non c'è sessione attiva, redirige comunque a /login senza errore.
func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
sess, _ := a.store.Get(r, sessionName)
// Svuota i valori e forza la scadenza del cookie con Path "/" (stesso scope
// usato in handleLogin), così il browser lo elimina davvero.
for k := range sess.Values {
delete(sess.Values, k)
}
sess.Options = &sessions.Options{
Path:     "/",
MaxAge:   -1,
HttpOnly: true,
SameSite: http.SameSiteLaxMode,
Secure:   shouldUseSecureCookie(r, a.cfg),
}
if err := sess.Save(r, w); err != nil {
logger.Error("logout session save: %v", err)
}
if r.Header.Get("HX-Request") == "true" {
w.Header().Set("HX-Redirect", "/login")
w.WriteHeader(http.StatusNoContent)
return
}
http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// GET /dashboard
func (a *App) handleDashboard(w http.ResponseWriter, r *http.Request) {
scorte, err := a.db.GetProdottiSottoSoglia()
if err != nil {
logger.Error("dashboard scorte: %v", err)
scorte = nil
}
a.render(w, r, "dashboard", a.viewData(r, map[string]any{
"Username": a.getUsername(r),
"Role":     a.getRole(r),
"Scorte":   scorte,
}))
}

// GET /dashboard — utente/funzionario landing
func (a *App) handleDashboardUtente(w http.ResponseWriter, r *http.Request) {
	if a.getRole(r) == "funzionario" {
		http.Redirect(w, r, "/dashboard/funzionario", http.StatusSeeOther)
		return
	}
	username := a.getUsername(r)
	bozza, _ := a.db.GetBozzaConRighe(username)
	categorie, _ := a.db.GetAllCategorie()
	catID, _ := strconv.ParseInt(r.URL.Query().Get("cat"), 10, 64)
	prodotti, _ := a.db.GetCatalogo(r.URL.Query().Get("q"), catID)
	ordini, _ := a.db.GetOrdiniUtente(username)
	a.render(w, r, "dashboard-utente", a.viewData(r, map[string]any{
		"Username":    username,
		"Role":        a.getRole(r),
		"Bozza":       bozza,
		"Categorie":   categorie,
		"CategoriaID": catID,
		"Prodotti":    prodotti,
		"Ordini":      ordini,
		"SettoreNome": a.settoreNomeFor(username),
	}))
}

// GET /carrello/badge — restituisce solo lo span del badge del carrello,
// utile come fallback HTMX (es. pagine senza il fragment principale del
// carrello) o per refresh manuale del contatore.
func (a *App) handleCartBadge(w http.ResponseWriter, r *http.Request) {
	username := a.getUsername(r)
	bozza, _ := a.db.GetBozzaConRighe(username)
	righeCount := 0
	if bozza != nil {
		righeCount = len(bozza.Righe)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if righeCount == 0 {
		fmt.Fprint(w, `<span id="cart-badge" class="ec-cart__count" style="display:none"></span>`)
		return
	}
	fmt.Fprintf(w, `<span id="cart-badge" class="ec-cart__count">%d prodotti</span>`, righeCount)
}

// renderCarrello writes an HTMX-swappable cart fragment that fits the
// ec-cart design system. The host template owns the <aside class="ec-cart">
// wrapper and the static head; this fragment replaces the inner body+foot
// (#{targetID}). Appende anche un fragment OOB per aggiornare il contatore
// `#cart-badge` mostrato nell'header del carrello.
func (a *App) renderCarrello(w http.ResponseWriter, targetID string, bozza *models.OrdineConRighe) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	righeCount := 0
	if bozza != nil {
		righeCount = len(bozza.Righe)
	}
	if righeCount == 0 {
		fmt.Fprintf(w, `<div id="%s">
  <div class="ec-cart__empty">
    <div class="ec-cart__empty-icon">
      <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M3 4h2.5l2.5 11h11l2-8H6.5"/><path d="M9 21a1.5 1.5 0 1 0 0-3 1.5 1.5 0 0 0 0 3Z"/><path d="M18 21a1.5 1.5 0 1 0 0-3 1.5 1.5 0 0 0 0 3Z"/></svg>
    </div>
    <div class="ec-cart__empty-title">Carrello vuoto</div>
    <div>Aggiungi prodotti dal catalogo</div>
  </div>
</div>`, targetID)
		fmt.Fprint(w, `<span id="cart-badge" hx-swap-oob="true" class="ec-cart__count" style="display:none"></span>`)
		return
	}
	totalPz := 0
	for _, r := range bozza.Righe {
		totalPz += r.QtaRichiesta
	}
	fmt.Fprintf(w, `<div id="%s"><div class="ec-cart__body">`, targetID)
	for _, r := range bozza.Righe {
		prenotPill := ""
		if r.Prenotazione {
			prenotPill = `<span class="ec-pill ec-pill--amber" style="margin-left:4px;font-size:10px">Prenot.</span>`
		}
		fmt.Fprintf(w,
			`<div class="ec-cart__row">
  <div class="ec-prod-img ec-prod-img--sm ec-prod-img--cat-default">
    <img src="/prodotti/%d/immagine" alt="" onerror="this.style.display='none'">
  </div>
  <div>
    <div class="ec-cart__row-name">%s%s</div>
    <div class="ec-qty-stepper">
      <button type="button" class="ec-qty-stepper__btn"
              hx-post="/bozza/righe/%d?cart=%s"
              hx-vals='{"delta":"-1"}'
              hx-target="#%s" hx-swap="outerHTML"
              aria-label="Diminuisci">−</button>
      <input type="number" min="1" value="%d" name="qta"
             class="ec-qty-stepper__input"
             hx-post="/bozza/righe/%d?cart=%s" hx-target="#%s" hx-swap="outerHTML" hx-trigger="change">
      <button type="button" class="ec-qty-stepper__btn"
              hx-post="/bozza/righe/%d?cart=%s"
              hx-vals='{"delta":"1"}'
              hx-target="#%s" hx-swap="outerHTML"
              aria-label="Aumenta">+</button>
    </div>
  </div>
  <button type="button" class="ec-cart__row-remove"
          hx-delete="/bozza/righe/%d?cart=%s" hx-target="#%s" hx-swap="outerHTML"
          aria-label="Rimuovi">
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 6l12 12"/><path d="M6 18 18 6"/></svg>
  </button>
</div>`,
			r.ProdottoID,
			template.HTMLEscapeString(r.ProdottoNome), prenotPill,
			r.ProdottoID, targetID, targetID,
			r.QtaRichiesta,
			r.ProdottoID, targetID, targetID,
			r.ProdottoID, targetID, targetID,
			r.ProdottoID, targetID, targetID,
		)
	}
	fmt.Fprintf(w, `</div>
<div class="ec-cart__foot">
  <div class="ec-cart__totals">
    <span>Totale articoli</span>
    <span class="ec-mono mono">%d pz · %d prodotti</span>
  </div>
  <button type="button" class="ec-btn ec-btn--primary ec-btn--lg ec-btn--block"
          hx-post="/ordini/%d/invia" hx-target="body" hx-push-url="true">
    <span style="flex:1;text-align:left">Invia richiesta</span>
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12h14"/><path d="M13 6l6 6-6 6"/></svg>
  </button>
</div></div>`, totalPz, righeCount, bozza.ID)
	fmt.Fprintf(w, `<span id="cart-badge" hx-swap-oob="true" class="ec-cart__count">%d prodotti</span>`, righeCount)
}

// POST /bozza/righe/{prodotto_id} — upsert riga bozza (HTMX).
// Accetta `qta` (assoluta) oppure `delta` (incremento ±N rispetto al valore corrente).
// `delta` ha precedenza se entrambi sono presenti.
func (a *App) handleUpsertRigaBozza(w http.ResponseWriter, r *http.Request) {
	prodottoID, err := strconv.ParseInt(r.PathValue("prodotto_id"), 10, 64)
	if err != nil {
		http.Error(w, "prodotto_id non valido", 400)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	username := a.getUsername(r)
	bozzaID, err := a.db.GetOrCreateBozza(username)
	if err != nil {
		logger.Error("get/create bozza: %v", err)
		http.Error(w, "settore non assegnato — contattare l'amministratore", 400)
		return
	}
	addMode := r.FormValue("add") == "1"
	var qta int
	var addedDelta int
	if deltaStr := r.FormValue("delta"); deltaStr != "" {
		delta, err := strconv.Atoi(deltaStr)
		if err != nil {
			http.Error(w, "delta non valido", 400)
			return
		}
		corrente, _ := a.db.GetRigaBozzaCorrente(bozzaID, prodottoID)
		qta = corrente + delta
		if qta < 1 {
			qta = 1
		}
		if disp, err := a.db.GetDisponibilitaProdotto(prodottoID); err == nil && disp > 0 && qta > disp {
			qta = disp
		}
		addedDelta = qta - corrente
	} else {
		input, err := strconv.Atoi(r.FormValue("qta"))
		if err != nil {
			http.Error(w, "quantità non valida", 400)
			return
		}
		if addMode {
			corrente, _ := a.db.GetRigaBozzaCorrente(bozzaID, prodottoID)
			qta = corrente + input
			if disp, err := a.db.GetDisponibilitaProdotto(prodottoID); err == nil && disp > 0 && qta > disp {
				qta = disp
			}
			addedDelta = qta - corrente
		} else {
			qta = input
		}
	}
	if err := a.db.UpsertRigaBozza(bozzaID, prodottoID, qta); err != nil {
		logger.Error("upsert riga bozza: %v", err)
		http.Error(w, "errore interno", 500)
		return
	}
	targetID := r.URL.Query().Get("cart")
	if targetID == "" {
		targetID = "carrello-content"
	}
	if addMode {
		var nome string
		if p, err := a.db.GetProdottoByID(prodottoID); err == nil {
			nome = p.Nome
		}
		nomeJSON, _ := json.Marshal(nome)
		w.Header().Set("HX-Trigger", fmt.Sprintf(
			`{"econ:added":{"prodottoID":%d,"qta":%d,"delta":%d,"totale":%d,"nome":%s}}`,
			prodottoID, qta, addedDelta, qta, nomeJSON,
		))
	}
	bozza, _ := a.db.GetBozzaConRighe(username)
	a.renderCarrello(w, targetID, bozza)
}

// DELETE /ordini/righe/{prodotto_id} — rimuovi riga bozza (HTMX)
func (a *App) handleDeleteRigaBozza(w http.ResponseWriter, r *http.Request) {
	prodottoID, err := strconv.ParseInt(r.PathValue("prodotto_id"), 10, 64)
	if err != nil {
		http.Error(w, "prodotto_id non valido", 400)
		return
	}
	username := a.getUsername(r)
	if bozza, _ := a.db.GetBozzaConRighe(username); bozza != nil {
		if err := a.db.DeleteRigaBozza(bozza.ID, prodottoID); err != nil {
			logger.Error("delete riga bozza: %v", err)
			http.Error(w, "errore interno", 500)
			return
		}
	}
	targetID := r.URL.Query().Get("cart")
	if targetID == "" {
		targetID = "carrello-content"
	}
	bozza, _ := a.db.GetBozzaConRighe(username)
	a.renderCarrello(w, targetID, bozza)
}

// POST /ordini/{id}/invia — conferma e invia la bozza
func (a *App) handleInviaOrdine(w http.ResponseWriter, r *http.Request) {
	ordineID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", 400)
		return
	}
	username := a.getUsername(r)
	if err := a.db.InviaOrdine(ordineID, username); err != nil {
		logger.Error("invia ordine %d: %v", ordineID, err)
		http.Error(w, "errore interno", 500)
		return
	}
	logger.Info("ordine %d inviato da %s", ordineID, username)
	a.notificaTransizione(ordineID, username)
	hxRedirect(w, r, a.dashboardURLWithView(a.getRole(r), "ordini-personali"))
}

// notificaTransizione legge lo stato post-InviaOrdine e instrada le notifiche
// al funzionario di settore (in_approvazione) o agli utenti+magazzinieri
// (in_preparazione, in caso di auto-approvazione del funzionario).
func (a *App) notificaTransizione(ordineID int64, attoreUsername string) {
	if a.notifier == nil {
		return
	}
	utente, settoreID, stato, err := a.db.GetOrdineMeta(ordineID)
	if err != nil {
		logger.Error("notify: GetOrdineMeta %d: %v", ordineID, err)
		return
	}
	switch stato {
	case "in_approvazione":
		funz, err := a.db.GetFunzionarioSettore(settoreID)
		if err != nil || funz == "" {
			logger.Warn("notify: nessun funzionario per settore %s, salto notifica", settoreID)
			return
		}
		a.notifier.EmitOrdine(notify.EventoOrdineParams{
			Tipo:          "ordine_inviato",
			OrdineID:      ordineID,
			OrdineSettore: settoreID,
			Destinatari:   []string{funz},
			Mittente:      attoreUsername,
			Messaggio:     fmt.Sprintf("Nuovo ordine #%d da approvare (settore %s).", ordineID, settoreID),
		})
	case "in_preparazione":
		dest := append([]string{utente}, a.notifier.MagazzinieriUsernames()...)
		a.notifier.EmitOrdine(notify.EventoOrdineParams{
			Tipo:          "ordine_in_preparazione",
			OrdineID:      ordineID,
			OrdineSettore: settoreID,
			Destinatari:   dest,
			Mittente:      attoreUsername,
			Messaggio:     fmt.Sprintf("Ordine #%d auto-approvato e in preparazione.", ordineID),
		})
	}
}

// -- Funzionario Handlers ──────────────────────────────────────────────────────

// GET /dashboard/funzionario
func (a *App) handleDashboardFunzionario(w http.ResponseWriter, r *http.Request) {
	username := a.getUsername(r)
	settoreID, err := a.db.GetSettoreIDByUsername(username)
	if err != nil {
		logger.Error("get settore for %s: %v", username, err)
		http.Error(w, "errore interno", 500)
		return
	}
	daApprovare, _ := a.db.GetOrdiniSettore(settoreID)
	storicoSettore, _ := a.db.GetOrdiniSettoreAll(settoreID)
	bozza, _ := a.db.GetBozzaConRighe(username)
	categorie, _ := a.db.GetAllCategorie()
	catID, _ := strconv.ParseInt(r.URL.Query().Get("cat"), 10, 64)
	prodotti, _ := a.db.GetCatalogo(r.URL.Query().Get("q"), catID)
	mieiOrdini, _ := a.db.GetOrdiniUtente(username)
	a.render(w, r, "dashboard-funzionario", a.viewData(r, map[string]any{
		"Username":       username,
		"Role":           a.getRole(r),
		"DaApprovare":    daApprovare,
		"StoricoSettore": storicoSettore,
		"Bozza":          bozza,
		"Categorie":      categorie,
		"CategoriaID":    catID,
		"Prodotti":       prodotti,
		"MieiOrdini":     mieiOrdini,
		"SettoreNome":    a.settoreNomeFor(username),
	}))
}

// POST /ordini/{id}/approva
func (a *App) handleApprovaOrdine(w http.ResponseWriter, r *http.Request) {
	ordineID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", 400)
		return
	}
	username := a.getUsername(r)
	settoreID, err := a.db.GetSettoreIDByUsername(username)
	if err != nil || settoreID == "" {
		http.Error(w, "settore non assegnato", 403)
		return
	}
	if err := a.db.VerificaOrdineSettore(ordineID, settoreID); err != nil {
		http.Error(w, "ordine non trovato o non autorizzato", 403)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	qtaPerRiga := map[int64]int{}
	for key, vals := range r.Form {
		if !strings.HasPrefix(key, "riga_") || len(vals) == 0 {
			continue
		}
		rigaID, err := strconv.ParseInt(strings.TrimPrefix(key, "riga_"), 10, 64)
		if err != nil {
			continue
		}
		qta, err := strconv.Atoi(vals[0])
		if err != nil || qta < 0 {
			continue
		}
		qtaPerRiga[rigaID] = qta
	}
	if err := a.db.ApprovaOrdine(ordineID, qtaPerRiga, r.FormValue("note")); err != nil {
		logger.Error("approva ordine %d: %v", ordineID, err)
		http.Error(w, "errore interno", 500)
		return
	}
	logger.Info("ordine %d approvato da %s", ordineID, a.getUsername(r))
	if a.notifier != nil {
		utente, settID, _, err := a.db.GetOrdineMeta(ordineID)
		if err == nil {
			dest := append([]string{utente}, a.notifier.MagazzinieriUsernames()...)
			a.notifier.EmitOrdine(notify.EventoOrdineParams{
				Tipo:          "ordine_approvato",
				OrdineID:      ordineID,
				OrdineSettore: settID,
				Destinatari:   dest,
				Mittente:      a.getUsername(r),
				Messaggio:     fmt.Sprintf("Ordine #%d approvato.", ordineID),
			})
		}
	}
	http.Redirect(w, r, "/dashboard/funzionario", http.StatusSeeOther)
}

// POST /ordini/{id}/rifiuta
func (a *App) handleRifiutaOrdine(w http.ResponseWriter, r *http.Request) {
	ordineID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", 400)
		return
	}
	username := a.getUsername(r)
	settoreID, err := a.db.GetSettoreIDByUsername(username)
	if err != nil || settoreID == "" {
		http.Error(w, "settore non assegnato", 403)
		return
	}
	if err := a.db.VerificaOrdineSettore(ordineID, settoreID); err != nil {
		http.Error(w, "ordine non trovato o non autorizzato", 403)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	note := strings.TrimSpace(r.FormValue("note"))
	if note == "" {
		http.Error(w, "motivazione obbligatoria", 400)
		return
	}
	if err := a.db.RifiutaOrdine(ordineID, note); err != nil {
		logger.Error("rifiuta ordine %d: %v", ordineID, err)
		http.Error(w, "errore interno", 500)
		return
	}
	logger.Info("ordine %d rifiutato da %s", ordineID, a.getUsername(r))
	if a.notifier != nil {
		utente, settID, _, err := a.db.GetOrdineMeta(ordineID)
		if err == nil {
			a.notifier.EmitOrdine(notify.EventoOrdineParams{
				Tipo:          "ordine_rifiutato",
				OrdineID:      ordineID,
				OrdineSettore: settID,
				Destinatari:   []string{utente},
				Mittente:      a.getUsername(r),
				Messaggio:     fmt.Sprintf("Ordine #%d rifiutato.", ordineID),
				NoteExtra:     note,
			})
		}
	}
	http.Redirect(w, r, "/dashboard/funzionario", http.StatusSeeOther)
}

// -- Magazziniere Handlers ────────────────────────────────────────────────────

// GET /dashboard/magazzino
func (a *App) handleDashboardMagazzino(w http.ResponseWriter, r *http.Request) {
	scorte, _ := a.db.GetProdottiSottoSoglia()
	ordini, _ := a.db.GetOrdiniAttivi()
	a.render(w, r, "dashboard-magazzino", a.viewData(r, map[string]any{
		"Username":    a.getUsername(r),
		"Role":        a.getRole(r),
		"Scorte":      scorte,
		"Ordini":      ordini,
		"ActiveNav":   "ordini",
		"SectionName": "Ordini da evadere",
	}))
}

// GET /storico-ordini — vista magazzino dello storico ordini con filtri.
// HTMX-aware: se HX-Request è presente ritorna solo il partial della lista.
func (a *App) handleStoricoOrdini(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	stato := strings.TrimSpace(r.URL.Query().Get("stato"))
	prodID, _ := strconv.ParseInt(r.URL.Query().Get("prodotto"), 10, 64)

	ordini, err := a.db.GetOrdiniStorico(stato, q, prodID)
	if err != nil {
		logger.Error("storico ordini: %v", err)
		http.Error(w, "errore interno", 500)
		return
	}

	var prodottoNome, prodottoCodice string
	if prodID > 0 {
		if p, err := a.db.GetProdottoByID(prodID); err == nil {
			prodottoNome = p.Nome
			prodottoCodice = p.CodiceArticolo
		}
	}

	data := a.viewData(r, map[string]any{
		"Username":       a.getUsername(r),
		"Role":           a.getRole(r),
		"Ordini":         ordini,
		"Stato":          stato,
		"Query":          q,
		"ProdottoID":     prodID,
		"ProdottoNome":   prodottoNome,
		"ProdottoCodice": prodottoCodice,
		"ActiveNav":      "storico",
		"SectionName":    "Storico ordini",
	})

	if r.Header.Get("HX-Request") == "true" {
		a.renderPartial(w, r, "storico-ordini", "storico-list", data)
		return
	}
	a.render(w, r, "storico-ordini", data)
}

// GET /prodotti/{id}/storico — scorciatoia che apre lo storico filtrato per prodotto.
func (a *App) handleProdottoStorico(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		http.Error(w, "id non valido", 400)
		return
	}
	http.Redirect(w, r, "/storico-ordini?prodotto="+id, http.StatusSeeOther)
}

// POST /ordini/{id}/prepara
func (a *App) handlePreparaOrdine(w http.ResponseWriter, r *http.Request) {
	ordineID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", 400)
		return
	}
	scorte, err := a.db.PreparaOrdineFIFO(ordineID)
	if err != nil {
		logger.Error("prepara FIFO ordine %d: %v", ordineID, err)
		http.Error(w, "errore interno", 500)
		return
	}
	logger.Info("ordine %d in preparazione (FIFO) da %s", ordineID, a.getUsername(r))
	if a.notifier != nil {
		utente, settID, _, err := a.db.GetOrdineMeta(ordineID)
		if err == nil && utente != "" {
			a.notifier.EmitOrdine(notify.EventoOrdineParams{
				Tipo:          "ordine_in_preparazione",
				OrdineID:      ordineID,
				OrdineSettore: settID,
				Destinatari:   []string{utente},
				Mittente:      a.getUsername(r),
				Messaggio:     fmt.Sprintf("Ordine #%d in preparazione.", ordineID),
			})
		}
		for _, s := range scorte {
			a.notifier.EmitScorta(s)
		}
	}
	http.Redirect(w, r, "/dashboard/magazzino", http.StatusSeeOther)
}

// GET /report — pagina di reportistica per il magazziniere con KPI, bar
// chart spesa mensile e legenda spesa per settore. Accetta ?anno=YYYY
// (default: anno corrente).
func (a *App) handleReportMagazzino(w http.ResponseWriter, r *http.Request) {
	anno := time.Now().Year()
	if q := strings.TrimSpace(r.URL.Query().Get("anno")); q != "" {
		if v, err := strconv.Atoi(q); err == nil && v > 2000 && v < 2100 {
			anno = v
		}
	}
	spesaAnno, err := a.db.GetSpesaAnno(anno)
	if err != nil {
		logger.Error("report spesa anno: %v", err)
		http.Error(w, "errore interno", 500)
		return
	}
	ordiniEvasi, _ := a.db.GetOrdiniEvasiAnno(anno)
	tempoMedio, _ := a.db.GetTempoMedioEvasioneAnno(anno)
	settoriAttivi, _ := a.db.GetSettoriAttiviAnno(anno)
	mensile, err := a.db.GetSpesaMensile(anno)
	if err != nil {
		logger.Error("report spesa mensile: %v", err)
		http.Error(w, "errore interno", 500)
		return
	}
	perSettore, err := a.db.GetSpesaPerSettore(anno)
	if err != nil {
		logger.Error("report spesa per settore: %v", err)
		http.Error(w, "errore interno", 500)
		return
	}
	var maxMese float64
	for _, m := range mensile {
		if m.Spesa > maxMese {
			maxMese = m.Spesa
		}
	}
	var totSettore float64
	for _, s := range perSettore {
		totSettore += s.Spesa
	}
	scorte, _ := a.db.GetProdottiSottoSoglia()
	ordini, _ := a.db.GetOrdiniAttivi()
	a.render(w, r, "report-magazzino", a.viewData(r, map[string]any{
		"Username":      a.getUsername(r),
		"Role":          a.getRole(r),
		"Anno":          anno,
		"AnnoPrec":      anno - 1,
		"AnnoSucc":      anno + 1,
		"SpesaAnno":     spesaAnno,
		"OrdiniEvasi":   ordiniEvasi,
		"TempoMedio":    tempoMedio,
		"SettoriAttivi": settoriAttivi,
		"Mensile":       mensile,
		"MensileMax":    maxMese,
		"PerSettore":    perSettore,
		"TotSettore":    totSettore,
		"Scorte":        scorte,
		"Ordini":        ordini,
		"ActiveNav":     "report",
		"SectionName":   "Report",
	}))
}

// GET /report.csv — esporta l'elenco dei movimenti dell'anno indicato in
// formato CSV compatibile con Excel italiano (BOM UTF-8, ';' separator,
// decimali con virgola).
func (a *App) handleReportCSV(w http.ResponseWriter, r *http.Request) {
	anno := time.Now().Year()
	if q := strings.TrimSpace(r.URL.Query().Get("anno")); q != "" {
		if v, err := strconv.Atoi(q); err == nil && v > 2000 && v < 2100 {
			anno = v
		}
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=movimenti-%d.csv", anno))
	if err := a.db.StreamMovimentiCSV(w, anno); err != nil {
		logger.Error("export csv movimenti %d: %v", anno, err)
		// Headers già scritti: chiudere silenziosamente.
		return
	}
}

// GET /ordini/{id}/anteprima-fifo — restituisce il partial della modale che
// mostra la simulazione non distruttiva dei prelievi FIFO per l'ordine.
func (a *App) handleAnteprimaFIFO(w http.ResponseWriter, r *http.Request) {
	ordineID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", 400)
		return
	}
	anteprima, err := a.db.SimulaOrdineFIFO(ordineID)
	if err != nil {
		logger.Error("simula FIFO ordine %d: %v", ordineID, err)
		http.Error(w, "errore interno", 500)
		return
	}
	utente, settore, stato, _ := a.db.GetOrdineMeta(ordineID)
	a.renderPartial(w, r, "dashboard-magazzino", "fifo-preview-modal", a.viewData(r, map[string]any{
		"Anteprima": anteprima,
		"Utente":    utente,
		"Settore":   settore,
		"Stato":     stato,
		"OrdineID":  ordineID,
	}))
}

// POST /ordini/{id}/pronto
func (a *App) handleSegnaPronte(w http.ResponseWriter, r *http.Request) {
	ordineID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", 400)
		return
	}
	utenteUsername, err := a.db.SegnaOrdinePronte(ordineID)
	if err != nil {
		logger.Error("segna pronto ordine %d: %v", ordineID, err)
		http.Error(w, "errore interno", 500)
		return
	}
	logger.Info("ordine %d pronto, notifica a %s", ordineID, utenteUsername)
	if a.notifier != nil && utenteUsername != "" {
		_, settID, _, _ := a.db.GetOrdineMeta(ordineID)
		a.notifier.EmitOrdine(notify.EventoOrdineParams{
			Tipo:          "ordine_pronto",
			OrdineID:      ordineID,
			OrdineSettore: settID,
			Destinatari:   []string{utenteUsername},
			Mittente:      a.getUsername(r),
			Messaggio:     fmt.Sprintf("Ordine #%d pronto al ritiro.", ordineID),
		})
	}
	http.Redirect(w, r, "/dashboard/magazzino", http.StatusSeeOther)
}

// -- Notifiche Handlers ──────────────────────────────────────────────────────

// GET /notifiche — pagina full delle notifiche dell'utente loggato.
func (a *App) handleNotifichePage(w http.ResponseWriter, r *http.Request) {
	username := a.getUsername(r)
	tab := strings.TrimSpace(r.URL.Query().Get("tab"))
	notifiche, err := a.db.ListNotifiche(username, tab, 200)
	if err != nil {
		logger.Error("list notifiche %s: %v", username, err)
		http.Error(w, "errore interno", 500)
		return
	}
	role := a.getRole(r)
	tmplName := "notifiche-utente"
	activeNav := "notifiche"
	switch role {
	case "funzionario":
		tmplName = "notifiche-funzionario"
	case "magazziniere", "admin":
		tmplName = "notifiche-magazzino"
	}
	data := a.viewData(r, map[string]any{
		"Username":    username,
		"Role":        role,
		"Notifiche":   notifiche,
		"Tab":         tab,
		"ActiveNav":   activeNav,
		"SectionName": "Notifiche",
		"SettoreNome": a.settoreNomeFor(username),
	})
	a.render(w, r, tmplName, data)
}

// GET /notifiche/badge — markup del bell aggiornato (per poll HTMX 60s).
func (a *App) handleNotificheBadge(w http.ResponseWriter, r *http.Request) {
	n, _ := a.db.CountNotificheNonLette(a.getUsername(r))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(renderTopbarBell(n))) //nolint:errcheck
}

// renderTopbarBell genera il markup del bell. Tenuto inline per evitare di
// caricare un template intero per ogni poll.
func renderTopbarBell(n int) string {
	count := ""
	if n > 0 {
		count = fmt.Sprintf(`<span class="ec-iconbtn__count">%d</span>`, n)
	}
	return fmt.Sprintf(`<a href="/notifiche" class="ec-iconbtn" id="ec-topbar-bell" title="Notifiche" hx-get="/notifiche/badge" hx-trigger="every 60s" hx-swap="outerHTML"><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M6 16V11a6 6 0 0 1 12 0v5l1.5 2H4.5L6 16Z"/><path d="M10 21h4"/></svg>%s</a>`, count)
}

// POST /notifiche/{id}/letta — segna una singola notifica come letta.
func (a *App) handleMarcaNotificaLetta(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", 400)
		return
	}
	username := a.getUsername(r)
	if err := a.db.MarcaNotificaLetta(id, username); err != nil {
		logger.Error("marca letta notifica %d: %v", id, err)
		http.Error(w, "errore interno", 500)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", "notifiche-aggiornate")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, "/notifiche", http.StatusSeeOther)
}

// POST /notifiche/leggi-tutte — segna come lette tutte le notifiche dell'utente.
func (a *App) handleLeggiTutteNotifiche(w http.ResponseWriter, r *http.Request) {
	username := a.getUsername(r)
	if err := a.db.MarcaTutteLette(username); err != nil {
		logger.Error("marca tutte lette %s: %v", username, err)
		http.Error(w, "errore interno", 500)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", "notifiche-aggiornate")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, "/notifiche", http.StatusSeeOther)
}

// POST /ordini/{id}/consegna
func (a *App) handleConsegnaOrdine(w http.ResponseWriter, r *http.Request) {
	ordineID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", 400)
		return
	}
	if err := a.db.ConsegnaOrdine(ordineID); err != nil {
		logger.Error("consegna ordine %d: %v", ordineID, err)
		http.Error(w, "errore interno", 500)
		return
	}
	logger.Info("ordine %d ritirato, confermato da %s", ordineID, a.getUsername(r))
	http.Redirect(w, r, "/dashboard/magazzino", http.StatusSeeOther)
}

// GET /dashboard/scorte — HTMX partial: prodotti sotto soglia
func (a *App) handleDashboardScorte(w http.ResponseWriter, r *http.Request) {
scorte, err := a.db.GetProdottiSottoSoglia()
if err != nil {
http.Error(w, "Errore DB", http.StatusInternalServerError)
return
}
w.Header().Set("Content-Type", "text/html; charset=utf-8")
if len(scorte) == 0 {
fmt.Fprint(w, `<div class="ec-empty"><div class="ec-empty__icon"><svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12.5l4.5 4.5L20 6.5"/></svg></div><div class="ec-empty__title">Nessun prodotto sotto soglia</div><div class="ec-empty__hint">Tutte le giacenze sono a posto.</div></div>`)
return
}
fmt.Fprint(w, `<table class="ec-table"><thead><tr><th>Codice</th><th>Articolo</th><th>Categoria</th><th style="text-align:right">Giacenza</th><th style="text-align:right">Scorta min.</th></tr></thead><tbody>`)
for _, p := range scorte {
codice := "—"
if p.CodiceArticolo != "" {
codice = template.HTMLEscapeString(p.CodiceArticolo)
}
fmt.Fprintf(w, `<tr><td class="mono">%s</td><td><strong>%s</strong></td><td><span class="ec-pill ec-pill--navy">%s</span></td><td class="mono" style="text-align:right;color:var(--ec-red-700);font-weight:700">%d</td><td class="mono" style="text-align:right;color:var(--ec-ink-500)">%d</td></tr>`,
codice,
template.HTMLEscapeString(p.Nome),
template.HTMLEscapeString(p.CategoriaName),
p.ScortaRimanente,
p.ScortaMinima,
)
}
fmt.Fprint(w, `</tbody></table>`)
}

// ── Categorie ────────────────────────────────────────────────────────────────

// renderPartial executes a named sub-template from the base template.
func (a *App) renderPartial(w http.ResponseWriter, r *http.Request, baseName, partialName string, data any) {
tmpl, ok := a.templates[baseName]
if !ok {
http.Error(w, "template not found", 500)
return
}
locale := a.requestLocale(r)
localized, err := tmpl.Clone()
if err != nil {
http.Error(w, "template clone error", 500)
return
}
localized = localized.Funcs(a.localizedTemplateFuncs(locale))
w.Header().Set("Content-Type", "text/html; charset=utf-8")
if err := localized.ExecuteTemplate(w, partialName, data); err != nil {
logger.Error("renderPartial %s/%s: %v", baseName, partialName, err)
}
}

// GET /categorie — HTMX partial list o full page (reload diretto / push-url).
func (a *App) handleListCategorie(w http.ResponseWriter, r *http.Request) {
cats, err := a.db.GetAllCategorie()
if err != nil {
http.Error(w, "Errore DB", http.StatusInternalServerError)
return
}
if r.Header.Get("HX-Request") == "true" {
a.renderPartial(w, r, "magazzino", "categorie-partial", map[string]any{
"Categorie":   cats,
"IconePicker": IconePicker,
})
return
}
// Full-page render: stessa layout di /prodotti ma con tab Categorie attivo.
prodotti, _ := a.db.GetAllProdotti()
a.render(w, r, "magazzino", a.viewData(r, map[string]any{
"Partial":     "categorie",
"Prodotti":    prodotti,
"Categorie":   cats,
"Username":    a.getUsername(r),
"Role":        a.getRole(r),
"ActiveNav":   "prodotti",
"SectionName": "Anagrafica prodotti",
"IconePicker": IconePicker,
}))
}

// POST /categorie
func (a *App) handleCreateCategoria(w http.ResponseWriter, r *http.Request) {
if err := r.ParseForm(); err != nil {
http.Error(w, "bad request", http.StatusBadRequest)
return
}
nome := strings.TrimSpace(r.FormValue("nome"))
if nome == "" {
http.Error(w, "nome obbligatorio", http.StatusBadRequest)
return
}
icona := normalizeIcon(r.FormValue("icona"), "fa-solid fa-box")
if _, err := a.db.CreateCategoria(nome, icona); err != nil {
logger.Error("create categoria: %v", err)
http.Error(w, "Errore DB", http.StatusInternalServerError)
return
}
a.renderCategorieListInner(w, r)
}

// PUT /categorie/{id}
func (a *App) handleUpdateCategoria(w http.ResponseWriter, r *http.Request) {
id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
if err != nil {
http.Error(w, "id non valido", http.StatusBadRequest)
return
}
if err := r.ParseForm(); err != nil {
http.Error(w, "bad request", http.StatusBadRequest)
return
}
nome := strings.TrimSpace(r.FormValue("nome"))
if nome == "" {
http.Error(w, "nome obbligatorio", http.StatusBadRequest)
return
}
icona := normalizeIcon(r.FormValue("icona"), "fa-solid fa-box")
if err := a.db.UpdateCategoria(id, nome, icona); err != nil {
logger.Error("update categoria %d: %v", id, err)
http.Error(w, "Errore DB", http.StatusInternalServerError)
return
}
a.renderCategorieListInner(w, r)
}

// renderCategorieListInner renderizza SOLO il contenuto interno di #cat-list
// (no wrapper ec-card) per evitare nidificazione DOM dopo create/update/delete.
func (a *App) renderCategorieListInner(w http.ResponseWriter, r *http.Request) {
cats, err := a.db.GetAllCategorie()
if err != nil {
http.Error(w, "Errore DB", http.StatusInternalServerError)
return
}
a.renderPartial(w, r, "magazzino", "categorie-list-inner", map[string]any{
"Categorie": cats,
})
}

// DELETE /categorie/{id}
func (a *App) handleDeleteCategoria(w http.ResponseWriter, r *http.Request) {
id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
if err != nil {
http.Error(w, "id non valido", http.StatusBadRequest)
return
}
if err := a.db.DeleteCategoria(id); err != nil {
logger.Error("delete categoria %d: %v", id, err)
http.Error(w, "Errore DB", http.StatusInternalServerError)
return
}
a.renderCategorieListInner(w, r)
}

// ── Prodotti ─────────────────────────────────────────────────────────────────

// GET /prodotti — HTMX partial table o full page.
func (a *App) handleMagazzino(w http.ResponseWriter, r *http.Request) {
prodotti, err := a.db.GetAllProdotti()
if err != nil {
logger.Error("get prodotti: %v", err)
prodotti = nil
}
if r.Header.Get("HX-Request") == "true" {
a.renderPartial(w, r, "magazzino", "prodotti-partial", map[string]any{
"Prodotti": prodotti,
})
return
}
categorie, err := a.db.GetAllCategorie()
if err != nil {
logger.Error("get categorie: %v", err)
categorie = nil
}
a.render(w, r, "magazzino", a.viewData(r, map[string]any{
"Partial":     "prodotti",
"Prodotti":    prodotti,
"Categorie":   categorie,
"Username":    a.getUsername(r),
"Role":        a.getRole(r),
"ActiveNav":   "prodotti",
"SectionName": "Anagrafica prodotti",
"IconePicker": IconePicker,
}))
}

// GET /prodotti/new — full-page form
func (a *App) handleNewProdottoForm(w http.ResponseWriter, r *http.Request) {
categorie, _ := a.db.GetAllCategorie()
a.render(w, r, "prodotto-form", a.viewData(r, map[string]any{
"Username":    a.getUsername(r),
"Role":        a.getRole(r),
"Prodotto":    models.Prodotto{},
"Categorie":   categorie,
"IconePicker": IconePicker,
"ActiveNav":   "prodotti",
"SectionName": "Nuovo prodotto",
}))
}

// GET /prodotti/{id}/edit — full-page edit form
func (a *App) handleEditProdottoForm(w http.ResponseWriter, r *http.Request) {
id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
if err != nil {
http.Error(w, "id non valido", http.StatusBadRequest)
return
}
p, err := a.db.GetProdottoByID(id)
if err != nil {
http.Error(w, "Prodotto non trovato", http.StatusNotFound)
return
}
categorie, _ := a.db.GetAllCategorie()
a.render(w, r, "prodotto-form", a.viewData(r, map[string]any{
"Username":    a.getUsername(r),
"Role":        a.getRole(r),
"Prodotto":    p,
"Categorie":   categorie,
"IconePicker": IconePicker,
"ActiveNav":   "prodotti",
"SectionName": "Modifica prodotto",
}))
}

// POST /prodotti — create
func (a *App) handleCreateProdotto(w http.ResponseWriter, r *http.Request) {
p, err := parseProdottoForm(r)
if err != nil {
http.Error(w, err.Error(), http.StatusBadRequest)
return
}
if _, err := a.db.CreateProdotto(p); err != nil {
logger.Error("create prodotto: %v", err)
http.Error(w, "Errore DB", http.StatusInternalServerError)
return
}
http.Redirect(w, r, "/prodotti", http.StatusSeeOther)
}

// POST /prodotti/{id} — update (HTML forms can't send PUT)
func (a *App) handleUpdateProdotto(w http.ResponseWriter, r *http.Request) {
id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
if err != nil {
http.Error(w, "id non valido", http.StatusBadRequest)
return
}
p, err := parseProdottoForm(r)
if err != nil {
http.Error(w, err.Error(), http.StatusBadRequest)
return
}
p.ID = id
if err := a.db.UpdateProdotto(p); err != nil {
logger.Error("update prodotto %d: %v", id, err)
http.Error(w, "Errore DB", http.StatusInternalServerError)
return
}
http.Redirect(w, r, "/prodotti", http.StatusSeeOther)
}

// DELETE /prodotti/{id}
func (a *App) handleDeleteProdotto(w http.ResponseWriter, r *http.Request) {
id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
if err != nil {
http.Error(w, "id non valido", http.StatusBadRequest)
return
}
if err := a.db.DeleteProdotto(id); err != nil {
logger.Error("delete prodotto %d: %v", id, err)
http.Error(w, "Errore DB", http.StatusInternalServerError)
return
}
w.WriteHeader(http.StatusOK)
}

// GET /prodotti/{id}/immagine — serve BLOB
func (a *App) handleProdottoImmagine(w http.ResponseWriter, r *http.Request) {
id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
if err != nil {
http.Error(w, "id non valido", http.StatusBadRequest)
return
}
blob, err := a.db.GetProdottoImmagine(id)
if err != nil || len(blob) == 0 {
http.NotFound(w, r)
return
}
ct := http.DetectContentType(blob)
w.Header().Set("Content-Type", ct)
w.Header().Set("Cache-Control", "public, max-age=86400")
w.Write(blob) //nolint:errcheck
}

// ── Acquisti / Lotti ────────────────────────────────────────────────────────

// GET /lotti/new — form acquisto (head + N righe lotto).
func (a *App) handleNewLottoForm(w http.ResponseWriter, r *http.Request) {
prodotti, _ := a.db.GetAllProdotti()
fornitori, _ := a.db.GetAllFornitori()
categorie, _ := a.db.GetAllCategorie()
a.render(w, r, "lotto-form", a.viewData(r, map[string]any{
"Username":    a.getUsername(r),
"Role":        a.getRole(r),
"Prodotti":    prodotti,
"Fornitori":   fornitori,
"Categorie":   categorie,
"ActiveNav":   "carico-merce",
"SectionName": "Carico merce",
}))
}

// POST /lotti — crea acquisto multi-riga.
// Form fields: data_acquisto, fornitore_id (opzionale), numero_doc, note,
// e per ogni riga: riga_prodotto_id[], riga_qta[], riga_costo[].
func (a *App) handleCreateAcquisto(w http.ResponseWriter, r *http.Request) {
if err := r.ParseForm(); err != nil {
http.Error(w, "bad request", http.StatusBadRequest)
return
}
// Header.
dataStr := r.FormValue("data_acquisto")
var dataAcquisto time.Time
if dataStr != "" {
dt, err := time.Parse("2006-01-02", dataStr)
if err != nil {
http.Error(w, "data non valida (YYYY-MM-DD)", http.StatusBadRequest)
return
}
dataAcquisto = dt
} else {
dataAcquisto = time.Now()
}
var fornitoreID *int64
if v := strings.TrimSpace(r.FormValue("fornitore_id")); v != "" {
id, err := strconv.ParseInt(v, 10, 64)
if err != nil || id <= 0 {
http.Error(w, "fornitore non valido", http.StatusBadRequest)
return
}
fornitoreID = &id
}
acq := models.Acquisto{
DataAcquisto: dataAcquisto,
FornitoreID:  fornitoreID,
NumeroDoc:    strings.TrimSpace(r.FormValue("numero_doc")),
Note:         strings.TrimSpace(r.FormValue("note")),
CreatedBy:    a.getUsername(r),
}
// Righe: parse array-style. Tutti gli slice devono avere stessa length.
prodIDs := r.Form["riga_prodotto_id"]
qtas := r.Form["riga_qta"]
costi := r.Form["riga_costo"]
if len(prodIDs) == 0 {
http.Error(w, "almeno una riga richiesta", http.StatusBadRequest)
return
}
if len(prodIDs) != len(qtas) || len(prodIDs) != len(costi) {
http.Error(w, "righe inconsistenti", http.StatusBadRequest)
return
}
var righe []models.LottoAcquisto
for i := range prodIDs {
pidStr := strings.TrimSpace(prodIDs[i])
if pidStr == "" {
continue // skip riga vuota
}
pid, err := strconv.ParseInt(pidStr, 10, 64)
if err != nil || pid <= 0 {
http.Error(w, fmt.Sprintf("prodotto non valido riga %d", i+1), http.StatusBadRequest)
return
}
qta, err := strconv.Atoi(strings.TrimSpace(qtas[i]))
if err != nil || qta <= 0 {
http.Error(w, fmt.Sprintf("quantità non valida riga %d", i+1), http.StatusBadRequest)
return
}
costo, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(costi[i]), ",", "."), 64)
if err != nil || costo < 0 {
http.Error(w, fmt.Sprintf("costo non valido riga %d", i+1), http.StatusBadRequest)
return
}
righe = append(righe, models.LottoAcquisto{
ProdottoID:       pid,
DataAcquisto:     dataAcquisto,
QuantitaIniziale: qta,
CostoUnitario:    costo,
})
}
if len(righe) == 0 {
http.Error(w, "almeno una riga richiesta", http.StatusBadRequest)
return
}
acqID, err := a.db.CreateAcquisto(acq, righe)
if err != nil {
logger.Error("create acquisto: %v", err)
http.Error(w, "Errore DB: "+err.Error(), http.StatusInternalServerError)
return
}
logger.Info("acquisto %d creato da %s con %d righe", acqID, acq.CreatedBy, len(righe))
http.Redirect(w, r, fmt.Sprintf("/acquisti/%d", acqID), http.StatusSeeOther)
}

// GET /acquisti — storico carichi merce.
func (a *App) handleListAcquisti(w http.ResponseWriter, r *http.Request) {
acquisti, err := a.db.GetAcquistiList(100)
if err != nil {
logger.Error("list acquisti: %v", err)
http.Error(w, "Errore DB", http.StatusInternalServerError)
return
}
a.render(w, r, "acquisti", a.viewData(r, map[string]any{
"Username":    a.getUsername(r),
"Role":        a.getRole(r),
"Acquisti":    acquisti,
"ActiveNav":   "acquisti",
"SectionName": "Storico acquisti",
}))
}

// GET /acquisti/{id} — dettaglio acquisto.
func (a *App) handleAcquistoDetail(w http.ResponseWriter, r *http.Request) {
id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
if err != nil {
http.Error(w, "id non valido", http.StatusBadRequest)
return
}
acq, err := a.db.GetAcquistoConRighe(id)
if err != nil {
http.Error(w, "acquisto non trovato", http.StatusNotFound)
return
}
a.render(w, r, "acquisto-detail", a.viewData(r, map[string]any{
"Username":    a.getUsername(r),
"Role":        a.getRole(r),
"Acquisto":    acq,
"ActiveNav":   "acquisti",
"SectionName": "Dettaglio acquisto",
}))
}

// ── Fornitori (CRUD magazziniere) ────────────────────────────────────────────

func (a *App) handleListFornitori(w http.ResponseWriter, r *http.Request) {
fornitori, err := a.db.GetAllFornitori()
if err != nil {
logger.Error("list fornitori: %v", err)
http.Error(w, "Errore DB", http.StatusInternalServerError)
return
}
a.render(w, r, "fornitori", a.viewData(r, map[string]any{
"Username":    a.getUsername(r),
"Role":        a.getRole(r),
"Fornitori":   fornitori,
"ActiveNav":   "fornitori",
"SectionName": "Fornitori",
}))
}

func (a *App) handleNewFornitoreForm(w http.ResponseWriter, r *http.Request) {
a.render(w, r, "fornitore-form", a.viewData(r, map[string]any{
"Username":    a.getUsername(r),
"Role":        a.getRole(r),
"Fornitore":   models.Fornitore{},
"ActiveNav":   "fornitori",
"SectionName": "Nuovo fornitore",
}))
}

func (a *App) handleEditFornitoreForm(w http.ResponseWriter, r *http.Request) {
id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
if err != nil {
http.Error(w, "id non valido", http.StatusBadRequest)
return
}
f, err := a.db.GetFornitoreByID(id)
if err != nil {
http.Error(w, "fornitore non trovato", http.StatusNotFound)
return
}
a.render(w, r, "fornitore-form", a.viewData(r, map[string]any{
"Username":    a.getUsername(r),
"Role":        a.getRole(r),
"Fornitore":   f,
"ActiveNav":   "fornitori",
"SectionName": "Modifica fornitore",
}))
}

func (a *App) handleCreateFornitore(w http.ResponseWriter, r *http.Request) {
f, err := parseFornitoreForm(r)
if err != nil {
http.Error(w, err.Error(), http.StatusBadRequest)
return
}
if _, err := a.db.CreateFornitore(f); err != nil {
logger.Error("create fornitore: %v", err)
http.Error(w, "Errore DB: "+err.Error(), http.StatusInternalServerError)
return
}
http.Redirect(w, r, "/fornitori", http.StatusSeeOther)
}

func (a *App) handleUpdateFornitore(w http.ResponseWriter, r *http.Request) {
id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
if err != nil {
http.Error(w, "id non valido", http.StatusBadRequest)
return
}
f, err := parseFornitoreForm(r)
if err != nil {
http.Error(w, err.Error(), http.StatusBadRequest)
return
}
f.ID = id
if err := a.db.UpdateFornitore(f); err != nil {
logger.Error("update fornitore %d: %v", id, err)
http.Error(w, "Errore DB", http.StatusInternalServerError)
return
}
http.Redirect(w, r, "/fornitori", http.StatusSeeOther)
}

func (a *App) handleDeleteFornitore(w http.ResponseWriter, r *http.Request) {
id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
if err != nil {
http.Error(w, "id non valido", http.StatusBadRequest)
return
}
if err := a.db.DeleteFornitore(id); err != nil {
logger.Error("delete fornitore %d: %v", id, err)
http.Error(w, "Errore DB", http.StatusInternalServerError)
return
}
w.WriteHeader(http.StatusOK)
}

func parseFornitoreForm(r *http.Request) (models.Fornitore, error) {
if err := r.ParseForm(); err != nil {
return models.Fornitore{}, fmt.Errorf("bad request")
}
nome := strings.TrimSpace(r.FormValue("nome"))
if nome == "" {
return models.Fornitore{}, fmt.Errorf("nome obbligatorio")
}
return models.Fornitore{
Nome:       nome,
PartitaIVA: strings.TrimSpace(r.FormValue("partita_iva")),
Email:      strings.TrimSpace(r.FormValue("email")),
Telefono:   strings.TrimSpace(r.FormValue("telefono")),
Note:       strings.TrimSpace(r.FormValue("note")),
Attivo:     true,
}, nil
}

// ── Quick-create prodotto (modal HTMX da form acquisto) ─────────────────────

// GET /prodotti/quick — modal con form minimal.
func (a *App) handleQuickProdottoForm(w http.ResponseWriter, r *http.Request) {
categorie, _ := a.db.GetAllCategorie()
a.renderPartial(w, r, "lotto-form", "modal-prodotto", map[string]any{
"Categorie": categorie,
})
}

// POST /prodotti/quick — crea prodotto minimal e ritorna HX-Trigger con i dati
// per consentire al form chiamante di aggiungere l'option alle select prodotto.
func (a *App) handleQuickCreateProdotto(w http.ResponseWriter, r *http.Request) {
if err := r.ParseForm(); err != nil {
http.Error(w, "bad request", http.StatusBadRequest)
return
}
nome := strings.TrimSpace(r.FormValue("nome"))
if nome == "" {
http.Error(w, "nome obbligatorio", http.StatusBadRequest)
return
}
catID, _ := strconv.ParseInt(r.FormValue("categoria_id"), 10, 64)
scorta, _ := strconv.Atoi(r.FormValue("scorta_minima"))
p := models.Prodotto{
Nome:         nome,
Descrizione:  strings.TrimSpace(r.FormValue("descrizione")),
CategoriaID:  catID,
ScortaMinima: scorta,
}
id, err := a.db.CreateProdotto(p)
if err != nil {
logger.Error("quick create prodotto: %v", err)
http.Error(w, "Errore DB: "+err.Error(), http.StatusInternalServerError)
return
}
payload := fmt.Sprintf(`{"prodotto-creato":{"id":%d,"nome":%q}}`, id, nome)
w.Header().Set("HX-Trigger", payload)
w.WriteHeader(http.StatusNoContent)
}

// ── Form parsing helper ───────────────────────────────────────────────────────

func parseProdottoForm(r *http.Request) (models.Prodotto, error) {
// Limit upload to 10 MB
if err := r.ParseMultipartForm(10 << 20); err != nil {
if err2 := r.ParseForm(); err2 != nil {
return models.Prodotto{}, fmt.Errorf("bad request")
}
}
nome := strings.TrimSpace(r.FormValue("nome"))
if nome == "" {
return models.Prodotto{}, fmt.Errorf("nome obbligatorio")
}
catID, _ := strconv.ParseInt(r.FormValue("categoria_id"), 10, 64)
scorta, _ := strconv.Atoi(r.FormValue("scorta_minima"))
var imgBytes []byte
if r.MultipartForm != nil {
if fhs := r.MultipartForm.File["immagine"]; len(fhs) > 0 {
f, err := fhs[0].Open()
if err != nil {
return models.Prodotto{}, fmt.Errorf("apertura immagine fallita: %w", err)
}
defer f.Close()
raw, err := io.ReadAll(f)
if err != nil {
return models.Prodotto{}, fmt.Errorf("lettura immagine fallita: %w", err)
}
if len(raw) > 0 {
norm, err := normalizeProductImage(raw, 1024)
if err != nil {
return models.Prodotto{}, err
}
imgBytes = norm
}
}
}
return models.Prodotto{
CodiceArticolo: strings.TrimSpace(r.FormValue("codice_articolo")),
Nome:           nome,
Descrizione:    strings.TrimSpace(r.FormValue("descrizione")),
CategoriaID:    catID,
ScortaMinima:   scorta,
ImmagineBLOB:   imgBytes,
Icona:          normalizeIcon(r.FormValue("icona"), ""),
}, nil
}

// IconePicker è la whitelist di classi Font Awesome ammesse nei form
// (per evitare classi arbitrarie nel DB). Solo Free, mix solid+regular.
var IconePicker = []string{
"fa-solid fa-box",
"fa-solid fa-boxes-stacked",
"fa-solid fa-pen",
"fa-solid fa-pencil",
"fa-solid fa-paperclip",
"fa-solid fa-note-sticky",
"fa-regular fa-note-sticky",
"fa-solid fa-file",
"fa-regular fa-file",
"fa-solid fa-file-lines",
"fa-regular fa-file-lines",
"fa-solid fa-folder",
"fa-regular fa-folder",
"fa-solid fa-folder-open",
"fa-solid fa-envelope",
"fa-regular fa-envelope",
"fa-solid fa-stamp",
"fa-solid fa-clipboard",
"fa-regular fa-clipboard",
"fa-solid fa-clipboard-list",
"fa-solid fa-book",
"fa-solid fa-bookmark",
"fa-regular fa-bookmark",
"fa-solid fa-print",
"fa-solid fa-keyboard",
"fa-regular fa-keyboard",
"fa-solid fa-computer-mouse",
"fa-solid fa-desktop",
"fa-solid fa-laptop",
"fa-solid fa-mobile",
"fa-solid fa-phone",
"fa-solid fa-fax",
"fa-solid fa-headphones",
"fa-solid fa-plug",
"fa-solid fa-battery-full",
"fa-solid fa-lightbulb",
"fa-regular fa-lightbulb",
"fa-solid fa-cube",
"fa-solid fa-cubes",
"fa-solid fa-toolbox",
"fa-solid fa-screwdriver",
"fa-solid fa-screwdriver-wrench",
"fa-solid fa-wrench",
"fa-solid fa-hammer",
"fa-solid fa-paint-roller",
"fa-solid fa-brush",
"fa-solid fa-broom",
"fa-solid fa-soap",
"fa-solid fa-toilet-paper",
"fa-solid fa-spray-can",
"fa-solid fa-mug-hot",
"fa-solid fa-mug-saucer",
"fa-solid fa-coffee",
"fa-solid fa-cookie-bite",
"fa-solid fa-utensils",
"fa-solid fa-bottle-water",
"fa-solid fa-first-aid",
"fa-solid fa-shield",
"fa-solid fa-tag",
"fa-solid fa-tags",
"fa-solid fa-truck",
"fa-solid fa-warehouse",
"fa-solid fa-building",
"fa-solid fa-briefcase",
"fa-solid fa-id-card",
"fa-regular fa-id-card",
}

// iconeSet è la stessa whitelist in forma di set, per check O(1).
var iconeSet = func() map[string]struct{} {
m := make(map[string]struct{}, len(IconePicker))
for _, ic := range IconePicker {
m[ic] = struct{}{}
}
return m
}()

// iconClassRe valida la shape di una classe Font Awesome:
// "fa-solid fa-NAME" | "fa-regular fa-NAME" | "fa-brands fa-NAME".
// NAME accetta solo [a-z0-9-] per evitare injection nel DOM via classe.
var iconClassRe = regexp.MustCompile(`^fa-(solid|regular|brands) fa-[a-z0-9]+(?:-[a-z0-9]+)*$`)

// normalizeIcon valida la shape della classe FA richiesta; ritorna fallback
// se vuota o malformata. La whitelist statica resta come elenco di icone
// "suggerite" ma non è più vincolante: il picker dinamico copre tutta la
// catalogo FA Free. fallback="" è ammesso (campo opzionale, es. prodotti).
func normalizeIcon(raw, fallback string) string {
v := strings.TrimSpace(raw)
if v == "" {
return fallback
}
if iconClassRe.MatchString(v) {
return v
}
// retro-compatibilità: la legacy whitelist resta valida (es. valori già in DB).
if _, ok := iconeSet[v]; ok {
return v
}
return fallback
}

// ── Prenotazione prodotto esaurito ───────────────────────────────────────────

// GET /prodotti/{id}/prenota — partial modale con form di prenotazione.
func (a *App) handlePrenotaProdottoForm(w http.ResponseWriter, r *http.Request) {
id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
if err != nil {
http.Error(w, "id non valido", 400)
return
}
p, err := a.db.GetProdottoByID(id)
if err != nil {
http.Error(w, "prodotto non trovato", 404)
return
}
a.renderPartial(w, r, "dashboard-utente", "prenotazione-form", map[string]any{
"Prodotto": p,
})
}

// POST /prodotti/{id}/prenota — crea/aggiorna riga bozza con flag prenotazione.
func (a *App) handlePrenotaProdotto(w http.ResponseWriter, r *http.Request) {
id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
if err != nil {
http.Error(w, "id non valido", 400)
return
}
if err := r.ParseForm(); err != nil {
http.Error(w, "bad request", 400)
return
}
qta, err := strconv.Atoi(r.FormValue("qta"))
if err != nil || qta < 1 {
http.Error(w, "quantità non valida", 400)
return
}
nota := strings.TrimSpace(r.FormValue("nota"))
username := a.getUsername(r)
bozzaID, err := a.db.GetOrCreateBozza(username)
if err != nil {
logger.Error("get/create bozza: %v", err)
http.Error(w, "settore non assegnato", 400)
return
}
if err := a.db.UpsertPrenotazione(bozzaID, id, qta, nota); err != nil {
logger.Error("upsert prenotazione: %v", err)
http.Error(w, "errore interno", 500)
return
}
logger.Info("prenotazione registrata: utente=%s prodotto=%d qta=%d", username, id, qta)
// HTMX: chiudere la modale e ricaricare la pagina sulla tab "I miei ordini".
w.Header().Set("HX-Redirect", a.dashboardURLWithView(a.getRole(r), "ordini-personali"))
w.WriteHeader(http.StatusOK)
}

// ── Impostazioni (magazziniere) ──────────────────────────────────────────────

// GET /impostazioni — pagina con tab Generale / Operatività / Notifiche / Sistema.
func (a *App) handleImpostazioniPage(w http.ResponseWriter, r *http.Request) {
imp, _ := a.db.GetAllImpostazioni()
hasLogo, _ := a.db.HasBrandingLogo()
a.render(w, r, "impostazioni", a.viewData(r, map[string]any{
"Username":     a.getUsername(r),
"Role":         a.getRole(r),
"Impostazioni": imp,
"HasLogo":      hasLogo,
"ActiveNav":    "impostazioni",
"SectionName":  "Impostazioni",
"LDAPHost":     a.cfg.LDAPHost,
"SMTPServer":   a.cfg.SMTPServer,
}))
}

// chiaviImpostazioniText elenca le chiavi testuali editabili dal form Generale/Operatività.
var chiaviImpostazioniText = []string{
"brand_name",
"brand_sub",
"soglia_scorta_default",
"evasione_parziale_abilitata",
"auto_approva_funzionario_proprio_settore",
"notif_email_cambio_stato",
"notif_email_soglia_scorta",
"notif_riepilogo_settimanale",
}

var chiaviImpostazioniSet = func() map[string]struct{} {
m := make(map[string]struct{}, len(chiaviImpostazioniText))
for _, k := range chiaviImpostazioniText {
m[k] = struct{}{}
}
return m
}()

// POST /impostazioni — salva i campi testuali e (eventuale) upload logo.
func (a *App) handleImpostazioniSave(w http.ResponseWriter, r *http.Request) {
// Multipart per supportare il logo upload (max 2 MB).
if err := r.ParseMultipartForm(2 << 20); err != nil {
if err2 := r.ParseForm(); err2 != nil {
http.Error(w, "bad request", 400)
return
}
}
for _, k := range chiaviImpostazioniText {
v := strings.TrimSpace(r.FormValue(k))
if _, ok := chiaviImpostazioniSet[k]; !ok {
continue
}
if err := a.db.SetImpostazione(k, v); err != nil {
logger.Error("set impostazione %s: %v", k, err)
http.Error(w, "errore DB", 500)
return
}
}
// Logo opzionale: se presente sostituisce, se "rimuovi=1" cancella.
if r.FormValue("rimuovi_logo") == "1" {
if err := a.db.DeleteBrandingLogo(); err != nil {
logger.Error("delete logo: %v", err)
}
} else if r.MultipartForm != nil {
if fhs := r.MultipartForm.File["brand_logo"]; len(fhs) > 0 {
fh := fhs[0]
f, err := fh.Open()
if err != nil {
http.Error(w, "logo non leggibile", 400)
return
}
defer f.Close()
blob, err := io.ReadAll(f)
if err == nil && len(blob) > 0 {
mime := fh.Header.Get("Content-Type")
if mime == "" {
mime = http.DetectContentType(blob)
}
if err := a.db.SetBrandingLogo(blob, mime); err != nil {
logger.Error("set logo: %v", err)
http.Error(w, "errore DB", 500)
return
}
}
}
}
http.Redirect(w, r, "/impostazioni", http.StatusSeeOther)
}

// -- Logging middleware --------------------------------------------------------

type loggingResponseWriter struct {
http.ResponseWriter
status int
bytes  int
}

func (lrw *loggingResponseWriter) WriteHeader(code int) {
lrw.status = code
lrw.ResponseWriter.WriteHeader(code)
}

func (lrw *loggingResponseWriter) Write(b []byte) (int, error) {
if lrw.status == 0 {
lrw.status = http.StatusOK
}
n, err := lrw.ResponseWriter.Write(b)
lrw.bytes += n
return n, err
}

func (a *App) withRequestLogging(next http.Handler) http.Handler {
return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
start := time.Now()
lrw := &loggingResponseWriter{ResponseWriter: w, status: http.StatusOK}
next.ServeHTTP(lrw, r)
user := a.getUsername(r)
if user == "" {
user = "-"
}
logger.Debug("http method=%s path=%s status=%d bytes=%d duration=%s user=%s",
r.Method, r.URL.Path, lrw.status, lrw.bytes,
time.Since(start).Round(time.Millisecond), user)
})
}

func shouldMaskEnvValue(key string) bool {
k := strings.ToUpper(key)
for _, marker := range []string{"SECRET", "PASSWORD", "PASS", "TOKEN", "KEY", "COOKIE", "CREDENTIAL", "PWD", "AUTH"} {
if strings.Contains(k, marker) {
return true
}
}
return false
}

func maskedEnvValue(v string) string {
if v == "" {
return ""
}
if len(v) <= 4 {
return "****"
}
return v[:2] + "****" + v[len(v)-2:]
}

func logEnvironmentDebug() {
envs := os.Environ()
sort.Strings(envs)
logger.Debug("startup env dump begin count=%d", len(envs))
for _, kv := range envs {
parts := strings.SplitN(kv, "=", 2)
key := parts[0]
val := ""
if len(parts) == 2 {
val = parts[1]
}
if shouldMaskEnvValue(key) {
val = maskedEnvValue(val)
}
logger.Debug("env %s=%s", key, val)
}
logger.Debug("startup env dump end")
}

// -- Main ---------------------------------------------------------------------

func main() {
_ = godotenv.Load()

cfg := config.Load()
if cfg.AppVersion != "" {
AppVersion = cfg.AppVersion
}
logger.Init(cfg.LogLevel)
if strings.EqualFold(cfg.LogLevel, "debug") {
logEnvironmentDebug()
}

dbDir := filepath.Dir(cfg.DBPath)
if err := os.MkdirAll(dbDir, 0750); err != nil {
logger.Error("create db dir: %v", err)
os.Exit(1)
}

db, err := database.InitDB(cfg.DBPath)
if err != nil {
logger.Error("init db: %v", err)
os.Exit(1)
}

if cfg.LDAPHost == "mock" {
if err := db.SeedMockData(); err != nil {
logger.Error("seed mock data: %v", err)
os.Exit(1)
}
logger.Warn("MOCK mode: seeded settore 'MOCK' + users mock.utente / mock.funzionario / mock.magazzino")
}

authKey := []byte(cfg.SessionSecret)
if len(authKey) < 32 {
authKey = append(authKey, make([]byte, 32-len(authKey))...)
}
encKey := sha256.Sum256([]byte(cfg.SessionSecret + "-encryption"))

app := &App{
cfg:   cfg,
db:    db,
store: sessions.NewCookieStore(authKey, encKey[:]),
}
app.notifier = notify.NewEmitter(db, cfg, func() (string, string) {
b := app.brand()
return b.Name, b.LogoSrc
})
ctx, cancel := context.WithCancel(context.Background())
defer cancel()
go notify.NewWorker(db, cfg, app.notifier).Run(ctx)

webDir := "web"
if _, err := os.Stat(webDir); os.IsNotExist(err) {
webDir = "/app/web"
}
if err := app.loadTemplates(filepath.Join(webDir, "templates")); err != nil {
logger.Error("load templates: %v", err)
os.Exit(1)
}

staticDir := filepath.Join(webDir, "static")

mux := http.NewServeMux()
mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(staticDir))))
mux.HandleFunc("/brand-logo", app.handleBrandLogo)
mux.HandleFunc("/", app.handleRoot)
mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
switch r.Method {
case http.MethodGet:
app.handleLoginPage(w, r)
case http.MethodPost:
app.handleLogin(w, r)
default:
http.Error(w, "method not allowed", 405)
}
})
// /logout pubblico (anti-loop se sessione invalida) ma solo POST per CSRF.
mux.HandleFunc("POST /logout", app.handleLogout)

// Dashboard per ruolo
mux.HandleFunc("/dashboard", app.requireRole("user", "funzionario")(app.handleDashboardUtente))
mux.HandleFunc("/dashboard/funzionario", app.requireRole("funzionario")(app.handleDashboardFunzionario))
mux.HandleFunc("/dashboard/magazzino", app.requireRole("magazziniere")(app.handleDashboardMagazzino))
mux.HandleFunc("/dashboard/scorte", app.requireRole("magazziniere")(app.handleDashboardScorte))

// Bozza / carrello
mux.HandleFunc("GET /carrello/badge", app.requireRole("user", "funzionario")(app.handleCartBadge))
mux.HandleFunc("POST /bozza/righe/{prodotto_id}", app.requireRole("user", "funzionario")(app.handleUpsertRigaBozza))
mux.HandleFunc("DELETE /bozza/righe/{prodotto_id}", app.requireRole("user", "funzionario")(app.handleDeleteRigaBozza))
mux.HandleFunc("POST /ordini/{id}/invia", app.requireRole("user", "funzionario")(app.handleInviaOrdine))

// Azioni funzionario
mux.HandleFunc("POST /ordini/{id}/approva", app.requireRole("funzionario")(app.handleApprovaOrdine))
mux.HandleFunc("POST /ordini/{id}/rifiuta", app.requireRole("funzionario")(app.handleRifiutaOrdine))

// Azioni magazziniere
mux.HandleFunc("GET /storico-ordini", app.requireRole("magazziniere")(app.handleStoricoOrdini))
mux.HandleFunc("GET /prodotti/{id}/storico", app.requireRole("magazziniere")(app.handleProdottoStorico))
mux.HandleFunc("GET /ordini/{id}/anteprima-fifo", app.requireRole("magazziniere")(app.handleAnteprimaFIFO))
mux.HandleFunc("POST /ordini/{id}/prepara", app.requireRole("magazziniere")(app.handlePreparaOrdine))
mux.HandleFunc("POST /ordini/{id}/pronto", app.requireRole("magazziniere")(app.handleSegnaPronte))
mux.HandleFunc("POST /ordini/{id}/consegna", app.requireRole("magazziniere")(app.handleConsegnaOrdine))

// Categorie (magazziniere only for writes)
mux.HandleFunc("GET /categorie", app.requireRole("magazziniere")(app.handleListCategorie))
mux.HandleFunc("POST /categorie", app.requireRole("magazziniere")(app.handleCreateCategoria))
mux.HandleFunc("PUT /categorie/{id}", app.requireRole("magazziniere")(app.handleUpdateCategoria))
mux.HandleFunc("DELETE /categorie/{id}", app.requireRole("magazziniere")(app.handleDeleteCategoria))

// Prodotti
mux.HandleFunc("GET /prodotti", app.requireRole("magazziniere")(app.handleMagazzino))
mux.HandleFunc("GET /prodotti/new", app.requireRole("magazziniere")(app.handleNewProdottoForm))
mux.HandleFunc("POST /prodotti", app.requireRole("magazziniere")(app.handleCreateProdotto))
mux.HandleFunc("GET /prodotti/quick", app.requireRole("magazziniere")(app.handleQuickProdottoForm))
mux.HandleFunc("POST /prodotti/quick", app.requireRole("magazziniere")(app.handleQuickCreateProdotto))
mux.HandleFunc("GET /prodotti/{id}/edit", app.requireRole("magazziniere")(app.handleEditProdottoForm))
mux.HandleFunc("POST /prodotti/{id}", app.requireRole("magazziniere")(app.handleUpdateProdotto))
mux.HandleFunc("DELETE /prodotti/{id}", app.requireRole("magazziniere")(app.handleDeleteProdotto))
mux.HandleFunc("GET /prodotti/{id}/immagine", app.requireAuth(app.handleProdottoImmagine))

// Fornitori (CRUD magazziniere)
mux.HandleFunc("GET /fornitori", app.requireRole("magazziniere")(app.handleListFornitori))
mux.HandleFunc("GET /fornitori/new", app.requireRole("magazziniere")(app.handleNewFornitoreForm))
mux.HandleFunc("POST /fornitori", app.requireRole("magazziniere")(app.handleCreateFornitore))
mux.HandleFunc("GET /fornitori/{id}/edit", app.requireRole("magazziniere")(app.handleEditFornitoreForm))
mux.HandleFunc("POST /fornitori/{id}", app.requireRole("magazziniere")(app.handleUpdateFornitore))
mux.HandleFunc("DELETE /fornitori/{id}", app.requireRole("magazziniere")(app.handleDeleteFornitore))

// Acquisti (carico merce: head + righe lotto)
mux.HandleFunc("GET /lotti/new", app.requireRole("magazziniere")(app.handleNewLottoForm))
mux.HandleFunc("POST /lotti", app.requireRole("magazziniere")(app.handleCreateAcquisto))
mux.HandleFunc("GET /acquisti", app.requireRole("magazziniere")(app.handleListAcquisti))
mux.HandleFunc("GET /acquisti/{id}", app.requireRole("magazziniere")(app.handleAcquistoDetail))

// Reportistica magazziniere
mux.HandleFunc("GET /report", app.requireRole("magazziniere")(app.handleReportMagazzino))
mux.HandleFunc("GET /report.csv", app.requireRole("magazziniere")(app.handleReportCSV))

// Impostazioni (magazziniere)
mux.HandleFunc("GET /impostazioni", app.requireRole("magazziniere")(app.handleImpostazioniPage))
mux.HandleFunc("POST /impostazioni", app.requireRole("magazziniere")(app.handleImpostazioniSave))

// Prenotazione prodotto esaurito (utente / funzionario)
mux.HandleFunc("GET /prodotti/{id}/prenota", app.requireRole("user", "funzionario")(app.handlePrenotaProdottoForm))
mux.HandleFunc("POST /prodotti/{id}/prenota", app.requireRole("user", "funzionario")(app.handlePrenotaProdotto))

// Notifiche (tutti gli utenti autenticati)
mux.HandleFunc("GET /notifiche", app.requireAuth(app.handleNotifichePage))
mux.HandleFunc("GET /notifiche/badge", app.requireAuth(app.handleNotificheBadge))
mux.HandleFunc("POST /notifiche/{id}/letta", app.requireAuth(app.handleMarcaNotificaLetta))
mux.HandleFunc("POST /notifiche/leggi-tutte", app.requireAuth(app.handleLeggiTutteNotifiche))

if cfg.LDAPHost == "mock" {
logger.Warn("RUNNING IN MOCK MODE - any credentials accepted")
}

addr := ":" + cfg.Port
logger.Info("E-conomato started on http://0.0.0.0%s", addr)

handler := http.Handler(mux)
if strings.EqualFold(cfg.LogLevel, "debug") {
handler = app.withRequestLogging(handler)
}

if err := http.ListenAndServe(addr, handler); err != nil {
logger.Error("server: %v", err)
os.Exit(1)
}
}
