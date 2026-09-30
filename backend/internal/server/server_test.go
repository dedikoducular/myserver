package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
	testHost     = "panel.test:8080"
	testPassword = "dogru-parola-12345"
	leak         = "S3CRET-dsn-password=hunter2"
	internalBody = `{"success":false,"data":null,"error":{"code":"internal_error","message":"Beklenmeyen bir sunucu hatası oluştu."}}` + "\n"
)

// ---- test modules ----

type plainModule struct {
	name     string
	register func(api, ws *httpx.Router)
}

func (m *plainModule) Name() string                   { return m.name }
func (m *plainModule) Register(api, ws *httpx.Router) { m.register(api, ws) }

type fullModule struct {
	plainModule
	start  func(ctx context.Context)
	health func(ctx context.Context) []module.HealthCheck
}

func (m *fullModule) Start(ctx context.Context) { m.start(ctx) }
func (m *fullModule) Health(ctx context.Context) []module.HealthCheck {
	return m.health(ctx)
}

func ping(name string) func(api, ws *httpx.Router) {
	return func(api, _ *httpx.Router) {
		api.Group("/"+name).Get("/ping", func(w http.ResponseWriter, _ *http.Request) error {
			httpx.OK(w, name)
			return nil
		})
	}
}

// ---- environment ----

type env struct {
	t    *testing.T
	db   *sql.DB
	deps module.Deps
	srv  *Server
	h    http.Handler
	logs *bytes.Buffer
}

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func newEnv(t *testing.T, modules ...module.Module) *env {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := database.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	store, err := settings.NewStore(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		DataDir:    dir,
		HelperPath: filepath.Join(dir, "no-such-helper"), // never a real helper
		DockerHost: "unix://" + filepath.ToSlash(filepath.Join(dir, "no-docker.sock")),
	}
	al := audit.New(db)
	priv := privileged.New(cfg.HelperPath)
	authSvc, err := auth.NewService(db, cfg, store, al, priv)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	buf := logbuf.NewBuffer(200)
	saved := slog.Default()
	slog.SetDefault(logbuf.New(&syncWriter{w: &out}, "info", buf))
	t.Cleanup(func() { slog.SetDefault(saved) })

	deps := module.Deps{Cfg: cfg, DB: db, Settings: store, Audit: al, Notify: notify.New(db), Logs: buf, Priv: priv, Auth: authSvc}
	srv := New(deps, settings.NewAPI(store, al, priv, auth.ActorFrom), modules)
	e := &env{t: t, db: db, deps: deps, srv: srv, logs: &out}
	e.h = srv.Handler()

	if err := store.Set(ctx, settings.KeySetupComplete, "true"); err != nil {
		t.Fatal(err)
	}
	e.addUser("admin", auth.RoleAdmin)
	e.addUser("calisan", auth.RoleUser)
	return e
}

