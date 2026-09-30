package auth

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func creds(u, p string) map[string]string {
	return map[string]string{"username": u, "password": p}
}

func TestLoginRefusedBeforeSetup(t *testing.T) {
	e := newEnv(t)
	e.addUser("admin", testPassword, RoleAdmin) // even with a valid account
	res := e.do(nil, "POST", "/api/v1/auth/login", creds("admin", testPassword))
	if res.Status != http.StatusConflict || res.Success {
		t.Fatalf("login before setup: status %d body %s", res.Status, res.Raw)
	}
	if sessionCookie(res) != nil {
		t.Fatal("a session cookie was issued before setup")
	}
	if n := e.count(`SELECT COUNT(*) FROM sessions`); n != 0 {
		t.Fatalf("%d sessions created before setup", n)
	}
}

func TestLoginSuccess(t *testing.T) {
	e := newEnv(t)
	e.completeSetup()
	e.addUser("admin", testPassword, RoleAdmin)
	res := e.do(nil, "POST", "/api/v1/auth/login", creds("admin", testPassword))
	if res.Status != http.StatusOK || !res.Success || res.Error != nil {
		t.Fatalf("status %d body %s", res.Status, res.Raw)
	}
	if strings.Contains(res.Raw, testPassword) || strings.Contains(res.Raw, "argon2") {
		t.Fatalf("login response leaks credentials: %s", res.Raw)
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'auth.login' AND success = 1`); n != 1 {
		t.Fatalf("successful login audited %d times", n)
	}
}

func TestLoginUsernameIsCaseInsensitive(t *testing.T) {
	e := newEnv(t)
	e.completeSetup()
	e.addUser("admin", testPassword, RoleAdmin)
	res := e.do(nil, "POST", "/api/v1/auth/login", creds("  ADMIN ", testPassword))
	if res.Status != http.StatusOK {
		t.Fatalf("status %d body %s", res.Status, res.Raw)
	}
}

// Unknown user, wrong password and disabled account must be
// indistinguishable, otherwise the endpoint enumerates accounts.
func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	e := newEnv(t)
	e.completeSetup()
	e.addUser("admin", testPassword, RoleAdmin)
	id := e.addUser("kapali", testPassword, RoleUser)
	if _, err := e.db.Exec(`UPDATE users SET disabled = 1 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	unknown := e.do(&client{ip: "192.0.2.1"}, "POST", "/api/v1/auth/login", creds("yokboyle", testPassword))
	wrong := e.do(&client{ip: "192.0.2.2"}, "POST", "/api/v1/auth/login", creds("admin", "yanlis-parola-123"))
	disabled := e.do(&client{ip: "192.0.2.3"}, "POST", "/api/v1/auth/login", creds("kapali", testPassword))

	for name, r := range map[string]*response{"unknown": unknown, "wrong": wrong, "disabled": disabled} {
		if r.Status != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, r.Status)
		}
		if r.code() != "invalid_credentials" {
			t.Errorf("%s: code %q", name, r.code())
		}
		if sessionCookie(r) != nil {
			t.Errorf("%s: session cookie issued", name)
		}
		if r.Raw != unknown.Raw {
			t.Errorf("%s: body differs from the unknown-user body:\n%s\n%s", name, r.Raw, unknown.Raw)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM sessions`); n != 0 {
		t.Fatalf("%d sessions exist after failed logins", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'auth.login' AND success = 0`); n != 3 {
		t.Fatalf("%d failed logins audited, want 3", n)
	}
}

func TestLoginDoesNotStoreOrAuditPassword(t *testing.T) {
	e := newEnv(t)
	e.completeSetup()
	e.addUser("admin", testPassword, RoleAdmin)
	const attempt = "denenen-gizli-parola-987"
	e.do(nil, "POST", "/api/v1/auth/login", creds("admin", attempt))
	e.do(nil, "POST", "/api/v1/auth/login", creds("admin", testPassword))
	for _, secret := range []string{attempt, testPassword} {
		n := e.count(`SELECT COUNT(*) FROM audit_log WHERE instr(detail, ?) > 0 OR instr(target, ?) > 0 OR instr(username, ?) > 0`,
			secret, secret, secret)
		if n != 0 {
			t.Fatalf("password %q found in the audit log", secret)
		}
		if n := e.count(`SELECT COUNT(*) FROM users WHERE instr(password_hash, ?) > 0`, secret); n != 0 {
			t.Fatal("password stored in clear text")
		}
	}
}

