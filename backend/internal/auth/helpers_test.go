package auth

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"myserver/internal/audit"
	"myserver/internal/config"
	"myserver/internal/database"
	"myserver/internal/httpx"
	"myserver/internal/privileged"
	"myserver/internal/settings"
	"myserver/migrations"
)

const (
	testHost     = "panel.test:8080"
	testOrigin   = "http://panel.test:8080"
	testPassword = "dogru-parola-12345"
)

// env is a complete auth stack over a temporary database, routed the same
// way internal/server routes it.
type env struct {
	t     *testing.T
	db    *sql.DB
	cfg   *config.Config
	store *settings.Store
	svc   *Service
	h     http.Handler
	// probes counts how often the state-changing probe handlers ran.
	probeMu sync.Mutex
	probes  int
}

func newEnv(t *testing.T) *env {
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
		DataDir: dir,
		// Deliberately nonexistent: no test may ever reach a real helper.
		HelperPath: filepath.Join(dir, "no-such-helper"),
		DockerHost: "unix://" + filepath.ToSlash(filepath.Join(dir, "no-docker.sock")),
	}
	svc, err := NewService(db, cfg, store, audit.New(db), privileged.New(cfg.HelperPath))
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	e := &env{t: t, db: db, cfg: cfg, store: store, svc: svc}

	mux := http.NewServeMux()
	root := httpx.NewRouter(mux)
	public := root.Group("/api/v1")
	api := root.Group("/api/v1", svc.Require)
	ws := root.Group("/api/v1", svc.RequireWebSocket)
	svc.RegisterPublic(public)
	svc.RegisterPrivate(api)
	probe := func(w http.ResponseWriter, r *http.Request) error {
		if !isSafeMethod(r.Method) {
			e.probeMu.Lock()
			e.probes++
			e.probeMu.Unlock()
		}
		httpx.OK(w, From(r.Context()).User.Username)
		return nil
	}
	for _, m := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"} {
		api.Handle(m, "/probe", probe)
	}
	ws.Raw("GET", "/probe-ws", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.OK(w, From(r.Context()).User.Username)
	}))
	e.h = httpx.Recover(httpx.SecurityHeaders(mux))
	return e
}

func (e *env) probeCount() int {
	e.probeMu.Lock()
	defer e.probeMu.Unlock()
	return e.probes
}

// completeSetup marks setup as done without going through the wizard.
func (e *env) completeSetup() {
	e.t.Helper()
	if err := e.store.Set(context.Background(), settings.KeySetupComplete, "true"); err != nil {
		e.t.Fatalf("set setup flag: %v", err)
	}
}

// addUser inserts a user directly and returns its id.
func (e *env) addUser(username, password, role string) int64 {
	e.t.Helper()
	hash, err := HashPassword(password)
	if err != nil {
		e.t.Fatalf("hash: %v", err)
	}
	res, err := e.db.Exec(`INSERT INTO users (username, password_hash, role, created_at) VALUES (?, ?, ?, ?)`,
		username, hash, role, time.Now().Unix())
	if err != nil {
		e.t.Fatalf("insert user: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(query, args...).Scan(&n); err != nil {
		e.t.Fatalf("count %q: %v", query, err)
	}
	return n
}

// client is one browser: address, cookie and CSRF token.
type client struct {
	ip     string
	cookie *http.Cookie
	csrf   string
	origin string // sent when non-empty
	tls    bool
	noCSRF bool
}

type response struct {
	Status  int
	Header  http.Header
	Raw     string
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Cookies []*http.Cookie
}

func (r *response) code() string {
	if r.Error == nil {
		return ""
	}
	return r.Error.Code
}

func (e *env) do(c *client, method, path string, body any) *response {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			e.t.Fatalf("marshal: %v", err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, "http://"+testHost+path, rd)
	req.Host = testHost
	ip := "192.0.2.10"
	if c != nil && c.ip != "" {
		ip = c.ip
	}
	req.RemoteAddr = ip + ":40000"
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c != nil {
		if c.cookie != nil {
			req.AddCookie(&http.Cookie{Name: c.cookie.Name, Value: c.cookie.Value})
		}
		if c.csrf != "" && !c.noCSRF {
			req.Header.Set(csrfHeader, c.csrf)
		}
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		if c.tls {
			req.TLS = &tls.ConnectionState{}
		}
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	res := &response{Status: rec.Code, Header: rec.Header(), Raw: rec.Body.String(), Cookies: rec.Result().Cookies()}
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(rec.Body.Bytes(), res); err != nil {
			e.t.Fatalf("%s %s: response is not an envelope: %v\n%s", method, path, err, res.Raw)
		}
	}
	return res
}

func sessionCookie(res *response) *http.Cookie {
	for _, c := range res.Cookies {
		if c.Name == cookieName {
			return c
		}
	}
	return nil
}

// login signs in and returns a client carrying the session.
func (e *env) login(username, password, ip string) *client {
	e.t.Helper()
	c := &client{ip: ip}
	res := e.do(c, "POST", "/api/v1/auth/login", map[string]string{"username": username, "password": password})
	if res.Status != http.StatusOK {
		e.t.Fatalf("login %s: status %d body %s", username, res.Status, res.Raw)
	}
	c.cookie = sessionCookie(res)
	if c.cookie == nil || c.cookie.Value == "" {
		e.t.Fatalf("login %s: no session cookie", username)
	}
	var st statusResponse
	if err := json.Unmarshal(res.Data, &st); err != nil {
		e.t.Fatalf("login data: %v", err)
	}
	if st.CSRFToken == "" {
		e.t.Fatalf("login %s: no csrf token", username)
	}
	c.csrf = st.CSRFToken
	return c
}

// session loads the Session object behind a client's cookie.
func (e *env) session(c *client) *Session {
	e.t.Helper()
	req := httptest.NewRequest("GET", "http://"+testHost+"/", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: c.cookie.Value})
	s, err := e.svc.sessionFromRequest(req)
	if err != nil {
		e.t.Fatalf("sessionFromRequest: %v", err)
	}
	return s
}

// alive reports whether the client's session is still accepted.
func (e *env) alive(c *client) bool {
	e.t.Helper()
	res := e.do(c, "GET", "/api/v1/probe", nil)
	switch res.Status {
	case http.StatusOK:
		return true
	case http.StatusUnauthorized:
		return false
	}
	e.t.Fatalf("probe: unexpected status %d body %s", res.Status, res.Raw)
	return false
}

// fakeClock is an adjustable time source for the limiters.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
}

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

func (e *env) useClock() *fakeClock {
	c := newFakeClock()
	e.svc.byIP.now = c.now
	e.svc.byUser.now = c.now
	return c
}
