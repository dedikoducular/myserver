package auth

import (
	"sync"
	"testing"
	"time"
)

func newTestLimiter(max int, window, lockout time.Duration) (*Limiter, *fakeClock) {
	c := newFakeClock()
	l := NewLimiter(max, window, lockout)
	l.now = c.now
	return l, c
}

func TestLimiterLocksAfterMaxFailures(t *testing.T) {
	l, _ := newTestLimiter(3, 10*time.Minute, 20*time.Minute)
	for i := 1; i <= 2; i++ {
		l.Fail("k")
		if d := l.Blocked("k"); d != 0 {
			t.Fatalf("blocked after %d failures (limit 3): %v", i, d)
		}
	}
	l.Fail("k")
	if d := l.Blocked("k"); d != 20*time.Minute {
		t.Fatalf("after 3 failures Blocked = %v, want 20m", d)
	}
	if d := l.Blocked("other"); d != 0 {
		t.Fatalf("unrelated key blocked: %v", d)
	}
}

func TestLimiterUnlocksAfterLockout(t *testing.T) {
	l, c := newTestLimiter(3, 10*time.Minute, 20*time.Minute)
	for i := 0; i < 3; i++ {
		l.Fail("k")
	}
	c.advance(20*time.Minute - time.Second)
	if d := l.Blocked("k"); d != time.Second {
		t.Fatalf("one second before the end Blocked = %v, want 1s", d)
	}
	c.advance(time.Second)
	if d := l.Blocked("k"); d != 0 {
		t.Fatalf("still blocked at the end of the lockout: %v", d)
	}
	// The count starts again from zero after a lock.
	l.Fail("k")
	l.Fail("k")
	if d := l.Blocked("k"); d != 0 {
		t.Fatalf("blocked after 2 new failures: %v", d)
	}
	l.Fail("k")
	if l.Blocked("k") == 0 {
		t.Fatal("not blocked after 3 new failures")
	}
}

func TestLimiterWindowForgetsOldFailures(t *testing.T) {
	l, c := newTestLimiter(3, 10*time.Minute, 20*time.Minute)
	l.Fail("k")
	l.Fail("k")
	c.advance(10 * time.Minute)
	l.Fail("k") // the first two are outside the window now
	if d := l.Blocked("k"); d != 0 {
		t.Fatalf("failures outside the window were counted: %v", d)
	}
	l.Fail("k")
	if d := l.Blocked("k"); d != 0 {
		t.Fatalf("blocked after 2 failures inside the window: %v", d)
	}
	l.Fail("k")
	if l.Blocked("k") == 0 {
		t.Fatal("not blocked after 3 failures inside the window")
	}
}

func TestLimiterReset(t *testing.T) {
	l, _ := newTestLimiter(3, 10*time.Minute, 20*time.Minute)
	l.Fail("k")
	l.Fail("k")
	l.Reset("k")
	l.Fail("k")
	l.Fail("k")
	if d := l.Blocked("k"); d != 0 {
		t.Fatalf("Reset did not clear the failure count: %v", d)
	}
}

func TestLimiterSweep(t *testing.T) {
	l, c := newTestLimiter(3, 10*time.Minute, 20*time.Minute)
	l.Fail("old")
	for i := 0; i < 3; i++ {
		l.Fail("locked")
	}
	c.advance(11 * time.Minute)
	l.Fail("fresh")
	l.Sweep()
	if _, ok := l.entries["old"]; ok {
		t.Error("expired record was not swept")
	}
	if _, ok := l.entries["fresh"]; !ok {
		t.Error("fresh record was swept")
	}
	if l.Blocked("locked") == 0 {
		t.Error("Sweep removed an active lock")
	}
	c.advance(10 * time.Minute)
	l.Sweep()
	if _, ok := l.entries["locked"]; ok {
		t.Error("expired lock was not swept")
	}
	if len(l.entries) != 0 {
		t.Errorf("%d records left after everything expired", len(l.entries))
	}
}

func TestLimiterConcurrentUse(t *testing.T) {
	l, _ := newTestLimiter(50, time.Minute, time.Minute)
	var wg sync.WaitGroup
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				l.Fail("k")
				l.Blocked("k")
				l.Sweep()
			}
		}()
	}
	wg.Wait()
	if l.Blocked("k") == 0 {
		t.Fatal("50 concurrent failures did not lock the key")
	}
}