func TestLoginMissingFields(t *testing.T) {
	e := newEnv(t)
	e.completeSetup()
	e.addUser("admin", testPassword, RoleAdmin)
	for _, body := range []any{creds("", testPassword), creds("admin", ""), "{}", "", "not json", `{"username":"admin","password":"` + testPassword + `","extra":1}`,
		`{"username":["admin"],"password":"x"}`} {
		res := e.do(nil, "POST", "/api/v1/auth/login", body)
		if res.Status != http.StatusBadRequest || res.Success {
			t.Errorf("body %v: status %d body %s", body, res.Status, res.Raw)
		}
		if sessionCookie(res) != nil {
			t.Errorf("body %v: cookie issued", body)
		}
	}
}

func TestLoginLocksUserAfterFiveFailures(t *testing.T) {
	e := newEnv(t)
	clock := e.useClock()
	e.completeSetup()
	e.addUser("admin", testPassword, RoleAdmin)

	// Each attempt comes from a different address, so only the per-user
	// limiter can be responsible for the lock.
	for i := 1; i <= 5; i++ {
		c := &client{ip: fmt.Sprintf("198.51.100.%d", i)}
		res := e.do(c, "POST", "/api/v1/auth/login", creds("admin", "yanlis-parola-123"))
		if res.Status != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d, want 401", i, res.Status)
		}
	}
	res := e.do(&client{ip: "198.51.100.200"}, "POST", "/api/v1/auth/login", creds("admin", testPassword))
	if res.Status != http.StatusTooManyRequests || res.code() != "rate_limited" {
		t.Fatalf("correct password after 5 failures: status %d body %s", res.Status, res.Raw)
	}
	if sessionCookie(res) != nil {
		t.Fatal("locked account received a session")
	}
	// Spelling variants of the name must hit the same lock.
	res = e.do(&client{ip: "198.51.100.201"}, "POST", "/api/v1/auth/login", creds(" Admin ", testPassword))
	if res.Status != http.StatusTooManyRequests {
		t.Fatalf("lock bypassed by changing the case of the username: status %d", res.Status)
	}
	// Another user is unaffected.
	e.addUser("diger", testPassword, RoleUser)
	if r := e.do(&client{ip: "198.51.100.202"}, "POST", "/api/v1/auth/login", creds("diger", testPassword)); r.Status != http.StatusOK {
		t.Fatalf("unrelated user locked: status %d", r.Status)
	}

	clock.advance(15*time.Minute - time.Second)
	if r := e.do(&client{ip: "198.51.100.203"}, "POST", "/api/v1/auth/login", creds("admin", testPassword)); r.Status != http.StatusTooManyRequests {
		t.Fatalf("lock ended early: status %d", r.Status)
	}
	clock.advance(2 * time.Second)
	if r := e.do(&client{ip: "198.51.100.204"}, "POST", "/api/v1/auth/login", creds("admin", testPassword)); r.Status != http.StatusOK {
		t.Fatalf("still locked after the window: status %d body %s", r.Status, r.Raw)
	}
}

func TestLoginFourFailuresDoNotLock(t *testing.T) {
	e := newEnv(t)
	e.useClock()
	e.completeSetup()
	e.addUser("admin", testPassword, RoleAdmin)
	for i := 0; i < 4; i++ {
		e.do(nil, "POST", "/api/v1/auth/login", creds("admin", "yanlis-parola-123"))
	}
	if r := e.do(nil, "POST", "/api/v1/auth/login", creds("admin", testPassword)); r.Status != http.StatusOK {
		t.Fatalf("locked after 4 failures: status %d", r.Status)
	}
}