func (e *env) addUser(name, role string) {
	e.t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO users (username, password_hash, role, created_at) VALUES (?, ?, ?, ?)`,
		name, hash, role, time.Now().Unix()); err != nil {
		e.t.Fatal(err)
	}
}

type client struct {
	cookie *http.Cookie
	csrf   string
}

type response struct {
	Status int
	Header http.Header
	Raw    string
	Data   json.RawMessage
	Code   string
}

func (e *env) do(c *client, method, path, body string) *response {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "http://"+testHost+path, rd)
	req.Host = testHost
	req.RemoteAddr = "192.0.2.10:40000"
	if c != nil {
		req.AddCookie(&http.Cookie{Name: c.cookie.Name, Value: c.cookie.Value})
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	res := &response{Status: rec.Code, Header: rec.Header(), Raw: rec.Body.String()}
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		var env struct {
			Success *bool           `json:"success"`
			Data    json.RawMessage `json:"data"`
			Error   *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Success == nil {
			e.t.Fatalf("%s %s: JSON response is not an envelope: %s", method, path, res.Raw)
		}
		if *env.Success != (env.Error == nil) {
			e.t.Fatalf("%s %s: success and error disagree: %s", method, path, res.Raw)
		}
		res.Data = env.Data
		if env.Error != nil {
			res.Code = env.Error.Code
			if env.Error.Message == "" {
				e.t.Fatalf("%s %s: error without a message: %s", method, path, res.Raw)
			}
		}
	}
	return res
}

func (e *env) login(user string) *client {
	e.t.Helper()
	res := e.do(nil, "POST", "/api/v1/auth/login", `{"username":"`+user+`","password":"`+testPassword+`"}`)
	if res.Status != 200 {
		e.t.Fatalf("login %s: %d %s", user, res.Status, res.Raw)
	}
	c := &client{}
	for _, part := range strings.Split(res.Header.Get("Set-Cookie"), ";") {
		if k, v, ok := strings.Cut(strings.TrimSpace(part), "="); ok && k == "myserver_session" {
			c.cookie = &http.Cookie{Name: k, Value: v}
		}
	}
	var st struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(res.Data, &st); err != nil || st.CSRF == "" || c.cookie == nil {
		e.t.Fatalf("login %s: no session in %s", user, res.Raw)
	}
	c.csrf = st.CSRF
	return c
}

// ---- authorisation ----

func TestAdminOnlyEndpoints(t *testing.T) {
	e := newEnv(t)
	e.deps.Notify.Publish(context.Background(), notify.Info, "test", "Bildirim", "")
	admin, user := e.login("admin"), e.login("calisan")
	endpoints := []struct{ method, path, body string }{
		{"GET", "/api/v1/settings", ""},
		{"PUT", "/api/v1/settings", `{"security.session_hours":"720"}`},
		{"GET", "/api/v1/settings/timezones", ""},
		{"GET", "/api/v1/audit", ""},
		{"GET", "/api/v1/logs", ""},
		{"GET", "/api/v1/auth/users", ""},
		{"POST", "/api/v1/auth/users", `{"username":"sizan","password":"` + testPassword + `","role":"admin"}`},
		{"DELETE", "/api/v1/notifications/1", ""},
		{"DELETE", "/api/v1/notifications", ""},
	}
	const forbidden = `{"success":false,"data":null,"error":{"code":"forbidden","message":"Yetkiniz bulunmuyor."}}` + "\n"
	const unauthorized = `{"success":false,"data":null,"error":{"code":"unauthorized","message":"Oturum açmanız gerekiyor."}}` + "\n"
	for _, ep := range endpoints {
		if res := e.do(user, ep.method, ep.path, ep.body); res.Status != http.StatusForbidden || res.Raw != forbidden {
			t.Errorf("%s %s as a normal user: %d %s", ep.method, ep.path, res.Status, res.Raw)
		}
		if res := e.do(nil, ep.method, ep.path, ep.body); res.Status != http.StatusUnauthorized || res.Raw != unauthorized {
			t.Errorf("%s %s without a session: %d %s", ep.method, ep.path, res.Status, res.Raw)
		}
	}
	// Nothing happened.
	if got := e.deps.Settings.Int(settings.KeySessionHours, 0); got != 12 {
		t.Errorf("a normal user changed a setting: %d", got)
	}
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil || n != 2 {
		t.Errorf("users: %d", n)
	}
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM notifications`).Scan(&n); err != nil || n != 1 {
		t.Errorf("a normal user deleted notifications: %d left", n)
	}
	// The same requests succeed for an administrator.
	for _, ep := range endpoints {
		res := e.do(admin, ep.method, ep.path, ep.body)
		if res.Status != 200 && res.Status != 201 {
			t.Errorf("%s %s as admin: %d %s", ep.method, ep.path, res.Status, res.Raw)
		}
	}
	// What every signed-in user may do.
	for _, p := range []string{"/api/v1/notifications", "/api/v1/health", "/api/v1/modules", "/api/v1/auth/sessions"} {
		if res := e.do(user, "GET", p, ""); res.Status != 200 {
			t.Errorf("GET %s as a normal user: %d", p, res.Status)
		}
		if res := e.do(nil, "GET", p, ""); res.Status != http.StatusUnauthorized {
			t.Errorf("GET %s without a session: %d", p, res.Status)
		}
	}
}

