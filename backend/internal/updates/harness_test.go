package updates

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
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
	"myserver/internal/notify"
	"myserver/internal/privileged"
	"myserver/internal/settings"
	"myserver/migrations"
)

// The module is mounted the way internal/server mounts it: real router,
// real auth middleware, real database. The privileged runner is the real
// one, pointed at a recording shell script instead of the root helper, and
// systemctl is a script as well. Nothing on the host is upgraded, restarted
// or queried, and no test needs the network.

const (
	adminToken = "admin-session-token"
	adminCSRF  = "admin-csrf-token"
	userToken  = "user-session-token"
	userCSRF   = "user-csrf-token"

	testJobID = "0123456789abcdef"
)

const fakeHelperScript = `#!/bin/sh
{
	for a in "$@"; do printf '%s\n' "$a"; done
	printf '%s\n' '--end--'
} >> '@DIR@/helper.log'
if [ -f '@DIR@/'"$1"'.err' ]; then
	printf 'MYSERVER_ERROR: %s\n' "$(cat '@DIR@/'"$1"'.err')" >&2
	exit 3
fi
if [ -f '@DIR@/'"$1"'.crash' ]; then
	echo "panic: internal detail /etc/secret" >&2
	exit 1
fi
case "$1" in
updates-apt-list)
	[ -f '@DIR@/list.out' ] && cat '@DIR@/list.out'
	;;
updates-apt-upgrade)
	printf 'id=%s\nstate=running\nstarted=100\nfinished=0\nmessage=\n' "$4" > '@DIR@/state/apt-upgrade.result'
	: > '@DIR@/state/apt-upgrade.log'
	;;
esac
exit 0
`

const fakeSystemctlScript = `#!/bin/sh
printf '%s\n' "$*" >> '@DIR@/systemctl.log'
for a in "$@"; do unit="$a"; done
case "$1" in
is-active)
	if grep -qx "$unit" '@DIR@/units-active' 2>/dev/null; then echo active; exit 0; fi
	if grep -qx "$unit" '@DIR@/units-activating' 2>/dev/null; then echo activating; exit 3; fi
	if grep -qx "$unit" '@DIR@/units-unknown' 2>/dev/null; then echo inactive; exit 4; fi
	echo inactive
	exit 3
	;;
show)
	if [ -f '@DIR@/show.txt' ]; then cat '@DIR@/show.txt'; exit 0; fi
	printf 'LoadState=not-found\nActiveState=inactive\nResult=success\nExecMainCode=0\nExecMainStatus=0\n'
	exit 0
	;;
esac
echo "unexpected systemctl call: $*" >> '@DIR@/systemctl.unexpected'
exit 1
`

type testEnv struct {
	t       *testing.T
	dir     string
	mod     *Module
	deps    module.Deps
	api     *settings.API
	handler http.Handler
	db      *sql.DB
}

type envOptions struct {
	dockerHost string
	// before runs after the database and the files exist and before New.
	before func(e *testEnv)
}