func TestLoginLocksAddressAfterTenFailures(t *testing.T) {
	e := newEnv(t)
	clock := e.useClock()
	e.completeSetup()
	e.addUser("admin", testPassword, RoleAdmin)

	attacker := &client{ip: "203.0.113.7"}
	// Ten different names: no per-user counter reaches its limit.
	for i := 1; i <= 10; i++ {
		res := e.do(attacker, "POST", "/api/v1/auth/login", creds(fmt.Sprintf("hedef%d", i), "yanlis-parola-123"))
		if res.Status != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d, want 401", i, res.Status)
		}
	}
	res := e.do(attacker, "POST", "/api/v1/auth/login", creds("admin", testPassword))
	if res.Status != http.StatusTooManyRequests {
		t.Fatalf("address not locked after 10 failures: status %d body %s", res.Status, res.Raw)
	}
	// The same address on another source port is the same client.
	if r := e.do(&client{ip: "203.0.113.7"}, "POST", "/api/v1/auth/login", creds("admin", testPassword)); r.Status != http.StatusTooManyRequests {
		t.Fatalf("status %d", r.Status)
	}
	if r := e.do(&client{ip: "203.0.113.8"}, "POST", "/api/v1/auth/login", creds("admin", testPassword)); r.Status != http.StatusOK {
		t.Fatalf("other address locked too: status %d", r.Status)
	}
	clock.advance(15*time.Minute + time.Second)
	if r := e.do(attacker, "POST", "/api/v1/auth/login", creds("admin", testPassword)); r.Status != http.StatusOK {
		t.Fatalf("address still locked after the window: status %d", r.Status)
	}
}

// Proxy headers must not influence the address used for rate limiting.
func TestLoginLimiterIgnoresForwardedHeaders(t *testing.T) {
	e := newEnv(t)
	e.useClock()
	e.completeSetup()
	e.addUser("admin", testPassword, RoleAdmin)
	for i := 1; i <= 10; i++ {
		e.doWithHeaders("203.0.113.9", map[string]string{
			"X-Forwarded-For": fmt.Sprintf("10.0.0.%d", i),
			"X-Real-IP":       fmt.Sprintf("10.0.0.%d", i),
			"Forwarded":       fmt.Sprintf("for=10.0.0.%d", i),
		}, creds(fmt.Sprintf("hedef%d", i), "yanlis-parola-123"))
	}
	res := e.doWithHeaders("203.0.113.9", map[string]string{"X-Forwarded-For": "10.9.9.9"}, creds("admin", testPassword))
	if res.Status != http.StatusTooManyRequests {
		t.Fatalf("rate limit bypassed with X-Forwarded-For: status %d", res.Status)
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE ip LIKE '10.%'`); n != 0 {
		t.Fatalf("%d audit records carry a spoofed address", n)
	}
}

func TestLoginSuccessResetsFailureCount(t *testing.T) {
	e := newEnv(t)
	e.useClock()
	e.completeSetup()
	e.addUser("admin", testPassword, RoleAdmin)
	for round := 0; round < 3; round++ {
		for i := 0; i < 4; i++ {
			if r := e.do(nil, "POST", "/api/v1/auth/login", creds("admin", "yanlis-parola-123")); r.Status != http.StatusUnauthorized {
				t.Fatalf("round %d attempt %d: status %d, want 401", round, i, r.Status)
			}
		}
		if r := e.do(nil, "POST", "/api/v1/auth/login", creds("admin", testPassword)); r.Status != http.StatusOK {
			t.Fatalf("round %d: correct login status %d", round, r.Status)
		}
	}
}

func TestStatusEndpoint(t *testing.T) {
	e := newEnv(t)
	res := e.do(nil, "GET", "/api/v1/auth/status", nil)
	if res.Status != http.StatusOK || !strings.Contains(res.Raw, `"setup_complete":false`) ||
		!strings.Contains(res.Raw, `"authenticated":false`) || !strings.Contains(res.Raw, `"user":null`) ||
		!strings.Contains(res.Raw, `"csrf_token":""`) {
		t.Fatalf("anonymous status: %d %s", res.Status, res.Raw)
	}
	e.completeSetup()
	e.addUser("admin", testPassword, RoleAdmin)
	c := e.login("admin", testPassword, "")
	res = e.do(c, "GET", "/api/v1/auth/status", nil)
	if !strings.Contains(res.Raw, `"authenticated":true`) || !strings.Contains(res.Raw, `"csrf_token":"`+c.csrf+`"`) {
		t.Fatalf("signed-in status: %s", res.Raw)
	}
	if strings.Contains(res.Raw, "password") || strings.Contains(res.Raw, "argon2") || strings.Contains(res.Raw, c.cookie.Value) {
		t.Fatalf("status leaks secrets: %s", res.Raw)
	}
}