func TestCSRFOnCoreEndpoints(t *testing.T) {
	e := newEnv(t)
	e.deps.Notify.Publish(context.Background(), notify.Info, "test", "Bildirim", "")
	admin := e.login("admin")
	other := e.login("admin")
	for _, tok := range []string{"", other.csrf, "x"} {
		forged := &client{cookie: admin.cookie, csrf: tok}
		for _, ep := range []struct{ method, path, body string }{
			{"PUT", "/api/v1/settings", `{"security.session_hours":"720"}`},
			{"DELETE", "/api/v1/notifications", ""},
			{"DELETE", "/api/v1/notifications/1", ""},
			{"POST", "/api/v1/notifications/read-all", ""},
			{"POST", "/api/v1/notifications/1/read", ""},
		} {
			if res := e.do(forged, ep.method, ep.path, ep.body); res.Status != http.StatusForbidden || res.Code != "csrf_invalid" {
				t.Errorf("%s %s with token %q: %d %s", ep.method, ep.path, tok, res.Status, res.Raw)
			}
		}
	}
	if got := e.deps.Settings.Int(settings.KeySessionHours, 0); got != 12 {
		t.Errorf("setting changed by a forged request: %d", got)
	}
	if n, _ := e.deps.Notify.UnreadCount(context.Background()); n != 1 {
		t.Errorf("notifications changed by a forged request: %d unread", n)
	}
}

// ---- error containment ----

func leakyModule() module.Module {
	return &plainModule{name: "leaky", register: func(api, _ *httpx.Router) {
		g := api.Group("/leaky")
		g.Get("/raw", func(http.ResponseWriter, *http.Request) error {
			return errors.New(leak + " in /var/lib/myserver/myserver.db")
		})
		g.Get("/wrapped", func(http.ResponseWriter, *http.Request) error {
			return httpx.NewError(http.StatusBadGateway, "mount_failed", "Disk bağlanamadı.").Wrap(errors.New(leak))
		})
		g.Get("/panic", func(http.ResponseWriter, *http.Request) error { panic(leak) })
		g.Post("/panic", func(http.ResponseWriter, *http.Request) error { panic(errors.New(leak)) })
		g.Get("/nil", func(http.ResponseWriter, *http.Request) error {
			var s *Server
			_ = s.deps.Cfg.DataDir // nil pointer dereference
			return nil
		})
		g.Get("/ok", func(w http.ResponseWriter, _ *http.Request) error { httpx.OK(w, "iyi"); return nil })
	}}
}

func TestInternalErrorsNeverReachTheClient(t *testing.T) {
	e := newEnv(t, leakyModule())
	c := e.login("admin")
	cases := []struct{ method, path, want string }{
		{"GET", "/api/v1/leaky/raw", internalBody},
		{"GET", "/api/v1/leaky/panic", internalBody},
		{"POST", "/api/v1/leaky/panic", internalBody},
		{"GET", "/api/v1/leaky/nil", internalBody},
		{"GET", "/api/v1/leaky/wrapped", `{"success":false,"data":null,"error":{"code":"mount_failed","message":"Disk bağlanamadı."}}` + "\n"},
	}
	for round := 0; round < 2; round++ {
		for _, tc := range cases {
			res := e.do(c, tc.method, tc.path, "")
			if res.Raw != tc.want {
				t.Errorf("%s %s: body %q", tc.method, tc.path, res.Raw)
			}
			if res.Status < 500 {
				t.Errorf("%s %s: status %d", tc.method, tc.path, res.Status)
			}
			for _, s := range []string{"S3CRET", "hunter2", "goroutine", ".go:", "runtime.", "nil pointer", "/var/lib", "panic"} {
				if strings.Contains(res.Raw, s) {
					t.Errorf("%s %s: response contains %q", tc.method, tc.path, s)
				}
			}
			for k, v := range res.Header {
				if strings.Contains(strings.Join(v, " "), "S3CRET") {
					t.Errorf("%s %s: header %s contains the internal error", tc.method, tc.path, k)
				}
			}
			// The panel keeps serving: the same module, the core and the UI.
			if ok := e.do(c, "GET", "/api/v1/leaky/ok", ""); ok.Status != 200 || ok.Raw != `{"success":true,"data":"iyi","error":null}`+"\n" {
				t.Fatalf("after %s %s the module answered %d %s", tc.method, tc.path, ok.Status, ok.Raw)
			}
			if ok := e.do(c, "GET", "/api/v1/health", ""); ok.Status != 200 {
				t.Fatalf("after %s %s health answered %d", tc.method, tc.path, ok.Status)
			}
		}
	}
	// The cause is kept for the administrator's log...
	if !strings.Contains(e.logs.String(), "S3CRET") {
		t.Error("the internal cause was not logged at all")
	}
	// ...but the log view of the panel never shows stack traces.
	logs := e.do(c, "GET", "/api/v1/logs?limit=500", "")
	if logs.Status != 200 {
		t.Fatalf("logs: %d", logs.Status)
	}
	if strings.Contains(logs.Raw, "goroutine ") || strings.Contains(logs.Raw, "runtime/debug") {
		t.Error("stack trace exposed by the log endpoint")
	}
}

