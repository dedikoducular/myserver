package terminal

import (
	"sync"
	"testing"
)

func newBareModule() *Module {
	return &Module{sessions: map[int64]int{}}
}

func TestAcquirePerUserLimit(t *testing.T) {
	m := newBareModule()
	for i := 0; i < maxSessionsPerUser; i++ {
		if ok, msg := m.acquire(1); !ok {
			t.Fatalf("session %d refused: %s", i+1, msg)
		}
	}
	ok, msg := m.acquire(1)
	if ok {
		t.Fatalf("session %d of one user was accepted", maxSessionsPerUser+1)
	}
	if msg == "" {
		t.Error("refusal has no message")
	}
	if m.total != maxSessionsPerUser || m.sessions[1] != maxSessionsPerUser {
		t.Errorf("a refused session changed the counters: total=%d own=%d", m.total, m.sessions[1])
	}
	// Another user is not affected by the first user's limit.
	if ok, _ := m.acquire(2); !ok {
		t.Error("second user refused although only the first reached the limit")
	}
	// Closing one makes room again.
	m.release(1)
	if ok, _ := m.acquire(1); !ok {
		t.Error("session refused after one was released")
	}
}

func TestAcquireTotalLimit(t *testing.T) {
	m := newBareModule()
	user := int64(1)
	for i := 0; i < maxSessionsTotal; i++ {
		if i > 0 && i%maxSessionsPerUser == 0 {
			user++
		}
		if ok, msg := m.acquire(user); !ok {
			t.Fatalf("session %d refused: %s", i+1, msg)
		}
	}
	ok, totalMsg := m.acquire(99)
	if ok {
		t.Fatalf("session %d was accepted", maxSessionsTotal+1)
	}
	if _, present := m.sessions[99]; present {
		t.Error("a refused user was recorded in the session table")
	}
	if m.total != maxSessionsTotal {
		t.Errorf("total = %d, want %d", m.total, maxSessionsTotal)
	}
	// The two refusals are told apart by the user.
	m2 := newBareModule()
	for i := 0; i < maxSessionsPerUser; i++ {
		m2.acquire(1)
	}
	_, userMsg := m2.acquire(1)
	if userMsg == totalMsg {
		t.Error("per-user and total refusals carry the same message")
	}
	m.release(1)
	if ok, _ := m.acquire(99); !ok {
		t.Error("session refused after one was released")
	}
}

func TestReleaseNeverUnderflows(t *testing.T) {
	m := newBareModule()
	m.release(5)
	m.release(5)
	if m.total != 0 || len(m.sessions) != 0 {
		t.Fatalf("release without acquire changed the counters: total=%d sessions=%v", m.total, m.sessions)
	}
	m.acquire(1)
	m.release(2) // a user without sessions must not take another user's slot
	if m.total != 1 || m.sessions[1] != 1 {
		t.Errorf("total=%d own=%d, want 1 and 1", m.total, m.sessions[1])
	}
	m.release(1)
	m.release(1)
	if m.total != 0 || len(m.sessions) != 0 {
		t.Errorf("total=%d sessions=%v, want empty", m.total, m.sessions)
	}
}

func TestAcquireReleaseConcurrent(t *testing.T) {
	m := newBareModule()
	var wg sync.WaitGroup
	var mu sync.Mutex
	granted := 0
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(user int64) {
			defer wg.Done()
			if ok, _ := m.acquire(user); ok {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}(int64(i % 16))
	}
	wg.Wait()
	if granted != maxSessionsTotal || m.total != maxSessionsTotal {
		t.Errorf("granted=%d total=%d, want %d", granted, m.total, maxSessionsTotal)
	}
	sum := 0
	for id, n := range m.sessions {
		if n < 1 || n > maxSessionsPerUser {
			t.Errorf("user %d holds %d sessions", id, n)
		}
		sum += n
	}
	if sum != m.total {
		t.Errorf("per-user counts add up to %d, total is %d", sum, m.total)
	}
}
