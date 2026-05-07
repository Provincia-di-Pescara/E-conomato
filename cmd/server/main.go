package main

import (
"crypto/sha256"
"fmt"
"html/template"
"io"
"net/http"
"os"
"path/filepath"
"sort"
"strconv"
"strings"
"time"

"github.com/gorilla/sessions"
"github.com/joho/godotenv"
"github.com/mirkochipdotcom/magazzino/internal/auth"
"github.com/mirkochipdotcom/magazzino/internal/config"
"github.com/mirkochipdotcom/magazzino/internal/database"
"github.com/mirkochipdotcom/magazzino/internal/i18n"
"github.com/mirkochipdotcom/magazzino/internal/logger"
"github.com/mirkochipdotcom/magazzino/internal/models"
)

// AppVersion is injected at build time via -ldflags "-X main.AppVersion=<ver>"
var AppVersion = "unknown"

type App struct {
cfg       *config.Config
db        *database.DB
store     *sessions.CookieStore
templates map[string]*template.Template
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
}

names := []string{"login", "dashboard", "admin", "magazzino", "dashboard-utente", "dashboard-funzionario", "dashboard-magazzino"}
a.templates = make(map[string]*template.Template, len(names))
for _, name := range names {
path := filepath.Join(baseDir, name+".html")
tmpl, err := template.New(name + ".html").Funcs(funcs).ParseFiles(path)
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
"t":       func(key string, args ...any) string { return i18n.T(locale, key, args...) },
"fmtDate": func(t time.Time) string { return t.Format("02 Jan 2006 15:04") },
}
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

func (a *App) requireAuth(next http.HandlerFunc) http.HandlerFunc {
return func(w http.ResponseWriter, r *http.Request) {
if a.getUsername(r) == "" {
http.Redirect(w, r, "/login", http.StatusSeeOther)
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
http.Redirect(w, r, "/login", http.StatusSeeOther)
return
}
role := a.getRole(r)
if role == "admin" {
next(w, r)
return
}
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
case "admin":
return "/admin"
default:
return "/dashboard"
}
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
a.render(w, r, "login", map[string]any{
"MockMode":  a.cfg.LDAPHost == "mock",
"Version":   AppVersion,
"BrandName": a.cfg.BrandName,
"BrandLogo": a.cfg.BrandLogoPath,
})
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
fmt.Fprintf(w, `<p id="login-error" class="error-msg">%s</p>`, template.HTMLEscapeString(msg))
}

// POST /logout
func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
sess, _ := a.store.Get(r, sessionName)
sess.Options.MaxAge = -1
sess.Save(r, w)
http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// GET /dashboard
func (a *App) handleDashboard(w http.ResponseWriter, r *http.Request) {
scorte, err := a.db.GetProdottiSottoSoglia()
if err != nil {
logger.Error("dashboard scorte: %v", err)
scorte = nil
}
a.render(w, r, "dashboard", map[string]any{
"Username":  a.getUsername(r),
"Role":      a.getRole(r),
"IsAdmin":   a.getRole(r) == "admin",
"Scorte":    scorte,
"Version":   AppVersion,
"BrandName": a.cfg.BrandName,
"BrandLogo": a.cfg.BrandLogoPath,
})
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
fmt.Fprint(w, `<div class="empty-state"><svg width="40" height="40" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" style="opacity:.3"><path d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z"/></svg><p style="margin-top:.75rem">Nessun prodotto sotto soglia</p></div>`)
return
}
fmt.Fprint(w, `<table><thead><tr><th>Codice</th><th>Nome</th><th>Categoria</th><th>Scorta att.</th><th>Scorta min.</th></tr></thead><tbody>`)
for _, p := range scorte {
codice := "—"
if p.CodiceArticolo != "" {
codice = template.HTMLEscapeString(p.CodiceArticolo)
}
fmt.Fprintf(w, `<tr><td>%s</td><td><strong>%s</strong></td><td>%s</td><td><span class="badge-low">%d</span></td><td>%d</td></tr>`,
codice,
template.HTMLEscapeString(p.Nome),
template.HTMLEscapeString(p.CategoriaName),
p.ScortaRimanente,
p.ScortaMinima,
)
}
fmt.Fprint(w, `</tbody></table>`)
}