func TestLogEndpointIsRedacted(t *testing.T) {
	e := newEnv(t)
	c := e.login("admin")
	slog.Info("deneme", "password", "hunter2-parola", "csrf_token", "hunter2-csrf", "cookie", "hunter2-cookie", "user", "ali")
	slog.Warn("deneme", slog.Group("request", slog.String("authorization", "hunter2-bearer")))
	res := e.do(c, "GET", "/api/v1/logs", "")
	if res.Status != 200 || !strings.Contains(res.Raw, `"deneme"`) {
		t.Fatalf("logs: %d %s", res.Status, res.Raw)
	}
	if strings.Contains(res.Raw, "hunter2") {
		t.Fatalf("secret value in the log endpoint: %s", res.Raw)
	}
	// Session secrets of real requests are not in the log either.
	for _, s := range []string{c.cookie.Value, c.csrf, testPassword} {
		if strings.Contains(res.Raw, s) || strings.Contains(e.logs.String(), s) {
			t.Fatal("a session token, CSRF token or password was logged")
		}
	}
	for _, q := range []string{"?limit=0", "?limit=-1", "?limit=100000", "?limit=abc", "?limit=1"} {
		if r := e.do(c, "GET", "/api/v1/logs"+q, ""); r.Status != 200 || !strings.HasPrefix(string(r.Data), "[") {
			t.Errorf("logs%s: %d %s", q, r.Status, r.Raw)
		}
	}
}

func TestLoginAttemptPasswordIsNotLogged(t *testing.T) {
	e := newEnv(t)
	const attempt = "denenen-gizli-parola-987"
	e.do(nil, "POST", "/api/v1/auth/login", `{"username":"admin","password":"`+attempt+`"}`)
	e.do(nil, "POST", "/api/v1/auth/login", `{"username":"admin","password":"`+attempt+`","x":1}`)
	e.do(nil, "POST", "/api/v1/auth/login", `{"username":"admin","password":"`+attempt+`"`)
	e.do(nil, "POST", "/api/v1/auth/login", `{"username":"admin","password":["`+attempt+`"]}`)
	if strings.Contains(e.logs.String(), attempt) {
		t.Fatalf("a submitted password was written to the log:\n%s", e.logs.String())
	}
	b, _ := json.Marshal(e.deps.Logs.Recent(0))
	if strings.Contains(string(b), attempt) {
		t.Fatal("a submitted password is in the log buffer")
	}
}

// ---- module isolation ----

