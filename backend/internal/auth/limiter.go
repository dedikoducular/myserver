package auth

import (
	"sync"
	"time"
)

// Limiter is the brute-force guard for login: after maxFailures failed
// attempts within window, the key is locked for lockout.
type Limiter struct {
	mu          sync.Mutex
	entries     map[string]*attempts
	maxFailures int
	window      time.Duration
	lockout     time.Duration
	now         func() time.Time
}

type attempts struct {
	failures    []time.Time
	lockedUntil time.Time
}

func NewLimiter(maxFailures int, window, lockout time.Duration) *Limiter {
	return &Limiter{
		entries:     map[string]*attempts{},
		maxFailures: maxFailures,
		window:      window,
		lockout:     lockout,
		now:         time.Now,
	}
}

// Blocked returns the remaining lock duration for key, or zero.
func (l *Limiter) Blocked(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok {
		return 0
	}
	if d := e.lockedUntil.Sub(l.now()); d > 0 {
		return d
	}
	return 0
}

// Fail records a failed attempt.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e, ok := l.entries[key]
	if !ok {
		e = &attempts{}
		l.entries[key] = e
	}
	kept := e.failures[:0]
	for _, t := range e.failures {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	e.failures = append(kept, now)
	if len(e.failures) >= l.maxFailures {
		e.lockedUntil = now.Add(l.lockout)
		e.failures = nil
	}
}

// Reset clears the record for key after a successful login.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.entries, key)
	l.mu.Unlock()
}

// Sweep drops expired records; call it periodically.
func (l *Limiter) Sweep() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for k, e := range l.entries {
		if now.After(e.lockedUntil) {
			fresh := false
			for _, t := range e.failures {
				if now.Sub(t) < l.window {
					fresh = true
					break
				}
			}
			if !fresh {
				delete(l.entries, k)
			}
		}
	}
}
