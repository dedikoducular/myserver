package storage

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"myserver/internal/httpx"
)

func sampleTicket() formatTicket {
	return formatTicket{device: "sdb", serial: "WD-WCC7K1234567", size: 4000787030016, username: "admin"}
}

func TestTokenIsSingleUse(t *testing.T) {
	s := newTokenStore()
	tok, expires, err := s.issue(sampleTicket())
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) < 32 {
		t.Errorf("token %q is too short to be unguessable", tok)
	}
	if d := time.Until(expires); d <= 0 || d > tokenTTL {
		t.Errorf("expires in %v, want within %v", d, tokenTTL)
	}
	got, ok := s.redeem(tok)
	if !ok {
		t.Fatal("fresh token refused")
	}
	if got.device != "sdb" || got.serial != "WD-WCC7K1234567" || got.size != 4000787030016 || got.username != "admin" {
		t.Errorf("ticket changed: %+v", got)
	}
	if _, ok := s.redeem(tok); ok {
		t.Error("token accepted a second time")
	}
	if len(s.tickets) != 0 {
		t.Errorf("%d tickets left in the store", len(s.tickets))
	}
}

func TestTokensAreUnique(t *testing.T) {
	s := newTokenStore()
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		tk := sampleTicket()
		tk.username = "user" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		tok, _, err := s.issue(tk)
		if err != nil {
			t.Fatal(err)
		}
		if seen[tok] {
			t.Fatal("token repeated")
		}
		seen[tok] = true
	}
}

func TestTokenRejectsUnknownValues(t *testing.T) {
	s := newTokenStore()
	tok, _, _ := s.issue(sampleTicket())
	for _, bad := range []string{"", " ", tok[:len(tok)-1], tok + "x", strings.ToUpper(tok), strings.Repeat("A", len(tok)), tok + "\n", "\x00"} {
		if bad == tok {
			continue
		}
		if _, ok := s.redeem(bad); ok {
			t.Errorf("redeem(%q) succeeded", bad)
		}
	}
	// Guessing must not consume somebody else's valid token.
	if _, ok := s.redeem(tok); !ok {
		t.Error("valid token lost after wrong guesses")
	}
	// An empty store never matches the empty token.
	if _, ok := newTokenStore().redeem(""); ok {
		t.Error("empty token accepted")
	}
}

func TestTokenExpires(t *testing.T) {
	s := newTokenStore()
	tok, _, _ := s.issue(sampleTicket())
	s.mu.Lock()
	tk := s.tickets[tok]
	tk.expires = time.Now().Add(-time.Second)
	s.tickets[tok] = tk
	s.mu.Unlock()
	if _, ok := s.redeem(tok); ok {
		t.Fatal("expired token accepted")
	}
	if len(s.tickets) != 0 {
		t.Error("expired ticket kept in the store")
	}

	// Still valid just before the deadline.
	tok, _, _ = s.issue(sampleTicket())
	s.mu.Lock()
	tk = s.tickets[tok]
	tk.expires = time.Now().Add(5 * time.Second)
	s.tickets[tok] = tk
	s.mu.Unlock()
	if _, ok := s.redeem(tok); !ok {
		t.Error("token refused before its deadline")
	}
	if tokenTTL > 10*time.Minute || tokenTTL < 30*time.Second {
		t.Errorf("token lifetime %v is not short-lived", tokenTTL)
	}
}

func TestTokenIssueSweepsExpired(t *testing.T) {
	s := newTokenStore()
	old, _, _ := s.issue(sampleTicket())
	s.mu.Lock()
	tk := s.tickets[old]
	tk.expires = time.Now().Add(-time.Minute)
	s.tickets[old] = tk
	s.mu.Unlock()
	other := sampleTicket()
	other.device = "sdc"
	if _, _, err := s.issue(other); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.tickets[old]; ok {
		t.Error("expired ticket survived")
	}
}