func newEnv(t *testing.T, opts ...envOptions) *testEnv {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root: the privileged runner only executes the helper directly as root")
	}
	var opt envOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	dir := t.TempDir()
	ctx := context.Background()
	e := &testEnv{t: t, dir: dir}

	db, err := database.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	e.db = db
	now := time.Now().Unix()
	for _, u := range []struct {
		id                int
		name, role        string
		token, csrf, addr string
	}{
		{1, "yonetici", auth.RoleAdmin, adminToken, adminCSRF, "192.0.2.1"},
		{2, "kullanici", auth.RoleUser, userToken, userCSRF, "192.0.2.1"},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO users (id, username, password_hash, role, disabled, created_at)
			VALUES (?, ?, 'x', ?, 0, ?)`, u.id, u.name, u.role, now); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(u.token))
		if _, err := db.ExecContext(ctx, `INSERT INTO sessions
			(token_hash, user_id, csrf_token, ip, user_agent, created_at, last_seen_at, expires_at)
			VALUES (?, ?, ?, ?, 'test', ?, ?, ?)`,
			hex.EncodeToString(sum[:]), u.id, u.csrf, u.addr, now, now, now+3600); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.Mkdir(filepath.Join(dir, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	helperPath := filepath.Join(dir, "fake-helper")
	e.write("fake-helper", strings.ReplaceAll(fakeHelperScript, "@DIR@", dir), 0o755)
	e.write("fake-systemctl", strings.ReplaceAll(fakeSystemctlScript, "@DIR@", dir), 0o755)

	oldCtl, oldLog, oldRes, oldFlag, oldPkgs := systemctlBin, aptLogFile, aptResultFile, rebootFlag, rebootPkgsFile
	oldTail, oldResult, oldActive := aptTailPeriod, aptResultPeriod, aptActivePeriod
	oldAPI, oldTransport, oldTimeout, oldVersion := githubAPI, releaseTransport, releaseTimeout, config.Version
	systemctlBin = filepath.Join(dir, "fake-systemctl")
	aptLogFile = filepath.Join(dir, "state", "apt-upgrade.log")
	aptResultFile = filepath.Join(dir, "state", "apt-upgrade.result")
	rebootFlag = filepath.Join(dir, "reboot-required")
	rebootPkgsFile = filepath.Join(dir, "reboot-required.pkgs")
	aptTailPeriod, aptResultPeriod, aptActivePeriod = 2*time.Millisecond, time.Millisecond, time.Millisecond
	// Unless a test installs its own server, nothing may be reachable.
	githubAPI = "https://127.0.0.1:1"
	config.Version = "1.2.0"
	t.Cleanup(func() {
		systemctlBin, aptLogFile, aptResultFile, rebootFlag, rebootPkgsFile = oldCtl, oldLog, oldRes, oldFlag, oldPkgs
		aptTailPeriod, aptResultPeriod, aptActivePeriod = oldTail, oldResult, oldActive
		githubAPI, releaseTransport, releaseTimeout, config.Version = oldAPI, oldTransport, oldTimeout, oldVersion
	})

	store, err := settings.NewStore(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	al := audit.New(db)
	priv := privileged.New(helperPath)
	if opt.dockerHost == "" {
		opt.dockerHost = refusedTCPHost(t)
	}
	cfg := &config.Config{DataDir: dir, HelperPath: helperPath, DockerHost: opt.dockerHost}
	authSvc, err := auth.NewService(db, cfg, store, al, priv)
	if err != nil {
		t.Fatal(err)
	}
	e.deps = module.Deps{
		Cfg: cfg, DB: db, Settings: store, Audit: al,
		Notify: notify.New(db), Priv: priv, Auth: authSvc,
	}
	if opt.before != nil {
		opt.before(e)
	}
	e.api = settings.NewAPI(store, al, priv, auth.ActorFrom)
	mod, err := New(e.deps, e.api)
	if err != nil {
		t.Fatalf("New must not fail: %v", err)
	}
	e.mod = mod.(*Module)

	mux := http.NewServeMux()
	root := httpx.NewRouter(mux)
	api := root.Group("/api/v1", authSvc.Require)
	ws := root.Group("/api/v1", authSvc.RequireWebSocket)
	mod.Register(api, ws)
	e.api.Register(api, auth.RequireAdmin)
	e.handler = mux
	return e
}

func (e *testEnv) path(name string) string { return filepath.Join(e.dir, name) }

func (e *testEnv) write(name, content string, mode os.FileMode) {
	e.t.Helper()
	if err := os.WriteFile(e.path(name), []byte(content), mode); err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) set(key, value string) {
	e.t.Helper()
	if err := e.deps.Settings.Set(context.Background(), key, value); err != nil {
		e.t.Fatal(err)
	}
}

type response struct {
	Status int
	Body   string
	Env    struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
		Error   *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
}

func (r response) code() string {
	if r.Env.Error == nil {
		return ""
	}
	return r.Env.Error.Code
}

func (r response) message() string {
	if r.Env.Error == nil {
		return ""
	}
	return r.Env.Error.Message
}

func (r response) into(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Env.Data, v); err != nil {
		t.Fatalf("response data: %v\n%s", err, r.Body)
	}
}

// do sends a request as "admin", "user" or "" (no session).
func (e *testEnv) do(method, path, body, who string, headers ...string) response {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/api/v1"+path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	switch who {
	case "admin":
		req.AddCookie(&http.Cookie{Name: "myserver_session", Value: adminToken})
		req.Header.Set("X-CSRF-Token", adminCSRF)
	case "user":
		req.AddCookie(&http.Cookie{Name: "myserver_session", Value: userToken})
		req.Header.Set("X-CSRF-Token", userCSRF)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	out := response{Status: rec.Code, Body: rec.Body.String()}
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(rec.Body.Bytes(), &out.Env); err != nil {
			e.t.Fatalf("%s %s: response is not an envelope: %v\n%s", method, path, err, out.Body)
		}
	}
	return out
}

// helperCalls returns every invocation of the fake helper, action first.
func (e *testEnv) helperCalls() [][]string {
	e.t.Helper()
	b, err := os.ReadFile(e.path("helper.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		e.t.Fatal(err)
	}
	var calls [][]string
	var cur []string
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if line == "--end--" {
			calls = append(calls, cur)
			cur = nil
			continue
		}
		cur = append(cur, line)
	}
	return calls
}

func (e *testEnv) helperActions() []string {
	out := []string{}
	for _, c := range e.helperCalls() {
		out = append(out, c[0])
	}
	return out
}

func (e *testEnv) wantNoHelperCalls(what string) {
	e.t.Helper()
	if calls := e.helperCalls(); len(calls) != 0 {
		e.t.Errorf("%s: the privileged runner was reached: %q", what, calls)
	}
}

func (e *testEnv) systemctlCalls() []string {
	e.t.Helper()
	if b, err := os.ReadFile(e.path("systemctl.unexpected")); err == nil {
		e.t.Errorf("unexpected systemctl calls: %s", b)
	}
	b, err := os.ReadFile(e.path("systemctl.log"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

type auditRow struct {
	Username, Action, Target, Detail string
	Success                          bool
}

func (e *testEnv) auditRows() []auditRow {
	e.t.Helper()
	rows, err := e.db.Query(`SELECT username, action, target, detail, success FROM audit_log ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		if err := rows.Scan(&r.Username, &r.Action, &r.Target, &r.Detail, &r.Success); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// notifications returns the stored notifications, oldest first.
func (e *testEnv) notifications() []notify.Notification {
	e.t.Helper()
	list, err := e.deps.Notify.List(context.Background(), 200, false)
	if err != nil {
		e.t.Fatal(err)
	}
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}
	return list
}

