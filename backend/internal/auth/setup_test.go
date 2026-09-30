package auth

import (
	"context"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"myserver/internal/settings"
)

// setupBody returns a request the wizard accepts on this machine. The zone
// database is a system facility; where it is absent the wizard cannot be
// completed and the tests that need a full run are skipped.
func setupBody(t *testing.T) map[string]string {
	t.Helper()
	if !settings.ValidTimezone("UTC") {
		t.Skip("no zone database at /usr/share/zoneinfo; mount one to run this test")
	}
	host, _ := os.Hostname()
	host = strings.ToLower(host)
	if !settings.ValidHostname(host) {
		host = "testhost"
	}
	return map[string]string{"username": "kurucu", "password": testPassword, "hostname": host, "timezone": "UTC"}
}

func TestSetupCreatesAdminAndSession(t *testing.T) {
	e := newEnv(t)
	body := setupBody(t)
	res := e.do(nil, "POST", "/api/v1/auth/setup", body)
	if res.Status != http.StatusCreated || !res.Success {
		t.Fatalf("setup: %d %s", res.Status, res.Raw)
	}
	if strings.Contains(res.Raw, testPassword) || strings.Contains(res.Raw, "argon2") {
		t.Fatalf("response leaks the password: %s", res.Raw)
	}
	if n := e.count(`SELECT COUNT(*) FROM users WHERE username = 'kurucu' AND role = 'admin' AND disabled = 0`); n != 1 {
		t.Fatal("admin not created")
	}
	if !e.store.Bool(settings.KeySetupComplete) {
		t.Fatal("setup flag not set")
	}
	// The flag must be persisted, not only cached.
	if n := e.count(`SELECT COUNT(*) FROM settings WHERE key = 'setup.complete' AND value = 'true'`); n != 1 {
		t.Fatal("setup flag not stored in the database")
	}
	c := sessionCookie(res)
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("setup session cookie: %+v", c)
	}
	if !e.alive(&client{cookie: c}) {
		t.Fatal("session issued by setup is not valid")
	}
	if r := e.do(nil, "POST", "/api/v1/auth/login", creds("kurucu", testPassword)); r.Status != http.StatusOK {
		t.Fatalf("login after setup: %d", r.Status)
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'setup.complete' AND success = 1`); n != 1 {
		t.Fatal("setup not audited")
	}
}

func TestSetupOnlyOnce(t *testing.T) {
	e := newEnv(t)
	body := setupBody(t)
	if res := e.do(nil, "POST", "/api/v1/auth/setup", body); res.Status != http.StatusCreated {
		t.Fatalf("first setup: %d %s", res.Status, res.Raw)
	}
	body["username"] = "ikinci"
	res := e.do(nil, "POST", "/api/v1/auth/setup", body)
	if res.Status != http.StatusForbidden || res.code() != "setup_complete" {
		t.Fatalf("second setup: %d %s", res.Status, res.Raw)
	}
	if sessionCookie(res) != nil {
		t.Fatal("second setup issued a session")
	}
	if n := e.count(`SELECT COUNT(*) FROM users`); n != 1 {
		t.Fatalf("%d users after a second setup", n)
	}
}

func TestSetupConcurrentRequestsCreateOneUser(t *testing.T) {
	e := newEnv(t)
	base := setupBody(t)
	const n = 8
	names := []string{"birinci", "ikinci", "ucuncu", "dorduncu", "besinci", "altinci", "yedinci", "sekizinci"}
	results := make([]*response, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := map[string]string{}
			for k, v := range base {
				body[k] = v
			}
			body["username"] = names[i]
			<-start
			results[i] = e.do(&client{ip: "192.0.2.50"}, "POST", "/api/v1/auth/setup", body)
		}(i)
	}
	close(start)
	wg.Wait()

	created, refused := 0, 0
	for i, r := range results {
		switch {
		case r.Status == http.StatusCreated:
			created++
		case r.Status == http.StatusForbidden && r.code() == "setup_complete":
			refused++
			if sessionCookie(r) != nil {
				t.Errorf("request %d was refused but received a session", i)
			}
		default:
			t.Errorf("request %d: unexpected status %d %s", i, r.Status, r.Raw)
		}
	}
	if created != 1 || refused != n-1 {
		t.Fatalf("%d setups succeeded and %d were refused, want 1 and %d", created, refused, n-1)
	}
	if got := e.count(`SELECT COUNT(*) FROM users`); got != 1 {
		t.Fatalf("%d users created by concurrent setup requests", got)
	}
	if got := e.count(`SELECT COUNT(*) FROM sessions`); got != 1 {
		t.Fatalf("%d sessions created by concurrent setup requests", got)
	}
}

func TestSetupRefusesBadInput(t *testing.T) {
	e := newEnv(t)
	host, _ := os.Hostname()
	host = strings.ToLower(host)
	if !settings.ValidHostname(host) {
		host = "testhost"
	}
	ok := func() map[string]string {
		return map[string]string{"username": "kurucu", "password": testPassword, "hostname": host, "timezone": "UTC"}
	}
	with := func(k, v string) map[string]string { m := ok(); m[k] = v; return m }
	cases := map[string]any{
		"weak password":       with("password", "kisa1234"),
		"empty password":      with("password", ""),
		"password is name":    with("password", "KURUCU"), // also short; both rules refuse
		"long name password":  map[string]string{"username": "uzunkullanici", "password": "UzunKullanici", "hostname": host, "timezone": "UTC"},
		"oversized password":  with("password", strings.Repeat("a", 300)),
		"root":                with("username", "root"),
		"root upper case":     with("username", "ROOT"),
		"root with spaces":    with("username", " root "),
		"empty username":      with("username", ""),
		"short username":      with("username", "ab"),
		"digit first":         with("username", "1admin"),
		"username with slash": with("username", "../admin"),
		"username with space": with("username", "ad min"),
		"username with quote": with("username", "admin'--"),
		"long username":       with("username", strings.Repeat("a", 33)),
		"hostname with space": with("hostname", "bad host"),
		"hostname with dot":   with("hostname", "a.b"),
		"hostname option":     with("hostname", "-rf"),
		"hostname shell":      with("hostname", "a;reboot"),
		"empty hostname":      with("hostname", ""),
		"long hostname":       with("hostname", strings.Repeat("a", 64)),
		"timezone traversal":  with("timezone", "../../etc/passwd"),
		"timezone absolute":   with("timezone", "/etc/passwd"),
		"timezone unknown":    with("timezone", "Mars/Olympus_Mons"),
		"empty timezone":      with("timezone", ""),
		"unknown field":       map[string]string{"username": "kurucu", "password": testPassword, "hostname": host, "timezone": "UTC", "role": "admin"},
		"not json":            "username=kurucu",
		"empty body":          "",
	}
	for name, body := range cases {
		res := e.do(nil, "POST", "/api/v1/auth/setup", body)
		if res.Status != http.StatusBadRequest || res.Success {
			t.Errorf("%s: status %d, want 400 (%s)", name, res.Status, res.Raw)
		}
		if sessionCookie(res) != nil {
			t.Errorf("%s: session issued", name)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM users`); n != 0 {
		t.Fatalf("%d users created by refused setup requests", n)
	}
	if e.store.Bool(settings.KeySetupComplete) {
		t.Fatal("setup marked complete by a refused request")
	}
}

