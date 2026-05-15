package config

import (
	"os"
	"strconv"
	"strings"
)

// Config holds all runtime configuration loaded from environment variables.
type Config struct {
	Port          string
	SessionSecret string

	// LDAP / Active Directory
	LDAPHost           string
	LDAPBaseDN         string
	LDAPUserDNTemplate string
	// LDAPTLSSkipVerify: impostare a true solo per DC con certificato self-signed.
	// Mai usare in produzione con certificati validi.
	LDAPTLSSkipVerify bool
	// LDAPStartTLS: se true (default), tenta l'upgrade StartTLS su connessioni ldap://
	LDAPStartTLS bool
	// LDAPBindDN + LDAPBindPassword: service account used for group-membership searches.
	// If empty, the user's own authenticated session is reused.
	LDAPBindDN       string
	LDAPBindPassword string
	// LDAPRequiredGroup: CN of the AD group required to log in (empty = allow all).
	LDAPRequiredGroup string
	// LDAPMagazziniereGroup: AD group granting the 'magazziniere' role.
	LDAPMagazziniereGroup string
	// LDAPFunzionarioGroup: AD group granting the 'funzionario' role.
	LDAPFunzionarioGroup string
	// LDAPEconomoGroup: AD group granting the 'economo' role (Cassa Economale).
	LDAPEconomoGroup string

	// SecureCookies: se true (default), imposta il flag Secure sui cookie di sessione,
	// richiedendo HTTPS. Impostare a false solo se dietro reverse proxy che termina TLS.
	SecureCookies bool
	// LoginSessionTTLHours: how long a login session (cookie) is valid. Default 24 hours.
	LoginSessionTTLHours int

	// Persistenza
	DBPath string

	// Branding aziendale opzionale
	// BrandName: nome dell'ente/azienda da affiancare al logo E-conomato
	// BrandLogoPath: path relativo a /static/ del logo PNG (es. "img/brand-logo.png")
	BrandName     string
	BrandLogoPath string

	// SMTP configuration for transactional emails
	SMTPServer   string
	SMTPPort     int
	SMTPSecurity string // auto|starttls|ssl|none
	SMTPUser     string
	SMTPPassword string
	SMTPFrom     string
	SMTPUserAuth bool

	// LogLevel controls the application logging verbosity (debug, info, warn, error)
	LogLevel string
	// AppVersion: optional manual override for version string
	AppVersion string

	// AppBaseURL: base assoluto usato nei link delle email transazionali
	// (es. "https://e-conomato.example.com"). Vuoto = i link nelle email usano
	// path relativi.
	AppBaseURL string
}

// Load reads environment variables and returns a populated Config.
// Falls back to sensible defaults when variables are absent.
func Load() *Config {
	return &Config{
		Port:                  getEnv("APP_PORT", "8080"),
		SessionSecret:         getEnv("SESSION_SECRET", "please-change-me"),
		LDAPHost:              getEnv("LDAP_HOST", "mock"),
		LDAPBaseDN:            getEnv("LDAP_BASE_DN", "dc=example,dc=com"),
		LDAPUserDNTemplate:    getEnv("LDAP_USER_DN_TEMPLATE", "uid=%s,ou=Users,dc=example,dc=com"),
		LDAPTLSSkipVerify:     getEnvBool("LDAP_TLS_SKIP_VERIFY", false),
		LDAPStartTLS:          getEnvBool("LDAP_STARTTLS", true),
		LDAPBindDN:            getEnv("LDAP_BIND_DN", ""),
		LDAPBindPassword:      getEnv("LDAP_BIND_PASSWORD", ""),
		LDAPRequiredGroup:     getEnv("LDAP_REQUIRED_GROUP", ""),
		LDAPMagazziniereGroup: getEnv("LDAP_MAGAZZINIERE_GROUP", ""),
		LDAPFunzionarioGroup:  getEnv("LDAP_FUNZIONARIO_GROUP", ""),
		LDAPEconomoGroup:      getEnv("LDAP_ECONOMO_GROUP", ""),

		SecureCookies:        getEnvBool("SECURE_COOKIES", true),
		LoginSessionTTLHours: getEnvInt("LOGIN_SESSION_TTL_HOURS", 24),

		DBPath: getEnv("DB_PATH", "/data/magazzino.db"),

		BrandName:     getEnv("BRAND_NAME", ""),
		BrandLogoPath: getEnv("BRAND_LOGO", ""),

		SMTPServer:   getEnv("SMTP_SERVER", ""),
		SMTPPort:     getEnvInt("SMTP_PORT", 587),
		SMTPSecurity: strings.ToLower(getEnv("SMTP_SECURITY", "auto")),
		SMTPUser:     getEnv("SMTP_USER", ""),
		SMTPPassword: getEnv("SMTP_PASSWORD", ""),
		SMTPFrom:     getEnv("SMTP_FROM", ""),
		SMTPUserAuth: getEnvBool("SMTP_USER_AUTH", false),

		LogLevel:   strings.ToLower(getEnv("LOG_LEVEL", "info")),
		AppVersion: getEnv("APP_VERSION", ""),
		AppBaseURL: strings.TrimRight(getEnv("APP_BASE_URL", ""), "/"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	v := strings.ToLower(os.Getenv(key))
	if v == "true" || v == "1" || v == "yes" {
		return true
	}
	if v == "false" || v == "0" || v == "no" {
		return false
	}
	return fallback
}
