package database

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
)

func open(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "sub", "dir", "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func applied(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM schema_migrations ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestOpenCreatesDatabaseWithPragmas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("database file not created: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("database file mode %o, want 600", perm)
		}
		di, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		if di.Mode().Perm()&0o007 != 0 {
			t.Errorf("database directory is accessible to others: %o", di.Mode().Perm())
		}
	}
	// Every pooled connection must have the pragmas, not only the first.
	ctx := context.Background()
	var conns []*sql.Conn
	for i := 0; i < 4; i++ {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	for i, c := range conns {
		var fk int
		if err := c.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
			t.Errorf("connection %d: foreign_keys = %d (%v)", i, fk, err)
		}
		var mode string
		if err := c.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil || !strings.EqualFold(mode, "wal") {
			t.Errorf("connection %d: journal_mode = %q (%v)", i, mode, err)
		}
		var busy int
		if err := c.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busy); err != nil || busy != 5000 {
			t.Errorf("connection %d: busy_timeout = %d (%v)", i, busy, err)
		}
		c.Close()
	}
}

func TestOpenPathWithSpecialCharacters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "veri dizini #1", "a?b=c")
	if runtime.GOOS == "windows" {
		dir = filepath.Join(t.TempDir(), "veri dizini #1")
	}
	path := filepath.Join(dir, "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the database was not created at the requested path %q: %v", path, err)
	}
}

func TestMigrateAppliesInOrderOnce(t *testing.T) {
	db := open(t)
	ctx := context.Background()
	fsys := fstest.MapFS{
		// Declared out of order on purpose; 0002 depends on 0001 and 0010
		// on 0002, so a wrong order fails.
		"0010_c.sql":     {Data: []byte(`INSERT INTO b (a_id) SELECT id FROM a;`)},
		"0002_b.sql":     {Data: []byte(`CREATE TABLE b (a_id INTEGER REFERENCES a(id)); INSERT INTO a (id) VALUES (1);`)},
		"0001_a.sql":     {Data: []byte(`CREATE TABLE a (id INTEGER PRIMARY KEY);`)},
		"notes.txt":      {Data: []byte(`this is not SQL`)},
		"0003_empty.sql": {Data: []byte("  \n")},
	}
	if err := Migrate(ctx, db, fsys); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	want := "0001_a.sql,0002_b.sql,0003_empty.sql,0010_c.sql"
	if got := strings.Join(applied(t, db), ","); got != want {
		t.Fatalf("applied %q, want %q", got, want)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM b`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows in b: %d (%v)", n, err)
	}

	// Second run: nothing is executed again (re-running 0010 would add a
	// row, re-running 0001 would fail).
	if err := Migrate(ctx, db, fsys); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if got := strings.Join(applied(t, db), ","); got != want {
		t.Fatalf("after the second run applied = %q", got)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM b`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("second run re-applied a migration: %d rows in b", n)
	}

	// A migration added later is the only one that runs.
	fsys["0011_d.sql"] = &fstest.MapFile{Data: []byte(`CREATE TABLE d (x INTEGER);`)}
	if err := Migrate(ctx, db, fsys); err != nil {
		t.Fatalf("third migrate: %v", err)
	}
	if !tableExists(t, db, "d") {
		t.Fatal("new migration not applied")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM b`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("third run re-applied a migration: %d rows in b", n)
	}
}

func TestMigrateFailureRollsBack(t *testing.T) {
	db := open(t)
	ctx := context.Background()
	fsys := fstest.MapFS{
		"0001_ok.sql": {Data: []byte(`CREATE TABLE ok1 (x INTEGER);`)},
		"0002_bad.sql": {Data: []byte(`CREATE TABLE half (x INTEGER);
			INSERT INTO half (x) VALUES (1);
			INSERT INTO ok1 (x) VALUES (7);
			CREATE TABLE ok1 (x INTEGER);`)}, // fails: the table exists
		"0003_later.sql": {Data: []byte(`CREATE TABLE later (x INTEGER);`)},
	}
	err := Migrate(ctx, db, fsys)
	if err == nil {
		t.Fatal("a failing migration was not reported")
	}
	if !strings.Contains(err.Error(), "0002_bad.sql") {
		t.Errorf("the error does not name the migration: %v", err)
	}
	if got := strings.Join(applied(t, db), ","); got != "0001_ok.sql" {
		t.Fatalf("recorded migrations %q, want only 0001_ok.sql", got)
	}
	if tableExists(t, db, "half") {
		t.Fatal("a table created by the failed migration is still there")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ok1`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows written by the failed migration survived: %d (%v)", n, err)
	}
	if tableExists(t, db, "later") {
		t.Fatal("a migration after the failed one was applied")
	}

	// The database stays usable and the corrected migration applies.
	fsys["0002_bad.sql"] = &fstest.MapFile{Data: []byte(`CREATE TABLE half (x INTEGER);`)}
	if err := Migrate(ctx, db, fsys); err != nil {
		t.Fatalf("migrate after the fix: %v", err)
	}
	if got := strings.Join(applied(t, db), ","); got != "0001_ok.sql,0002_bad.sql,0003_later.sql" {
		t.Fatalf("applied %q", got)
	}
}

func TestMigrateSyntaxErrorRollsBack(t *testing.T) {
	db := open(t)
	fsys := fstest.MapFS{
		"0001_bad.sql": {Data: []byte(`CREATE TABLE first (x INTEGER); THIS IS NOT SQL;`)},
	}
	if err := Migrate(context.Background(), db, fsys); err == nil {
		t.Fatal("syntax error not reported")
	}
	if tableExists(t, db, "first") {
		t.Fatal("partial migration left a table behind")
	}
	if got := applied(t, db); len(got) != 0 {
		t.Fatalf("recorded %v", got)
	}
}

func TestMigrateCancelledContext(t *testing.T) {
	db := open(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Migrate(ctx, db, fstest.MapFS{"0001_a.sql": {Data: []byte(`CREATE TABLE a (x INTEGER);`)}})
	if err == nil {
		t.Fatal("migration ran with a cancelled context")
	}
	if tableExists(t, db, "a") {
		t.Fatal("table created despite cancellation")
	}
}