func TestNewTokenRevokesEarlierOneForSameUserAndDevice(t *testing.T) {
	s := newTokenStore()
	first, _, _ := s.issue(sampleTicket())
	otherDev := sampleTicket()
	otherDev.device = "sdc"
	tokDev, _, _ := s.issue(otherDev)
	otherUser := sampleTicket()
	otherUser.username = "ayse"
	tokUser, _, _ := s.issue(otherUser)
	second, _, _ := s.issue(sampleTicket())

	if _, ok := s.redeem(first); ok {
		t.Error("the earlier token is still valid")
	}
	for name, tok := range map[string]string{"other device": tokDev, "other user": tokUser, "new": second} {
		if _, ok := s.redeem(tok); !ok {
			t.Errorf("token for %s was revoked", name)
		}
	}
}

func TestTokenStoreIsBounded(t *testing.T) {
	s := newTokenStore()
	for i := 0; i < 1000; i++ {
		tk := sampleTicket()
		tk.username = "u" + strings.Repeat("x", i%7) + string(rune('0'+i%10)) + string(rune('a'+(i/10)%26)) + string(rune('a'+(i/260)%26))
		if _, _, err := s.issue(tk); err != nil {
			t.Fatal(err)
		}
		if len(s.tickets) > 64 {
			t.Fatalf("store grew to %d tickets", len(s.tickets))
		}
	}
}

func TestTokenConcurrentRedeemSucceedsOnce(t *testing.T) {
	s := newTokenStore()
	tok, _, _ := s.issue(sampleTicket())
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := s.redeem(tok); ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Errorf("token redeemed %d times", wins)
	}
}

func TestTicketBinding(t *testing.T) {
	tk := sampleTicket()
	if !tk.boundTo("sdb", "admin") {
		t.Error("ticket does not match its own device and user")
	}
	for _, c := range [][2]string{{"sdc", "admin"}, {"sdb1", "admin"}, {"sdb", "ayse"}, {"sdb", ""}, {"", "admin"}, {"sdb", "Admin"}, {"SDB", "admin"}, {"sdb", "-"}} {
		if tk.boundTo(c[0], c[1]) {
			t.Errorf("ticket for sdb/admin accepted for %q/%q", c[0], c[1])
		}
	}
	if !tk.sameDisk("WD-WCC7K1234567", 4000787030016) {
		t.Error("ticket does not match its own disk")
	}
	cases := []struct {
		serial string
		size   int64
	}{
		{"WD-WCC7K7654321", 4000787030016}, {"", 4000787030016}, {"wd-wcc7k1234567", 4000787030016},
		{"WD-WCC7K1234567", 4000787030015}, {"WD-WCC7K1234567", 0}, {"WD-WCC7K1234567", -4000787030016},
		{"WD-WCC7K1234567 ", 4000787030016},
	}
	for _, c := range cases {
		if tk.sameDisk(c.serial, c.size) {
			t.Errorf("ticket accepted for serial %q size %d", c.serial, c.size)
		}
	}
}

/* ---------- through the HTTP handler ---------- */

// formatRequest calls handleFormat the way the router does. There is no
// session in the context, so the actor's user name is "-".
func formatRequest(t *testing.T, m *Module, body map[string]any) (int, string) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/storage/format", strings.NewReader(string(b)))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	if err := m.handleFormat(w, r); err != nil {
		httpx.Fail(w, r, err)
	}
	var env struct {
		Success bool `json:"success"`
		Error   *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not an envelope: %q", w.Body.String())
	}
	if env.Success || env.Error == nil {
		t.Fatalf("the request succeeded: %q", w.Body.String())
	}
	return w.Code, env.Error.Code
}

func handlerModule() *Module {
	return &Module{smart: newSmartCache(), tokens: newTokenStore(), events: newBroker()}
}

func TestHandleFormatRejectsMissingOrUnknownToken(t *testing.T) {
	m := handlerModule()
	valid, _, _ := m.tokens.issue(formatTicket{device: "sdb", serial: "S1", size: 1024, username: "-"})
	for _, tok := range []string{"", "x", strings.Repeat("A", 32)} {
		code, ec := formatRequest(t, m, map[string]any{
			"device": "sdb", "token": tok, "confirm_name": "sdb", "fstype": "ext4", "label": "",
		})
		if code != http.StatusForbidden || ec != "confirmation_invalid" {
			t.Errorf("token %q: %d %s, want 403 confirmation_invalid", tok, code, ec)
		}
	}
	if _, ok := m.tokens.tickets[valid]; !ok {
		t.Error("an unrelated valid token was destroyed by wrong guesses")
	}
}

