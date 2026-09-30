package security

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"myserver/internal/audit"
	"myserver/internal/auth"
	"myserver/internal/config"
	"myserver/internal/database"
	"myserver/internal/httpx"
	"myserver/internal/module"
	"myserver/internal/privileged"
	"myserver/internal/security/fwcheck"
	"myserver/internal/settings"
	"myserver/migrations"
)

// fakeFW stands in for ufw and the root helper. Nothing is executed: the
// "firewall" is a list of rule lines in memory. The texts it produces follow
// ufw's formats from memory; they are not captured from a live system.
type fakeFW struct {
	mu        sync.Mutex
	installed bool
	active    bool
	added     []string // lines of `ufw show added`
	numbered  string   // rows of `ufw status numbered` (active firewall)
	calls     [][]string
	fail      map[string]error
	stateErr  error
}

func (f *fakeFW) state(context.Context) (*State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stateErr != nil {
		return nil, f.stateErr
	}
	raw := fwcheck.RawStatus{Installed: f.installed}
	if f.installed {
		raw.DefaultConf = "DEFAULT_INPUT_POLICY=\"DROP\"\nDEFAULT_OUTPUT_POLICY=\"ACCEPT\"\n"
		raw.Added = "Added user rules (see 'ufw status' for running firewall):\n" + strings.Join(f.added, "\n") + "\n"
		if f.active {
			raw.Verbose = "Status: active\nLogging: on (low)\nDefault: deny (incoming), allow (outgoing), disabled (routed)\n"
			raw.Numbered = tblHead + f.numbered
		} else {
			raw.Verbose = "Status: inactive\n"
			raw.Numbered = "Status: inactive\n"
		}
	}
	return buildState(raw, ":8080"), nil
}

func (f *fakeFW) run(_ context.Context, action string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string{action}, args...))
	if err := f.fail[action]; err != nil {
		return nil, err
	}
	switch action {
	case "firewall-rule-add":
		// ufw appends a new rule to the end of the list.
		line := "ufw " + args[0] + " from " + args[3] + " to any port " + args[1]
		if args[2] != "any" {
			line += " proto " + args[2]
		}
		f.added = append(f.added, line)
	case "firewall-enable":
		f.active = true
	case "firewall-disable":
		f.active = false
	}
	return nil, nil
}

func (f *fakeFW) actions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for _, c := range f.calls {
		out = append(out, c[0])
	}
	return out
}

type login struct {
	cookie *http.Cookie
	csrf   string
}

type harness struct {
	t     *testing.T
	fw    *fakeFW
	mux   *http.ServeMux
	admin login
	user  login
	db    *audit.Logger
}

const testPassword = "dogru-at-pil-zimba-1"

