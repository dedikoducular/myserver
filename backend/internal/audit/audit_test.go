package audit

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"myserver/internal/database"
	"myserver/migrations"
)

func newLogger(t *testing.T) (*Logger, *sql.DB) {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return New(db), db
}

var ctx = context.Background()

func TestLogAndList(t *testing.T) {
	l, _ := newLogger(t)
	items, total, err := l.List(ctx, 10, 0)
	if err != nil || total != 0 || items == nil || len(items) != 0 {
		t.Fatalf("empty log must list as []: %v %d %v", items, total, err)
	}
	before := time.Now().Unix()
	l.Log(ctx, Actor{Username: "ali", IP: "192.0.2.1"}, "docker.container_restart", "nginx", "ayrıntı", true)
	l.Log(ctx, Actor{Username: "veli", IP: "192.0.2.2"}, "storage.mount", "/dev/sdb1", "başarısız", false)
	items, total, err = l.List(ctx, 10, 0)
	if err != nil || total != 2 || len(items) != 2 {
		t.Fatalf("%v %d %v", items, total, err)
	}
	if items[0].Username != "veli" || items[1].Username != "ali" {
		t.Fatalf("not newest first: %+v", items)
	}
	e := items[0]
	if e.IP != "192.0.2.2" || e.Action != "storage.mount" || e.Target != "/dev/sdb1" || e.Detail != "başarısız" || e.Success {
		t.Fatalf("record %+v", e)
	}
	if !items[1].Success {
		t.Fatalf("record %+v", items[1])
	}
	if e.CreatedAt < before || e.CreatedAt > time.Now().Unix() {
		t.Fatalf("created_at %d", e.CreatedAt)
	}
}

// Values are data: they are stored as given and cannot alter the statement.
func TestLogStoresHostileValuesVerbatim(t *testing.T) {
	l, db := newLogger(t)
	hostile := `x'); DROP TABLE audit_log; --`
	l.Log(ctx, Actor{Username: hostile, IP: hostile}, hostile, hostile, hostile, true)
	items, total, err := l.List(ctx, 10, 0)
	if err != nil || total != 1 {
		t.Fatalf("%d %v", total, err)
	}
	e := items[0]
	if e.Username != hostile || e.IP != hostile || e.Action != hostile || e.Target != hostile || e.Detail != hostile {
		t.Fatalf("record %+v", e)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'audit_log'`).Scan(&n); err != nil || n != 1 {
		t.Fatal("audit_log table is gone")
	}
}

func TestLogSurvivesCancelledRequest(t *testing.T) {
	l, _ := newLogger(t)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	l.Log(cctx, Actor{Username: "ali", IP: "192.0.2.1"}, "users.delete", "veli", "", true)
	if _, total, _ := l.List(ctx, 10, 0); total != 1 {
		t.Fatal("an audit record was lost because the request was cancelled")
	}
}

func TestLogDoesNotPanicWhenStorageFails(t *testing.T) {
	l, db := newLogger(t)
	db.Close()
	l.Log(ctx, Actor{Username: "ali"}, "x.y", "", "", true) // must only log the failure
	if _, _, err := l.List(ctx, 10, 0); err == nil {
		t.Fatal("List on a closed database reported no error")
	}
}

func TestListPaging(t *testing.T) {
	l, _ := newLogger(t)
	for i := 1; i <= 120; i++ {
		l.Log(ctx, Actor{Username: "ali"}, "x.y", fmt.Sprint(i), "", true)
	}
	page := func(limit, offset int) []Entry {
		t.Helper()
		items, total, err := l.List(ctx, limit, offset)
		if err != nil || total != 120 {
			t.Fatalf("total %d err %v", total, err)
		}
		return items
	}
	if p := page(10, 0); len(p) != 10 || p[0].Target != "120" || p[9].Target != "111" {
		t.Fatalf("first page: %d items, %s..%s", len(p), p[0].Target, p[len(p)-1].Target)
	}
	if p := page(10, 10); len(p) != 10 || p[0].Target != "110" {
		t.Fatalf("second page starts at %s", p[0].Target)
	}
	if p := page(10, 115); len(p) != 5 || p[4].Target != "1" {
		t.Fatalf("last page has %d items", len(p))
	}
	if p := page(10, 500); p == nil || len(p) != 0 {
		t.Fatalf("page past the end: %v", p)
	}
	for _, limit := range []int{0, -5, 501, 1 << 30} {
		if p := page(limit, 0); len(p) != 100 {
			t.Errorf("limit %d returned %d items, want the default 100", limit, len(p))
		}
	}
	if p := page(500, 0); len(p) != 120 {
		t.Errorf("limit 500 returned %d", len(p))
	}
	if p := page(10, -20); len(p) != 10 || p[0].Target != "120" {
		t.Errorf("negative offset: %d items from %s", len(p), p[0].Target)
	}
}

func TestPrune(t *testing.T) {
	l, db := newLogger(t)
	l.Log(ctx, Actor{Username: "ali"}, "x.y", "eski", "", true)
	l.Log(ctx, Actor{Username: "ali"}, "x.y", "sinirda", "", true)
	l.Log(ctx, Actor{Username: "ali"}, "x.y", "yeni", "", true)
	year := int64(365 * 24 * 3600)
	now := time.Now().Unix()
	if _, err := db.Exec(`UPDATE audit_log SET created_at = ? WHERE target = 'eski'`, now-year-3600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE audit_log SET created_at = ? WHERE target = 'sinirda'`, now-year+3600); err != nil {
		t.Fatal(err)
	}
	if err := l.Prune(ctx, 365*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	items, total, _ := l.List(ctx, 10, 0)
	if total != 2 {
		t.Fatalf("%d records left: %+v", total, items)
	}
	for _, e := range items {
		if e.Target == "eski" {
			t.Fatal("old record not pruned")
		}
	}
}