func TestModuleIsolation(t *testing.T) {
	started := make(chan string, 8)
	modules := []module.Module{
		&plainModule{name: "good1", register: ping("good1")},
		&plainModule{name: "broken", register: func(api, _ *httpx.Router) {
			panic(leak)
		}},
		&plainModule{name: "nilrouter", register: func(api, _ *httpx.Router) {
			var m map[string]int
			m["x"] = 1
		}},
		&plainModule{name: "duplicate", register: func(api, _ *httpx.Router) {
			// Same method and path as good1.
			api.Group("/good1").Get("/ping", func(w http.ResponseWriter, _ *http.Request) error {
				httpx.OK(w, "hijacked")
				return nil
			})
		}},
		&plainModule{name: "dupcore", register: func(api, _ *httpx.Router) {
			api.Get("/health", func(w http.ResponseWriter, _ *http.Request) error {
				httpx.OK(w, "hijacked")
				return nil
			})
		}},
		&fullModule{plainModule: plainModule{name: "badstart", register: ping("badstart")},
			start:  func(context.Context) { started <- "badstart"; panic(leak) },
			health: func(context.Context) []module.HealthCheck { return nil }},
		&plainModule{name: "good2", register: ping("good2")},
	}
	e := newEnv(t, modules...)
	c := e.login("calisan")

	for _, name := range []string{"good1", "good2", "badstart"} {
		res := e.do(c, "GET", "/api/v1/"+name+"/ping", "")
		if res.Status != 200 || string(res.Data) != `"`+name+`"` {
			t.Errorf("module %s: %d %s", name, res.Status, res.Raw)
		}
	}
	if res := e.do(c, "GET", "/api/v1/health", ""); res.Status != 200 || strings.Contains(res.Raw, "hijacked") {
		t.Errorf("core route replaced by a module: %d %s", res.Status, res.Raw)
	}

	want := map[string]bool{"good1": true, "broken": false, "nilrouter": false, "duplicate": false, "dupcore": false, "badstart": true, "good2": true}
	res := e.do(c, "GET", "/api/v1/modules", "")
	var states []ModuleState
	if err := json.Unmarshal(res.Data, &states); err != nil {
		t.Fatalf("modules: %v %s", err, res.Raw)
	}
	if len(states) != len(want) {
		t.Fatalf("%d module states, want %d: %s", len(states), len(want), res.Raw)
	}
	for i, st := range states {
		if st.Name != modules[i].Name() {
			t.Errorf("state %d is for %q, want %q", i, st.Name, modules[i].Name())
		}
		if st.Loaded != want[st.Name] {
			t.Errorf("module %s: loaded = %v, want %v", st.Name, st.Loaded, want[st.Name])
		}
		if !st.Loaded && st.Error == "" {
			t.Errorf("module %s: failed without a message", st.Name)
		}
		if st.Loaded && st.Error != "" {
			t.Errorf("module %s: loaded with error %q", st.Name, st.Error)
		}
	}
	if strings.Contains(res.Raw, "S3CRET") || strings.Contains(res.Raw, "panic") || strings.Contains(res.Raw, "pattern") {
		t.Errorf("module states expose internals: %s", res.Raw)
	}

	// Failed modules appear as warnings in the health report.
	rep := e.srv.Health(context.Background())
	if rep.Status != module.WarningLevel {
		t.Errorf("health status %s with failed modules, want WARNING", rep.Status)
	}
	for name, loaded := range want {
		found := false
		for _, ch := range rep.Checks {
			if ch.ID == "module."+name {
				found = true
			}
		}
		if found == loaded {
			t.Errorf("module %s: loaded=%v but health warning present=%v", name, loaded, found)
		}
	}

	// Background work: only loaded modules are started, and a panic in
	// Start stays inside the supervisor.
	if e.srv.loaded("broken") || !e.srv.loaded("badstart") || e.srv.loaded("unknown") {
		t.Error("loaded() disagrees with the module states")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.srv.supervise(ctx, "badstart", modules[5].(module.Starter).Start)
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("Start was not called")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the supervisor did not stop after cancellation")
	}
	if res := e.do(c, "GET", "/api/v1/good1/ping", ""); res.Status != 200 {
		t.Errorf("after the Start panic: %d", res.Status)
	}
}

func TestSuperviseReturnsWhenWorkEnds(t *testing.T) {
	e := newEnv(t)
	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.srv.supervise(context.Background(), "x", func(context.Context) { calls.Add(1) })
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the supervisor restarts work that ended normally")
	}
	if calls.Load() != 1 {
		t.Fatalf("work ran %d times", calls.Load())
	}
}

// ---- health ----

func healthModule(name string, fn func(context.Context) []module.HealthCheck) module.Module {
	return &fullModule{plainModule: plainModule{name: name, register: ping(name)},
		start: func(context.Context) {}, health: fn}
}