func TestSetupEndpointsRefuseAfterCompletion(t *testing.T) {
	e := ready(t) // setup complete, one admin
	host, _ := os.Hostname()
	bodies := map[string]any{
		"valid request":   map[string]string{"username": "ikinci", "password": testPassword, "hostname": strings.ToLower(host), "timezone": "UTC"},
		"invalid request": map[string]string{"username": "x", "password": "y", "hostname": "", "timezone": ""},
		"empty body":      "",
	}
	for name, body := range bodies {
		res := e.do(nil, "POST", "/api/v1/auth/setup", body)
		if res.Status != http.StatusForbidden || res.code() != "setup_complete" {
			t.Errorf("POST setup, %s: status %d code %q, want 403 setup_complete", name, res.Status, res.code())
		}
		if sessionCookie(res) != nil {
			t.Errorf("POST setup, %s: session issued", name)
		}
	}
	for _, p := range []string{"/api/v1/auth/setup/checks", "/api/v1/auth/setup/timezones"} {
		res := e.do(nil, "GET", p, nil)
		if res.Status != http.StatusForbidden || res.code() != "setup_complete" {
			t.Errorf("GET %s: status %d code %q, want 403 setup_complete", p, res.Status, res.code())
		}
		if res.Raw != `{"success":false,"data":null,"error":{"code":"setup_complete","message":"Kurulum zaten tamamlandı. Bu işlem tekrar kullanılamaz."}}`+"\n" {
			t.Errorf("GET %s: body %s", p, res.Raw)
		}
		// Signed in or not makes no difference.
		c := e.login("admin", testPassword, "")
		if res := e.do(c, "GET", p, nil); res.Status != http.StatusForbidden {
			t.Errorf("GET %s signed in: status %d", p, res.Status)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM users`); n != 1 {
		t.Fatalf("%d users", n)
	}
}

// If the flag is lost but users exist, setup must repair the flag rather
// than create a second "first" administrator.
func TestSetupRefusedWhenUsersExistWithoutFlag(t *testing.T) {
	e := newEnv(t)
	body := setupBody(t)
	e.addUser("admin", testPassword, RoleAdmin)
	res := e.do(nil, "POST", "/api/v1/auth/setup", body)
	if res.Status != http.StatusForbidden || res.code() != "setup_complete" {
		t.Fatalf("status %d %s", res.Status, res.Raw)
	}
	if n := e.count(`SELECT COUNT(*) FROM users`); n != 1 {
		t.Fatalf("%d users", n)
	}
	if !e.store.Bool(settings.KeySetupComplete) {
		t.Fatal("flag not repaired")
	}
}

func TestSetupChecksBeforeCompletion(t *testing.T) {
	e := newEnv(t)
	res := e.do(nil, "GET", "/api/v1/auth/setup/checks", nil)
	if res.Status != http.StatusOK {
		t.Fatalf("status %d %s", res.Status, res.Raw)
	}
	// The helper path of the test environment does not exist.
	if !strings.Contains(res.Raw, `"helper":{"ok":false`) {
		t.Fatalf("missing helper reported as available: %s", res.Raw)
	}
	if !strings.Contains(res.Raw, `"docker":{"ok":false`) {
		t.Fatalf("missing Docker socket reported as available: %s", res.Raw)
	}
	if strings.Contains(res.Raw, e.cfg.HelperPath) || strings.Contains(res.Raw, "no-such-helper") {
		t.Fatalf("checks expose internal paths: %s", res.Raw)
	}
	if res := e.do(nil, "GET", "/api/v1/auth/setup/timezones", nil); res.Status != http.StatusOK || !strings.Contains(res.Raw, `"UTC"`) {
		t.Fatalf("timezones: %d %s", res.Status, res.Raw)
	}
}

// The flag is what gates login; a request must not be able to set it except
// by completing setup.
func TestSetupFlagSurvivesRestart(t *testing.T) {
	e := newEnv(t)
	e.completeSetup()
	st, err := settings.NewStore(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Bool(settings.KeySetupComplete) {
		t.Fatal("setup flag lost when the store is reloaded")
	}
}