// GET /admin
func (a *App) handleAdminDashboard(w http.ResponseWriter, r *http.Request) {
utenti, err := a.db.GetAllUtenti()
if err != nil {
logger.Error("admin utenti: %v", err)
utenti = nil
}
a.render(w, r, "admin", map[string]any{
"Username":  a.getUsername(r),
"Role":      a.getRole(r),
"IsAdmin":   true,
"Utenti":    utenti,
"Version":   AppVersion,
"BrandName": a.cfg.BrandName,
"BrandLogo": a.cfg.BrandLogoPath,
})
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

// GET /categorie — HTMX partial list
func (a *App) handleListCategorie(w http.ResponseWriter, r *http.Request) {
cats, err := a.db.GetAllCategorie()
if err != nil {
http.Error(w, "Errore DB", http.StatusInternalServerError)
return
}
a.renderPartial(w, r, "magazzino", "categorie-partial", map[string]any{
"Categorie": cats,
})
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
if _, err := a.db.CreateCategoria(nome); err != nil {
logger.Error("create categoria: %v", err)
http.Error(w, "Errore DB", http.StatusInternalServerError)
return
}
// Return updated list as HTMX partial
a.handleListCategorie(w, r)
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
if err := a.db.UpdateCategoria(id, nome); err != nil {
logger.Error("update categoria %d: %v", id, err)
http.Error(w, "Errore DB", http.StatusInternalServerError)
return
}
a.handleListCategorie(w, r)
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
a.handleListCategorie(w, r)
}

// ── Prodotti ─────────────────────────────────────────────────────────────────

// GET /prodotti
func (a *App) handleMagazzino(w http.ResponseWriter, r *http.Request) {
prodotti, err := a.db.GetAllProdotti()
if err != nil {
logger.Error("get prodotti: %v", err)
prodotti = nil
}
categorie, err := a.db.GetAllCategorie()
if err != nil {
logger.Error("get categorie: %v", err)
categorie = nil
}
a.render(w, r, "magazzino", map[string]any{
"Partial":   "prodotti",
"Prodotti":  prodotti,
"Categorie": categorie,
"Username":  a.getUsername(r),
"Role":      a.getRole(r),
"IsAdmin":   a.getRole(r) == "admin",
"Version":   AppVersion,
"BrandName": a.cfg.BrandName,
"BrandLogo": a.cfg.BrandLogoPath,
})
}

// GET /prodotti/new — inline form fragment
func (a *App) handleNewProdottoForm(w http.ResponseWriter, r *http.Request) {
categorie, _ := a.db.GetAllCategorie()
a.renderPartial(w, r, "magazzino", "prodotto-form-partial", map[string]any{
"Prodotto":  models.Prodotto{},
"Categorie": categorie,
})
}

// GET /prodotti/{id}/edit — inline edit form fragment
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
a.renderPartial(w, r, "magazzino", "prodotto-form-partial", map[string]any{
"Prodotto":  p,
"Categorie": categorie,
})
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

// ── Lotti ────────────────────────────────────────────────────────────────────

// GET /lotti/new — form fragment
func (a *App) handleNewLottoForm(w http.ResponseWriter, r *http.Request) {
prodotti, _ := a.db.GetAllProdotti()
a.renderPartial(w, r, "magazzino", "lotto-form-partial", map[string]any{
"Prodotti": prodotti,
})
}

// POST /lotti — create
func (a *App) handleCreateLotto(w http.ResponseWriter, r *http.Request) {
if err := r.ParseForm(); err != nil {
http.Error(w, "bad request", http.StatusBadRequest)
return
}
prodottoID, err := strconv.ParseInt(r.FormValue("prodotto_id"), 10, 64)
if err != nil || prodottoID == 0 {
http.Error(w, "prodotto non valido", http.StatusBadRequest)
return
}
qta, err := strconv.Atoi(r.FormValue("quantita"))
if err != nil || qta <= 0 {
http.Error(w, "quantità non valida", http.StatusBadRequest)
return
}
costo, err := strconv.ParseFloat(strings.ReplaceAll(r.FormValue("costo_unitario"), ",", "."), 64)
if err != nil || costo < 0 {
http.Error(w, "costo non valido", http.StatusBadRequest)
return
}
dataStr := r.FormValue("data_acquisto")
var dataAcquisto time.Time
if dataStr != "" {
dataAcquisto, err = time.Parse("2006-01-02", dataStr)
if err != nil {
http.Error(w, "data non valida (YYYY-MM-DD)", http.StatusBadRequest)
return
}
} else {
dataAcquisto = time.Now()
}
lotto := models.LottoAcquisto{
ProdottoID:       prodottoID,
DataAcquisto:     dataAcquisto,
QuantitaIniziale: qta,
CostoUnitario:    costo,
}
if _, err := a.db.CreateLotto(lotto); err != nil {
logger.Error("create lotto: %v", err)
http.Error(w, "Errore DB", http.StatusInternalServerError)
return
}
http.Redirect(w, r, "/prodotti", http.StatusSeeOther)
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
if err == nil {
defer f.Close()
imgBytes, _ = io.ReadAll(f)
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
}, nil
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
mux.HandleFunc("/logout", app.handleLogout)

// Dashboard per ruolo
mux.HandleFunc("/dashboard", app.requireRole("user", "funzionario")(app.handleDashboardUtente))
mux.HandleFunc("/dashboard/funzionario", app.requireRole("funzionario")(app.handleDashboardFunzionario))
mux.HandleFunc("/dashboard/magazzino", app.requireRole("magazziniere")(app.handleDashboardMagazzino))
mux.HandleFunc("/dashboard/scorte", app.requireRole("magazziniere")(app.handleDashboardScorte))

// Bozza / carrello
mux.HandleFunc("POST /ordini/righe/{prodotto_id}", app.requireRole("user", "funzionario")(app.handleUpsertRigaBozza))
mux.HandleFunc("DELETE /ordini/righe/{prodotto_id}", app.requireRole("user", "funzionario")(app.handleDeleteRigaBozza))
mux.HandleFunc("POST /ordini/{id}/invia", app.requireRole("user", "funzionario")(app.handleInviaOrdine))

// Azioni funzionario
mux.HandleFunc("POST /ordini/{id}/approva", app.requireRole("funzionario")(app.handleApprovaOrdine))
mux.HandleFunc("POST /ordini/{id}/rifiuta", app.requireRole("funzionario")(app.handleRifiutaOrdine))

// Azioni magazziniere
mux.HandleFunc("POST /ordini/{id}/prepara", app.requireRole("magazziniere")(app.handlePreparaOrdine))
mux.HandleFunc("POST /ordini/{id}/pronto", app.requireRole("magazziniere")(app.handleSegnaPronte))
mux.HandleFunc("POST /ordini/{id}/consegna", app.requireRole("magazziniere")(app.handleConsegnaOrdine))

// Admin area
mux.HandleFunc("/admin", app.requireRole("admin")(app.handleAdminDashboard))

// Categorie (magazziniere only for writes)
mux.HandleFunc("GET /categorie", app.requireRole("magazziniere")(app.handleListCategorie))
mux.HandleFunc("POST /categorie", app.requireRole("magazziniere")(app.handleCreateCategoria))
mux.HandleFunc("PUT /categorie/{id}", app.requireRole("magazziniere")(app.handleUpdateCategoria))
mux.HandleFunc("DELETE /categorie/{id}", app.requireRole("magazziniere")(app.handleDeleteCategoria))

// Prodotti
mux.HandleFunc("GET /prodotti", app.requireRole("magazziniere")(app.handleMagazzino))
mux.HandleFunc("GET /prodotti/new", app.requireRole("magazziniere")(app.handleNewProdottoForm))
mux.HandleFunc("POST /prodotti", app.requireRole("magazziniere")(app.handleCreateProdotto))
mux.HandleFunc("GET /prodotti/{id}/edit", app.requireRole("magazziniere")(app.handleEditProdottoForm))
mux.HandleFunc("POST /prodotti/{id}", app.requireRole("magazziniere")(app.handleUpdateProdotto))
mux.HandleFunc("DELETE /prodotti/{id}", app.requireRole("magazziniere")(app.handleDeleteProdotto))
mux.HandleFunc("GET /prodotti/{id}/immagine", app.requireAuth(app.handleProdottoImmagine))

// Lotti
mux.HandleFunc("GET /lotti/new", app.requireRole("magazziniere")(app.handleNewLottoForm))
mux.HandleFunc("POST /lotti", app.requireRole("magazziniere")(app.handleCreateLotto))

if cfg.LDAPHost == "mock" {
logger.Warn("RUNNING IN MOCK MODE - any credentials accepted")
}

addr := ":" + cfg.Port
logger.Info("Gestionale Magazzino started on http://0.0.0.0%s", addr)

handler := http.Handler(mux)
if strings.EqualFold(cfg.LogLevel, "debug") {
handler = app.withRequestLogging(handler)
}

if err := http.ListenAndServe(addr, handler); err != nil {
logger.Error("server: %v", err)
os.Exit(1)
}
}
