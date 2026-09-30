package migrations

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"myserver/internal/database"
)

func names(t *testing.T) []string {
	t.Helper()
	n, err := fs.Glob(FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(n)
	return n
}

func TestMigrationFileNames(t *testing.T) {
	list := names(t)
	if len(list) == 0 || list[0] != "0001_init.sql" {
		t.Fatalf("the first migration must be 0001_init.sql, got %v", list)
	}
	re := regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)
	seen := map[string]string{}
	for _, n := range list {
		m := re.FindStringSubmatch(n)
		if m == nil {
			t.Errorf("%s: name does not follow NNNN_module.sql", n)
			continue
		}
		if other, dup := seen[m[1]]; dup {
			t.Errorf("%s and %s share the number %s; their order would depend on the name", n, other, m[1])
		}
		seen[m[1]] = n
	}
}

func migrated(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(context.Background(), db, FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestEmbeddedMigrationsApplyAndAreIdempotent(t *testing.T) {
	db := migrated(t)
	schema := func() string {
		rows, err := db.Query(`SELECT type || ' ' || name || ' ' || COALESCE(sql, '') FROM sqlite_master ORDER BY type, name`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var all []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			all = append(all, s)
		}
		return strings.Join(all, "\n")
	}
	before := schema()
	for _, table := range []string{"users", "sessions", "settings", "audit_log", "notifications", "schema_migrations"} {
		if !strings.Contains(before, "table "+table+" ") {
			t.Errorf("core table %s missing", table)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != len(names(t)) {
		t.Fatalf("%d migrations recorded, %d embedded (%v)", n, len(names(t)), err)
	}
	if err := database.Migrate(context.Background(), db, FS); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if after := schema(); after != before {
		t.Fatal("the second run changed the schema")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != len(names(t)) {
		t.Fatalf("second run recorded migrations again: %d", n)
	}
}

// A fresh installation has no users and no settings: setup must be
// required, and nothing may ship with a default account.
func TestFreshDatabaseIsEmpty(t *testing.T) {
	db := migrated(t)
	for _, table := range []string{"users", "sessions", "settings", "audit_log", "notifications"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("table %s has %d rows after migration", table, n)
		}
	}
}

func TestSchemaConstraints(t *testing.T) {
	db := migrated(t)
	mustFail := func(what, q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err == nil {
			t.Errorf("%s: statement succeeded", what)
		}
	}
	if _, err := db.Exec(`INSERT INTO users (username, password_hash, role, created_at) VALUES ('ali', 'x', 'user', 1)`); err != nil {
		t.Fatal(err)
	}
	mustFail("duplicate username", `INSERT INTO users (username, password_hash, role, created_at) VALUES ('ali', 'x', 'user', 1)`)
	mustFail("duplicate username in another case", `INSERT INTO users (username, password_hash, role, created_at) VALUES ('ALI', 'x', 'user', 1)`)
	mustFail("unknown role", `INSERT INTO users (username, password_hash, role, created_at) VALUES ('veli', 'x', 'root', 1)`)
	mustFail("missing password hash", `INSERT INTO users (username, role, created_at) VALUES ('veli', 'user', 1)`)
	mustFail("session for a missing user", `INSERT INTO sessions (token_hash, user_id, csrf_token, ip, user_agent, created_at, last_seen_at, expires_at)
		VALUES ('h0', 999, 'c', 'i', 'u', 1, 1, 2)`)
	mustFail("unknown severity", `INSERT INTO notifications (created_at, severity, source, title) VALUES (1, 'FATAL', 's', 't')`)

	if _, err := db.Exec(`INSERT INTO sessions (token_hash, user_id, csrf_token, ip, user_agent, created_at, last_seen_at, expires_at)
		VALUES ('h1', 1, 'c', 'i', 'u', 1, 1, 2)`); err != nil {
		t.Fatal(err)
	}
	mustFail("duplicate session token", `INSERT INTO sessions (token_hash, user_id, csrf_token, ip, user_agent, created_at, last_seen_at, expires_at)
		VALUES ('h1', 1, 'c', 'i', 'u', 1, 1, 2)`)
	if _, err := db.Exec(`DELETE FROM users WHERE username = 'ali'`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("sessions of a deleted user remain: %d (%v)", n, err)
	}
}