func check(id string, st module.HealthStatus) module.HealthCheck {
	return module.HealthCheck{ID: id, Name: id, Status: st, Message: "m"}
}

func TestHealthAggregation(t *testing.T) {
	cases := []struct {
		name    string
		modules []module.Module
		want    module.HealthStatus
		checks  int
	}{
		{"no modules", nil, module.Healthy, 0},
		{"all healthy", []module.Module{
			healthModule("a", func(context.Context) []module.HealthCheck { return []module.HealthCheck{check("a.1", module.Healthy)} }),
			healthModule("b", func(context.Context) []module.HealthCheck { return nil }),
		}, module.Healthy, 1},
		{"one warning", []module.Module{
			healthModule("a", func(context.Context) []module.HealthCheck { return []module.HealthCheck{check("a.1", module.Healthy)} }),
			healthModule("b", func(context.Context) []module.HealthCheck {
				return []module.HealthCheck{check("b.1", module.WarningLevel), check("b.2", module.Healthy)}
			}),
		}, module.WarningLevel, 3},
		{"critical before warning", []module.Module{
			healthModule("a", func(context.Context) []module.HealthCheck {
				return []module.HealthCheck{check("a.1", module.CriticalLvl)}
			}),
			healthModule("b", func(context.Context) []module.HealthCheck {
				return []module.HealthCheck{check("b.1", module.WarningLevel)}
			}),
			healthModule("c", func(context.Context) []module.HealthCheck { return []module.HealthCheck{check("c.1", module.Healthy)} }),
		}, module.CriticalLvl, 3},
		{"critical after warning", []module.Module{
			healthModule("a", func(context.Context) []module.HealthCheck {
				return []module.HealthCheck{check("a.1", module.WarningLevel)}
			}),
			healthModule("b", func(context.Context) []module.HealthCheck {
				return []module.HealthCheck{check("b.1", module.CriticalLvl)}
			}),
			healthModule("c", func(context.Context) []module.HealthCheck { return []module.HealthCheck{check("c.1", module.Healthy)} }),
		}, module.CriticalLvl, 3},
		{"panicking reporter", []module.Module{
			healthModule("a", func(context.Context) []module.HealthCheck { return []module.HealthCheck{check("a.1", module.Healthy)} }),
			healthModule("boom", func(context.Context) []module.HealthCheck { panic(leak) }),
			healthModule("c", func(context.Context) []module.HealthCheck { return []module.HealthCheck{check("c.1", module.Healthy)} }),
		}, module.WarningLevel, 3},
		{"panicking reporter and a critical module", []module.Module{
			healthModule("boom", func(context.Context) []module.HealthCheck { panic(errors.New(leak)) }),
			healthModule("c", func(context.Context) []module.HealthCheck {
				return []module.HealthCheck{check("c.1", module.CriticalLvl)}
			}),
		}, module.CriticalLvl, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, tc.modules...)
			c := e.login("calisan")
			res := e.do(c, "GET", "/api/v1/health", "")
			if res.Status != 200 {
				t.Fatalf("status %d %s", res.Status, res.Raw)
			}
			var rep HealthReport
			if err := json.Unmarshal(res.Data, &rep); err != nil {
				t.Fatal(err)
			}
			if rep.Status != tc.want {
				t.Errorf("status %s, want %s (%s)", rep.Status, tc.want, res.Raw)
			}
			if len(rep.Checks) != tc.checks {
				t.Errorf("%d checks, want %d (%s)", len(rep.Checks), tc.checks, res.Raw)
			}
			if !strings.Contains(res.Raw, `"checks":[`) {
				t.Errorf("checks must be a list, never null: %s", res.Raw)
			}
			if rep.CheckedAt == 0 {
				t.Error("checked_at missing")
			}
			if strings.Contains(res.Raw, "S3CRET") {
				t.Errorf("panic text in the health report: %s", res.Raw)
			}
			for _, m := range tc.modules {
				if m.Name() == "boom" {
					found := false
					for _, ch := range rep.Checks {
						if ch.ID == "module.boom" && ch.Status == module.WarningLevel {
							found = true
						}
					}
					if !found {
						t.Errorf("the failed reporter is not shown: %s", res.Raw)
					}
				}
			}
		})
	}
}