func (e *testEnv) notificationTitles() []string {
	out := []string{}
	for _, n := range e.notifications() {
		out = append(out, string(n.Severity)+" "+n.Title)
	}
	return out
}

// waitFor polls a condition that another goroutine brings about. It is
// used for synchronisation only; no assertion depends on how long it takes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitIdle waits until the goroutine that followed an apt job has finished
// all of its work.
func (e *testEnv) waitIdle(job *Job) {
	e.t.Helper()
	waitFor(e.t, "the job to finish", func() bool { return job.Meta().Status != jobRunning })
	waitFor(e.t, "the follow-up package listing", func() bool {
		n := 0
		for _, a := range e.helperActions() {
			if a == "updates-apt-list" {
				n++
			}
		}
		e.mod.mu.RLock()
		busy := e.mod.aptChecking
		e.mod.mu.RUnlock()
		return n > 0 && !busy
	})
}

func refusedTCPHost(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return "tcp://" + addr
}

/* ---------- fake Docker Engine API ---------- */

var versionPrefixRe = regexp.MustCompile(`^/v1\.[0-9]+`)

// fakeDocker implements only the endpoints the module calls. Any other
// request is recorded as unexpected and fails the test.
type fakeDocker struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	requests   []string
	unexpected []string
	handlers   map[string]http.HandlerFunc
}

func newFakeDocker(t *testing.T) *fakeDocker {
	t.Helper()
	f := &fakeDocker{t: t, handlers: map[string]http.HandlerFunc{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(func() {
		f.srv.Close()
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.unexpected) > 0 {
			t.Errorf("unexpected Docker API requests: %q", f.unexpected)
		}
	})
	return f
}

func (f *fakeDocker) host() string { return "tcp://" + f.srv.Listener.Addr().String() }

func (f *fakeDocker) handle(key string, h http.HandlerFunc) {
	f.mu.Lock()
	f.handlers[key] = h
	f.mu.Unlock()
}

func (f *fakeDocker) json(key string, status int, body string) {
	f.handle(key, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	})
}

func (f *fakeDocker) serve(w http.ResponseWriter, r *http.Request) {
	path := versionPrefixRe.ReplaceAllString(r.URL.Path, "")
	w.Header().Set("Api-Version", "1.47")
	w.Header().Set("Ostype", "linux")
	w.Header().Set("Server", "Docker/27.3.1 (linux)")
	if path == "/_ping" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			io.WriteString(w, "OK")
		}
		return
	}
	line := r.Method + " " + path
	f.mu.Lock()
	f.requests = append(f.requests, line)
	h := f.handlers[line]
	if h == nil {
		f.unexpected = append(f.unexpected, line+"?"+r.URL.RawQuery)
	}
	f.mu.Unlock()
	if h == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		io.WriteString(w, `{"message":"not implemented by the fake"}`)
		return
	}
	h(w, r)
}

func (f *fakeDocker) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

/* ---------- SSE parsing ---------- */

type sseEvent struct {
	ID, Name, Data string
}

// parseSSEStrict parses a complete SSE body and fails the test on anything
// that is not a well-formed event or a comment.
func parseSSEStrict(t *testing.T, body string) []sseEvent {
	t.Helper()
	var out []sseEvent
	if !strings.HasSuffix(body, "\n\n") {
		t.Errorf("stream does not end with an event terminator: %q", body[max(0, len(body)-40):])
	}
	for _, block := range strings.Split(strings.TrimSuffix(body, "\n\n"), "\n\n") {
		if block == ": ping" {
			continue
		}
		var ev sseEvent
		fields := map[string]int{}
		for _, line := range strings.Split(block, "\n") {
			k, v, ok := strings.Cut(line, ": ")
			if !ok {
				t.Errorf("malformed SSE line %q in block %q", line, block)
				continue
			}
			fields[k]++
			switch k {
			case "id":
				ev.ID = v
			case "event":
				ev.Name = v
			case "data":
				ev.Data = v
			default:
				t.Errorf("unexpected SSE field %q in block %q", k, block)
			}
		}
		if fields["event"] != 1 || fields["data"] != 1 || fields["id"] > 1 {
			t.Errorf("block does not hold exactly one event: %q", block)
		}
		if !json.Valid([]byte(ev.Data)) {
			t.Errorf("event data is not JSON: %q", ev.Data)
		}
		out = append(out, ev)
	}
	return out
}
