package docker

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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
	"myserver/internal/logbuf"
	"myserver/internal/module"
	"myserver/internal/notify"
	"myserver/internal/privileged"
	"myserver/internal/settings"
	"myserver/migrations"
)

const (
	testHost   = "panel.test:8080"
	testOrigin = "http://panel.test:8080"

	idWeb   = "1111111111111111111111111111111111111111111111111111111111111111"
	idDB    = "2222222222222222222222222222222222222222222222222222222222222222"
	idAlpha = "3333333333333333333333333333333333333333333333333333333333333333"
)

// creds is one signed-in browser: session cookie and CSRF token.
type creds struct {
	name  string
	token string
	csrf  string
}

// testEnv is the docker module mounted the way internal/server mounts it:
// real router, real auth middleware, real database.
type testEnv struct {
	t      *testing.T
	db     *sql.DB
	mod    *Module
	notify *notify.Center
	h      http.Handler
	mux    *http.ServeMux
	admin  *creds
	user   *creds
}

func newTestEnv(t *testing.T, dockerHost string) *testEnv {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := database.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := settings.NewStore(ctx, db)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	cfg := &config.Config{
		DataDir:    dir,
		HelperPath: filepath.Join(dir, "no-such-helper"),
		DockerHost: dockerHost,
	}
	al := audit.New(db)
	priv := privileged.New(cfg.HelperPath)
	svc, err := auth.NewService(db, cfg, store, al, priv)
	if err != nil {
		t.Fatalf("auth service: %v", err)
	}
	nc := notify.New(db)
	deps := module.Deps{
		Cfg: cfg, DB: db, Settings: store, Audit: al, Notify: nc,
		Logs: logbuf.NewBuffer(16), Priv: priv, Auth: svc,
	}
	mod, err := New(deps, nil)
	if err != nil {
		t.Fatalf("New must not fail: %v", err)
	}
	m, ok := mod.(*Module)
	if !ok {
		t.Fatalf("New returned %T", mod)
	}
	t.Cleanup(m.cancel)

	mux := http.NewServeMux()
	root := httpx.NewRouter(mux)
	api := root.Group("/api/v1", svc.Require)
	ws := root.Group("/api/v1", svc.RequireWebSocket)
	m.Register(api, ws)

	e := &testEnv{t: t, db: db, mod: m, notify: nc, h: httpx.Recover(mux), mux: mux}
	e.admin = e.addSession("yonetici", auth.RoleAdmin)
	e.user = e.addSession("kullanici", auth.RoleUser)
	return e
}

// addSession inserts a user and a valid session for it.
func (e *testEnv) addSession(username, role string) *creds {
	e.t.Helper()
	now := time.Now().Unix()
	res, err := e.db.Exec(`INSERT INTO users (username, password_hash, role, disabled, created_at)
		VALUES (?, 'unused', ?, 0, ?)`, username, role, now)
	if err != nil {
		e.t.Fatalf("insert user: %v", err)
	}
	uid, _ := res.LastInsertId()
	c := &creds{name: username, token: "token-" + username, csrf: "csrf-" + username}
	sum := sha256.Sum256([]byte(c.token))
	_, err = e.db.Exec(`INSERT INTO sessions
		(token_hash, user_id, csrf_token, ip, user_agent, created_at, last_seen_at, expires_at)
		VALUES (?, ?, ?, '192.0.2.1', 'test', ?, ?, ?)`,
		hex.EncodeToString(sum[:]), uid, c.csrf, now, now, now+3600)
	if err != nil {
		e.t.Fatalf("insert session: %v", err)
	}
	return c
}

func isSafe(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func sign(r *http.Request, who *creds) {
	if who == nil {
		return
	}
	r.AddCookie(&http.Cookie{Name: "myserver_session", Value: who.token})
	if !isSafe(r.Method) {
		r.Header.Set("X-CSRF-Token", who.csrf)
	}
}

// do sends one request through the router and waits for the handler.
func (e *testEnv) do(method, path string, who *creds, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, "http://"+testHost+path, rd)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	sign(r, who)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

type envelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func parseEnvelope(t *testing.T, body []byte) envelope {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("response is not an envelope: %v\n%s", err, body)
	}
	return env
}