func TestHealthIsCached(t *testing.T) {
	var calls atomic.Int32
	e := newEnv(t, healthModule("a", func(context.Context) []module.HealthCheck {
		calls.Add(1)
		return []module.HealthCheck{check("a.1", module.Healthy)}
	}))
	c := e.login("calisan")
	for i := 0; i < 20; i++ {
		if res := e.do(c, "GET", "/api/v1/health", ""); res.Status != 200 {
			t.Fatalf("status %d", res.Status)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("20 requests ran the probes %d times", n)
	}
}

// ---- notifications API ----

func TestNotificationEndpoints(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.login("calisan")
	if res := e.do(c, "GET", "/api/v1/notifications", ""); res.Status != 200 || !strings.Contains(res.Raw, `"items":[]`) || !strings.Contains(res.Raw, `"unread":0`) {
		t.Fatalf("empty list: %d %s", res.Status, res.Raw)
	}
	e.deps.Notify.Publish(ctx, notify.Warning, "storage", "Bir", "")
	e.deps.Notify.Publish(ctx, notify.Error, "docker", "İki", "")
	res := e.do(c, "GET", "/api/v1/notifications", "")
	if !strings.Contains(res.Raw, `"unread":2`) {
		t.Fatalf("list: %s", res.Raw)
	}
	if r := e.do(c, "POST", "/api/v1/notifications/1/read", ""); r.Status != 200 {
		t.Fatalf("read: %d %s", r.Status, r.Raw)
	}
	if res := e.do(c, "GET", "/api/v1/notifications?unread=true", ""); !strings.Contains(res.Raw, `"unread":1`) || strings.Contains(res.Raw, `"Bir"`) {
		t.Fatalf("after reading one: %s", res.Raw)
	}
	for _, id := range []string{"0", "-1", "abc", "1.5", "99999999999999999999", "1%20OR%201=1"} {
		if r := e.do(c, "POST", "/api/v1/notifications/"+id+"/read", ""); r.Status != http.StatusBadRequest {
			t.Errorf("id %q: status %d, want 400", id, r.Status)
		}
	}
	if r := e.do(c, "POST", "/api/v1/notifications/read-all", ""); r.Status != 200 {
		t.Fatalf("read-all: %d", r.Status)
	}
	if n, _ := e.deps.Notify.UnreadCount(ctx); n != 0 {
		t.Fatalf("%d unread after read-all", n)
	}
}

// ---- static files, unknown API paths, headers ----

func TestUnknownAPIPathsReturnTheEnvelope(t *testing.T) {
	e := newEnv(t)
	c := e.login("admin")
	const want = `{"success":false,"data":null,"error":{"code":"not_found","message":"İstenen API adresi bulunamadı."}}` + "\n"
	for _, target := range []string{
		"GET /api/", "GET /api/v1", "GET /api/v1/", "GET /api/v1/no-such-module", "GET /api/v1/no/such/path", "GET /api/v2/auth/status",
		"POST /api/v1/no-such-module", "DELETE /api/v1/auth/login", "PUT /api/v1/health", "PATCH /api/v1/settings", "GET /api/v1/auth/login",
		"GET /api/assets/x.js", "GET /api/index.html",
	} {
		m, p, _ := strings.Cut(target, " ")
		for _, who := range []*client{nil, c} {
			res := e.do(who, m, p, "")
			if res.Status != http.StatusNotFound || res.Raw != want {
				t.Errorf("%s: %d %q", target, res.Status, res.Raw)
			}
			if ct := res.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
				t.Errorf("%s: Content-Type %q", target, ct)
			}
		}
	}
}

