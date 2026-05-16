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
"github.com/Provincia-di-Pescara/e-conomato/internal/report"
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
"derefInt":  derefInt,
"derefStr":  derefStr,
"derefTime": derefTime,
"deref":     deref,
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
// Template del modulo Cassa Economale (sidebar dedicata).
// I template multi-ruolo (spese-utente, spesa-form, spesa-detail) caricano
// comunque la sidebar economo perché renderizzano condizionalmente nel template.
economoTemplates := map[string]bool{
"dashboard-economo":       true,
"capitoli":                true,
"capitolo-form":           true,
"spese-utente":            true,
"spesa-form":              true,
"spesa-detail":            true,
"report-giornale-cassa":   true,
"reintegri":               true,
"reintegro-form":          true,
"reintegro-detail":        true,
"report-conto-giudiziale": true,
}
// Template con doppia sidebar (magazziniere + economo).
combinedTemplates := map[string]bool{
"dashboard-magazzino-economo": true,
}
sidebarMagazzino := filepath.Join(baseDir, "_sidebar-magazzino.html")
sidebarEconomo := filepath.Join(baseDir, "_sidebar-economo.html")
sidebarCombinata := filepath.Join(baseDir, "_sidebar-magazzino-economo.html")
prenotPartial := filepath.Join(baseDir, "_prenotazione-form.html")
topbarBell := filepath.Join(baseDir, "_topbar-bell.html")
drawerPartial := filepath.Join(baseDir, "_drawer.html")
notifBody := filepath.Join(baseDir, "_notifiche-body.html")