func errorCode(env envelope) string {
	if env.Error == nil {
		return ""
	}
	return env.Error.Code
}

type auditRow struct {
	Username, Action, Target, Detail string
	Success                          bool
}

func (e *testEnv) auditRows() []auditRow {
	e.t.Helper()
	rows, err := e.db.Query(`SELECT username, action, target, detail, success FROM audit_log ORDER BY id`)
	if err != nil {
		e.t.Fatalf("audit query: %v", err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var a auditRow
		if err := rows.Scan(&a.Username, &a.Action, &a.Target, &a.Detail, &a.Success); err != nil {
			e.t.Fatalf("audit scan: %v", err)
		}
		out = append(out, a)
	}
	return out
}

func (e *testEnv) notifications() []notify.Notification {
	e.t.Helper()
	list, err := e.notify.List(context.Background(), 50, false)
	if err != nil {
		e.t.Fatalf("notifications: %v", err)
	}
	return list
}

/* ---------- fake Docker Engine API ---------- */

var versionPrefixRe = regexp.MustCompile(`^/v1\.[0-9]+`)

// fakeDocker is a minimal Docker Engine API over loopback TCP. Handlers are
// keyed by "METHOD /path" without the API version prefix.
type fakeDocker struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests []string // "METHOD /path?query", pings excluded
	handlers map[string]http.HandlerFunc
}

func newFakeDocker(t *testing.T) *fakeDocker {
	t.Helper()
	f := &fakeDocker{handlers: map[string]http.HandlerFunc{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDocker) host() string {
	return "tcp://" + f.srv.Listener.Addr().String()
}

func (f *fakeDocker) handle(key string, h http.HandlerFunc) {
	f.mu.Lock()
	f.handlers[key] = h
	f.mu.Unlock()
}

func (f *fakeDocker) handleJSON(key string, status int, body string) {
	f.handle(key, func(w http.ResponseWriter, r *http.Request) {
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
	if path == "/_ping" {
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			io.WriteString(w, "OK")
		}
		return
	}
	line := r.Method + " " + path
	if r.URL.RawQuery != "" {
		line += "?" + r.URL.RawQuery
	}
	f.mu.Lock()
	f.requests = append(f.requests, line)
	h := f.handlers[r.Method+" "+path]
	f.mu.Unlock()
	if h == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"message":"No such object"}`)
		return
	}
	h(w, r)
}

func (f *fakeDocker) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

// seenPath reports whether a request with this method and path (query
// ignored) arrived.
func (f *fakeDocker) seenPath(method, path string) (string, bool) {
	for _, line := range f.seen() {
		rest, ok := strings.CutPrefix(line, method+" ")
		if !ok {
			continue
		}
		p, _, _ := strings.Cut(rest, "?")
		if p == path {
			return line, true
		}
	}
	return "", false
}

// refusedTCPHost returns a loopback address on which nothing listens.
func refusedTCPHost(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	return "tcp://" + addr
}

/* ---------- SSE parsing ---------- */

type sseEvent struct {
	Name string
	Data string
}

// parseSSE parses a complete SSE body; comments are skipped.
func parseSSE(body string) []sseEvent {
	var out []sseEvent
	for _, block := range strings.Split(body, "\n\n") {
		var ev sseEvent
		for _, line := range strings.Split(block, "\n") {
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				ev.Name = v
			} else if v, ok := strings.CutPrefix(line, "data: "); ok {
				ev.Data = v
			}
		}
		if ev.Name != "" {
			out = append(out, ev)
		}
	}
	return out
}

// readSSEEvent reads a live stream until the first event with this name.
func readSSEEvent(t *testing.T, r io.Reader, name string) sseEvent {
	t.Helper()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var ev sseEvent
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if ev.Name == name {
				return ev
			}
			ev = sseEvent{}
		case strings.HasPrefix(line, "event: "):
			ev.Name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			ev.Data = strings.TrimPrefix(line, "data: ")
		}
	}
	t.Fatalf("stream ended before event %q: %v", name, sc.Err())
	return ev
}
