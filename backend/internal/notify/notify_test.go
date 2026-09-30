package notify

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"myserver/internal/database"
	"myserver/migrations"
)

func newCenter(t *testing.T) (*Center, *sql.DB) {
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

func count(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM notifications`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

var ctx = context.Background()

func TestPublishStoresAndLists(t *testing.T) {
	c, _ := newCenter(t)
	c.Publish(ctx, Info, "docker", "Birinci", "m1")
	c.Publish(ctx, Warning, "storage", "İkinci", "m2")
	c.Publish(ctx, Critical, "storage", "Üçüncü", "")
	items, err := c.List(ctx, 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("%d items", len(items))
	}
	if items[0].Title != "Üçüncü" || items[2].Title != "Birinci" {
		t.Fatalf("not newest first: %+v", items)
	}
	n := items[1]
	if n.Severity != Warning || n.Source != "storage" || n.Message != "m2" || n.Read || n.ID == 0 || n.CreatedAt == 0 {
		t.Fatalf("stored notification %+v", n)
	}
	if items, _ := c.List(ctx, 2, false); len(items) != 2 || items[0].Title != "Üçüncü" {
		t.Fatalf("limit 2: %+v", items)
	}
	empty, _ := newCenter(t)
	items, err = empty.List(ctx, 10, false)
	if err != nil || items == nil || len(items) != 0 {
		t.Fatalf("empty list must be [] and not nil: %v %v", items, err)
	}
}

func TestPublishRejectsUnknownSeverity(t *testing.T) {
	c, db := newCenter(t)
	c.Publish(ctx, Severity("FATAL"), "x", "t", "m")
	if n := count(t, db); n != 0 {
		t.Fatalf("notification with an unknown severity stored")
	}
}

func TestPublishSurvivesCancelledRequest(t *testing.T) {
	c, db := newCenter(t)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	c.Publish(cctx, Error, "backup", "Yedekleme başarısız", "")
	if n := count(t, db); n != 1 {
		t.Fatal("a notification was lost because the request that caused it was cancelled")
	}
}

func TestPublishOnceDeduplicatesWithinWindow(t *testing.T) {
	c, db := newCenter(t)
	for i := 0; i < 5; i++ {
		c.PublishOnce(ctx, Warning, "storage", "Disk dolu", "m", "disk-full:/data", time.Hour)
	}
	if n := count(t, db); n != 1 {
		t.Fatalf("%d notifications for one condition inside the window", n)
	}
	// Other keys are independent.
	c.PublishOnce(ctx, Warning, "storage", "Disk dolu", "m", "disk-full:/media", time.Hour)
	if n := count(t, db); n != 2 {
		t.Fatalf("another key was suppressed: %d", n)
	}
	// Publish is never deduplicated.
	c.Publish(ctx, Warning, "storage", "Disk dolu", "m")
	c.Publish(ctx, Warning, "storage", "Disk dolu", "m")
	if n := count(t, db); n != 4 {
		t.Fatalf("Publish was deduplicated: %d", n)
	}
	// Reading or the state of a notification does not reopen the window.
	if err := c.MarkAllRead(ctx); err != nil {
		t.Fatal(err)
	}
	c.PublishOnce(ctx, Warning, "storage", "Disk dolu", "m", "disk-full:/data", time.Hour)
	if n := count(t, db); n != 4 {
		t.Fatalf("a read notification ended the window: %d", n)
	}
}

func TestPublishOnceAfterWindow(t *testing.T) {
	c, db := newCenter(t)
	const key = "temp-high"
	c.PublishOnce(ctx, Warning, "system", "Sıcaklık yüksek", "m", key, time.Hour)
	// Age the stored notification instead of waiting.
	age := func(seconds int64) {
		t.Helper()
		if _, err := db.Exec(`UPDATE notifications SET created_at = ? WHERE dedupe_key = ?`, time.Now().Unix()-seconds, key); err != nil {
			t.Fatal(err)
		}
	}
	age(3600 - 60)
	c.PublishOnce(ctx, Warning, "system", "Sıcaklık yüksek", "m", key, time.Hour)
	if n := count(t, db); n != 1 {
		t.Fatalf("published again one minute before the end of the window: %d", n)
	}
	age(3600 + 60)
	c.PublishOnce(ctx, Warning, "system", "Sıcaklık yüksek", "m", key, time.Hour)
	if n := count(t, db); n != 2 {
		t.Fatalf("not published after the window: %d", n)
	}
	// The new one opens a new window.
	c.PublishOnce(ctx, Warning, "system", "Sıcaklık yüksek", "m", key, time.Hour)
	if n := count(t, db); n != 2 {
		t.Fatalf("new window not honoured: %d", n)
	}
}

func TestPublishOnceConcurrent(t *testing.T) {
	c, db := newCenter(t)
	const workers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			c.PublishOnce(ctx, Critical, "storage", "Disk arızası", "m", "disk-fail:sda", time.Hour)
		}()
	}
	close(start)
	wg.Wait()
	if n := count(t, db); n != 1 {
		t.Fatalf("%d notifications created by %d concurrent reports of one condition", n, workers)
	}
}

func TestSubscribersReceiveNotifications(t *testing.T) {
	c, _ := newCenter(t)
	a, cancelA := c.Subscribe()
	b, cancelB := c.Subscribe()
	defer cancelB()
	c.Publish(ctx, Success, "apps", "Kuruldu", "m")
	for name, ch := range map[string]<-chan Notification{"a": a, "b": b} {
		select {
		case n := <-ch:
			if n.Title != "Kuruldu" || n.Severity != Success || n.ID == 0 || n.Read {
				t.Errorf("subscriber %s received %+v", name, n)
			}
		default:
			t.Errorf("subscriber %s received nothing", name)
		}
	}
	cancelA()
	cancelA() // cancelling twice is harmless
	c.Publish(ctx, Success, "apps", "İkinci", "m")
	select {
	case n := <-a:
		t.Errorf("cancelled subscriber received %+v", n)
	default:
	}
	if n := <-b; n.Title != "İkinci" {
		t.Errorf("remaining subscriber received %+v", n)
	}
	// Suppressed duplicates are not broadcast either.
	c.PublishOnce(ctx, Info, "x", "Bir", "", "k", time.Hour)
	<-b
	c.PublishOnce(ctx, Info, "x", "Bir", "", "k", time.Hour)
	select {
	case n := <-b:
		t.Errorf("suppressed notification was broadcast: %+v", n)
	default:
	}
}

func TestSlowSubscriberDoesNotBlockPublishers(t *testing.T) {
	c, db := newCenter(t)
	slow, cancel := c.Subscribe() // never read
	defer cancel()
	const total = 100
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < total; i++ {
			c.Publish(ctx, Info, "test", fmt.Sprintf("n%d", i), "")
		}
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("publishers are blocked by a subscriber that does not read")
	}
	if n := count(t, db); n != total {
		t.Fatalf("%d of %d notifications stored", n, total)
	}
	// What the slow subscriber did receive is the oldest part, in order.
	got := len(slow)
	if got == 0 || got >= total {
		t.Fatalf("slow subscriber has %d queued notifications", got)
	}
	for i := 0; i < got; i++ {
		if n := <-slow; n.Title != fmt.Sprintf("n%d", i) {
			t.Fatalf("queued notification %d is %q", i, n.Title)
		}
	}
	// A subscriber that arrives later is served normally.
	fresh, cancelFresh := c.Subscribe()
	defer cancelFresh()
	c.Publish(ctx, Info, "test", "son", "")
	select {
	case n := <-fresh:
		if n.Title != "son" {
			t.Fatalf("received %+v", n)
		}
	default:
		t.Fatal("new subscriber received nothing")
	}
}

func TestUnreadCountAndMarkRead(t *testing.T) {
	c, _ := newCenter(t)
	unread := func() int {
		t.Helper()
		n, err := c.UnreadCount(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if unread() != 0 {
		t.Fatal("unread count of an empty center")
	}
	for i := 0; i < 4; i++ {
		c.Publish(ctx, Info, "s", fmt.Sprintf("n%d", i), "")
	}
	items, _ := c.List(ctx, 10, false)
	if unread() != 4 {
		t.Fatalf("unread %d", unread())
	}
	if err := c.MarkRead(ctx, items[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := c.MarkRead(ctx, items[0].ID); err != nil { // again: no effect
		t.Fatal(err)
	}
	if err := c.MarkRead(ctx, 99999); err != nil { // unknown: no effect
		t.Fatal(err)
	}
	if unread() != 3 {
		t.Fatalf("unread %d after reading one", unread())
	}
	all, _ := c.List(ctx, 10, false)
	if !all[0].Read || all[1].Read {
		t.Fatalf("read flags: %+v", all)
	}
	only, _ := c.List(ctx, 10, true)
	if len(only) != 3 {
		t.Fatalf("unread filter returned %d", len(only))
	}
	for _, n := range only {
		if n.Read || n.ID == items[0].ID {
			t.Fatalf("unread filter returned %+v", n)
		}
	}
	if err := c.Delete(ctx, items[1].ID); err != nil {
		t.Fatal(err)
	}
	if unread() != 2 {
		t.Fatalf("unread %d after deleting an unread one", unread())
	}
	if err := c.MarkAllRead(ctx); err != nil {
		t.Fatal(err)
	}
	if unread() != 0 {
		t.Fatalf("unread %d after MarkAllRead", unread())
	}
	if err := c.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if all, _ := c.List(ctx, 10, false); len(all) != 0 {
		t.Fatalf("%d left after Clear", len(all))
	}
}

func TestListLimitBounds(t *testing.T) {
	c, _ := newCenter(t)
	for i := 0; i < 60; i++ {
		c.Publish(ctx, Info, "s", "n", "")
	}
	for _, limit := range []int{0, -1, 201, 1 << 30} {
		items, err := c.List(ctx, limit, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 50 {
			t.Errorf("limit %d returned %d items, want the default 50", limit, len(items))
		}
	}
}

func TestPrune(t *testing.T) {
	c, db := newCenter(t)
	c.Publish(ctx, Info, "s", "eski", "")
	c.Publish(ctx, Info, "s", "yeni", "")
	if _, err := db.Exec(`UPDATE notifications SET created_at = ? WHERE title = 'eski'`,
		time.Now().Add(-91*24*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := c.Prune(ctx, 90*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	items, _ := c.List(ctx, 10, false)
	if len(items) != 1 || items[0].Title != "yeni" {
		t.Fatalf("after pruning: %+v", items)
	}
}
