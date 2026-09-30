package storage

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"sync"
	"time"
)

const tokenTTL = 3 * time.Minute

// formatTicket binds a confirmation to one exact device as it was shown
// to the user in step 1.
type formatTicket struct {
	device   string
	serial   string
	size     int64
	username string
	expires  time.Time
}

// boundTo reports whether the ticket was issued to this user for this
// device.
func (t formatTicket) boundTo(device, username string) bool {
	return t.device == device && t.username == username
}

// sameDisk reports whether the disk still has the serial number and size
// that were confirmed.
func (t formatTicket) sameDisk(serial string, size int64) bool {
	return t.serial == serial && t.size == size
}

type tokenStore struct {
	mu      sync.Mutex
	tickets map[string]formatTicket
}

func newTokenStore() *tokenStore { return &tokenStore{tickets: map[string]formatTicket{}} }

func (s *tokenStore) sweep(now time.Time) {
	for k, t := range s.tickets {
		if now.After(t.expires) {
			delete(s.tickets, k)
		}
	}
}

// issue creates a single-use token. Earlier tokens of the same user for
// the same device are revoked.
func (s *tokenStore) issue(t formatTicket) (string, time.Time, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	now := time.Now()
	t.expires = now.Add(tokenTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep(now)
	for k, old := range s.tickets {
		if old.username == t.username && old.device == t.device {
			delete(s.tickets, k)
		}
	}
	if len(s.tickets) >= 64 {
		// Far more than any real use; start over rather than grow.
		s.tickets = map[string]formatTicket{}
	}
	s.tickets[token] = t
	return token, t.expires, nil
}

// redeem returns the ticket of a token and destroys it, whether or not
// the caller goes on to succeed.
func (s *tokenStore) redeem(token string) (formatTicket, bool) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep(now)
	for k, t := range s.tickets {
		if len(k) == len(token) && subtle.ConstantTimeCompare([]byte(k), []byte(token)) == 1 {
			delete(s.tickets, k)
			return t, true
		}
	}
	return formatTicket{}, false
}