func newHarness(t *testing.T, fw *fakeFW) *harness {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := database.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	store, err := settings.NewStore(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{ListenAddr: ":8080", DataDir: dir, HelperPath: filepath.Join(dir, "no-helper")}
	al := audit.New(db)
	priv := privileged.New(cfg.HelperPath)
	authSvc, err := auth.NewService(db, cfg, store, al, priv)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	for name, role := range map[string]string{"yonetici": auth.RoleAdmin, "kullanici": auth.RoleUser} {
		if _, err := db.ExecContext(ctx, `INSERT INTO users (username, password_hash, role, created_at) VALUES (?, ?, ?, ?)`,
			name, hash, role, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Set(ctx, settings.KeySetupComplete, "true"); err != nil {
		t.Fatal(err)
	}

	deps := module.Deps{Cfg: cfg, DB: db, Settings: store, Audit: al, Priv: priv, Auth: authSvc}
	mod, err := New(deps, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := mod.(*Module)
	if m.readState == nil || m.run == nil {
		t.Fatal("New did not set the system accessors")
	}
	m.readState, m.run = fw.state, fw.run

	mux := http.NewServeMux()
	root := httpx.NewRouter(mux).Group("/api/v1")
	authSvc.RegisterPublic(root)
	api := root.Group("", authSvc.Require)
	m.Register(api, api)

	h := &harness{t: t, fw: fw, mux: mux, db: al}
	h.admin = h.login("yonetici")
	h.user = h.login("kullanici")
	return h
}

func (h *harness) login(name string) login {
	h.t.Helper()
	body, _ := json.Marshal(map[string]string{"username": name, "password": testPassword})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	req.RemoteAddr = "192.168.1.50:40000"
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("login %s: %d %s", name, rec.Code, rec.Body.String())
	}
	var env struct {
		Data struct {
			CSRF string `json:"csrf_token"`
			User struct {
				Role string `json:"role"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Data.CSRF == "" {
		h.t.Fatalf("login %s: bad response %s", name, rec.Body.String())
	}
	cs := rec.Result().Cookies()
	if len(cs) == 0 {
		h.t.Fatalf("login %s: no cookie", name)
	}
	return login{cookie: cs[0], csrf: env.Data.CSRF}
}

type response struct {
	Status int
	Code   string
	Msg    string
	Data   json.RawMessage
}

// do sends a request as the given login (nil: not signed in) from client.
func (h *harness) do(l *login, client, method, path string, body any) response {
	h.t.Helper()
	var rd *bytes.Reader
	switch b := body.(type) {
	case nil:
		rd = bytes.NewReader(nil)
	case string:
		rd = bytes.NewReader([]byte(b))
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			h.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, "/api/v1"+path, rd)
	req.RemoteAddr = client
	req.Header.Set("Content-Type", "application/json")
	if l != nil {
		req.AddCookie(l.cookie)
		if l.csrf != "" {
			req.Header.Set("X-CSRF-Token", l.csrf)
		}
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	var env struct {
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		h.t.Fatalf("%s %s: response is not JSON: %q", method, path, rec.Body.String())
	}
	out := response{Status: rec.Code, Data: env.Data}
	if env.Error != nil {
		out.Code, out.Msg = env.Error.Code, env.Error.Message
	}
	return out
}

const lanAddr = "192.168.1.50:51000"

type accessRule struct {
	Kind   string `json:"kind"`
	Port   int    `json:"port"`
	Source string `json:"source"`
}

func enableBody(rules ...accessRule) map[string]any {
	if rules == nil {
		rules = []accessRule{}
	}
	return map[string]any{"add_rules": rules}
}

func requireSSH22(t *testing.T) {
	t.Helper()
	if p, _ := detectSSHPorts(); !reflect.DeepEqual(p, []int{22}) {
		t.Skipf("sshd of this machine listens on %v; the fixtures assume port 22", p)
	}
}

func TestEnableRefusedWithoutAccessRules(t *testing.T) {
	requireSSH22(t)
	cases := []struct {
		name  string
		added []string
		want  []string // fragments of the message
	}{
		{"no rules", nil, []string{"SSH portu (22/tcp)", "panel portu (8080/tcp)"}},
		{"ssh only", []string{"ufw allow 22/tcp"}, []string{"panel portu (8080/tcp)"}},
		{"panel only", []string{"ufw allow 8080/tcp"}, []string{"SSH portu (22/tcp)"}},
		{"panel rule for another network", []string{"ufw allow 22/tcp", "ufw allow from 10.0.0.0/8 to any port 8080 proto tcp"},
			[]string{"panel portu (8080/tcp)"}},
		{"only deny rules", []string{"ufw deny 22/tcp", "ufw deny 8080/tcp"}, []string{"SSH", "panel"}},
		{"allow rules shadowed by earlier deny rules",
			[]string{"ufw deny 22/tcp", "ufw allow 22/tcp", "ufw reject 8080/tcp", "ufw allow 8080/tcp"}, []string{"SSH", "panel"}},
		{"panel allow shadowed by a deny for the client's network",
			[]string{"ufw allow 22/tcp", "ufw deny from 192.168.1.0/24", "ufw allow 8080/tcp"}, []string{"panel"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fw := &fakeFW{installed: true, added: c.added}
			h := newHarness(t, fw)
			res := h.do(&h.admin, lanAddr, http.MethodPost, "/firewall/enable", enableBody())
			if res.Status != http.StatusConflict || res.Code != "firewall_access_rules_missing" {
				t.Fatalf("got %+v", res)
			}
			for _, w := range c.want {
				if !strings.Contains(res.Msg, w) {
					t.Errorf("message %q lacks %q", res.Msg, w)
				}
			}
			if len(fw.calls) != 0 {
				t.Errorf("helper was called: %v", fw.calls)
			}
			if fw.active {
				t.Error("firewall was enabled")
			}

			chk := h.do(&h.admin, lanAddr, http.MethodGet, "/firewall/enable-check", nil)
			var ec EnableCheck
			if err := json.Unmarshal(chk.Data, &ec); err != nil || chk.Status != 200 {
				t.Fatalf("enable-check: %+v %v", chk, err)
			}
			if ec.CanEnable || len(ec.Missing) == 0 || ec.ClientIP != "192.168.1.50" {
				t.Errorf("enable-check: %+v", ec)
			}
		})
	}
}

func TestEnableWithExistingRules(t *testing.T) {
	requireSSH22(t)
	fw := &fakeFW{installed: true, added: []string{"ufw limit OpenSSH", "ufw allow from 192.168.1.0/24 to any port 8080 proto tcp"}}
	h := newHarness(t, fw)
	res := h.do(&h.admin, lanAddr, http.MethodPost, "/firewall/enable", enableBody())
	if res.Status != http.StatusOK {
		t.Fatalf("got %+v", res)
	}
	if got := fw.actions(); !reflect.DeepEqual(got, []string{"firewall-enable"}) {
		t.Errorf("calls %v", fw.calls)
	}
	// The same rules do not admit a client outside the network.
	fw2 := &fakeFW{installed: true, added: fw.added}
	h2 := newHarness(t, fw2)
	res = h2.do(&h2.admin, "203.0.113.9:1234", http.MethodPost, "/firewall/enable", enableBody())
	if res.Status != http.StatusConflict || len(fw2.calls) != 0 {
		t.Errorf("outside client: %+v calls %v", res, fw2.calls)
	}
}

func TestEnableAddsAgreedRules(t *testing.T) {
	requireSSH22(t)
	fw := &fakeFW{installed: true}
	h := newHarness(t, fw)
	res := h.do(&h.admin, lanAddr, http.MethodPost, "/firewall/enable", enableBody(
		accessRule{"ssh", 22, "any"}, accessRule{"panel", 8080, "192.168.1.77/24"}))
	if res.Status != http.StatusOK {
		t.Fatalf("got %+v", res)
	}
	want := [][]string{
		{"firewall-rule-add", "allow", "22", "tcp", "any", "SSH"},
		{"firewall-rule-add", "allow", "8080", "tcp", "192.168.1.0/24", "MyServer panel"},
		{"firewall-enable"},
	}
	if !reflect.DeepEqual(fw.calls, want) {
		t.Errorf("calls\n got %q\nwant %q", fw.calls, want)
	}
}

func TestEnableRefusesPanelRuleThatExcludesClient(t *testing.T) {
	requireSSH22(t)
	for _, src := range []string{"10.0.0.0/8", "192.168.1.51", "192.168.2.0/24", "2001:db8::/32"} {
		fw := &fakeFW{installed: true}
		h := newHarness(t, fw)
		res := h.do(&h.admin, lanAddr, http.MethodPost, "/firewall/enable", enableBody(
			accessRule{"ssh", 22, "any"}, accessRule{"panel", 8080, src}))
		if res.Status != http.StatusConflict {
			t.Errorf("source %s: got %+v", src, res)
		}
		for _, a := range fw.actions() {
			if a == "firewall-enable" {
				t.Errorf("source %s: firewall was enabled", src)
			}
		}
		if fw.active {
			t.Errorf("source %s: firewall active", src)
		}
	}
}

func TestEnableIsAllOrNothing(t *testing.T) {
	requireSSH22(t)
	// Only one of the two missing rules is supplied.
	fw := &fakeFW{installed: true}
	h := newHarness(t, fw)
	res := h.do(&h.admin, lanAddr, http.MethodPost, "/firewall/enable", enableBody(accessRule{"ssh", 22, "any"}))
	if res.Status != http.StatusConflict || res.Code != "firewall_access_rules_missing" || len(fw.calls) != 0 {
		t.Errorf("partial: %+v calls %v", res, fw.calls)
	}
	// A rule with the wrong port does not satisfy the requirement.
	res = h.do(&h.admin, lanAddr, http.MethodPost, "/firewall/enable", enableBody(
		accessRule{"ssh", 2222, "any"}, accessRule{"panel", 8080, "any"}))
	if res.Status != http.StatusConflict || len(fw.calls) != 0 {
		t.Errorf("wrong port: %+v calls %v", res, fw.calls)
	}
	// Invalid sources never reach the helper.
	for _, src := range []string{"example.com", "-o", "--force", "192.168.1.0/24 --force", "0.0.0.0/0", "192.168.1.0/24\n--force"} {
		res = h.do(&h.admin, lanAddr, http.MethodPost, "/firewall/enable", enableBody(
			accessRule{"ssh", 22, src}, accessRule{"panel", 8080, src}))
		if res.Status != http.StatusBadRequest || len(fw.calls) != 0 {
			t.Errorf("source %q: %+v calls %v", src, res, fw.calls)
		}
	}
	if fw.active {
		t.Error("firewall active")
	}
}

// A rule added by the enable dialog lands behind the existing rules. When
// an earlier deny rule shadows it, the firewall must stay off.
func TestEnableVerifiesAddedRulesAgainstOrder(t *testing.T) {
	requireSSH22(t)
	fw := &fakeFW{installed: true, added: []string{"ufw deny 22/tcp", "ufw allow 8080/tcp"}}
	h := newHarness(t, fw)
	res := h.do(&h.admin, lanAddr, http.MethodPost, "/firewall/enable", enableBody(accessRule{"ssh", 22, "any"}))
	if res.Status != http.StatusConflict || res.Code != "firewall_access_rules_missing" {
		t.Fatalf("got %+v", res)
	}
	if got := fw.actions(); !reflect.DeepEqual(got, []string{"firewall-rule-add"}) {
		t.Errorf("calls %v", fw.calls)
	}
	if fw.active {
		t.Error("firewall was enabled although SSH is denied by an earlier rule")
	}
}

func TestEnableFailures(t *testing.T) {
	requireSSH22(t)
	userErr := &privileged.Error{Action: "x", ExitCode: privileged.ExitUser, Message: "Kural eklenemedi."}
	fw := &fakeFW{installed: true, fail: map[string]error{"firewall-rule-add": userErr}}
	h := newHarness(t, fw)
	res := h.do(&h.admin, lanAddr, http.MethodPost, "/firewall/enable", enableBody(
		accessRule{"ssh", 22, "any"}, accessRule{"panel", 8080, "any"}))
	if res.Status != http.StatusBadGateway || res.Msg != "Kural eklenemedi." {
		t.Errorf("got %+v", res)
	}
	if got := fw.actions(); !reflect.DeepEqual(got, []string{"firewall-rule-add"}) {
		t.Errorf("after a failed rule nothing else may run: %v", fw.calls)
	}

	// Raw helper errors are never shown to the user.
	fw = &fakeFW{installed: true, added: []string{"ufw allow 22/tcp", "ufw allow 8080/tcp"},
		fail: map[string]error{"firewall-enable": errors.New("exec: /secret/path: permission denied")}}
	h = newHarness(t, fw)
	res = h.do(&h.admin, lanAddr, http.MethodPost, "/firewall/enable", enableBody())
	if res.Status != http.StatusBadGateway || strings.Contains(res.Msg, "secret") || res.Msg == "" {
		t.Errorf("got %+v", res)
	}

	fw = &fakeFW{installed: false}
	h = newHarness(t, fw)
	for _, p := range []string{"/firewall/enable", "/firewall/disable"} {
		res = h.do(&h.admin, lanAddr, http.MethodPost, p, map[string]any{})
		if p == "/firewall/disable" {
			res = h.do(&h.admin, lanAddr, http.MethodPost, p, nil)
		}
		if res.Status != http.StatusConflict || res.Code != "firewall_not_installed" {
			t.Errorf("%s: %+v", p, res)
		}
	}
	if len(fw.calls) != 0 {
		t.Errorf("calls %v", fw.calls)
	}

	fw = &fakeFW{installed: true, stateErr: errors.New("boom")}
	h = newHarness(t, fw)
	res = h.do(&h.admin, lanAddr, http.MethodPost, "/firewall/enable", enableBody())
	if res.Status != http.StatusBadGateway || len(fw.calls) != 0 || strings.Contains(res.Msg, "boom") {
		t.Errorf("unreadable state: %+v calls %v", res, fw.calls)
	}
}

func TestEnableWhenAlreadyActiveDoesNothing(t *testing.T) {
	fw := &fakeFW{installed: true, active: true, numbered: "[ 1] 22/tcp                     ALLOW IN    Anywhere\n"}
	h := newHarness(t, fw)
	res := h.do(&h.admin, lanAddr, http.MethodPost, "/firewall/enable", enableBody())
	if res.Status != http.StatusOK || len(fw.calls) != 0 {
		t.Errorf("got %+v calls %v", res, fw.calls)
	}
}

const activeRows = "[ 1] 22/tcp                     ALLOW IN    Anywhere\n" +
	"[ 2] 8080/tcp                   ALLOW IN    192.168.1.0/24\n" +
	"[ 3] 445/tcp                    ALLOW IN    192.168.1.0/24             # SMB\n"

func delBody(origin string, n int, id string, ack bool) map[string]any {
	return map[string]any{"origin": origin, "number": n, "id": id, "acknowledge_access_risk": ack}
}

func TestDeleteProtectedRuleNeedsAcknowledgement(t *testing.T) {
	requireSSH22(t)
	for _, c := range []struct {
		n    int
		id   string
		what string
	}{{1, "22/tcp ALLOW IN Anywhere", "SSH"}, {2, "8080/tcp ALLOW IN 192.168.1.0/24", "panel"}} {
		fw := &fakeFW{installed: true, active: true, numbered: activeRows}
		h := newHarness(t, fw)
		res := h.do(&h.admin, lanAddr, http.MethodPost, "/firewall/rules/delete", delBody("numbered", c.n, c.id, false))
		if res.Status != http.StatusConflict || res.Code != "firewall_protected_rule" || !strings.Contains(res.Msg, c.what) {
			t.Errorf("rule %d: %+v", c.n, res)
		}
		if len(fw.calls) != 0 {
			t.Errorf("rule %d deleted without acknowledgement: %v", c.n, fw.calls)
		}
		res = h.do(&h.admin, lanAddr, http.MethodPost, "/firewall/rules/delete", delBody("numbered", c.n, c.id, true))
		want := [][]string{{"firewall-rule-delete", "numbered", "1", c.id}}
		want[0][2] = string(rune('0' + c.n))
		if res.Status != http.StatusOK || !reflect.DeepEqual(fw.calls, want) {
			t.Errorf("rule %d with acknowledgement: %+v calls %q", c.n, res, fw.calls)
		}
	}
	// Rules of an inactive firewall are protected in the same way.
	fw := &fakeFW{installed: true, added: []string{"ufw limit OpenSSH", "ufw allow 445/tcp"}}
	h := newHarness(t, fw)
	res := h.do(&h.admin, lanAddr, http.MethodPost, "/firewall/rules/delete", delBody("added", 1, "ufw limit OpenSSH", false))
	if res.Status != http.StatusConflict || res.Code != "firewall_protected_rule" || len(fw.calls) != 0 {
		t.Errorf("added: %+v calls %v", res, fw.calls)
	}
}

func TestDeleteOrdinaryRule(t *testing.T) {
	requireSSH22(t)
	fw := &fakeFW{installed: true, active: true, numbered: activeRows}
	h := newHarness(t, fw)
	id := "445/tcp ALLOW IN 192.168.1.0/24 # SMB"
	res := h.do(&h.admin, lanAddr, http.MethodPost, "/firewall/rules/delete", delBody("numbered", 3, id, false))
	want := [][]string{{"firewall-rule-delete", "numbered", "3", id}}
	if res.Status != http.StatusOK || !reflect.DeepEqual(fw.calls, want) {
		t.Errorf("got %+v calls %q", res, fw.calls)
	}
}

func TestDeleteRefusesStaleOrInvalidRequests(t *testing.T) {
	fw := &fakeFW{installed: true, active: true, numbered: activeRows}
	h := newHarness(t, fw)
	smb := "445/tcp ALLOW IN 192.168.1.0/24 # SMB"
	stale := []map[string]any{
		delBody("numbered", 2, smb, true),                         // the rule moved
		delBody("numbered", 4, smb, true),                         // no such number
		delBody("numbered", 3, "445/tcp ALLOW IN Anywhere", true), // text differs
		delBody("added", 3, smb, true),                            // other list
	}
	for _, b := range stale {
		res := h.do(&h.admin, lanAddr, http.MethodPost, "/firewall/rules/delete", b)
		if res.Status != http.StatusConflict || !strings.Contains(res.Msg, "Kural listesi değişmiş") {
			t.Errorf("%v: %+v", b, res)
		}
	}
	invalid := []any{
		delBody("", 3, smb, true),
		delBody("status", 3, smb, true),
		delBody("numbered", 0, smb, true),
		delBody("numbered", -1, smb, true),
		delBody("numbered", 100001, smb, true),
		delBody("numbered", 3, "", true),
		delBody("numbered", 3, smb+"\nufw reset", true),
		delBody("numbered", 3, smb+"\x00", true),
		delBody("numbered", 3, strings.Repeat("a", 401), true),
		map[string]any{"origin": "numbered", "number": "3", "id": smb},
		map[string]any{"origin": "numbered", "number": 3, "id": smb, "force": true},
		"", "{", "[]",
	}
	for _, b := range invalid {
		res := h.do(&h.admin, lanAddr, http.MethodPost, "/firewall/rules/delete", b)
		if res.Status != http.StatusBadRequest {
			t.Errorf("%v: %+v", b, res)
		}
	}
	if len(fw.calls) != 0 {
		t.Errorf("helper was called: %q", fw.calls)
	}
}

func ruleBody(action, port, proto, source, comment string, ack bool) map[string]any {
	return map[string]any{"action": action, "port": port, "protocol": proto, "source": source,
		"comment": comment, "acknowledge_access_risk": ack}
}

func TestRuleAdd(t *testing.T) {
	requireSSH22(t)
	fw := &fakeFW{installed: true}
	h := newHarness(t, fw)
	res := h.do(&h.admin, lanAddr, http.MethodPost, "/firewall/rules", ruleBody("allow", "445", "tcp", "192.168.1.9/24", " SMB ", false))
	want := [][]string{{"firewall-rule-add", "allow", "445", "tcp", "192.168.1.0/24", "SMB"}}
	if res.Status != http.StatusOK || !reflect.DeepEqual(fw.calls, want) {
		t.Fatalf("got %+v calls %q", res, fw.calls)
	}
	fw.calls = nil

	// Rules that cut SSH or the panel need an acknowledgement.
	for _, b := range []map[string]any{
		ruleBody("deny", "22", "tcp", "any", "", false),
		ruleBody("reject", "22", "any", "any", "", false),
		ruleBody("deny", "1:1024", "tcp", "any", "", false),
		ruleBody("deny", "8080", "tcp", "any", "", false),
		ruleBody("reject", "8000:9000", "tcp", "203.0.113.5", "", false),
	} {
		res := h.do(&h.admin, lanAddr, http.MethodPost, "/firewall/rules", b)
		if res.Status != http.StatusConflict || res.Code != "firewall_access_risk" {
			t.Errorf("%v: %+v", b, res)
		}
	}
	if len(fw.calls) != 0 {
		t.Fatalf("risky rule added without acknowledgement: %q", fw.calls)
	}
	res = h.do(&h.admin, lanAddr, http.MethodPost, "/firewall/rules", ruleBody("deny", "22", "tcp", "203.0.113.5", "", true))
	want = [][]string{{"firewall-rule-add", "deny", "22", "tcp", "203.0.113.5", ""}}
	if res.Status != http.StatusOK || !reflect.DeepEqual(fw.calls, want) {
		t.Errorf("acknowledged: %+v calls %q", res, fw.calls)
	}
	fw.calls = nil

	invalid := []any{
		ruleBody("allow", "0", "tcp", "any", "", true),
		ruleBody("allow", "65536", "tcp", "any", "", true),
		ruleBody("allow", "90:80", "tcp", "any", "", true),
		ruleBody("allow", "http", "tcp", "any", "", true),
		ruleBody("allow", "80:90", "any", "any", "", true),
		ruleBody("allow", "80", "icmp", "any", "", true),
		ruleBody("allow", "80", "", "any", "", true),
		ruleBody("permit", "80", "tcp", "any", "", true),
		ruleBody("--force", "80", "tcp", "any", "", true),
		ruleBody("allow", "80", "tcp", "example.com", "", true),
		ruleBody("allow", "80", "tcp", "", "", true),
		ruleBody("allow", "80", "tcp", "-o", "", true),
		ruleBody("allow", "80", "tcp", "any", "a;b", true),
		ruleBody("allow", "80", "tcp", "any", "--dry-run", true),
		ruleBody("allow", "80", "tcp", "any", "new\nline", true),
		ruleBody("allow", "80", "tcp", "any", strings.Repeat("x", 65), true),
		map[string]any{"action": "allow", "port": 80, "protocol": "tcp", "source": "any"},
		map[string]any{"action": "allow", "port": "80", "protocol": "tcp", "source": "any", "insert": 1},
		"",
	}
	for _, b := range invalid {
		res := h.do(&h.admin, lanAddr, http.MethodPost, "/firewall/rules", b)
		if res.Status != http.StatusBadRequest || res.Msg == "" {
			t.Errorf("%v: %+v", b, res)
		}
	}
	if len(fw.calls) != 0 {
		t.Errorf("helper was called for invalid input: %q", fw.calls)
	}
}

func TestDisable(t *testing.T) {
	fw := &fakeFW{installed: true, active: true, numbered: activeRows}
	h := newHarness(t, fw)
	res := h.do(&h.admin, lanAddr, http.MethodPost, "/firewall/disable", nil)
	if res.Status != http.StatusOK || !reflect.DeepEqual(fw.actions(), []string{"firewall-disable"}) || fw.active {
		t.Errorf("got %+v calls %q", res, fw.calls)
	}
}

func TestAuthorisation(t *testing.T) {
	fw := &fakeFW{installed: true, added: []string{"ufw allow 22/tcp", "ufw allow 8080/tcp"}}
	h := newHarness(t, fw)
	admin := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/firewall/enable-check", nil},
		{http.MethodPost, "/firewall/enable", enableBody()},
		{http.MethodPost, "/firewall/disable", nil},
		{http.MethodPost, "/firewall/rules", ruleBody("allow", "80", "tcp", "any", "", false)},
		{http.MethodPost, "/firewall/rules/delete", delBody("added", 1, "ufw allow 22/tcp", true)},
	}
	for _, e := range admin {
		if res := h.do(&h.user, lanAddr, e.method, e.path, e.body); res.Status != http.StatusForbidden {
			t.Errorf("ordinary user %s %s: %+v", e.method, e.path, res)
		}
		if res := h.do(nil, lanAddr, e.method, e.path, e.body); res.Status != http.StatusUnauthorized {
			t.Errorf("anonymous %s %s: %+v", e.method, e.path, res)
		}
		if e.method != http.MethodGet {
			noCSRF := login{cookie: h.admin.cookie}
			if res := h.do(&noCSRF, lanAddr, e.method, e.path, e.body); res.Status != http.StatusForbidden || res.Code != "csrf_invalid" {
				t.Errorf("admin without CSRF token %s %s: %+v", e.method, e.path, res)
			}
		}
	}
	if len(fw.calls) != 0 || fw.active {
		t.Fatalf("an unauthorised request reached the helper: %q", fw.calls)
	}
	// Reading is open to every signed-in user, and to nobody else.
	for _, p := range []string{"/firewall/status", "/firewall/suggestions"} {
		if res := h.do(&h.user, lanAddr, http.MethodGet, p, nil); res.Status != http.StatusOK {
			t.Errorf("user GET %s: %+v", p, res)
		}
		if res := h.do(nil, lanAddr, http.MethodGet, p, nil); res.Status != http.StatusUnauthorized {
			t.Errorf("anonymous GET %s: %+v", p, res)
		}
	}
}

func TestStatusResponseShape(t *testing.T) {
	fw := &fakeFW{installed: false}
	h := newHarness(t, fw)
	res := h.do(&h.user, lanAddr, http.MethodGet, "/firewall/status", nil)
	var got map[string]any
	if err := json.Unmarshal(res.Data, &got); err != nil {
		t.Fatal(err)
	}
	if got["installed"] != false || got["active"] != false {
		t.Errorf("got %v", got)
	}
	if rules, ok := got["rules"].([]any); !ok || len(rules) != 0 {
		t.Errorf("rules must be an empty list, got %v", got["rules"])
	}
}

func TestHealth(t *testing.T) {
	cases := []struct {
		fw     *fakeFW
		status module.HealthStatus
	}{
		{&fakeFW{installed: true, active: true}, module.Healthy},
		{&fakeFW{installed: true}, module.WarningLevel},
		{&fakeFW{}, module.Healthy},
		{&fakeFW{stateErr: errors.New("x")}, module.WarningLevel},
	}
	for i, c := range cases {
		m := &Module{readState: c.fw.state, run: c.fw.run}
		got := m.Health(context.Background())
		if len(got) != 1 || got[0].Status != c.status || got[0].Message == "" {
			t.Errorf("case %d: %+v", i, got)
		}
	}
}
