package auth

import (
	"crypto/tls"
	"fmt"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"
	"github.com/Provincia-di-Pescara/e-conomato/internal/config"
	"github.com/Provincia-di-Pescara/e-conomato/internal/logger"
)

// Authenticate verifies username/password against the configured LDAP/AD server
// and, if LDAP_REQUIRED_GROUP is set, checks that the user is a member of that group.
//
// Supported connection modes (auto-detected from LDAP_HOST):
//   - ldap://host:389  → tries plain, then StartTLS if server requires it
//   - ldaps://host:636 → TLS from the start
//
// Set LDAP_TLS_SKIP_VERIFY=true for self-signed / internal CA certificates.
// Set LDAP_HOST=mock to bypass LDAP entirely (dev mode).
//
// Returns (isAuthenticated, role, error). Role is one of: "admin", "magazziniere", "funzionario", "user".
func Authenticate(username, password string, cfg *config.Config) (bool, string, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return false, "", nil
	}

	// ── MOCK / DEV MODE ─────────────────────────────────────────────────────
	if cfg.LDAPHost == "mock" {
		isAdminMock := false
		for _, adminUser := range strings.Split(cfg.AdminUsers, ";") {
			if strings.EqualFold(strings.TrimSpace(adminUser), username) {
				isAdminMock = true
				break
			}
		}
		return true, resolveRole(isAdminMock, false, false), nil
	}

	// ── TLS CONFIG ──────────────────────────────────────────────────────────
	tlsCfg := &tls.Config{
		InsecureSkipVerify: cfg.LDAPTLSSkipVerify, //nolint:gosec
		ServerName:         ldapHostname(cfg.LDAPHost),
	}

	// ── CONNECT ─────────────────────────────────────────────────────────────
	logger.Debug("ldap: dialing %s", cfg.LDAPHost)
	l, err := ldap.DialURL(cfg.LDAPHost, ldap.DialWithTLSConfig(tlsCfg))
	if err != nil {
		return false, "", fmt.Errorf("ldap dial: %w", err)
	}
	defer l.Close()

	l.SetTimeout(5 * time.Second)

	// ── StartTLS (per ldap:// su porta 389) ─────────────────────────────────
	if cfg.LDAPStartTLS && strings.HasPrefix(cfg.LDAPHost, "ldap://") {
		logger.Debug("ldap: attempting StartTLS...")
		if err := l.StartTLS(tlsCfg); err != nil {
			logger.Warn("ldap: StartTLS failed/ignored: %v. Reconnecting in plain text...", err)
			l.Close()
			// Riconnettiti in chiaro poichè il socket precedente è rimasto "sporco"
			l, err = ldap.DialURL(cfg.LDAPHost)
			if err != nil {
				return false, "", fmt.Errorf("ldap dial (fallback): %w", err)
			}
			l.SetTimeout(5 * time.Second)
		} else {
			logger.Debug("ldap: StartTLS successful")
		}
	}

	// ── USER BIND (verifica credenziali) ────────────────────────────────────
	userDN := fmt.Sprintf(cfg.LDAPUserDNTemplate, username)
	logger.Debug("ldap: attempting bind for %s", userDN)
	if err := l.Bind(userDN, password); err != nil {
		if ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) {
			return false, "", nil // credenziali errate → login fallito
		}
		return false, "", fmt.Errorf("ldap bind (%s): %w", userDN, err)
	}

	// ── EXPLICIT ADMIN LIST CHECK ───────────────────────────────────────────
	isAdmin := false
	if cfg.AdminUsers != "" {
		for _, adminUser := range strings.Split(cfg.AdminUsers, ";") {
			if strings.EqualFold(strings.TrimSpace(adminUser), username) {
				isAdmin = true
				break
			}
		}
	}

	// ── GROUP MEMBERSHIP CHECK ───────────────────────────────────────────────
	// If no LDAP groups are required for login or role derivation, return early.
	noGroupsNeeded := cfg.LDAPRequiredGroup == "" && cfg.LDAPAdminGroup == "" &&
		cfg.LDAPMagazziniereGroup == "" && cfg.LDAPFunzionarioGroup == ""
	if noGroupsNeeded {
		role := resolveRole(isAdmin, false, false)
		return true, role, nil
	}

	// Re-bind con service account se configurato (utile se l'utente non può cercare)
	if cfg.LDAPBindDN != "" {
		if err := l.Bind(cfg.LDAPBindDN, cfg.LDAPBindPassword); err != nil {
			return false, "", fmt.Errorf("ldap service bind: %w", err)
		}
	}

	// Cerca l'utente per ottenere il suo full DN
	searchFilter := fmt.Sprintf(
		"(|(sAMAccountName=%s)(userPrincipalName=%s)(uid=%s))",
		ldap.EscapeFilter(username),
		ldap.EscapeFilter(username),
		ldap.EscapeFilter(strings.Split(username, "@")[0]), // supporta UPN e username puro
	)
	searchReq := ldap.NewSearchRequest(
		cfg.LDAPBaseDN,
		ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		0, 0, false,
		searchFilter,
		[]string{"dn", "memberOf", "sAMAccountName", "cn"},
		nil,
	)

	result, err := l.Search(searchReq)
	if err != nil {
		return false, "", fmt.Errorf("ldap search (base=%s, filter=%s): %w", cfg.LDAPBaseDN, searchFilter, err)
	}
	if len(result.Entries) == 0 {
		return false, "", fmt.Errorf("ldap: user %q not found under base DN %q", username, cfg.LDAPBaseDN)
	}

	userEntry := result.Entries[0]
	userFullDN := userEntry.DN

	requiredGroup := cfg.LDAPRequiredGroup
	adminGroup := cfg.LDAPAdminGroup
	magazziniereGroup := cfg.LDAPMagazziniereGroup
	funzionarioGroup := cfg.LDAPFunzionarioGroup

	hasRequiredGroup := (requiredGroup == "")
	isMagazziniere := false
	isFunzionario := false

	// 1. Controllo base sugli attributi memberOf (più veloce, per membership diretta o server non-AD)
	requiredLower := strings.ToLower(requiredGroup)
	adminLower := strings.ToLower(adminGroup)
	magLower := strings.ToLower(magazziniereGroup)
	funLower := strings.ToLower(funzionarioGroup)

	for _, memberOf := range userEntry.GetAttributeValues("memberOf") {
		memberLower := strings.ToLower(memberOf)

		if !hasRequiredGroup && requiredLower != "" {
			if strings.Contains(memberLower, "cn="+requiredLower+",") || strings.EqualFold(memberOf, requiredLower) || strings.HasPrefix(memberLower, "cn="+requiredLower) {
				hasRequiredGroup = true
			}
		}
		if !isAdmin && adminLower != "" {
			if strings.Contains(memberLower, "cn="+adminLower+",") || strings.EqualFold(memberOf, adminLower) || strings.HasPrefix(memberLower, "cn="+adminLower) {
				isAdmin = true
			}
		}
		if !isMagazziniere && magLower != "" {
			if strings.Contains(memberLower, "cn="+magLower+",") || strings.EqualFold(memberOf, magLower) || strings.HasPrefix(memberLower, "cn="+magLower) {
				isMagazziniere = true
			}
		}
		if !isFunzionario && funLower != "" {
			if strings.Contains(memberLower, "cn="+funLower+",") || strings.EqualFold(memberOf, funLower) || strings.HasPrefix(memberLower, "cn="+funLower) {
				isFunzionario = true
			}
		}
	}

	// 2. Controllo query nested (LDAP_MATCHING_RULE_IN_CHAIN) se non risolto con memberOf e siamo su AD (non mock)
	needsNested := (!hasRequiredGroup && requiredGroup != "") ||
		(!isAdmin && adminGroup != "") ||
		(!isMagazziniere && magazziniereGroup != "") ||
		(!isFunzionario && funzionarioGroup != "")
	if needsNested {
		logger.Debug("ldap: %s: checking nested group membership (IN_CHAIN mode)", username)
	}

	// Helper per interrogazione IN_CHAIN
	checkNestedMembership := func(groupName string, userDN string) bool {
		if groupName == "" {
			return false
		}

		// Gestione: groupName può essere solo il CN (es "gruppoA") o un full DN.
		// Spesso in cfg le persone mettono solo "gruppoA". Cerchiamo un oggetto group con quel CN e member IN_CHAIN userDN.
		var nestedFilter string
		if strings.Contains(groupName, "=") {
			// Probabilmente un DN completo, es "CN=GruppoA,OU=Groups,DC=example,DC=com"
			nestedFilter = fmt.Sprintf(
				"(&(objectClass=group)(distinguishedName=%s)(member:1.2.840.113556.1.4.1941:=%s))",
				ldap.EscapeFilter(groupName),
				ldap.EscapeFilter(userDN),
			)
		} else {
			// Solo il nome del gruppo, es "gruppoA"
			nestedFilter = fmt.Sprintf(
				"(&(objectClass=group)(cn=%s)(member:1.2.840.113556.1.4.1941:=%s))",
				ldap.EscapeFilter(groupName),
				ldap.EscapeFilter(userDN),
			)
		}

		nestedReq := ldap.NewSearchRequest(
			cfg.LDAPBaseDN,
			ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
			0, 0, false,
			nestedFilter,
			[]string{"cn"},
			nil,
		)

		nestedRes, errNested := l.Search(nestedReq)
		if errNested != nil {
			logger.Error("ldap: error checking nested membership for %q in %q: %v", userDN, groupName, errNested)
			return false
		}
		return len(nestedRes.Entries) > 0
	}

	if !hasRequiredGroup && requiredGroup != "" {
		if checkNestedMembership(requiredGroup, userFullDN) {
			hasRequiredGroup = true
		}
	}

	if !isAdmin && adminGroup != "" {
		if checkNestedMembership(adminGroup, userFullDN) {
			isAdmin = true
		}
	}

	if !isMagazziniere && magazziniereGroup != "" {
		if checkNestedMembership(magazziniereGroup, userFullDN) {
			isMagazziniere = true
		}
	}

	if !isFunzionario && funzionarioGroup != "" {
		if checkNestedMembership(funzionarioGroup, userFullDN) {
			isFunzionario = true
		}
	}

	if !hasRequiredGroup {
		return false, "", fmt.Errorf("ldap: user %q authenticated but not in required group %q (direct or nested)", username, cfg.LDAPRequiredGroup)
	}

	role := resolveRole(isAdmin, isMagazziniere, isFunzionario)
	return true, role, nil
}

// resolveRole maps LDAP group flags to a single role string.
// Precedence: admin > magazziniere > funzionario > user.
func resolveRole(isAdmin, isMagazziniere, isFunzionario bool) string {
	switch {
	case isAdmin:
		return "admin"
	case isMagazziniere:
		return "magazziniere"
	case isFunzionario:
		return "funzionario"
	default:
		return "user"
	}
}

// ldapHostname estrae l'hostname dal URL LDAP per la verifica TLS.
func ldapHostname(ldapURL string) string {
	s := strings.TrimPrefix(ldapURL, "ldaps://")
	s = strings.TrimPrefix(s, "ldap://")
	if i := strings.Index(s, ":"); i != -1 {
		return s[:i]
	}
	return s
}