names := []string{"login", "dashboard", "magazzino", "dashboard-utente", "dashboard-funzionario", "dashboard-magazzino", "dashboard-magazzino-economo", "prodotto-form", "lotto-form", "impostazioni", "fornitori", "fornitore-form", "acquisti", "acquisto-detail", "storico-ordini", "notifiche-utente", "notifiche-funzionario", "notifiche-magazzino", "report-magazzino", "dashboard-economo", "capitoli", "capitolo-form", "spese-utente", "spesa-form", "spesa-detail", "report-giornale-cassa", "reintegri", "reintegro-form", "reintegro-detail", "report-conto-giudiziale"}
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
if combinedTemplates[name] {
files = append(files, sidebarMagazzino, sidebarEconomo, sidebarCombinata)
} else {
if magazzinoTemplates[name] {
// Include anche la sidebar combinata: sidebar-magazzino vi delega per ruoli compositi.
files = append(files, sidebarMagazzino, sidebarCombinata)
}
if economoTemplates[name] {
// Include anche la sidebar combinata: sidebar-economo vi delega per ruoli compositi.
files = append(files, sidebarEconomo, sidebarCombinata)
}
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
"deref":        deref,
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

func derefStr(p *string) string {
if p == nil {
return ""
}
return *p
}

func derefTime(p *time.Time) time.Time {
if p == nil {
return time.Time{}
}
return *p
}

func deref(p *float64) float64 {
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

// hasRole restituisce true se roleStr è uguale a target oppure se roleStr è
// il ruolo composto "magazziniere+economo" e target è uno dei due ruoli.
func hasRole(roleStr, target string) bool {
if roleStr == target {
return true
}
if roleStr == "magazziniere+economo" {
return target == "magazziniere" || target == "economo"
}
return false
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
	// Badge sidebar magazzino: inietta Ordini e Scorte se non già presenti,
	// necessari per i contatori nel menu laterale su qualsiasi pagina.
	role := a.getRole(r)
	if hasRole(role, "magazziniere") {
		if _, ok := data["Ordini"]; !ok {
			if ordini, err := a.db.GetOrdiniAttivi(); err == nil {
				data["Ordini"] = ordini
			}
		}
		if _, ok := data["Scorte"]; !ok {
			if scorte, err := a.db.GetProdottiSottoSoglia(); err == nil {
				data["Scorte"] = scorte
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
if role == "admin" {
next(w, r)
return
}
for _, allowed := range roles {
if hasRole(role, allowed) {
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
case "economo":
return "/dashboard-economo"
case "magazziniere+economo":
return "/dashboard/magazzino-economo"
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

// Sync user to SQLite. Per il ruolo composto "magazziniere+economo", splittiamo
// in primario + secondario per la colonna DB; la sessione mantiene la stringa composita.
{
primaryRole := role
var secondaryRole *string
if role == "magazziniere+economo" {
eco := "economo"
primaryRole = "magazziniere"
secondaryRole = &eco
}
if err := a.db.UpsertUtente(username, "", primaryRole, secondaryRole); err != nil {
logger.Error("upsert utente %s: %v", username, err)
}
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
	mieSpese, _ := a.db.GetSpeseUtente(username)
	a.render(w, r, "dashboard-utente", a.viewData(r, map[string]any{
		"Username":    username,
		"Role":        a.getRole(r),
		"Bozza":       bozza,
		"Categorie":   categorie,
		"CategoriaID": catID,
		"Prodotti":    prodotti,
		"Ordini":      ordini,
		"MieSpese":    mieSpese,
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
	mieSpese, _ := a.db.GetSpeseUtente(username)
	speseSettore, _ := a.db.GetSpeseSettore(settoreID)
	var speseDaAutorizzare []models.SpesaEconomale
	for _, s := range speseSettore {
		if s.Stato == "in_approvazione" {
			speseDaAutorizzare = append(speseDaAutorizzare, s)
		}
	}
	a.render(w, r, "dashboard-funzionario", a.viewData(r, map[string]any{
		"Username":            username,
		"Role":                a.getRole(r),
		"DaApprovare":         daApprovare,
		"StoricoSettore":      storicoSettore,
		"SpeseSettore":        speseSettore,
		"Bozza":               bozza,
		"Categorie":           categorie,
		"CategoriaID":         catID,
		"Prodotti":            prodotti,
		"MieiOrdini":          mieiOrdini,
		"MieSpese":            mieSpese,
		"SpeseDaAutorizzare":  speseDaAutorizzare,
		"SettoreNome":         a.settoreNomeFor(username),
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

// GET /dashboard/magazzino-economo — dashboard unificata per utenti con doppio ruolo magazziniere+economo.
func (a *App) handleDashboardMagazzinoEconomo(w http.ResponseWriter, r *http.Request) {
	scorte, _ := a.db.GetProdottiSottoSoglia()
	ordini, _ := a.db.GetOrdiniAttivi()
	anno := time.Now().Year()
	capitoli, err := a.db.GetCapitoliConSaldi(anno)
	if err != nil {
		logger.Error("get capitoli con saldi: %v", err)
		http.Error(w, "Errore DB", http.StatusInternalServerError)
		return
	}
	speseInApprov, _ := a.db.GetSpeseConStato("in_approvazione")
	totaleStanziato := 0.0
	numAttivi := 0
	for _, c := range capitoli {
		if c.Attivo {
			totaleStanziato += c.ImportoStanziato
			numAttivi++
		}
	}
	a.render(w, r, "dashboard-magazzino-economo", a.viewData(r, map[string]any{
		"Username":            a.getUsername(r),
		"Role":                a.getRole(r),
		"ActiveNav":           "ordini",
		"SectionName":         "Dashboard Operativa",
		"Scorte":              scorte,
		"Ordini":              ordini,
		"Anno":                anno,
		"NumCapitoliAttivi":   numAttivi,
		"TotaleStanziato":     totaleStanziato,
		"Capitoli":            capitoli,
		"SpeseInApprovazione": speseInApprov,
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

func (a *App) handleEvadiParziale(w http.ResponseWriter, r *http.Request) {
	ordineID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", 400)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "form non valido", 400)
		return
	}
	qtaPerRiga := map[int64]int{}
	for k, vals := range r.Form {
		if strings.HasPrefix(k, "riga_") {
			rigaID, err := strconv.ParseInt(strings.TrimPrefix(k, "riga_"), 10, 64)
			if err != nil {
				continue
			}
			qty, err := strconv.Atoi(vals[0])
			if err != nil || qty <= 0 {
				continue
			}
			qtaPerRiga[rigaID] = qty
		}
	}
	if len(qtaPerRiga) == 0 {
		http.Error(w, "nessuna quantità specificata", 400)
		return
	}
	scorte, err := a.db.EvadiOrdineParziale(ordineID, qtaPerRiga)
	if err != nil {
		logger.Error("evadi parziale ordine %d: %v", ordineID, err)
		http.Error(w, "errore interno", 500)
		return
	}
	logger.Info("ordine %d evaso parzialmente da %s", ordineID, a.getUsername(r))
	if a.notifier != nil {
		utente, settID, _, err := a.db.GetOrdineMeta(ordineID)
		if err == nil && utente != "" {
			a.notifier.EmitOrdine(notify.EventoOrdineParams{
				Tipo:          "ordine_in_preparazione",
				OrdineID:      ordineID,
				OrdineSettore: settID,
				Destinatari:   []string{utente},
				Mittente:      a.getUsername(r),
				Messaggio:     fmt.Sprintf("Ordine #%d in preparazione (consegna parziale).", ordineID),
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
	switch {
	case role == "funzionario":
		tmplName = "notifiche-funzionario"
	case hasRole(role, "magazziniere") || role == "admin":
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

// GET /scorte — pagina standalone scorte sotto soglia (magazziniere)
func (a *App) handleScortePage(w http.ResponseWriter, r *http.Request) {
	scorte, _ := a.db.GetProdottiSottoSoglia()
	ordini, _ := a.db.GetOrdiniAttivi()
	a.render(w, r, "scorte", a.viewData(r, map[string]any{
		"Username":  a.getUsername(r),
		"Role":      a.getRole(r),
		"Scorte":    scorte,
		"Ordini":    ordini,
		"ActiveNav": "scorte",
	}))
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
	q := r.URL.Query().Get("q")
	filterSottoSoglia := r.URL.Query().Get("filter") == "sotto_soglia"
	var prodotti []database.ProdottoRow
	var err error
	if filterSottoSoglia {
		prodotti, err = a.db.GetProdottiSottoSoglia()
	} else {
		prodotti, err = a.db.GetAllProdotti()
		if err == nil && q != "" {
			ql := strings.ToLower(q)
			filtered := prodotti[:0]
			for _, p := range prodotti {
				if strings.Contains(strings.ToLower(p.Nome), ql) ||
					strings.Contains(strings.ToLower(p.CodiceArticolo), ql) ||
					strings.Contains(strings.ToLower(p.CategoriaName), ql) {
					filtered = append(filtered, p)
				}
			}
			prodotti = filtered
		}
	}
	if err != nil {
		logger.Error("get prodotti: %v", err)
		prodotti = nil
	}
	if r.Header.Get("HX-Request") == "true" {
		a.renderPartial(w, r, "magazzino", "prodotti-partial", map[string]any{
			"Prodotti":          prodotti,
			"FilterSottoSoglia": filterSottoSoglia,
			"Q":                 q,
		})
		return
	}
	categorie, err := a.db.GetAllCategorie()
	if err != nil {
		logger.Error("get categorie: %v", err)
		categorie = nil
	}
	a.render(w, r, "magazzino", a.viewData(r, map[string]any{
		"Partial":           "prodotti",
		"Prodotti":          prodotti,
		"Categorie":         categorie,
		"Username":          a.getUsername(r),
		"Role":              a.getRole(r),
		"ActiveNav":         "prodotti",
		"SectionName":       "Anagrafica prodotti",
		"IconePicker":       IconePicker,
		"FilterSottoSoglia": filterSottoSoglia,
		"Q":                 q,
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

// GET /impostazioni — pagina con tab Generale / Operatività / Notifiche / Sistema / (Utenti per admin).
func (a *App) handleImpostazioniPage(w http.ResponseWriter, r *http.Request) {
imp, _ := a.db.GetAllImpostazioni()
hasLogo, _ := a.db.HasBrandingLogo()
role := a.getRole(r)
data := a.viewData(r, map[string]any{
"Username":     a.getUsername(r),
"Role":         role,
"Impostazioni": imp,
"HasLogo":      hasLogo,
"ActiveNav":    "impostazioni",
"SectionName":  "Impostazioni",
"LDAPHost":     a.cfg.LDAPHost,
"SMTPServer":   a.cfg.SMTPServer,
})
if role == "admin" {
if utenti, err := a.db.GetAllUtenti(); err == nil {
data["Utenti"] = utenti
}
}
a.render(w, r, "impostazioni", data)
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

// POST /impostazioni/utenti/{username}/ruolo — imposta il ruolo di un utente (solo admin).
func (a *App) handleImpostazioniSetRuolo(w http.ResponseWriter, r *http.Request) {
if a.getRole(r) != "admin" {
http.Error(w, "Accesso negato", http.StatusForbidden)
return
}
username := r.PathValue("username")
if username == "" {
http.Error(w, "username mancante", 400)
return
}
if err := r.ParseForm(); err != nil {
http.Error(w, "form non valido", 400)
return
}
newRole := r.FormValue("ruolo")
validRoles := map[string]bool{
"user": true, "funzionario": true, "magazziniere": true,
"economo": true, "magazziniere+economo": true, "admin": true,
}
if !validRoles[newRole] {
http.Error(w, "ruolo non valido", 400)
return
}
primaryRole := newRole
var secondaryRole *string
if newRole == "magazziniere+economo" {
eco := "economo"
primaryRole = "magazziniere"
secondaryRole = &eco
}
if err := a.db.UpsertUtente(username, "", primaryRole, secondaryRole); err != nil {
logger.Error("set ruolo %s→%s: %v", username, newRole, err)
http.Error(w, "errore interno", 500)
return
}
logger.Info("admin %s ha impostato ruolo %s per %s", a.getUsername(r), newRole, username)
http.Redirect(w, r, "/impostazioni?tab=utenti", http.StatusSeeOther)
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
mux.HandleFunc("/scorte", app.requireRole("magazziniere")(app.handleScortePage))

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
mux.HandleFunc("POST /ordini/{id}/evadi-parziale", app.requireRole("magazziniere")(app.handleEvadiParziale))
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

// Impostazioni (magazziniere; admin ha bypass implicito)
mux.HandleFunc("GET /impostazioni", app.requireRole("magazziniere")(app.handleImpostazioniPage))
mux.HandleFunc("POST /impostazioni", app.requireRole("magazziniere")(app.handleImpostazioniSave))
mux.HandleFunc("POST /impostazioni/utenti/{username}/ruolo", app.requireRole("magazziniere")(app.handleImpostazioniSetRuolo))

// Prenotazione prodotto esaurito (utente / funzionario)
mux.HandleFunc("GET /prodotti/{id}/prenota", app.requireRole("user", "funzionario")(app.handlePrenotaProdottoForm))
mux.HandleFunc("POST /prodotti/{id}/prenota", app.requireRole("user", "funzionario")(app.handlePrenotaProdotto))

// Notifiche (tutti gli utenti autenticati)
mux.HandleFunc("GET /notifiche", app.requireAuth(app.handleNotifichePage))
mux.HandleFunc("GET /notifiche/badge", app.requireAuth(app.handleNotificheBadge))
mux.HandleFunc("POST /notifiche/{id}/letta", app.requireAuth(app.handleMarcaNotificaLetta))
mux.HandleFunc("POST /notifiche/leggi-tutte", app.requireAuth(app.handleLeggiTutteNotifiche))

// ── Cassa Economale (Economo) ─────────────────────────────────────────
mux.HandleFunc("GET /dashboard-economo", app.requireRole("economo")(app.handleDashboardEconomo))
mux.HandleFunc("GET /dashboard/magazzino-economo", app.requireRole("magazziniere")(app.handleDashboardMagazzinoEconomo))
mux.HandleFunc("GET /capitoli", app.requireRole("economo")(app.handleListCapitoli))
mux.HandleFunc("GET /capitoli/nuovo", app.requireRole("economo")(app.handleNewCapitoloForm))
mux.HandleFunc("POST /capitoli", app.requireRole("economo")(app.handleCreateCapitolo))
mux.HandleFunc("GET /capitoli/{id}/edit", app.requireRole("economo")(app.handleEditCapitoloForm))
mux.HandleFunc("POST /capitoli/{id}", app.requireRole("economo")(app.handleUpdateCapitolo))
mux.HandleFunc("POST /capitoli/{id}/disattiva", app.requireRole("economo")(app.handleDisattivaCapitolo))
// /spese* aperto a tutti gli autenticati: scoping ruolo-aware nel handler.
mux.HandleFunc("GET /spese", app.requireAuth(app.handleListSpese))
mux.HandleFunc("GET /spese/nuova", app.requireAuth(app.handleNewSpesaForm))
mux.HandleFunc("POST /spese", app.requireAuth(app.handleCreateSpesa))
mux.HandleFunc("GET /spese/{id}", app.requireAuth(app.handleSpesaDetail))
mux.HandleFunc("POST /spese/{id}/autorizza",    app.requireRole("funzionario")(app.handleAutorizzaSpesa))
mux.HandleFunc("POST /spese/{id}/rifiuta-funz", app.requireRole("funzionario")(app.handleRifiutaSpesaFunz))
mux.HandleFunc("POST /spese/{id}/impegna",      app.requireRole("economo")(app.handleImpegnaSpesa))
mux.HandleFunc("POST /spese/{id}/rifiuta-econ", app.requireRole("economo")(app.handleRifiutaSpesaEcon))
mux.HandleFunc("POST /spese/{id}/rendiconta",   app.requireAuth(app.handleRendicontaSpesa))
mux.HandleFunc("POST /spese/{id}/chiudi",              app.requireRole("economo")(app.handleChiudiSpesa))
mux.HandleFunc("POST /spese/{id}/allegati",            app.requireAuth(app.handleUploadAllegato))
mux.HandleFunc("GET /spese/{id}/allegati/{aid}",       app.requireAuth(app.handleServeAllegato))
mux.HandleFunc("POST /spese/{id}/allegati/{aid}/elimina", app.requireAuth(app.handleEliminaAllegato))
// Movimenti cassa (anticipazione / restituzione tesoreria) e giornale.
mux.HandleFunc("POST /economo/anticipazione",           app.requireRole("economo")(app.handleAnticipazione))
mux.HandleFunc("POST /economo/restituzione-tesoreria",  app.requireRole("economo")(app.handleRestituzioneTesoreria))
mux.HandleFunc("GET /economo/giornale-cassa",           app.requireRole("economo")(app.handleGiornaleCassa))
// Reintegri.
mux.HandleFunc("GET /economo/reintegri",                app.requireRole("economo")(app.handleListReintegri))
mux.HandleFunc("GET /economo/reintegro/nuovo",          app.requireRole("economo")(app.handleNewReintegroForm))
mux.HandleFunc("POST /economo/reintegro",               app.requireRole("economo")(app.handleCreaReintegro))
mux.HandleFunc("GET /economo/reintegro/{id}",           app.requireRole("economo")(app.handleReintegroDetail))
mux.HandleFunc("POST /economo/reintegro/{id}/invia",    app.requireRole("economo")(app.handleInviaReintegro))
mux.HandleFunc("POST /economo/reintegro/{id}/liquida",  app.requireRole("economo")(app.handleLiquidaReintegro))
// Export reintegro.
mux.HandleFunc("GET /economo/reintegro/{id}/pdf",          app.requireRole("economo")(app.handleReintegroPDF))
mux.HandleFunc("GET /economo/reintegro/{id}/csv",          app.requireRole("economo")(app.handleReintegroCSV))
mux.HandleFunc("GET /economo/reintegro/{id}/allegati.zip", app.requireRole("economo")(app.handleReintegroZip))
// Conto Giudiziale.
mux.HandleFunc("GET /economo/conto-giudiziale",         app.requireRole("economo")(app.handleContoGiudiziale))

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

// ── Cassa Economale handlers ────────────────────────────────────────────────

// GET /dashboard-economo
func (a *App) handleDashboardEconomo(w http.ResponseWriter, r *http.Request) {
	anno := time.Now().Year()
	capitoli, err := a.db.GetCapitoliConSaldi(anno)
	if err != nil {
		logger.Error("get capitoli con saldi: %v", err)
		http.Error(w, "Errore DB", http.StatusInternalServerError)
		return
	}
	speseInApprov, _ := a.db.GetSpeseConStato("in_approvazione")
	saldoCassa, _ := a.db.GetSaldoCassa(anno)
	totaleStanziato := 0.0
	numAttivi := 0
	for _, c := range capitoli {
		if c.Attivo {
			totaleStanziato += c.ImportoStanziato
			numAttivi++
		}
	}
	a.render(w, r, "dashboard-economo", a.viewData(r, map[string]any{
		"Username":            a.getUsername(r),
		"Role":                a.getRole(r),
		"ActiveNav":           "economo-dashboard",
		"SectionName":         "Dashboard Economo",
		"Anno":                anno,
		"NumCapitoliAttivi":   numAttivi,
		"TotaleStanziato":     totaleStanziato,
		"SaldoCassa":          saldoCassa,
		"Capitoli":            capitoli,
		"SpeseInApprovazione": speseInApprov,
	}))
}

// GET /capitoli
func (a *App) handleListCapitoli(w http.ResponseWriter, r *http.Request) {
	anno := time.Now().Year()
	if y := r.URL.Query().Get("anno"); y != "" {
		if n, err := strconv.Atoi(y); err == nil && n >= 2000 && n <= 2100 {
			anno = n
		}
	}
	capitoli, err := a.db.GetCapitoliConSaldi(anno)
	if err != nil {
		logger.Error("get capitoli: %v", err)
		http.Error(w, "Errore DB", http.StatusInternalServerError)
		return
	}
	a.render(w, r, "capitoli", a.viewData(r, map[string]any{
		"Username":  a.getUsername(r),
		"Role":      a.getRole(r),
		"ActiveNav": "capitoli",
		"Anno":      anno,
		"Capitoli":  capitoli,
	}))
}

// GET /capitoli/nuovo
func (a *App) handleNewCapitoloForm(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "capitolo-form", a.viewData(r, map[string]any{
		"Username":     a.getUsername(r),
		"Role":         a.getRole(r),
		"ActiveNav":    "capitoli",
		"AnnoCorrente": time.Now().Year(),
		"Capitolo":     models.CapitoloSpesa{Attivo: true},
	}))
}

// POST /capitoli
func (a *App) handleCreateCapitolo(w http.ResponseWriter, r *http.Request) {
	c, err := parseCapitoloForm(r)
	if err != nil {
		a.renderCapitoloFormErr(w, r, c, err.Error())
		return
	}
	if _, err := a.db.CreaCapitolo(c); err != nil {
		logger.Error("crea capitolo: %v", err)
		a.renderCapitoloFormErr(w, r, c, "Errore salvataggio: "+err.Error())
		return
	}
	http.Redirect(w, r, "/capitoli", http.StatusSeeOther)
}

// GET /capitoli/{id}/edit
func (a *App) handleEditCapitoloForm(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", http.StatusBadRequest)
		return
	}
	c, err := a.db.GetCapitoloByID(id)
	if err != nil {
		http.Error(w, "capitolo non trovato", http.StatusNotFound)
		return
	}
	a.render(w, r, "capitolo-form", a.viewData(r, map[string]any{
		"Username":     a.getUsername(r),
		"Role":         a.getRole(r),
		"ActiveNav":    "capitoli",
		"AnnoCorrente": time.Now().Year(),
		"Capitolo":     c,
	}))
}

// POST /capitoli/{id}
func (a *App) handleUpdateCapitolo(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", http.StatusBadRequest)
		return
	}
	c, err := parseCapitoloForm(r)
	if err != nil {
		c.ID = id
		a.renderCapitoloFormErr(w, r, c, err.Error())
		return
	}
	c.ID = id
	if err := a.db.AggiornaCapitolo(c); err != nil {
		logger.Error("aggiorna capitolo %d: %v", id, err)
		a.renderCapitoloFormErr(w, r, c, "Errore salvataggio: "+err.Error())
		return
	}
	http.Redirect(w, r, "/capitoli", http.StatusSeeOther)
}

// POST /capitoli/{id}/disattiva
func (a *App) handleDisattivaCapitolo(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", http.StatusBadRequest)
		return
	}
	if err := a.db.DisattivaCapitolo(id); err != nil {
		logger.Error("disattiva capitolo %d: %v", id, err)
		http.Error(w, "Errore DB", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/capitoli", http.StatusSeeOther)
}

func (a *App) renderCapitoloFormErr(w http.ResponseWriter, r *http.Request, c models.CapitoloSpesa, msg string) {
	w.WriteHeader(http.StatusBadRequest)
	a.render(w, r, "capitolo-form", a.viewData(r, map[string]any{
		"Username":     a.getUsername(r),
		"Role":         a.getRole(r),
		"ActiveNav":    "capitoli",
		"AnnoCorrente": time.Now().Year(),
		"Capitolo":     c,
		"Error":        msg,
	}))
}

func parseCapitoloForm(r *http.Request) (models.CapitoloSpesa, error) {
	if err := r.ParseForm(); err != nil {
		return models.CapitoloSpesa{}, fmt.Errorf("bad request")
	}
	anno, err := strconv.Atoi(strings.TrimSpace(r.FormValue("anno")))
	if err != nil || anno < 2000 || anno > 2100 {
		return models.CapitoloSpesa{}, fmt.Errorf("anno non valido")
	}
	codice := strings.TrimSpace(r.FormValue("codice_peg"))
	if codice == "" {
		return models.CapitoloSpesa{}, fmt.Errorf("codice PEG obbligatorio")
	}
	descr := strings.TrimSpace(r.FormValue("descrizione"))
	if descr == "" {
		return models.CapitoloSpesa{}, fmt.Errorf("descrizione obbligatoria")
	}
	importoStr := strings.Replace(strings.TrimSpace(r.FormValue("importo_stanziato")), ",", ".", 1)
	importo, err := strconv.ParseFloat(importoStr, 64)
	if err != nil || importo < 0 {
		return models.CapitoloSpesa{}, fmt.Errorf("importo non valido")
	}
	// Form di creazione: nessun campo "_has_attivo" → default true.
	// Form di edit: presente "_has_attivo=1"; valore di "attivo" determina lo stato
	// (assente = unchecked = false; presente = checked = true).
	attivo := true
	if r.FormValue("_has_attivo") != "" {
		v := r.FormValue("attivo")
		attivo = v == "1" || v == "on" || v == "true"
	}
	return models.CapitoloSpesa{
		Anno:             anno,
		CodicePEG:        codice,
		Descrizione:      descr,
		ImportoStanziato: importo,
		Attivo:           attivo,
	}, nil
}

// GET /spese
func (a *App) handleListSpese(w http.ResponseWriter, r *http.Request) {
	username := a.getUsername(r)
	role := a.getRole(r)
	var (
		spese []models.SpesaEconomale
		err   error
	)
	switch role {
	case "economo", "admin", "magazziniere+economo":
		spese, err = a.db.GetSpeseAll()
	case "funzionario":
		settoreID, errSet := a.db.GetSettoreIDByUsername(username)
		if errSet != nil || settoreID == "" {
			spese = nil
		} else {
			spese, err = a.db.GetSpeseSettore(settoreID)
		}
	default:
		spese, err = a.db.GetSpeseUtente(username)
	}
	if err != nil {
		logger.Error("list spese (%s): %v", role, err)
		http.Error(w, "Errore DB", http.StatusInternalServerError)
		return
	}
	a.render(w, r, "spese-utente", a.viewData(r, map[string]any{
		"Username":  username,
		"Role":      role,
		"ActiveNav": "spese",
		"Spese":     spese,
	}))
}

// GET /spese/nuova
func (a *App) handleNewSpesaForm(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "spesa-form", a.viewData(r, map[string]any{
		"Username":  a.getUsername(r),
		"Role":      a.getRole(r),
		"ActiveNav": "spese",
		"Spesa":     models.SpesaEconomale{TipoPagamento: "contanti"},
	}))
}

// POST /spese
func (a *App) handleCreateSpesa(w http.ResponseWriter, r *http.Request) {
	username := a.getUsername(r)
	settoreID, _ := a.db.GetSettoreIDByUsername(username)
	if settoreID == "" {
		a.renderSpesaFormErr(w, r, models.SpesaEconomale{}, "Settore non assegnato all'utente. Contatta l'amministratore.")
		return
	}
	s, err := parseSpesaForm(r)
	if err != nil {
		a.renderSpesaFormErr(w, r, s, err.Error())
		return
	}
	s.UtenteUsername = username
	s.SettoreID = settoreID
	spesaID, err := a.db.CreaSpesa(s)
	if err != nil {
		logger.Error("crea spesa: %v", err)
		a.renderSpesaFormErr(w, r, s, "Errore salvataggio: "+err.Error())
		return
	}
	if funz, ferr := a.db.GetFunzionarioSettore(settoreID); ferr == nil && funz != "" {
		a.notifier.EmitSpesa(notify.EventoSpesaParams{
			Tipo:         "spesa_inviata",
			SpesaID:      spesaID,
			SpesaSettore: settoreID,
			Motivazione:  s.Motivazione,
			Destinatari:  []string{funz},
			Mittente:     username,
			Messaggio:    fmt.Sprintf("Nuova spesa #%d da autorizzare (settore %s).", spesaID, settoreID),
		})
	}
	http.Redirect(w, r, "/spese", http.StatusSeeOther)
}

// GET /spese/{id}
func (a *App) handleSpesaDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", http.StatusBadRequest)
		return
	}
	spesa, err := a.db.GetSpesaByID(id)
	if err != nil {
		http.Error(w, "spesa non trovata", http.StatusNotFound)
		return
	}
	username := a.getUsername(r)
	role := a.getRole(r)
	// Access control: utente proprio, funzionario stesso settore, economo/admin tutto.
	isEconomo := hasRole(role, "economo") || role == "admin"
	isFunzionario := hasRole(role, "funzionario") || role == "admin"
	isOwner := spesa.UtenteUsername == username
	allowed := isEconomo
	if !allowed {
		settoreID, _ := a.db.GetSettoreIDByUsername(username)
		if isFunzionario {
			allowed = (settoreID != "" && settoreID == spesa.SettoreID)
		} else {
			allowed = isOwner
		}
	}
	if !allowed {
		http.Error(w, "Accesso negato", http.StatusForbidden)
		return
	}
	anno := time.Now().Year()
	capitoli, _ := a.db.GetCapitoliConSaldi(anno)
	allegatiRaw, _ := a.db.GetAllegatiBySpesa(spesa.ID)
	allegati := make([]AllegatoConPermesso, len(allegatiRaw))
	for i, al := range allegatiRaw {
		allegati[i] = AllegatoConPermesso{al, al.CaricatoDa == username || isEconomo}
	}
	terminalStato := map[string]bool{"chiusa": true, "rifiutata_funz": true, "rifiutata_econ": true}
	canUpload := !terminalStato[spesa.Stato] && (isOwner || isFunzionario || isEconomo)
	a.render(w, r, "spesa-detail", a.viewData(r, map[string]any{
		"Username":      username,
		"Role":          role,
		"ActiveNav":     "spese",
		"Spesa":         spesa,
		"Capitoli":      capitoli,
		"IsEconomo":     isEconomo,
		"IsFunzionario": isFunzionario,
		"IsOwner":       isOwner,
		"Allegati":      allegati,
		"CanUpload":     canUpload,
	}))
}

func (a *App) renderSpesaFormErr(w http.ResponseWriter, r *http.Request, s models.SpesaEconomale, msg string) {
	w.WriteHeader(http.StatusBadRequest)
	a.render(w, r, "spesa-form", a.viewData(r, map[string]any{
		"Username":  a.getUsername(r),
		"Role":      a.getRole(r),
		"ActiveNav": "spese",
		"Spesa":     s,
		"Error":     msg,
	}))
}

// spesaRedirect reindirizza alla pagina di dettaglio della spesa.
// Usa HX-Redirect per richieste HTMX, altrimenti redirect standard.
func spesaRedirect(w http.ResponseWriter, r *http.Request, id int64) {
	target := fmt.Sprintf("/spese/%d", id)
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// POST /spese/{id}/autorizza
func (a *App) handleAutorizzaSpesa(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", http.StatusBadRequest)
		return
	}
	username := a.getUsername(r)
	role := a.getRole(r)
	if role != "admin" {
		spesa, err := a.db.GetSpesaByID(id)
		if err != nil {
			http.Error(w, "spesa non trovata", http.StatusNotFound)
			return
		}
		settoreID, _ := a.db.GetSettoreIDByUsername(username)
		if settoreID == "" || settoreID != spesa.SettoreID {
			http.Error(w, "Accesso negato: settore diverso", http.StatusForbidden)
			return
		}
	}
	if err := a.db.AutorizzaSpesa(id, username); err != nil {
		logger.Error("autorizza spesa %d: %v", id, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	logger.Info("spesa %d autorizzata da %s", id, username)
	if economi, err := a.db.GetUtentiByRuolo("economo"); err == nil {
		dest := make([]string, 0, len(economi))
		for _, u := range economi {
			dest = append(dest, u.Username)
		}
		if sp, err := a.db.GetSpesaByID(id); err == nil {
			a.notifier.EmitSpesa(notify.EventoSpesaParams{
				Tipo:         "spesa_autorizzata",
				SpesaID:      id,
				SpesaSettore: sp.SettoreID,
				Motivazione:  sp.Motivazione,
				Destinatari:  dest,
				Mittente:     username,
				Messaggio:    fmt.Sprintf("Spesa #%d autorizzata, pronta per impegno su capitolo.", id),
			})
		}
	}
	if next := r.FormValue("next"); next != "" && r.Header.Get("HX-Request") == "" {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	spesaRedirect(w, r, id)
}

// POST /spese/{id}/rifiuta-funz
func (a *App) handleRifiutaSpesaFunz(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	username := a.getUsername(r)
	role := a.getRole(r)
	if role != "admin" {
		spesa, err := a.db.GetSpesaByID(id)
		if err != nil {
			http.Error(w, "spesa non trovata", http.StatusNotFound)
			return
		}
		settoreID, _ := a.db.GetSettoreIDByUsername(username)
		if settoreID == "" || settoreID != spesa.SettoreID {
			http.Error(w, "Accesso negato: settore diverso", http.StatusForbidden)
			return
		}
	}
	note := strings.TrimSpace(r.FormValue("note"))
	spesaPreRif, _ := a.db.GetSpesaByID(id)
	if err := a.db.RifiutaSpesaFunz(id, username, note); err != nil {
		logger.Error("rifiuta spesa funz %d: %v", id, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	logger.Info("spesa %d rifiutata (funz) da %s", id, username)
	if spesaPreRif.ID > 0 {
		a.notifier.EmitSpesa(notify.EventoSpesaParams{
			Tipo:         "spesa_rifiutata_funz",
			SpesaID:      id,
			SpesaSettore: spesaPreRif.SettoreID,
			Motivazione:  spesaPreRif.Motivazione,
			Destinatari:  []string{spesaPreRif.UtenteUsername},
			Mittente:     username,
			NoteExtra:    note,
			Messaggio:    fmt.Sprintf("La tua spesa #%d è stata rifiutata dal funzionario.", id),
		})
	}
	if next := r.FormValue("next"); next != "" && r.Header.Get("HX-Request") == "" {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	spesaRedirect(w, r, id)
}

// POST /spese/{id}/impegna
func (a *App) handleImpegnaSpesa(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	capitoloID, err := strconv.ParseInt(r.FormValue("capitolo_id"), 10, 64)
	if err != nil || capitoloID <= 0 {
		http.Error(w, "capitolo non valido", http.StatusBadRequest)
		return
	}
	username := a.getUsername(r)
	if err := a.db.ImpegnaSpesa(id, capitoloID, username); err != nil {
		logger.Error("impegna spesa %d: %v", id, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	logger.Info("spesa %d impegnata su capitolo %d da %s", id, capitoloID, username)
	if sp, err := a.db.GetSpesaByID(id); err == nil {
		a.notifier.EmitSpesa(notify.EventoSpesaParams{
			Tipo:         "spesa_impegnata",
			SpesaID:      id,
			SpesaSettore: sp.SettoreID,
			Motivazione:  sp.Motivazione,
			Destinatari:  []string{sp.UtenteUsername},
			Mittente:     username,
			Messaggio:    fmt.Sprintf("La tua spesa #%d è stata impegnata su capitolo. Procedi con la rendicontazione.", id),
		})
	}
	spesaRedirect(w, r, id)
}

// POST /spese/{id}/rifiuta-econ
func (a *App) handleRifiutaSpesaEcon(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	username := a.getUsername(r)
	note := strings.TrimSpace(r.FormValue("note"))
	spesaPreRifEcon, _ := a.db.GetSpesaByID(id)
	if err := a.db.RifiutaSpesaEcon(id, username, note); err != nil {
		logger.Error("rifiuta spesa econ %d: %v", id, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	logger.Info("spesa %d rifiutata (econ) da %s", id, username)
	if spesaPreRifEcon.ID > 0 {
		a.notifier.EmitSpesa(notify.EventoSpesaParams{
			Tipo:         "spesa_rifiutata_econ",
			SpesaID:      id,
			SpesaSettore: spesaPreRifEcon.SettoreID,
			Motivazione:  spesaPreRifEcon.Motivazione,
			Destinatari:  []string{spesaPreRifEcon.UtenteUsername},
			Mittente:     username,
			NoteExtra:    note,
			Messaggio:    fmt.Sprintf("La tua spesa #%d non è stata impegnata dall'economo.", id),
		})
	}
	spesaRedirect(w, r, id)
}

// POST /spese/{id}/rendiconta
func (a *App) handleRendicontaSpesa(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", http.StatusBadRequest)
		return
	}
	spesa, err := a.db.GetSpesaByID(id)
	if err != nil {
		http.Error(w, "spesa non trovata", http.StatusNotFound)
		return
	}
	username := a.getUsername(r)
	role := a.getRole(r)
	isEconomo := hasRole(role, "economo") || role == "admin"
	isFunzionario := hasRole(role, "funzionario") || role == "admin"
	isOwner := spesa.UtenteUsername == username
	allowed := isEconomo || isOwner
	if !allowed && isFunzionario {
		settoreID, _ := a.db.GetSettoreIDByUsername(username)
		allowed = (settoreID != "" && settoreID == spesa.SettoreID)
	}
	if !allowed {
		http.Error(w, "Accesso negato", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	fornitore := strings.TrimSpace(r.FormValue("fornitore"))
	estremiDoc := strings.TrimSpace(r.FormValue("estremi_documento"))
	importoStr := strings.TrimSpace(r.FormValue("importo_effettivo"))
	dataDocStr := strings.TrimSpace(r.FormValue("data_documento"))
	if fornitore == "" || estremiDoc == "" || dataDocStr == "" || importoStr == "" {
		http.Error(w, "tutti i campi sono obbligatori", http.StatusBadRequest)
		return
	}
	importoEff, err := strconv.ParseFloat(strings.ReplaceAll(importoStr, ",", "."), 64)
	if err != nil || importoEff <= 0 {
		http.Error(w, "importo effettivo non valido", http.StatusBadRequest)
		return
	}
	dataDoc, err := time.Parse("2006-01-02", dataDocStr)
	if err != nil {
		http.Error(w, "data documento non valida (formato: AAAA-MM-GG)", http.StatusBadRequest)
		return
	}
	if err := a.db.RendicontaSpesa(id, fornitore, estremiDoc, dataDoc, importoEff); err != nil {
		logger.Error("rendiconta spesa %d: %v", id, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	logger.Info("spesa %d rendicontata da %s", id, username)
	if economi, err := a.db.GetUtentiByRuolo("economo"); err == nil {
		dest := make([]string, 0, len(economi))
		for _, u := range economi {
			dest = append(dest, u.Username)
		}
		a.notifier.EmitSpesa(notify.EventoSpesaParams{
			Tipo:         "spesa_rendicontata",
			SpesaID:      id,
			SpesaSettore: spesa.SettoreID,
			Motivazione:  spesa.Motivazione,
			Destinatari:  dest,
			Mittente:     username,
			Messaggio:    fmt.Sprintf("Spesa #%d rendicontata, pronta per chiusura.", id),
		})
	}
	spesaRedirect(w, r, id)
}

// POST /spese/{id}/chiudi
func (a *App) handleChiudiSpesa(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", http.StatusBadRequest)
		return
	}
	username := a.getUsername(r)
	spesaPreChiudi, _ := a.db.GetSpesaByID(id)
	if err := a.db.ChiudiSpesa(id, username); err != nil {
		logger.Error("chiudi spesa %d: %v", id, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	logger.Info("spesa %d chiusa da %s, movimento_cassa uscita registrato", id, username)
	if spesaPreChiudi.ID > 0 {
		a.notifier.EmitSpesa(notify.EventoSpesaParams{
			Tipo:         "spesa_chiusa",
			SpesaID:      id,
			SpesaSettore: spesaPreChiudi.SettoreID,
			Motivazione:  spesaPreChiudi.Motivazione,
			Destinatari:  []string{spesaPreChiudi.UtenteUsername},
			Mittente:     username,
			Messaggio:    fmt.Sprintf("La tua spesa #%d è stata chiusa e l'uscita è stata registrata.", id),
		})
	}
	spesaRedirect(w, r, id)
}

// AllegatoConPermesso arricchisce AllegatoSpesa con il flag di eliminazione per la view.
type AllegatoConPermesso struct {
	models.AllegatoSpesa
	CanDelete bool
}

// mimeWhitelistAllegati contiene i MIME type accettati per le pezze d'appoggio.
var mimeWhitelistAllegati = map[string]bool{
	"application/pdf": true,
	"image/jpeg":      true,
	"image/png":       true,
}

// spesaAccessCheck verifica che l'utente possa accedere a una spesa.
// Restituisce isEconomo, isFunzionario, isOwner, settoreID e allowed.
func (a *App) spesaAccessCheck(username, role string, spesa models.SpesaEconomale) (isEconomo, isFunzionario, isOwner, allowed bool) {
	isEconomo = hasRole(role, "economo") || role == "admin"
	isFunzionario = hasRole(role, "funzionario") || role == "admin"
	isOwner = spesa.UtenteUsername == username
	if isEconomo {
		allowed = true
		return
	}
	if isFunzionario {
		settoreID, _ := a.db.GetSettoreIDByUsername(username)
		allowed = settoreID != "" && settoreID == spesa.SettoreID
		return
	}
	allowed = isOwner
	return
}

// POST /spese/{id}/allegati
func (a *App) handleUploadAllegato(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", http.StatusBadRequest)
		return
	}
	spesa, err := a.db.GetSpesaByID(id)
	if err != nil {
		http.Error(w, "spesa non trovata", http.StatusNotFound)
		return
	}
	username := a.getUsername(r)
	role := a.getRole(r)
	_, _, _, allowed := a.spesaAccessCheck(username, role, spesa)
	if !allowed {
		http.Error(w, "Accesso negato", http.StatusForbidden)
		return
	}
	terminalStato := map[string]bool{"chiusa": true, "rifiutata_funz": true, "rifiutata_econ": true}
	if terminalStato[spesa.Stato] {
		http.Error(w, "impossibile aggiungere allegati a una spesa in stato "+spesa.Stato, http.StatusBadRequest)
		return
	}
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, "file troppo grande o richiesta non valida (max 10 MB)", http.StatusBadRequest)
		return
	}
	fhs := r.MultipartForm.File["allegato"]
	if len(fhs) == 0 {
		http.Error(w, "nessun file allegato", http.StatusBadRequest)
		return
	}
	fh := fhs[0]
	f, err := fh.Open()
	if err != nil {
		http.Error(w, "apertura file fallita", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	blob, err := io.ReadAll(f)
	if err != nil {
		http.Error(w, "lettura file fallita", http.StatusInternalServerError)
		return
	}
	// MIME whitelist server-side: non fidarsi del Content-Type client.
	detected := http.DetectContentType(blob)
	if !mimeWhitelistAllegati[detected] {
		http.Error(w, "tipo file non ammesso (sono accettati: PDF, JPEG, PNG)", http.StatusBadRequest)
		return
	}
	filename := filepath.Base(fh.Filename)
	if filename == "" || filename == "." {
		filename = "allegato"
	}
	if _, err := a.db.CreaAllegato(models.AllegatoSpesa{
		SpesaID:    id,
		Filename:   filename,
		MimeType:   detected,
		Dimensione: int64(len(blob)),
		BlobData:   blob,
		CaricatoDa: username,
	}); err != nil {
		logger.Error("crea allegato spesa %d: %v", id, err)
		http.Error(w, "errore salvataggio allegato", http.StatusInternalServerError)
		return
	}
	logger.Info("allegato caricato su spesa %d da %s (%d byte, %s)", id, username, len(blob), detected)
	spesaRedirect(w, r, id)
}

// GET /spese/{id}/allegati/{aid}
func (a *App) handleServeAllegato(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", http.StatusBadRequest)
		return
	}
	aid, err := strconv.ParseInt(r.PathValue("aid"), 10, 64)
	if err != nil {
		http.Error(w, "id allegato non valido", http.StatusBadRequest)
		return
	}
	spesa, err := a.db.GetSpesaByID(id)
	if err != nil {
		http.Error(w, "spesa non trovata", http.StatusNotFound)
		return
	}
	username := a.getUsername(r)
	role := a.getRole(r)
	_, _, _, allowed := a.spesaAccessCheck(username, role, spesa)
	if !allowed {
		http.Error(w, "Accesso negato", http.StatusForbidden)
		return
	}
	blob, mimeType, filename, err := a.db.GetAllegatoBlob(aid, id)
	if err != nil || len(blob) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Write(blob) //nolint:errcheck
}

// POST /spese/{id}/allegati/{aid}/elimina
func (a *App) handleEliminaAllegato(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", http.StatusBadRequest)
		return
	}
	aid, err := strconv.ParseInt(r.PathValue("aid"), 10, 64)
	if err != nil {
		http.Error(w, "id allegato non valido", http.StatusBadRequest)
		return
	}
	spesa, err := a.db.GetSpesaByID(id)
	if err != nil {
		http.Error(w, "spesa non trovata", http.StatusNotFound)
		return
	}
	username := a.getUsername(r)
	role := a.getRole(r)
	isEconomo := hasRole(role, "economo") || role == "admin"
	// Carica allegato per verificare uploader.
	allegatiList, _ := a.db.GetAllegatiBySpesa(id)
	var uploaderUsername string
	for _, al := range allegatiList {
		if al.ID == aid {
			uploaderUsername = al.CaricatoDa
			break
		}
	}
	if uploaderUsername == "" {
		http.NotFound(w, r)
		return
	}
	if uploaderUsername != username && !isEconomo {
		http.Error(w, "Accesso negato: solo l'uploader o l'economo può eliminare", http.StatusForbidden)
		return
	}
	if spesa.Stato == "chiusa" {
		http.Error(w, "impossibile eliminare allegati da una spesa chiusa", http.StatusBadRequest)
		return
	}
	if err := a.db.EliminaAllegato(aid); err != nil {
		logger.Error("elimina allegato %d spesa %d: %v", aid, id, err)
		http.Error(w, "errore eliminazione", http.StatusInternalServerError)
		return
	}
	logger.Info("allegato %d eliminato da spesa %d da %s", aid, id, username)
	spesaRedirect(w, r, id)
}

// POST /economo/anticipazione
func (a *App) handleAnticipazione(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	importoStr := strings.TrimSpace(r.FormValue("importo"))
	importo, err := strconv.ParseFloat(strings.ReplaceAll(importoStr, ",", "."), 64)
	if err != nil || importo <= 0 {
		http.Error(w, "importo non valido", http.StatusBadRequest)
		return
	}
	descrizione := strings.TrimSpace(r.FormValue("descrizione"))
	username := a.getUsername(r)
	if err := a.db.RegistraAnticipazione(importo, username, descrizione); err != nil {
		logger.Error("registra anticipazione: %v", err)
		http.Error(w, "errore registrazione: "+err.Error(), http.StatusInternalServerError)
		return
	}
	logger.Info("anticipazione %.2f registrata da %s", importo, username)
	target := "/dashboard/economo"
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// POST /economo/restituzione-tesoreria
func (a *App) handleRestituzioneTesoreria(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	importoStr := strings.TrimSpace(r.FormValue("importo"))
	importo, err := strconv.ParseFloat(strings.ReplaceAll(importoStr, ",", "."), 64)
	if err != nil || importo <= 0 {
		http.Error(w, "importo non valido", http.StatusBadRequest)
		return
	}
	descrizione := strings.TrimSpace(r.FormValue("descrizione"))
	username := a.getUsername(r)
	if err := a.db.RegistraRestituzione(importo, username, descrizione); err != nil {
		logger.Error("registra restituzione: %v", err)
		http.Error(w, "errore registrazione: "+err.Error(), http.StatusInternalServerError)
		return
	}
	logger.Info("restituzione %.2f registrata da %s", importo, username)
	target := "/dashboard/economo"
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// GET /economo/giornale-cassa
func (a *App) handleGiornaleCassa(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	primoMese := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	daStr := r.URL.Query().Get("da")
	aStr := r.URL.Query().Get("a")
	da := primoMese
	a2 := now
	if t, err := time.Parse("2006-01-02", daStr); err == nil {
		da = t
	}
	if t, err := time.Parse("2006-01-02", aStr); err == nil {
		a2 = t
	}
	if r.URL.Query().Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="giornale-cassa-%s-%s.csv"`, da.Format("2006-01-02"), a2.Format("2006-01-02")))
		if err := a.db.StreamGiornaleCSV(w, da, a2); err != nil {
			logger.Error("giornale cassa csv: %v", err)
		}
		return
	}
	righe, err := a.db.GetGiornaleCassa(da, a2)
	if err != nil {
		logger.Error("giornale cassa: %v", err)
		http.Error(w, "errore caricamento giornale", http.StatusInternalServerError)
		return
	}
	// Calcola KPI dal risultato.
	var totEntrate, totUscite float64
	for _, riga := range righe {
		totEntrate += riga.ImportoEntrata
		totUscite += riga.ImportoUscita
	}
	saldoFinale := totEntrate - totUscite
	a.render(w, r, "report-giornale-cassa", a.viewData(r, map[string]any{
		"Username":    a.getUsername(r),
		"Role":        a.getRole(r),
		"ActiveNav":   "giornale_cassa",
		"Righe":       righe,
		"Da":          da.Format("2006-01-02"),
		"A":           a2.Format("2006-01-02"),
		"TotEntrate":  totEntrate,
		"TotUscite":   totUscite,
		"SaldoFinale": saldoFinale,
	}))
}

// rigaReintegroGruppo raggruppa le righe di un reintegro per capitolo PEG.
type rigaReintegroGruppo struct {
	CapitoloID  int64
	CodicePEG   string
	Descrizione string
	Spese       []models.RigaReintegro
	Subtotale   float64
}

// GET /economo/reintegri
func (a *App) handleListReintegri(w http.ResponseWriter, r *http.Request) {
	reintegri, err := a.db.GetReintegri()
	if err != nil {
		logger.Error("list reintegri: %v", err)
		http.Error(w, "errore caricamento reintegri", http.StatusInternalServerError)
		return
	}
	a.render(w, r, "reintegri", a.viewData(r, map[string]any{
		"Username":  a.getUsername(r),
		"Role":      a.getRole(r),
		"ActiveNav": "reintegri",
		"Reintegri": reintegri,
	}))
}

// GET /economo/reintegro/nuovo
func (a *App) handleNewReintegroForm(w http.ResponseWriter, r *http.Request) {
	spese, err := a.db.GetSpeseChiuseNonReintegrate()
	if err != nil {
		logger.Error("spese non reintegrate: %v", err)
		http.Error(w, "errore caricamento spese", http.StatusInternalServerError)
		return
	}
	var totale float64
	for _, s := range spese {
		if s.ImportoEffettivo != nil {
			totale += *s.ImportoEffettivo
		}
	}
	a.render(w, r, "reintegro-form", a.viewData(r, map[string]any{
		"Username":       a.getUsername(r),
		"Role":           a.getRole(r),
		"ActiveNav":      "reintegri",
		"Spese":          spese,
		"ImportoTotale":  totale,
	}))
}

// POST /economo/reintegro
func (a *App) handleCreaReintegro(w http.ResponseWriter, r *http.Request) {
	username := a.getUsername(r)
	reintegro, err := a.db.CreaReintegro(username)
	if err != nil {
		logger.Error("crea reintegro: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	logger.Info("reintegro %d/%d creato da %s, importo %.2f", reintegro.Anno, reintegro.Numero, username, reintegro.ImportoTotale)
	target := fmt.Sprintf("/economo/reintegro/%d", reintegro.ID)
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// GET /economo/reintegro/{id}
func (a *App) handleReintegroDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", http.StatusBadRequest)
		return
	}
	reintegro, err := a.db.GetReintegro(id)
	if err != nil {
		http.Error(w, "reintegro non trovato", http.StatusNotFound)
		return
	}
	righe, err := a.db.BuildRichiestaReintegro(id)
	if err != nil {
		logger.Error("build richiesta reintegro %d: %v", id, err)
		http.Error(w, "errore caricamento righe", http.StatusInternalServerError)
		return
	}
	// Raggruppamento per capitolo PEG.
	var gruppi []rigaReintegroGruppo
	for _, riga := range righe {
		if len(gruppi) == 0 || gruppi[len(gruppi)-1].CapitoloID != riga.CapitoloID {
			gruppi = append(gruppi, rigaReintegroGruppo{
				CapitoloID:  riga.CapitoloID,
				CodicePEG:   riga.CodicePEG,
				Descrizione: riga.DescrizionePEG,
			})
		}
		g := &gruppi[len(gruppi)-1]
		g.Spese = append(g.Spese, riga)
		g.Subtotale += riga.Importo
	}
	a.render(w, r, "reintegro-detail", a.viewData(r, map[string]any{
		"Username":  a.getUsername(r),
		"Role":      a.getRole(r),
		"ActiveNav": "reintegri",
		"Reintegro": reintegro,
		"Gruppi":    gruppi,
	}))
}

// POST /economo/reintegro/{id}/invia
func (a *App) handleInviaReintegro(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", http.StatusBadRequest)
		return
	}
	if err := a.db.AvanzaStatoReintegro(id, "inviata", nil); err != nil {
		logger.Error("invia reintegro %d: %v", id, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	logger.Info("reintegro %d inviato a Ragioneria da %s", id, a.getUsername(r))
	target := fmt.Sprintf("/economo/reintegro/%d", id)
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// POST /economo/reintegro/{id}/liquida
func (a *App) handleLiquidaReintegro(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	dataEmissione := strings.TrimSpace(r.FormValue("data_emissione_mandato"))
	if err := a.db.AvanzaStatoReintegro(id, "liquidata", &dataEmissione); err != nil {
		logger.Error("liquida reintegro %d: %v", id, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	logger.Info("reintegro %d liquidato da %s", id, a.getUsername(r))
	target := fmt.Sprintf("/economo/reintegro/%d", id)
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// GET /economo/conto-giudiziale
func (a *App) handleContoGiudiziale(w http.ResponseWriter, r *http.Request) {
	annoStr := r.URL.Query().Get("anno")
	anno := time.Now().Year()
	if n, err := strconv.Atoi(annoStr); err == nil && n > 2000 {
		anno = n
	}
	sezione, err := a.db.BuildContoGiudiziale(anno)
	if err != nil {
		logger.Error("conto giudiziale anno %d: %v", anno, err)
		http.Error(w, "errore calcolo conto giudiziale", http.StatusInternalServerError)
		return
	}
	switch r.URL.Query().Get("format") {
	case "pdf":
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="conto-giudiziale-%d.pdf"`, anno))
		if err := report.WriteContoGiudizialePDF(w, sezione, a.brand().Name); err != nil {
			logger.Error("conto giudiziale pdf: %v", err)
		}
		return
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="conto-giudiziale-%d.csv"`, anno))
		if err := report.WriteContoGiudizialeCSV(w, sezione); err != nil {
			logger.Error("conto giudiziale csv: %v", err)
		}
		return
	}
	a.render(w, r, "report-conto-giudiziale", a.viewData(r, map[string]any{
		"Username":  a.getUsername(r),
		"Role":      a.getRole(r),
		"ActiveNav": "conto_giudiziale",
		"Sezione":   sezione,
		"Anno":      anno,
	}))
}

// GET /economo/reintegro/{id}/pdf
func (a *App) handleReintegroPDF(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", http.StatusBadRequest)
		return
	}
	reintegro, err := a.db.GetReintegro(id)
	if err != nil {
		http.Error(w, "reintegro non trovato", http.StatusNotFound)
		return
	}
	righe, err := a.db.BuildRichiestaReintegro(id)
	if err != nil {
		logger.Error("reintegro pdf build: %v", err)
		http.Error(w, "errore generazione PDF", http.StatusInternalServerError)
		return
	}
	filename := fmt.Sprintf("reintegro-%d-%d.pdf", reintegro.Anno, reintegro.Numero)
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	if err := report.WriteRichiestaReintegroPDF(w, *reintegro, righe, a.brand().Name); err != nil {
		logger.Error("reintegro pdf: %v", err)
	}
}

// GET /economo/reintegro/{id}/csv
func (a *App) handleReintegroCSV(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", http.StatusBadRequest)
		return
	}
	reintegro, err := a.db.GetReintegro(id)
	if err != nil {
		http.Error(w, "reintegro non trovato", http.StatusNotFound)
		return
	}
	righe, err := a.db.BuildRichiestaReintegro(id)
	if err != nil {
		logger.Error("reintegro csv build: %v", err)
		http.Error(w, "errore generazione CSV", http.StatusInternalServerError)
		return
	}
	filename := fmt.Sprintf("reintegro-%d-%d.csv", reintegro.Anno, reintegro.Numero)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	if err := report.WriteRichiestaReintegroCSV(w, *reintegro, righe); err != nil {
		logger.Error("reintegro csv: %v", err)
	}
}

// GET /economo/reintegro/{id}/allegati.zip
func (a *App) handleReintegroZip(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id non valido", http.StatusBadRequest)
		return
	}
	reintegro, err := a.db.GetReintegro(id)
	if err != nil {
		http.Error(w, "reintegro non trovato", http.StatusNotFound)
		return
	}
	righe, err := a.db.BuildRichiestaReintegro(id)
	if err != nil {
		logger.Error("reintegro zip build: %v", err)
		http.Error(w, "errore generazione ZIP", http.StatusInternalServerError)
		return
	}
	// Raccoglie spesa IDs univoci.
	seen := map[int64]bool{}
	var spesaIDs []int64
	for _, riga := range righe {
		if !seen[riga.SpesaID] {
			seen[riga.SpesaID] = true
			spesaIDs = append(spesaIDs, riga.SpesaID)
		}
	}
	files := map[string][]byte{}
	for _, spesaID := range spesaIDs {
		allegati, err := a.db.GetAllegatiBySpesa(spesaID)
		if err != nil {
			logger.Error("reintegro zip allegati spesa %d: %v", spesaID, err)
			continue
		}
		for _, all := range allegati {
			blob, _, filename, err := a.db.GetAllegatoBlob(all.ID, spesaID)
			if err != nil {
				logger.Error("reintegro zip blob %d: %v", all.ID, err)
				continue
			}
			zipName := fmt.Sprintf("pratica-%d__%s", spesaID, filename)
			files[zipName] = blob
		}
	}
	filename := fmt.Sprintf("reintegro-%d-%d-allegati.zip", reintegro.Anno, reintegro.Numero)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	if err := report.WriteAllegatiZip(w, files); err != nil {
		logger.Error("reintegro zip: %v", err)
	}
}

func parseSpesaForm(r *http.Request) (models.SpesaEconomale, error) {
	if err := r.ParseForm(); err != nil {
		return models.SpesaEconomale{}, fmt.Errorf("bad request")
	}
	mot := strings.TrimSpace(r.FormValue("motivazione"))
	if mot == "" {
		return models.SpesaEconomale{}, fmt.Errorf("motivazione obbligatoria")
	}
	importoStr := strings.Replace(strings.TrimSpace(r.FormValue("importo_presunto")), ",", ".", 1)
	importo, err := strconv.ParseFloat(importoStr, 64)
	if err != nil || importo <= 0 {
		return models.SpesaEconomale{}, fmt.Errorf("importo presunto non valido")
	}
	tp := strings.TrimSpace(r.FormValue("tipo_pagamento"))
	if tp != "contanti" && tp != "carta" {
		return models.SpesaEconomale{}, fmt.Errorf("tipo pagamento non valido")
	}
	return models.SpesaEconomale{
		Motivazione:     mot,
		ImportoPresunto: importo,
		TipoPagamento:   tp,
		Stato:           "in_approvazione",
	}, nil
}
