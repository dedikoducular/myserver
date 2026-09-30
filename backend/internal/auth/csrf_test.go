package auth

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

var stateChanging = []string{"POST", "PUT", "PATCH", "DELETE"}

func TestCSRFRequiredOnStateChangingMethods(t *testing.T) {
	e := ready(t)
	c := e.login("admin", testPassword, "")
	other := e.login("admin", testPassword, "") // same user, different session
	e.addUser("diger", testPassword, RoleUser)
	foreign := e.login("diger", testPassword, "")

	tokens := map[string]string{
		"missing":                 "",
		"garbage":                 "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"other session same user": other.csrf,
		"other user":              foreign.csrf,
		"session token":           c.cookie.Value,
		"prefix":                  c.csrf[:len(c.csrf)-1],
		"with suffix":             c.csrf + "A",
		"upper case":              strings.ToUpper(c.csrf),
	}
	for name, tok := range tokens {
		if tok == c.csrf {
			continue // upper-casing may be a no-op for some tokens
		}
		for _, m := range stateChanging {
			attacker := &client{cookie: c.cookie, csrf: tok}
			res := e.do(attacker, m, "/api/v1/probe", nil)
			if res.Status != http.StatusForbidden || res.code() != "csrf_invalid" {
				t.Errorf("%s token, %s: status %d code %q, want 403 csrf_invalid", name, m, res.Status, res.code())
			}
		}
	}
	if n := e.probeCount(); n != 0 {
		t.Fatalf("%d state-changing handlers ran without a valid CSRF token", n)
	}
	for _, m := range stateChanging {
		if res := e.do(c, m, "/api/v1/probe", nil); res.Status != http.StatusOK {
			t.Errorf("%s with the valid token: status %d", m, res.Status)
		}
	}
	if n := e.probeCount(); n != len(stateChanging) {
		t.Fatalf("handlers ran %d times with a valid token, want %d", n, len(stateChanging))
	}
}

func TestCSRFNotRequiredForSafeMethods(t *testing.T) {
	e := ready(t)
	c := e.login("admin", testPassword, "")
	c.noCSRF = true
	for _, m := range []string{"GET", "HEAD"} {
		if res := e.do(c, m, "/api/v1/probe", nil); res.Status != http.StatusOK {
			t.Errorf("%s without a CSRF token: status %d", m, res.Status)
		}
	}
	for _, p := range []string{"/api/v1/auth/sessions", "/api/v1/auth/users", "/api/v1/auth/status"} {
		if res := e.do(c, "GET", p, nil); res.Status != http.StatusOK {
			t.Errorf("GET %s without a CSRF token: status %d", p, res.Status)
		}
	}
}

// The real endpoints, not only the probe: a request without the token must
// leave no trace.
func TestCSRFProtectsRealEndpoints(t *testing.T) {
	e, admin, id := adminAndUser(t)
	victim := &client{cookie: admin.cookie} // a forged request carries the cookie only
	cases := []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/v1/auth/users", map[string]string{"username": "sizan", "password": testPassword, "role": "admin"}},
		{"PUT", fmt.Sprintf("/api/v1/auth/users/%d", id), map[string]any{"role": "admin"}},
		{"DELETE", fmt.Sprintf("/api/v1/auth/users/%d", id), nil},
		{"POST", "/api/v1/auth/password", map[string]string{"current_password": testPassword, "new_password": "yepyeni-parola-6789"}},
		{"POST", "/api/v1/auth/logout-all", nil},
		{"POST", "/api/v1/auth/logout", nil},
	}
	for _, c := range cases {
		res := e.do(victim, c.method, c.path, c.body)
		if res.Status != http.StatusForbidden || res.code() != "csrf_invalid" {
			t.Errorf("%s %s: status %d code %q", c.method, c.path, res.Status, res.code())
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM users`); n != 2 {
		t.Errorf("user count changed to %d", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM users WHERE role = 'admin'`); n != 1 {
		t.Errorf("admin count changed to %d", n)
	}
	if !e.alive(admin) {
		t.Error("session ended by a forged logout")
	}
	if r := e.do(nil, "POST", "/api/v1/auth/login", creds("admin", testPassword)); r.Status != http.StatusOK {
		t.Error("password changed by a forged request")
	}
}

func TestOriginGuardOnPublicEndpoints(t *testing.T) {
	e := ready(t)
	foreign := []string{
		"http://evil.example",
		"https://evil.example",
		"http://panel.test",      // same name, other port
		"http://panel.test:8081", // other port
		"http://panel.test:8080.evil.example",
		"http://evil.example/panel.test:8080",
		"http://sub.panel.test:8080",
		"null",
		"panel.test:8080", // no scheme
	}
	for _, o := range foreign {
		for _, p := range []string{"/api/v1/auth/login", "/api/v1/auth/setup"} {
			c := &client{origin: o}
			res := e.do(c, "POST", p, creds("admin", testPassword))
			if res.Status != http.StatusForbidden || res.code() != "forbidden" {
				t.Errorf("Origin %q POST %s: status %d code %q, want 403", o, p, res.Status, res.code())
			}
			if sessionCookie(res) != nil {
				t.Errorf("Origin %q: session cookie issued to a cross-origin login", o)
			}
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM sessions`); n != 0 {
		t.Fatalf("%d sessions created by cross-origin requests", n)
	}
	// Same origin and non-browser clients (no Origin header) pass.
	for _, o := range []string{testOrigin, "https://panel.test:8080", "HTTP://PANEL.TEST:8080", ""} {
		res := e.do(&client{origin: o}, "POST", "/api/v1/auth/login", creds("admin", testPassword))
		if res.Status != http.StatusOK {
			t.Errorf("Origin %q: status %d, want 200", o, res.Status)
		}
	}
	// Reading is not blocked.
	if res := e.do(&client{origin: "http://evil.example"}, "GET", "/api/v1/auth/status", nil); res.Status != http.StatusOK {
		t.Errorf("cross-origin GET blocked: %d", res.Status)
	}
}

func TestWebSocketGuard(t *testing.T) {
	e := ready(t)
	c := e.login("admin", testPassword, "")
	for _, o := range []string{"", "http://evil.example", "https://evil.example", "http://panel.test", "http://panel.test:8081",
		"http://panel.test:8080.evil.example", "null", "panel.test:8080"} {
		ws := &client{cookie: c.cookie, origin: o}
		res := e.do(ws, "GET", "/api/v1/probe-ws", nil)
		if res.Status != http.StatusForbidden {
			t.Errorf("Origin %q: status %d, want 403", o, res.Status)
		}
	}
	if res := e.do(&client{origin: testOrigin}, "GET", "/api/v1/probe-ws", nil); res.Status != http.StatusUnauthorized {
		t.Errorf("same origin without a session: status %d, want 401", res.Status)
	}
	if res := e.do(&client{cookie: c.cookie, origin: testOrigin}, "GET", "/api/v1/probe-ws", nil); res.Status != http.StatusOK {
		t.Errorf("same origin with a session: status %d, want 200", res.Status)
	}
	// Ended sessions cannot open sockets.
	e.do(c, "POST", "/api/v1/auth/logout", nil)
	if res := e.do(&client{cookie: c.cookie, origin: testOrigin}, "GET", "/api/v1/probe-ws", nil); res.Status != http.StatusUnauthorized {
		t.Errorf("logged-out session on a socket: status %d, want 401", res.Status)
	}
}
