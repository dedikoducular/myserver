package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"myserver/internal/httpx"
	"myserver/internal/settings"
)

func (e *env) doWithHeaders(ip string, headers map[string]string, body any) *response {
	e.t.Helper()
	// Reuse do() by routing through a handler wrapper that adds headers.
	saved := e.h
	e.h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		saved.ServeHTTP(w, r)
	})
	defer func() { e.h = saved }()
	return e.do(&client{ip: ip}, "POST", "/api/v1/auth/login", body)
}

func ready(t *testing.T) *env {
	e := newEnv(t)
	e.completeSetup()
	e.addUser("admin", testPassword, RoleAdmin)
	return e
}

func TestSessionCookieAttributes(t *testing.T) {
	e := ready(t)
	res := e.do(nil, "POST", "/api/v1/auth/login", creds("admin", testPassword))
	c := sessionCookie(res)
	if c == nil {
		t.Fatal("no session cookie")
	}
	if !c.HttpOnly {
		t.Error("cookie is not HttpOnly")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", c.SameSite)
	}
	if c.Path != "/" {
		t.Errorf("Path = %q", c.Path)
	}
	if c.Domain != "" {
		t.Errorf("Domain = %q, want host-only cookie", c.Domain)
	}
	if c.Secure {
		t.Error("Secure set on a plain HTTP request without CookieSecure; the panel would be unusable over HTTP")
	}
	if len(c.Value) < 43 {
		t.Errorf("token is only %d characters", len(c.Value))
	}
	raw := res.Header.Get("Set-Cookie")
	if !strings.Contains(raw, "HttpOnly") || !strings.Contains(raw, "SameSite=Strict") {
		t.Errorf("Set-Cookie header: %q", raw)
	}
	// The default session length is 12 hours.
	want := time.Now().Add(12 * time.Hour)
	if d := c.Expires.Sub(want); d < -time.Minute || d > time.Minute {
		t.Errorf("cookie expires %v, want about %v", c.Expires, want)
	}
}

func TestSessionCookieSecureOnTLS(t *testing.T) {
	e := ready(t)
	res := e.do(&client{tls: true}, "POST", "/api/v1/auth/login", creds("admin", testPassword))
	if c := sessionCookie(res); c == nil || !c.Secure {
		t.Fatalf("cookie on a TLS request is not Secure: %+v", c)
	}
}

func TestSessionCookieSecureWhenConfigured(t *testing.T) {
	e := ready(t)
	e.cfg.CookieSecure = true
	res := e.do(nil, "POST", "/api/v1/auth/login", creds("admin", testPassword))
	if c := sessionCookie(res); c == nil || !c.Secure {
		t.Fatalf("cookie with CookieSecure is not Secure: %+v", c)
	}
	// The cookie that clears the session must carry the same attributes.
	c := e.login("admin", testPassword, "")
	out := e.do(c, "POST", "/api/v1/auth/logout", nil)
	cleared := sessionCookie(out)
	if cleared == nil || cleared.Value != "" || cleared.MaxAge >= 0 {
		t.Fatalf("logout did not clear the cookie: %+v", cleared)
	}
	if !cleared.Secure || !cleared.HttpOnly || cleared.SameSite != http.SameSiteStrictMode {
		t.Fatalf("clearing cookie lost its attributes: %+v", cleared)
	}
}

func TestSessionTokensAreUniqueAndUnrelatedToCSRF(t *testing.T) {
	e := ready(t)
	a := e.login("admin", testPassword, "")
	b := e.login("admin", testPassword, "")
	if a.cookie.Value == b.cookie.Value {
		t.Fatal("two logins received the same session token")
	}
	if a.csrf == b.csrf {
		t.Fatal("two sessions share a CSRF token")
	}
	if a.csrf == a.cookie.Value {
		t.Fatal("CSRF token equals the session token")
	}
}

