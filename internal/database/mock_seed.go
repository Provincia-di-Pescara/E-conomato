package database

// SeedMockData inserisce un settore "MOCK" e tre utenti hardcoded di test
// (uno per ciascun ruolo), tutti assegnati a quel settore. Idempotente: usa
// INSERT OR IGNORE, quindi non sovrascrive record già presenti.
//
// Va chiamata SOLO in mock mode (LDAP_HOST=mock). I tre username si
// allineano alla derivazione del ruolo da suffisso fatta in
// internal/auth/ldap.go: il login con qualunque password produce il
// ruolo corretto e UpsertUtente non tocca settore_id.
func (db *DB) SeedMockData() error {
	if _, err := db.conn.Exec(`
		INSERT OR IGNORE INTO settori (id, nome, funzionario_username)
		VALUES ('MOCK', 'Settore Mock (test)', 'mock.funzionario');
	`); err != nil {
		return err
	}
	_, err := db.conn.Exec(`
		INSERT OR IGNORE INTO utenti (username, email, ruolo, settore_id) VALUES
			('mock.utente',      '', 'user',         'MOCK'),
			('mock.funzionario', '', 'funzionario',  'MOCK'),
			('mock.magazzino',   '', 'magazziniere', 'MOCK');
	`)
	return err
}