func TestStaticFilesThroughTheServer(t *testing.T) {
	e := newEnv(t)
	root := e.do(nil, "GET", "/", "")
	if root.Status != 200 && root.Status != http.StatusServiceUnavailable {
		t.Fatalf("GET /: %d", root.Status)
	}
	// Client-side routes get the same document as "/", without a session.
	for _, p := range []string{"/docker", "/settings", "/files/home/ali", "/no-such-page"} {
		res := e.do(nil, "GET", p, "")
		if res.Status != root.Status || res.Raw != root.Raw {
			t.Errorf("GET %s: %d, differs from the application shell", p, res.Status)
		}
	}
	if res := e.do(nil, "GET", "/assets/no-such-file.js", ""); res.Status != http.StatusNotFound {
		t.Errorf("missing asset: %d", res.Status)
	}
	for _, p := range []string{"/../../../etc/passwd", "/assets/../../../etc/passwd", "/..%2f..%2f..%2fetc%2fpasswd", "/%2e%2e/%2e%2e/etc/passwd",
		"/assets/..%2f..%2fwebui.go", "//etc/passwd", `/..\..\etc\passwd`} {
		req := httptest.NewRequest("GET", "http://"+testHost+"/", nil)
		req.URL.Path = p
		req.RequestURI = p
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		body := rec.Body.String()
		if strings.Contains(body, "root:") || strings.Contains(body, "package webui") {
			t.Errorf("GET %q read a file outside the embedded files", p)
		}
		if rec.Code >= 500 && rec.Code != http.StatusServiceUnavailable {
			t.Errorf("GET %q: status %d", p, rec.Code)
		}
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	e := newEnv(t, leakyModule())
	c := e.login("admin")
	cases := []struct {
		who          *client
		method, path string
	}{
		{nil, "GET", "/"},
		{nil, "GET", "/docker"},
		{nil, "GET", "/assets/no-such-file.js"},
		{nil, "POST", "/"},
		{nil, "GET", "/api/v1/auth/status"},
		{nil, "POST", "/api/v1/auth/login"},
		{nil, "GET", "/api/v1/health"},
		{nil, "GET", "/api/v1/no-such-path"},
		{c, "GET", "/api/v1/health"},
		{c, "GET", "/api/v1/settings"},
		{c, "GET", "/api/v1/leaky/raw"},
		{c, "GET", "/api/v1/leaky/panic"},
		{c, "GET", "/api/v1/leaky/nil"},
		{&client{cookie: c.cookie, csrf: "x"}, "DELETE", "/api/v1/notifications"},
	}
	for _, tc := range cases {
		res := e.do(tc.who, tc.method, tc.path, "")
		for k, want := range map[string]string{
			"X-Content-Type-Options": "nosniff",
			"X-Frame-Options":        "DENY",
			"Referrer-Policy":        "same-origin",
		} {
			if got := res.Header.Get(k); got != want {
				t.Errorf("%s %s (%d): %s = %q", tc.method, tc.path, res.Status, k, got)
			}
		}
		if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("%s %s (%d): CSP %q", tc.method, tc.path, res.Status, csp)
		}
		if res.Header.Get("Permissions-Policy") == "" {
			t.Errorf("%s %s (%d): no Permissions-Policy", tc.method, tc.path, res.Status)
		}
		if strings.HasPrefix(tc.path, "/api/") && res.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s %s (%d): API response may be cached: %q", tc.method, tc.path, res.Status, res.Header.Get("Cache-Control"))
		}
	}
}

func TestAuditEndpoint(t *testing.T) {
	e := newEnv(t)
	c := e.login("admin")
	res := e.do(c, "GET", "/api/v1/audit?limit=5", "")
	if res.Status != 200 || !strings.Contains(res.Raw, `"auth.login"`) || !strings.Contains(res.Raw, `"total":1`) {
		t.Fatalf("audit: %d %s", res.Status, res.Raw)
	}
	for _, s := range []string{c.cookie.Value, c.csrf, testPassword, "argon2"} {
		if strings.Contains(res.Raw, s) {
			t.Fatal("the audit log exposes a secret")
		}
	}
	for _, q := range []string{"?limit=-1&offset=-1", "?limit=abc&offset=abc", "?limit=999999999&offset=999999999", "?offset=1%3BDROP%20TABLE%20audit_log"} {
		if r := e.do(c, "GET", "/api/v1/audit"+q, ""); r.Status != 200 {
			t.Errorf("audit%s: %d %s", q, r.Status, r.Raw)
		}
	}
}