// Every failed attempt destroys the token, whatever the reason: the
// second request in each case presents the same token with a fully
// correct request and must be refused because the token is gone.
func TestHandleFormatDestroysTokenOnFailedAttempt(t *testing.T) {
	cases := []struct {
		name     string
		username string
		body     map[string]any
		status   int
		code     string
	}{
		{"token issued for another device", "-",
			map[string]any{"device": "sdc", "confirm_name": "sdc", "fstype": "ext4", "label": ""},
			http.StatusForbidden, "confirmation_invalid"},
		{"token issued for the disk, used for its partition", "-",
			map[string]any{"device": "sdb1", "confirm_name": "sdb1", "fstype": "ext4", "label": ""},
			http.StatusForbidden, "confirmation_invalid"},
		{"token issued to another user", "admin",
			map[string]any{"device": "sdb", "confirm_name": "sdb", "fstype": "ext4", "label": ""},
			http.StatusForbidden, "confirmation_invalid"},
		{"invalid device name", "-",
			map[string]any{"device": "/dev/sdb", "confirm_name": "/dev/sdb", "fstype": "ext4", "label": ""},
			http.StatusBadRequest, "bad_request"},
		{"confirmation text does not match", "-",
			map[string]any{"device": "sdb", "confirm_name": "sdc", "fstype": "ext4", "label": ""},
			http.StatusBadRequest, "bad_request"},
		{"confirmation text empty", "-",
			map[string]any{"device": "sdb", "confirm_name": "", "fstype": "ext4", "label": ""},
			http.StatusBadRequest, "bad_request"},
		{"unknown filesystem", "-",
			map[string]any{"device": "sdb", "confirm_name": "sdb", "fstype": "ntfs", "label": ""},
			http.StatusBadRequest, "bad_request"},
		{"option-like label", "-",
			map[string]any{"device": "sdb", "confirm_name": "sdb", "fstype": "ext4", "label": "--force"},
			http.StatusBadRequest, "bad_request"},
		{"label too long", "-",
			map[string]any{"device": "sdb", "confirm_name": "sdb", "fstype": "ext4", "label": strings.Repeat("a", 17)},
			http.StatusBadRequest, "bad_request"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := handlerModule()
			tok, _, err := m.tokens.issue(formatTicket{device: "sdb", serial: "S1", size: 1024, username: c.username})
			if err != nil {
				t.Fatal(err)
			}
			c.body["token"] = tok
			status, code := formatRequest(t, m, c.body)
			if status != c.status {
				t.Errorf("status = %d (%s), want %d", status, code, c.status)
			}
			if c.code != "bad_request" && code != c.code {
				t.Errorf("error code = %s, want %s", code, c.code)
			}
			if len(m.tokens.tickets) != 0 {
				t.Fatal("the token survived a failed attempt")
			}
			// The retry stops at the token check, before any device lookup.
			status, code = formatRequest(t, m, map[string]any{
				"device": "sdb", "token": tok, "confirm_name": "sdb", "fstype": "ext4", "label": "",
			})
			if status != http.StatusForbidden || code != "confirmation_invalid" {
				t.Errorf("retry with the same token: %d %s, want 403 confirmation_invalid", status, code)
			}
		})
	}
}

func TestHandleFormatRejectsUnknownFields(t *testing.T) {
	m := handlerModule()
	tok, _, _ := m.tokens.issue(formatTicket{device: "sdb", serial: "S1", size: 1024, username: "-"})
	status, _ := formatRequest(t, m, map[string]any{
		"device": "sdb", "token": tok, "confirm_name": "sdb", "fstype": "ntfs", "label": "", "force": true,
	})
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", status)
	}
}

func TestInsideRoots(t *testing.T) {
	roots := []string{"/home", "/mnt/", ""}
	if !insideRoots("/home", roots) || !insideRoots("/home/ali", roots) || !insideRoots("/mnt/data", roots) {
		t.Error("path inside a root not recognised")
	}
	for _, p := range []string{"/homes", "/homes/x", "/mntx", "/media/usb", "/"} {
		if insideRoots(p, roots) {
			t.Errorf("%s reported inside %v", p, roots)
		}
	}
}