func TestRawSessionTokenIsNotStored(t *testing.T) {
	e := ready(t)
	c := e.login("admin", testPassword, "")
	token := c.cookie.Value

	sum := sha256.Sum256([]byte(token))
	var stored string
	if err := e.db.QueryRow(`SELECT token_hash FROM sessions`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != hex.EncodeToString(sum[:]) {
		t.Fatalf("token_hash is not SHA-256 of the token: %q", stored)
	}
	// The raw token must not appear in any column of any table.
	tables, err := e.db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var n string
		if err := tables.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	tables.Close()
	for _, table := range names {
		rows, err := e.db.Query(`SELECT * FROM "` + table + `"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for i, v := range vals {
				if strings.Contains(fmt.Sprint(v), token) {
					t.Errorf("raw session token stored in %s.%s", table, cols[i])
				}
				if b, ok := v.([]byte); ok && strings.Contains(string(b), token) {
					t.Errorf("raw session token stored in %s.%s", table, cols[i])
				}
			}
		}
		rows.Close()
	}
}

func TestStoredHashIsNotAcceptedAsToken(t *testing.T) {
	e := ready(t)
	e.login("admin", testPassword, "")
	var stored string
	if err := e.db.QueryRow(`SELECT token_hash FROM sessions`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	// Someone who reads the database must not be able to use what it holds.
	thief := &client{cookie: &http.Cookie{Name: cookieName, Value: stored}}
	if e.alive(thief) {
		t.Fatal("the stored token hash works as a session cookie")
	}
}

func TestUnauthenticatedRequests(t *testing.T) {
	e := ready(t)
	const want = `{"success":false,"data":null,"error":{"code":"unauthorized","message":"Oturum açmanız gerekiyor."}}` + "\n"
	cases := map[string]*client{
		"no cookie":     nil,
		"empty cookie":  {cookie: &http.Cookie{Name: cookieName, Value: ""}},
		"random cookie": {cookie: &http.Cookie{Name: cookieName, Value: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}},
		"sql in cookie": {cookie: &http.Cookie{Name: cookieName, Value: "x' OR '1'='1"}},
	}
	for name, c := range cases {
		for _, target := range []string{"GET /api/v1/probe", "POST /api/v1/probe", "GET /api/v1/auth/users", "GET /api/v1/auth/sessions",
			"POST /api/v1/auth/logout", "POST /api/v1/auth/password", "DELETE /api/v1/auth/users/1"} {
			method, path, _ := strings.Cut(target, " ")
			res := e.do(c, method, path, nil)
			if res.Status != http.StatusUnauthorized {
				t.Errorf("%s, %s: status %d, want 401", name, target, res.Status)
				continue
			}
			if res.Raw != want {
				t.Errorf("%s, %s: body %q", name, target, res.Raw)
			}
			if ct := res.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
				t.Errorf("%s, %s: Content-Type %q", name, target, ct)
			}
		}
	}
	if e.probeCount() != 0 {
		t.Fatal("a handler ran for an unauthenticated request")
	}
	if n := e.count(`SELECT COUNT(*) FROM users`); n != 1 {
		t.Fatalf("users changed: %d", n)
	}
}

func TestExpiredSessionRejected(t *testing.T) {
	e := ready(t)
	c := e.login("admin", testPassword, "")
	sess := e.session(c)
	if sess == nil || !e.svc.SessionActive(context.Background(), sess) {
		t.Fatal("fresh session is not active")
	}
	if !e.alive(c) {
		t.Fatal("fresh session rejected")
	}
	if _, err := e.db.Exec(`UPDATE sessions SET expires_at = ?`, time.Now().Unix()-1); err != nil {
		t.Fatal(err)
	}
	if e.alive(c) {
		t.Fatal("expired session accepted")
	}
	if e.svc.SessionActive(context.Background(), sess) {
		t.Fatal("SessionActive is true for an expired session")
	}
	res := e.do(c, "GET", "/api/v1/auth/status", nil)
	if !strings.Contains(res.Raw, `"authenticated":false`) || strings.Contains(res.Raw, c.csrf) {
		t.Fatalf("status for an expired session: %s", res.Raw)
	}
}

func TestSessionLengthFollowsSetting(t *testing.T) {
	e := ready(t)
	if err := e.store.Set(context.Background(), settings.KeySessionHours, "1"); err != nil {
		t.Fatal(err)
	}
	e.login("admin", testPassword, "")
	var created, expires int64
	if err := e.db.QueryRow(`SELECT created_at, expires_at FROM sessions`).Scan(&created, &expires); err != nil {
		t.Fatal(err)
	}
	if expires-created != 3600 {
		t.Fatalf("session lasts %d s, want 3600", expires-created)
	}
	// Out-of-range stored values fall back to the default, never to an
	// unbounded or already-expired session.
	for _, bad := range []string{"0", "-5", "100000", "abc"} {
		if _, err := e.db.Exec(`DELETE FROM sessions`); err != nil {
			t.Fatal(err)
		}
		if err := e.store.Set(context.Background(), settings.KeySessionHours, bad); err != nil {
			t.Fatal(err)
		}
		e.login("admin", testPassword, "")
		if err := e.db.QueryRow(`SELECT created_at, expires_at FROM sessions`).Scan(&created, &expires); err != nil {
			t.Fatal(err)
		}
		if expires-created != 12*3600 {
			t.Errorf("session_hours=%q: session lasts %d s, want the 12 h default", bad, expires-created)
		}
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	e := ready(t)
	c := e.login("admin", testPassword, "")
	other := e.login("admin", testPassword, "")
	sess := e.session(c)

	res := e.do(c, "POST", "/api/v1/auth/logout", nil)
	if res.Status != http.StatusOK {
		t.Fatalf("logout: %d %s", res.Status, res.Raw)
	}
	if e.alive(c) {
		t.Fatal("session usable after logout")
	}
	if e.svc.SessionActive(context.Background(), sess) {
		t.Fatal("SessionActive is true after logout")
	}
	if !e.alive(other) {
		t.Fatal("logout ended another session of the user")
	}
}

func TestLogoutAllKeepsOnlyCurrent(t *testing.T) {
	e := ready(t)
	e.addUser("diger", testPassword, RoleUser)
	cur := e.login("admin", testPassword, "")
	old := e.login("admin", testPassword, "")
	foreign := e.login("diger", testPassword, "")
	if res := e.do(cur, "POST", "/api/v1/auth/logout-all", nil); res.Status != http.StatusOK {
		t.Fatalf("logout-all: %d", res.Status)
	}
	if e.alive(old) {
		t.Fatal("other session survived logout-all")
	}
	if !e.alive(cur) {
		t.Fatal("current session ended by logout-all")
	}
	if !e.alive(foreign) {
		t.Fatal("logout-all ended another user's session")
	}
}

func TestPasswordChange(t *testing.T) {
	e := ready(t)
	e.useClock()
	e.addUser("diger", testPassword, RoleUser)
	cur := e.login("admin", testPassword, "")
	old := e.login("admin", testPassword, "")
	foreign := e.login("diger", testPassword, "")
	oldSess := e.session(old)
	const next = "yepyeni-parola-6789"

	// Wrong current password.
	res := e.do(cur, "POST", "/api/v1/auth/password", map[string]string{"current_password": "yanlis-parola-123", "new_password": next})
	if res.Status != http.StatusBadRequest {
		t.Fatalf("wrong current password: %d %s", res.Status, res.Raw)
	}
	// Weak new password.
	res = e.do(cur, "POST", "/api/v1/auth/password", map[string]string{"current_password": testPassword, "new_password": "kisa"})
	if res.Status != http.StatusBadRequest {
		t.Fatalf("weak new password: %d %s", res.Status, res.Raw)
	}
	if !e.alive(old) {
		t.Fatal("a refused password change ended sessions")
	}

	res = e.do(cur, "POST", "/api/v1/auth/password", map[string]string{"current_password": testPassword, "new_password": next})
	if res.Status != http.StatusOK {
		t.Fatalf("password change: %d %s", res.Status, res.Raw)
	}
	if strings.Contains(res.Raw, next) {
		t.Fatal("response echoes the new password")
	}
	if e.alive(old) {
		t.Fatal("other session survived the password change")
	}
	if e.svc.SessionActive(context.Background(), oldSess) {
		t.Fatal("SessionActive is true for a session ended by the password change")
	}
	if !e.alive(cur) {
		t.Fatal("password change ended the session that made it")
	}
	if !e.alive(foreign) {
		t.Fatal("password change ended another user's session")
	}
	if r := e.do(nil, "POST", "/api/v1/auth/login", creds("admin", testPassword)); r.Status != http.StatusUnauthorized {
		t.Fatalf("old password still works: %d", r.Status)
	}
	if r := e.do(nil, "POST", "/api/v1/auth/login", creds("admin", next)); r.Status != http.StatusOK {
		t.Fatalf("new password refused: %d", r.Status)
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE instr(detail, ?) > 0 OR instr(target, ?) > 0`, next, next); n != 0 {
		t.Fatal("new password written to the audit log")
	}
}

// The current-password check is a password oracle for a stolen session and
// must be rate limited like login.
func TestPasswordChangeIsRateLimited(t *testing.T) {
	e := ready(t)
	clock := e.useClock()
	c := e.login("admin", testPassword, "")
	body := map[string]string{"current_password": "yanlis-parola-123", "new_password": "yepyeni-parola-6789"}
	for i := 1; i <= 5; i++ {
		if r := e.do(c, "POST", "/api/v1/auth/password", body); r.Status != http.StatusBadRequest {
			t.Fatalf("attempt %d: status %d", i, r.Status)
		}
	}
	good := map[string]string{"current_password": testPassword, "new_password": "yepyeni-parola-6789"}
	if r := e.do(c, "POST", "/api/v1/auth/password", good); r.Status != http.StatusTooManyRequests {
		t.Fatalf("not limited after 5 wrong guesses: status %d", r.Status)
	}
	clock.advance(15*time.Minute + time.Second)
	if r := e.do(c, "POST", "/api/v1/auth/password", good); r.Status != http.StatusOK {
		t.Fatalf("still limited after the window: status %d", r.Status)
	}
}

func adminAndUser(t *testing.T) (e *env, admin *client, userID int64) {
	e = ready(t)
	userID = e.addUser("calisan", testPassword, RoleUser)
	admin = e.login("admin", testPassword, "")
	return
}

func TestDisablingUserEndsSessions(t *testing.T) {
	e, admin, id := adminAndUser(t)
	u := e.login("calisan", testPassword, "")
	sess := e.session(u)
	res := e.do(admin, "PUT", fmt.Sprintf("/api/v1/auth/users/%d", id), map[string]any{"disabled": true})
	if res.Status != http.StatusOK {
		t.Fatalf("disable: %d %s", res.Status, res.Raw)
	}
	if e.alive(u) {
		t.Fatal("disabled user's session still accepted")
	}
	if e.svc.SessionActive(context.Background(), sess) {
		t.Fatal("SessionActive is true for a disabled user")
	}
	if r := e.do(nil, "POST", "/api/v1/auth/login", creds("calisan", testPassword)); r.Status != http.StatusUnauthorized {
		t.Fatalf("disabled user logged in: %d", r.Status)
	}
	// Re-enabling does not resurrect the old session.
	res = e.do(admin, "PUT", fmt.Sprintf("/api/v1/auth/users/%d", id), map[string]any{"disabled": false})
	if res.Status != http.StatusOK {
		t.Fatalf("enable: %d", res.Status)
	}
	if e.alive(u) {
		t.Fatal("old session came back after the account was re-enabled")
	}
	if e.svc.SessionActive(context.Background(), sess) {
		t.Fatal("SessionActive is true for a session ended by disabling")
	}
}

// Disabling must take effect even if the session row survives (for example
// when the flag is changed by another path than the users API).
func TestDisabledFlagAloneBlocksSession(t *testing.T) {
	e, _, id := adminAndUser(t)
	u := e.login("calisan", testPassword, "")
	sess := e.session(u)
	if _, err := e.db.Exec(`UPDATE users SET disabled = 1 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if e.alive(u) {
		t.Fatal("session of a disabled user accepted")
	}
	if e.svc.SessionActive(context.Background(), sess) {
		t.Fatal("SessionActive is true for a disabled user")
	}
	if res := e.do(u, "GET", "/api/v1/auth/status", nil); !strings.Contains(res.Raw, `"authenticated":false`) {
		t.Fatalf("status: %s", res.Raw)
	}
}

func TestRoleChangeEndsSessions(t *testing.T) {
	e, admin, id := adminAndUser(t)
	u := e.login("calisan", testPassword, "")
	sess := e.session(u)
	res := e.do(admin, "PUT", fmt.Sprintf("/api/v1/auth/users/%d", id), map[string]any{"role": "admin"})
	if res.Status != http.StatusOK {
		t.Fatalf("promote: %d %s", res.Status, res.Raw)
	}
	if e.alive(u) {
		t.Fatal("session survived a role change")
	}
	if e.svc.SessionActive(context.Background(), sess) {
		t.Fatal("SessionActive is true after a role change")
	}
}

// A long-lived connection authenticated as admin must notice a demotion even
// when the session row itself is still there.
func TestSessionActiveDetectsRoleChange(t *testing.T) {
	e := ready(t)
	id := e.addUser("ikinci", testPassword, RoleAdmin)
	c := e.login("ikinci", testPassword, "")
	sess := e.session(c)
	if _, err := e.db.Exec(`UPDATE users SET role = 'user' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if e.svc.SessionActive(context.Background(), sess) {
		t.Fatal("SessionActive is true although the role changed")
	}
	if e.svc.SessionActive(context.Background(), nil) {
		t.Fatal("SessionActive(nil) is true")
	}
}

func TestUnchangedUpdateKeepsSessions(t *testing.T) {
	e, admin, id := adminAndUser(t)
	u := e.login("calisan", testPassword, "")
	res := e.do(admin, "PUT", fmt.Sprintf("/api/v1/auth/users/%d", id), map[string]any{"role": "user", "disabled": false})
	if res.Status != http.StatusOK {
		t.Fatalf("update: %d", res.Status)
	}
	if !e.alive(u) {
		t.Fatal("an update that changed nothing ended the session")
	}
}

func TestAdminPasswordResetEndsSessions(t *testing.T) {
	e, admin, id := adminAndUser(t)
	u := e.login("calisan", testPassword, "")
	const next = "sifirlanan-parola-1"
	res := e.do(admin, "PUT", fmt.Sprintf("/api/v1/auth/users/%d", id), map[string]any{"password": next})
	if res.Status != http.StatusOK {
		t.Fatalf("reset: %d %s", res.Status, res.Raw)
	}
	if strings.Contains(res.Raw, next) || strings.Contains(res.Raw, "argon2") {
		t.Fatalf("response leaks the password: %s", res.Raw)
	}
	if e.alive(u) {
		t.Fatal("session survived an administrative password reset")
	}
	if r := e.do(nil, "POST", "/api/v1/auth/login", creds("calisan", testPassword)); r.Status != http.StatusUnauthorized {
		t.Fatal("old password works after reset")
	}
	if r := e.do(nil, "POST", "/api/v1/auth/login", creds("calisan", next)); r.Status != http.StatusOK {
		t.Fatal("new password refused after reset")
	}
	if !e.alive(admin) {
		t.Fatal("the administrator's own session ended")
	}
}

func TestDeletingUserEndsSessions(t *testing.T) {
	e, admin, id := adminAndUser(t)
	u := e.login("calisan", testPassword, "")
	sess := e.session(u)
	res := e.do(admin, "DELETE", fmt.Sprintf("/api/v1/auth/users/%d", id), nil)
	if res.Status != http.StatusOK {
		t.Fatalf("delete: %d %s", res.Status, res.Raw)
	}
	if e.alive(u) {
		t.Fatal("deleted user's session still accepted")
	}
	if e.svc.SessionActive(context.Background(), sess) {
		t.Fatal("SessionActive is true for a deleted user")
	}
	if n := e.count(`SELECT COUNT(*) FROM sessions WHERE user_id = ?`, id); n != 0 {
		t.Fatalf("%d session rows left for the deleted user", n)
	}
}

func TestSessionsListHidesTokens(t *testing.T) {
	e := ready(t)
	a := e.login("admin", testPassword, "")
	b := e.login("admin", testPassword, "")
	res := e.do(a, "GET", "/api/v1/auth/sessions", nil)
	if res.Status != http.StatusOK {
		t.Fatalf("status %d", res.Status)
	}
	if strings.Count(res.Raw, `"current":true`) != 1 || strings.Count(res.Raw, `"current":false`) != 1 {
		t.Fatalf("sessions: %s", res.Raw)
	}
	var hashes []string
	rows, err := e.db.Query(`SELECT token_hash, csrf_token FROM sessions`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var h, c string
		if err := rows.Scan(&h, &c); err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, h, c)
	}
	for _, secret := range append(hashes, a.cookie.Value, b.cookie.Value) {
		if strings.Contains(res.Raw, secret) {
			t.Fatalf("session list exposes a token or hash: %s", res.Raw)
		}
	}
}

func TestClientIPIgnoresProxyHeaders(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.5:4711"
	r.Header.Set("X-Forwarded-For", "10.0.0.1")
	r.Header.Set("X-Real-IP", "10.0.0.2")
	if ip := httpx.ClientIP(r); ip != "203.0.113.5" {
		t.Fatalf("ClientIP = %q", ip)
	}
}
