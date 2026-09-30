//go:build linux

package terminal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"myserver/internal/audit"
	"myserver/internal/auth"
	"myserver/internal/config"
	"myserver/internal/database"
	"myserver/internal/httpx"
	"myserver/internal/module"
	"myserver/internal/privileged"
	"myserver/internal/settings"
	"myserver/internal/terminal/termcheck"
	"myserver/migrations"
)

// testPasswd: the first part is /etc/passwd captured from ubuntu:24.04
// (shortened), the accounts after "ubuntu" are written by hand.
const testPasswd = `root:x:0:0:root:/root:/bin/bash
daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin
sync:x:4:65534:sync:/bin:/bin/sync
www-data:x:33:33:www-data:/var/www:/usr/sbin/nologin
nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin
ubuntu:x:1000:1000:Ubuntu:/home/ubuntu:/bin/bash
ayse:x:1001:1001:Ayşe Yılmaz,,,:/home/ayse:/bin/sh
zshuser:x:1002:1002::/home/zshuser:/usr/bin/zsh-is-not-installed
dirshell:x:1003:1003::/home/dirshell:/usr/bin
noexec:x:1004:1004::/home/noexec:/etc/hostname
gituser:x:1005:1005::/home/git:/usr/bin/git-shell
`

const waitFor = 10 * time.Second

// fakeClock drives a session's supervisor from the test.
type fakeClock struct {
	mu      sync.Mutex
	t       time.Time
	ticks   chan time.Time
	timers  chan time.Time
	tickD   time.Duration
	timerD  time.Duration
	stopped int
}

func newFakeClock() *fakeClock {
	return &fakeClock{
		t:      time.Unix(1_700_000_000, 0),
		ticks:  make(chan time.Time),
		timers: make(chan time.Time),
	}
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

func (f *fakeClock) clock() clock {
	return clock{
		now: f.now,
		ticker: func(d time.Duration) (<-chan time.Time, func()) {
			f.mu.Lock()
			f.tickD = d
			f.mu.Unlock()
			return f.ticks, func() { f.mu.Lock(); f.stopped++; f.mu.Unlock() }
		},
		timer: func(d time.Duration) (<-chan time.Time, func()) {
			f.mu.Lock()
			f.timerD = d
			f.mu.Unlock()
			return f.timers, func() { f.mu.Lock(); f.stopped++; f.mu.Unlock() }
		},
	}
}

// tick delivers one supervisor tick; it reports false when no supervisor
// took it (the session has ended).
func (f *fakeClock) tick() bool {
	select {
	case f.ticks <- f.now():
		return true
	case <-time.After(2 * time.Second):
		return false
	}
}

type testEnv struct {
	t      *testing.T
	db     interface{ Close() error }
	store  *settings.Store
	audit  *audit.Logger
	auth   *auth.Service
	m      *Module
	srv    *httptest.Server
	helper string
	clk    *fakeClock
	exec   func(query string, args ...any)
	count  func(query string, args ...any) int
}

// helperScript stands in for "myserver-helper terminal-shell <user>": it
// records its pid and arguments, announces the shell like the real helper
// and becomes a shell on the terminal it was given.
const helperScript = `#!/bin/sh
echo "$$ $*" >> "$0.pids"
echo MYSERVER_SHELL_STARTED >&2
PS1='ready> ' exec /bin/sh
`

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root: the panel starts the helper directly only as root, otherwise through sudo")
	}
}

func newEnv(t *testing.T, fake bool) *testEnv {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open(filepath.Join(dir, "data", "test.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	ctx := context.Background()
	if err := database.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := settings.NewStore(ctx, db)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	helper := filepath.Join(dir, "helper.sh")
	if err := os.WriteFile(helper, []byte(helperScript), 0o755); err != nil {
		t.Fatal(err)
	}
	passwd := filepath.Join(dir, "passwd")
	if err := os.WriteFile(passwd, []byte(testPasswd), 0o644); err != nil {
		t.Fatal(err)
	}
	oldPasswd := passwdPath
	passwdPath = passwd

	cfg := &config.Config{DataDir: dir, HelperPath: helper}
	al := audit.New(db)
	priv := privileged.New(helper)
	as, err := auth.NewService(db, cfg, store, al, priv)
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	api := settings.NewAPI(store, al, priv, auth.ActorFrom)
	mod, err := New(module.Deps{Cfg: cfg, DB: db, Settings: store, Audit: al, Priv: priv, Auth: as}, api)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m := mod.(*Module)
	e := &testEnv{t: t, db: db, store: store, audit: al, auth: as, m: m, helper: helper}
	if fake {
		e.clk = newFakeClock()
		m.clk = e.clk.clock()
	}
	e.exec = func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	e.count = func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	e.set(settings.KeyTerminalEnabled, "true")
	e.set(settings.KeyTerminalUser, "ubuntu")

	mux := http.NewServeMux()
	root := httpx.NewRouter(mux)
	m.Register(root.Group("/api/v1", as.Require), root.Group("/api/v1", as.RequireWebSocket))
	e.srv = httptest.NewServer(mux)

	t.Cleanup(func() {
		m.cancel()
		done := make(chan struct{})
		go func() { m.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Error("terminal sessions did not end on shutdown")
		}
		e.srv.Close()
		db.Close()
		passwdPath = oldPasswd
	})
	return e
}

func (e *testEnv) set(key, value string) {
	e.t.Helper()
	if err := e.store.Set(context.Background(), key, value); err != nil {
		e.t.Fatalf("set %s: %v", key, err)
	}
}

// login creates a panel user with a live session and returns its cookie
// token. The rows follow migrations/0001_init.sql.
func (e *testEnv) login(username, role string) string {
	e.t.Helper()
	now := time.Now().Unix()
	e.exec(`INSERT INTO users (username, password_hash, role, disabled, created_at) VALUES (?, 'x', ?, 0, ?)`,
		username, role, now)
	return e.session(username, now+3600)
}

func (e *testEnv) session(username string, expires int64) string {
	e.t.Helper()
	token := "token-" + username + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	sum := sha256.Sum256([]byte(token))
	now := time.Now().Unix()
	e.exec(`INSERT INTO sessions (token_hash, user_id, csrf_token, ip, user_agent, created_at, last_seen_at, expires_at)
		SELECT ?, id, 'csrf', '127.0.0.1', 'test', ?, ?, ? FROM users WHERE username = ?`,
		hex.EncodeToString(sum[:]), now, now, expires, username)
	return token
}

func (e *testEnv) host() string { return strings.TrimPrefix(e.srv.URL, "http://") }

func (e *testEnv) get(path, token string) (int, httpx.Envelope, []byte) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	if token != "" {
		req.AddCookie(&http.Cookie{Name: "myserver_session", Value: token})
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	var env struct {
		httpx.Envelope
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		e.t.Fatalf("GET %s: body is not an envelope: %q", path, body)
	}
	return res.StatusCode, env.Envelope, env.Data
}

type wsMsg struct {
	mt   int
	data []byte
	err  error
}

type wsClient struct {
	t     *testing.T
	conn  *websocket.Conn
	msgs  chan wsMsg
	out   bytes.Buffer
	texts []serverMessage
	err   error
}

// dialRaw performs the handshake with exactly the given credentials.
func (e *testEnv) dialRaw(token, origin, query string) (*websocket.Conn, *http.Response, error) {
	h := http.Header{}
	if token != "" {
		h.Set("Cookie", "myserver_session="+token)
	}
	if origin != "" {
		h.Set("Origin", origin)
	}
	d := websocket.Dialer{HandshakeTimeout: waitFor}
	return d.Dial("ws://"+e.host()+"/api/v1/terminal/ws"+query, h)
}

func (e *testEnv) dial(token, query string) *wsClient {
	e.t.Helper()
	conn, res, err := e.dialRaw(token, "http://"+e.host(), query)
	if err != nil {
		status := 0
		if res != nil {
			status = res.StatusCode
		}
		e.t.Fatalf("websocket handshake failed: %v (status %d)", err, status)
	}
	c := &wsClient{t: e.t, conn: conn, msgs: make(chan wsMsg, 4096)}
	go func() {
		for {
			mt, data, err := conn.ReadMessage()
			c.msgs <- wsMsg{mt, data, err}
			if err != nil {
				close(c.msgs)
				return
			}
		}
	}()
	e.t.Cleanup(func() { conn.Close() })
	return c
}

// next takes one message; ok is false on timeout or when the socket closed.
func (c *wsClient) next(timeout time.Duration) bool {
	if c.err != nil {
		return false
	}
	select {
	case m, open := <-c.msgs:
		if !open {
			return false
		}
		switch {
		case m.err != nil:
			c.err = m.err
			return false
		case m.mt == websocket.BinaryMessage:
			c.out.Write(m.data)
		case m.mt == websocket.TextMessage:
			var sm serverMessage
			if err := json.Unmarshal(m.data, &sm); err != nil {
				c.t.Errorf("server sent a text frame that is not JSON: %q", m.data)
			}
			c.texts = append(c.texts, sm)
		}
		return true
	case <-time.After(timeout):
		return false
	}
}

func (c *wsClient) send(input string) {
	c.t.Helper()
	if err := c.conn.WriteMessage(websocket.BinaryMessage, []byte(input)); err != nil {
		c.t.Fatalf("send input: %v", err)
	}
}

func (c *wsClient) sendText(s string) {
	c.t.Helper()
	if err := c.conn.WriteMessage(websocket.TextMessage, []byte(s)); err != nil {
		c.t.Fatalf("send control message: %v", err)
	}
}

// waitOutput waits until the terminal output contains want and returns the
// output up to that point; the consumed output is discarded.
func (c *wsClient) waitOutput(want string) string {
	c.t.Helper()
	deadline := time.Now().Add(waitFor)
	for {
		if i := strings.Index(c.out.String(), want); i >= 0 {
			s := c.out.String()
			c.out.Reset()
			c.out.WriteString(s[i+len(want):])
			return s[:i+len(want)]
		}
		if time.Now().After(deadline) || !c.next(time.Until(deadline)) {
			c.t.Fatalf("terminal output does not contain %q; output so far %q, control messages %+v, error %v",
				want, c.out.String(), c.texts, c.err)
		}
	}
}

// run executes a command in the shell and returns what it printed. The
// markers are built by the shell so the echo of the command cannot match.
func (c *wsClient) run(command string) string {
	c.t.Helper()
	c.send("echo B$((1))B; " + command + "; echo E$((2))E\n")
	c.waitOutput("B1B")
	out := c.waitOutput("E2E")
	return strings.TrimSpace(strings.ReplaceAll(strings.TrimSuffix(out, "E2E"), "\r", ""))
}

// waitText waits for a control message of the given type.
func (c *wsClient) waitText(typ string) serverMessage {
	c.t.Helper()
	deadline := time.Now().Add(waitFor)
	for {
		for i, m := range c.texts {
			if m.Type == typ {
				c.texts = append(c.texts[:i], c.texts[i+1:]...)
				return m
			}
		}
		if time.Now().After(deadline) || !c.next(time.Until(deadline)) {
			c.t.Fatalf("no %q message; control messages %+v, error %v", typ, c.texts, c.err)
		}
	}
}

// noText asserts that no control message of the given type arrives shortly.
func (c *wsClient) noText(typ string) {
	c.t.Helper()
	for c.next(150 * time.Millisecond) {
	}
	for _, m := range c.texts {
		if m.Type == typ {
			c.t.Fatalf("unexpected %q message: %+v", typ, m)
		}
	}
	if c.err != nil {
		c.t.Fatalf("socket closed unexpectedly: %v", c.err)
	}
}

// waitClosed reads until the server closes the socket and returns the close
// code (or -1 when the connection ended without a close frame).
func (c *wsClient) waitClosed() int {
	c.t.Helper()
	deadline := time.Now().Add(2 * waitFor)
	for c.err == nil {
		if time.Now().After(deadline) {
			c.t.Fatalf("socket still open; control messages %+v", c.texts)
		}
		c.next(time.Until(deadline))
	}
	var ce *websocket.CloseError
	if errors.As(c.err, &ce) {
		return ce.Code
	}
	return -1
}

// expectEnd asserts the final message and close code of a session.
func (c *wsClient) expectEnd(reason string, closeCode int) serverMessage {
	c.t.Helper()
	code := c.waitClosed()
	var final *serverMessage
	for i := range c.texts {
		if c.texts[i].Type == "error" || c.texts[i].Type == "exit" {
			final = &c.texts[i]
		}
	}
	if final == nil {
		c.t.Fatalf("socket closed (code %d) without a final message; control messages %+v", code, c.texts)
	}
	if reason == endExit {
		if final.Type != "exit" {
			c.t.Fatalf("final message = %+v, want type exit", *final)
		}
	} else {
		if final.Type != "error" || final.Reason != reason {
			c.t.Fatalf("final message = %+v, want error with reason %q", *final, reason)
		}
		if strings.TrimSpace(final.Message) == "" {
			c.t.Errorf("final message for %q has no text", reason)
		}
	}
	if code != closeCode {
		c.t.Errorf("close code = %d, want %d", code, closeCode)
	}
	return *final
}

func (e *testEnv) total() int {
	e.m.mu.Lock()
	defer e.m.mu.Unlock()
	return e.m.total
}

func (e *testEnv) waitTotal(n int) {
	e.t.Helper()
	deadline := time.Now().Add(2 * waitFor)
	for e.total() != n {
		if time.Now().After(deadline) {
			e.t.Fatalf("open sessions = %d, want %d", e.total(), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// spawned returns the pid and arguments of every helper invocation.
func (e *testEnv) spawned() (pids []int, args []string) {
	data, err := os.ReadFile(e.helper + ".pids")
	if err != nil {
		return nil, nil
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		p, a, _ := strings.Cut(line, " ")
		pid, err := strconv.Atoi(p)
		if err != nil {
			e.t.Fatalf("pid file line %q", line)
		}
		pids = append(pids, pid)
		args = append(args, a)
	}
	return pids, args
}

// groupAlive lists the live (not zombie) processes of a process group.
func groupAlive(pgid int) []int {
	var alive []int
	entries, _ := os.ReadDir("/proc")
	for _, de := range entries {
		pid, err := strconv.Atoi(de.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile("/proc/" + de.Name() + "/stat")
		if err != nil {
			continue
		}
		st, ok := termcheck.ParseStat(string(data))
		if !ok || st.PGRP != pgid {
			continue
		}
		s := string(data)
		if i := strings.LastIndexByte(s, ')'); i >= 0 && strings.HasPrefix(strings.TrimSpace(s[i+1:]), "Z") {
			continue
		}
		alive = append(alive, pid)
	}
	return alive
}

func (e *testEnv) waitGroupGone(pgid int) {
	e.t.Helper()
	deadline := time.Now().Add(waitFor)
	for {
		alive := groupAlive(pgid)
		if len(alive) == 0 {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("process group %d still has live processes %v", pgid, alive)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (e *testEnv) lastAudit(action string) (detail string, success bool, target string, user string) {
	e.t.Helper()
	entries, _, err := e.audit.List(context.Background(), 100, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, a := range entries {
		if a.Action == action {
			return a.Detail, a.Success, a.Target, a.Username
		}
	}
	e.t.Fatalf("no audit record %q in %+v", action, entries)
	return
}

// ---- refusals before the upgrade ----

func TestSocketRefusedBeforeUpgrade(t *testing.T) {
	e := newEnv(t, true)
	admin := e.login("yonetici", auth.RoleAdmin)
	normal := e.login("kullanici", auth.RoleUser)
	e.exec(`INSERT INTO users (username, password_hash, role, disabled, created_at) VALUES ('eski', 'x', 'admin', 0, 1)`)
	expired := e.session("eski", time.Now().Unix()-10)
	e.exec(`INSERT INTO users (username, password_hash, role, disabled, created_at) VALUES ('kapali', 'x', 'admin', 1, 1)`)
	disabled := e.session("kapali", time.Now().Unix()+3600)
	origin := "http://" + e.host()

	cases := []struct {
		name, token, origin string
		status              int
	}{
		{"no session cookie", "", origin, http.StatusUnauthorized},
		{"unknown token", "not-a-session", origin, http.StatusUnauthorized},
		{"expired session", expired, origin, http.StatusUnauthorized},
		{"disabled admin", disabled, origin, http.StatusUnauthorized},
		{"normal user", normal, origin, http.StatusForbidden},
		{"admin without Origin", admin, "", http.StatusForbidden},
		{"admin from another origin", admin, "http://evil.example", http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			conn, res, err := e.dialRaw(c.token, c.origin, "")
			if err == nil {
				conn.Close()
				t.Fatal("the WebSocket was upgraded")
			}
			if res == nil {
				t.Fatalf("no HTTP response: %v", err)
			}
			if res.StatusCode != c.status {
				t.Errorf("status = %d, want %d", res.StatusCode, c.status)
			}
			if res.Header.Get("Upgrade") != "" {
				t.Errorf("response carries an Upgrade header")
			}
		})
	}
	if pids, _ := e.spawned(); len(pids) != 0 {
		t.Errorf("refused requests started %d processes", len(pids))
	}
	if e.total() != 0 {
		t.Errorf("refused requests hold %d session slots", e.total())
	}
	detail, ok, _, user := e.lastAudit("terminal.open")
	if ok || user != "kullanici" {
		t.Errorf("audit of the refused normal user: user=%q success=%v detail=%q", user, ok, detail)
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'terminal.open'`); n != 1 {
		t.Errorf("%d terminal.open audit records, want 1 (the normal user's attempt)", n)
	}
}

// ---- refusals after the upgrade, before any process is started ----

func TestSocketRefusedBySettings(t *testing.T) {
	e := newEnv(t, true)
	admin := e.login("yonetici", auth.RoleAdmin)

	cases := []struct {
		name, enabled, user, reason string
	}{
		{"terminal disabled", "false", "ubuntu", endDisabled},
		{"no user chosen", "true", "", "no_user"},
		{"blank user", "true", "   ", "no_user"},
		{"root", "true", "root", "invalid_user"},
		{"system account", "true", "www-data", "invalid_user"},
		{"nobody", "true", "nobody", "invalid_user"},
		{"unknown user", "true", "nosuchuser", "invalid_user"},
		{"invalid name", "true", "ubuntu;id", "invalid_user"},
		{"option-like name", "true", "-u", "invalid_user"},
		{"shell not installed", "true", "zshuser", "invalid_user"},
		{"shell is a directory", "true", "dirshell", "invalid_user"},
		{"shell not executable", "true", "noexec", "invalid_user"},
		{"git-shell", "true", "gituser", "invalid_user"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e.set(settings.KeyTerminalEnabled, c.enabled)
			e.set(settings.KeyTerminalUser, c.user)
			ws := e.dial(admin, "")
			ws.t = t
			ws.expectEnd(c.reason, websocket.ClosePolicyViolation)
		})
	}
	if pids, _ := e.spawned(); len(pids) != 0 {
		t.Errorf("refused sessions started %d processes", len(pids))
	}
	if e.total() != 0 {
		t.Errorf("refused sessions hold %d slots", e.total())
	}
}

func TestSocketRefusedWhenHelperFails(t *testing.T) {
	requireRoot(t)
	e := newEnv(t, true)
	admin := e.login("yonetici", auth.RoleAdmin)

	script := "#!/bin/sh\necho 'MYSERVER_ERROR: Kullanıcının kabuğu güvenli değil.' >&2\nexit 3\n"
	if err := os.WriteFile(e.helper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ws := e.dial(admin, "")
	final := ws.expectEnd("spawn_failed", websocket.ClosePolicyViolation)
	if final.Message != "Kullanıcının kabuğu güvenli değil." {
		t.Errorf("message = %q, want the helper's message", final.Message)
	}
	e.waitTotal(0)
	detail, ok, target, _ := e.lastAudit("terminal.open")
	if ok || target != "ubuntu" || !strings.Contains(detail, "güvenli değil") {
		t.Errorf("audit: success=%v target=%q detail=%q", ok, target, detail)
	}

	// A helper that fails without a message must not leak its stderr.
	script = "#!/bin/sh\necho 'panic: secret internal detail' >&2\nexit 1\n"
	if err := os.WriteFile(e.helper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ws = e.dial(admin, "")
	final = ws.expectEnd("spawn_failed", websocket.ClosePolicyViolation)
	if strings.Contains(final.Message, "secret") || strings.Contains(final.Message, "panic") {
		t.Errorf("internal error text reached the client: %q", final.Message)
	}
	e.waitTotal(0)
}

// ---- status and user list ----

func TestStatusEndpoint(t *testing.T) {
	e := newEnv(t, true)
	admin := e.login("yonetici", auth.RoleAdmin)
	normal := e.login("kullanici", auth.RoleUser)
	e.set(settings.KeyTerminalTimeout, "20")

	if status, env, _ := e.get("/api/v1/terminal/status", ""); status != http.StatusUnauthorized || env.Success {
		t.Errorf("without session: status %d success %v", status, env.Success)
	}

	var res statusResponse
	status, _, data := e.get("/api/v1/terminal/status", normal)
	if status != http.StatusOK {
		t.Fatalf("normal user: status %d", status)
	}
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatal(err)
	}
	if res.Allowed || res.User != "" || res.UserValid || res.HelperAvailable != nil || res.UserError != nil {
		t.Errorf("normal user sees administrator fields: %+v", res)
	}
	if !res.Enabled || !res.Supported || res.IdleTimeoutMinutes != 20 ||
		res.MaxSessions != maxSessionsTotal || res.MaxSessionsPerUser != maxSessionsPerUser ||
		res.MaxSessionSeconds != 12*3600 {
		t.Errorf("normal user status = %+v", res)
	}

	res = statusResponse{}
	_, _, data = e.get("/api/v1/terminal/status", admin)
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatal(err)
	}
	if !res.Allowed || res.User != "ubuntu" || !res.UserValid || res.UserError != nil {
		t.Errorf("admin status = %+v", res)
	}
	if res.HelperAvailable == nil {
		t.Error("helper_available is null on Linux")
	}

	e.set(settings.KeyTerminalUser, "root")
	e.set(settings.KeyTerminalEnabled, "false")
	res = statusResponse{}
	_, _, data = e.get("/api/v1/terminal/status", admin)
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatal(err)
	}
	if res.Enabled || res.UserValid || res.UserError == nil || *res.UserError != termcheck.Message(termcheck.IsRoot) {
		t.Errorf("admin status with root = %+v", res)
	}
}

func TestUsersEndpoint(t *testing.T) {
	e := newEnv(t, true)
	admin := e.login("yonetici", auth.RoleAdmin)
	normal := e.login("kullanici", auth.RoleUser)

	if status, _, _ := e.get("/api/v1/terminal/users", normal); status != http.StatusForbidden {
		t.Errorf("normal user: status %d, want 403", status)
	}
	if status, _, _ := e.get("/api/v1/terminal/users", ""); status != http.StatusUnauthorized {
		t.Errorf("no session: status %d, want 401", status)
	}
	status, _, data := e.get("/api/v1/terminal/users", admin)
	if status != http.StatusOK {
		t.Fatalf("admin: status %d", status)
	}
	var users []systemUser
	if err := json.Unmarshal(data, &users); err != nil {
		t.Fatal(err)
	}
	want := []systemUser{
		{Username: "ubuntu", UID: 1000, FullName: "Ubuntu", Home: "/home/ubuntu", Shell: "/bin/bash"},
		{Username: "ayse", UID: 1001, FullName: "Ayşe Yılmaz", Home: "/home/ayse", Shell: "/bin/sh"},
	}
	if len(users) != len(want) {
		t.Fatalf("users = %+v, want %+v", users, want)
	}
	for i := range want {
		if users[i] != want[i] {
			t.Errorf("user %d = %+v, want %+v", i, users[i], want[i])
		}
	}

	// No eligible account: an empty list, not null.
	if err := os.WriteFile(passwdPath, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, data = e.get("/api/v1/terminal/users", admin)
	if string(data) != "[]" {
		t.Errorf("data = %s, want []", data)
	}

	// Unreadable account database: a specific error, not a 500.
	if err := os.Remove(passwdPath); err != nil {
		t.Fatal(err)
	}
	status, env, _ := e.get("/api/v1/terminal/users", admin)
	if status != http.StatusServiceUnavailable || env.Error == nil || env.Error.Code != "passwd_unreadable" {
		t.Errorf("missing passwd: status %d error %+v", status, env.Error)
	}
}

func TestValidateUserSetting(t *testing.T) {
	newEnv(t, true)
	for in, want := range map[string]string{"": "", "  ": "", "ubuntu": "ubuntu", " ayse \n": "ayse"} {
		got, err := validateUserSetting(in)
		if err != nil || got != want {
			t.Errorf("validateUserSetting(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"root", "daemon", "sync", "www-data", "nobody", "nosuchuser", "zshuser", "dirshell", "noexec",
		"gituser", "Ubuntu", "ubuntu;id", "-u", "../ubuntu", "ubuntu ubuntu",
	} {
		got, err := validateUserSetting(in)
		if err == nil {
			t.Errorf("validateUserSetting(%q) accepted as %q", in, got)
			continue
		}
		var he *httpx.Error
		if !errors.As(err, &he) || he.Status != http.StatusBadRequest || he.Message == "" {
			t.Errorf("validateUserSetting(%q) error = %#v, want a 400 with a message", in, err)
		}
	}
}

func TestIdleTimeoutSetting(t *testing.T) {
	e := newEnv(t, true)
	for value, want := range map[string]time.Duration{
		"1": time.Minute, "15": 15 * time.Minute, "240": 240 * time.Minute,
		"0": 15 * time.Minute, "241": 15 * time.Minute, "-3": 15 * time.Minute,
		"abc": 15 * time.Minute, "": 15 * time.Minute, "99999999999999999999": 15 * time.Minute,
	} {
		e.set(settings.KeyTerminalTimeout, value)
		if got := e.m.idleTimeout(); got != want {
			t.Errorf("idle timeout for %q = %v, want %v", value, got, want)
		}
	}
}

// ---- the PTY session, end to end ----

func TestSessionShell(t *testing.T) {
	requireRoot(t)
	e := newEnv(t, false)
	admin := e.login("yonetici", auth.RoleAdmin)

	ws := e.dial(admin, "?cols=132&rows=43")
	if got := ws.run("echo r$((40+2))"); got != "r42" {
		t.Errorf("command output = %q, want r42", got)
	}
	// Typed input is echoed back by the terminal.
	ws.send("echo typed-text\n")
	ws.waitOutput("echo typed-text")
	ws.waitOutput("typed-text")

	if got := ws.run("tty"); !strings.HasPrefix(got, "/dev/pts/") {
		t.Errorf("tty = %q, want a pseudo-terminal", got)
	}
	if got := ws.run("stty size"); got != "43 132" {
		t.Errorf("initial size = %q, want \"43 132\"", got)
	}
	ws.sendText(`{"type":"resize","cols":120,"rows":40}`)
	ws.sendText(`{"type":"ping"}`)
	ws.waitText("pong") // handled after the resize, by the same goroutine
	if got := ws.run("stty size"); got != "40 120" {
		t.Errorf("size after resize = %q, want \"40 120\"", got)
	}

	pids, args := e.spawned()
	if len(pids) != 1 {
		t.Fatalf("%d helper processes started, want 1", len(pids))
	}
	if args[0] != "terminal-shell ubuntu" {
		t.Errorf("helper arguments = %q, want \"terminal-shell ubuntu\"", args[0])
	}
	if got := ws.run("echo $$"); got != strconv.Itoa(pids[0]) {
		t.Errorf("shell pid = %q, helper pid = %d", got, pids[0])
	}
	if e.total() != 1 {
		t.Errorf("open sessions = %d, want 1", e.total())
	}
	if _, ok, target, user := e.lastAudit("terminal.open"); !ok || target != "ubuntu" || user != "yonetici" {
		t.Errorf("terminal.open audit: success=%v target=%q user=%q", ok, target, user)
	}

	ws.send("exit 7\n")
	final := ws.expectEnd(endExit, websocket.CloseNormalClosure)
	if final.Code == nil || *final.Code != 7 {
		t.Errorf("exit message = %+v, want code 7", final)
	}
	e.waitTotal(0)
	e.waitGroupGone(pids[0])
	detail, ok, _, _ := e.lastAudit("terminal.close")
	if !ok || !strings.Contains(detail, "neden=exit") || !strings.Contains(detail, "çıkış_kodu=7") {
		t.Errorf("terminal.close audit: success=%v detail=%q", ok, detail)
	}
	// Terminal input and output are never written to the audit log.
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE detail LIKE '%typed-text%' OR target LIKE '%typed-text%'`); n != 0 {
		t.Error("terminal input reached the audit log")
	}
}

func TestSessionInitialSizeFallback(t *testing.T) {
	requireRoot(t)
	e := newEnv(t, true)
	admin := e.login("yonetici", auth.RoleAdmin)
	for _, q := range []string{"", "?cols=0&rows=0", "?cols=9999&rows=24", "?cols=abc&rows=24", "?cols=-80&rows=24", "?cols=65616&rows=65560", "?cols=80"} {
		ws := e.dial(admin, q)
		if got := ws.run("stty size"); got != "24 80" {
			t.Errorf("query %q: size = %q, want \"24 80\"", q, got)
		}
		ws.conn.Close()
		e.waitTotal(0)
	}
}

func TestSessionControlMessages(t *testing.T) {
	requireRoot(t)
	e := newEnv(t, true)
	admin := e.login("yonetici", auth.RoleAdmin)
	ws := e.dial(admin, "?cols=100&rows=30")
	if got := ws.run("stty size"); got != "30 100" {
		t.Fatalf("initial size = %q", got)
	}
	ignored := []string{
		`{"type":"resize","cols":1,"rows":24}`,
		`{"type":"resize","cols":80,"rows":1}`,
		`{"type":"resize","cols":0,"rows":0}`,
		`{"type":"resize","cols":501,"rows":24}`,
		`{"type":"resize","cols":80,"rows":501}`,
		`{"type":"resize","cols":-80,"rows":24}`,
		`{"type":"resize","cols":65616,"rows":65560}`, // 80x24 if truncated to 16 bits
		`{"type":"resize","cols":4294967376,"rows":24}`,
		`{"type":"resize","cols":1e400,"rows":24}`,
		`{"type":"resize","cols":"80","rows":"24"}`,
		`{"type":"resize","cols":80.5,"rows":24}`,
		`{"type":"resize"}`,
		`{"type":"resize","cols":80}`,
		`{"type":"RESIZE","cols":80,"rows":24}`,
		`{"type":"exec","cmd":"id"}`,
		`{"type":"","cols":80,"rows":24}`,
		`{"type":5,"cols":80,"rows":24}`,
		`{"cols":80,"rows":24}`,
		`{`, ``, `null`, `[]`, `"resize"`, `42`, `not json`,
		`{"type":"resize","cols":80,"rows":24`,
		"\x00\x01\x02",
		`{"type":"resize","cols":80,"rows":24}{"type":"resize","cols":80,"rows":24}`,
		strings.Repeat(`{"a":`, 5000) + "1" + strings.Repeat("}", 5000),
	}
	for _, msg := range ignored {
		ws.sendText(msg)
	}
	ws.sendText(`{"type":"ping"}`)
	ws.waitText("pong")
	if got := ws.run("stty size"); got != "30 100" {
		t.Errorf("size after invalid control messages = %q, want \"30 100\"", got)
	}
	// Control messages are never typed into the shell.
	if got := ws.run("echo alive"); got != "alive" {
		t.Errorf("shell output = %q after control messages", got)
	}

	for _, c := range []struct {
		msg, want string
	}{
		{`{"type":"resize","cols":2,"rows":2}`, "2 2"},
		{`{"type":"resize","cols":500,"rows":500}`, "500 500"},
		{`{"type":"resize","cols":90,"rows":25,"extra":true}`, "25 90"},
	} {
		ws.sendText(c.msg)
		ws.sendText(`{"type":"ping"}`)
		ws.waitText("pong")
		if got := ws.run("stty size"); got != c.want {
			t.Errorf("after %s: size = %q, want %q", c.msg, got, c.want)
		}
	}
	if e.total() != 1 {
		t.Errorf("open sessions = %d, want 1", e.total())
	}
}

func TestSessionOversizedFrame(t *testing.T) {
	requireRoot(t)
	e := newEnv(t, true)
	admin := e.login("yonetici", auth.RoleAdmin)

	for _, mt := range []int{websocket.TextMessage, websocket.BinaryMessage} {
		ws := e.dial(admin, "")
		// A frame of exactly the limit is accepted.
		if mt == websocket.TextMessage {
			ws.sendText(`{"type":"x","pad":"` + strings.Repeat("a", readLimit-22) + `"}`)
		}
		if got := ws.run("echo ok"); got != "ok" {
			t.Fatalf("shell output = %q", got)
		}
		pids, _ := e.spawned()
		pid := pids[len(pids)-1]

		big := bytes.Repeat([]byte("a"), readLimit+1)
		if err := ws.conn.WriteMessage(mt, big); err != nil {
			t.Fatalf("send: %v", err)
		}
		ws.waitClosed()
		e.waitTotal(0)
		e.waitGroupGone(pid)
		detail, _, _, _ := e.lastAudit("terminal.close")
		if !strings.Contains(detail, "neden="+endClient) {
			t.Errorf("terminal.close detail = %q, want reason %s", detail, endClient)
		}
	}
}

func TestSessionClientDisconnect(t *testing.T) {
	requireRoot(t)
	e := newEnv(t, true)
	admin := e.login("yonetici", auth.RoleAdmin)
	ws := e.dial(admin, "")
	// A foreground program that ignores the end of input.
	ws.send("sleep 300\n")
	ws.waitOutput("sleep 300")
	pids, _ := e.spawned()
	ws.conn.Close()
	e.waitTotal(0)
	e.waitGroupGone(pids[0])
	detail, ok, _, _ := e.lastAudit("terminal.close")
	if !ok || !strings.Contains(detail, "neden="+endClient) {
		t.Errorf("terminal.close audit: success=%v detail=%q", ok, detail)
	}
}

func TestSessionLimits(t *testing.T) {
	requireRoot(t)
	e := newEnv(t, true)
	a := e.login("yonetici", auth.RoleAdmin)
	b := e.login("ikinci", auth.RoleAdmin)
	c := e.login("ucuncu", auth.RoleAdmin)

	var open []*wsClient
	for i := 0; i < maxSessionsPerUser; i++ {
		open = append(open, e.dial(a, ""))
		e.waitTotal(i + 1)
	}
	own := e.dial(a, "").expectEnd("session_limit", websocket.ClosePolicyViolation)
	if e.total() != maxSessionsPerUser {
		t.Fatalf("open sessions = %d after a refusal, want %d", e.total(), maxSessionsPerUser)
	}
	for i := 0; i < maxSessionsTotal-maxSessionsPerUser; i++ {
		open = append(open, e.dial(b, ""))
		e.waitTotal(maxSessionsPerUser + i + 1)
	}
	all := e.dial(c, "").expectEnd("session_limit", websocket.ClosePolicyViolation)
	if own.Message == all.Message {
		t.Error("per-user and total refusals carry the same message")
	}
	if pids, _ := e.spawned(); len(pids) != maxSessionsTotal {
		t.Errorf("%d processes started, want %d (refused sessions must start none)", len(pids), maxSessionsTotal)
	}

	var st statusResponse
	_, _, data := e.get("/api/v1/terminal/status", a)
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	if st.ActiveSessions != maxSessionsTotal || st.OwnSessions != maxSessionsPerUser {
		t.Errorf("status: active=%d own=%d", st.ActiveSessions, st.OwnSessions)
	}

	// The open sessions work, and closing one frees exactly one slot.
	if got := open[0].run("echo first"); got != "first" {
		t.Errorf("first session output = %q", got)
	}
	open[0].send("exit\n")
	open[0].expectEnd(endExit, websocket.CloseNormalClosure)
	e.waitTotal(maxSessionsTotal - 1)
	third := e.dial(c, "")
	if got := third.run("echo third"); got != "third" {
		t.Errorf("third user's output = %q", got)
	}
	e.dial(a, "").expectEnd("session_limit", websocket.ClosePolicyViolation)

	for _, ws := range append(open[1:], third) {
		ws.conn.Close()
	}
	e.waitTotal(0)
	e.m.mu.Lock()
	left := len(e.m.sessions)
	e.m.mu.Unlock()
	if left != 0 {
		t.Errorf("%d users still recorded after all sessions closed", left)
	}
	pids, _ := e.spawned()
	for _, pid := range pids {
		e.waitGroupGone(pid)
	}
}

// ---- supervisor, driven by the fake clock ----

func startSupervised(t *testing.T) (*testEnv, *wsClient, int) {
	t.Helper()
	requireRoot(t)
	e := newEnv(t, true)
	admin := e.login("yonetici", auth.RoleAdmin)
	ws := e.dial(admin, "")
	if got := ws.run("echo up"); got != "up" {
		t.Fatalf("shell output = %q", got)
	}
	pids, _ := e.spawned()
	e.clk.mu.Lock()
	tickD, timerD := e.clk.tickD, e.clk.timerD
	e.clk.mu.Unlock()
	if tickD != time.Second {
		t.Errorf("supervisor tick = %v, want 1s", tickD)
	}
	if timerD != 12*time.Hour {
		t.Errorf("lifetime limit = %v, want 12h", timerD)
	}
	return e, ws, pids[0]
}

func (e *testEnv) ticks(n int) {
	e.t.Helper()
	for i := 0; i < n; i++ {
		if !e.clk.tick() {
			e.t.Fatalf("tick %d of %d was not taken: the session has ended", i+1, n)
		}
	}
}

func (e *testEnv) expectClosed(ws *wsClient, pid int, reason string, closeCode int) serverMessage {
	e.t.Helper()
	final := ws.expectEnd(reason, closeCode)
	e.waitTotal(0)
	e.waitGroupGone(pid)
	detail, ok, target, _ := e.lastAudit("terminal.close")
	if !ok || target != "ubuntu" || !strings.Contains(detail, "neden="+reason) {
		e.t.Errorf("terminal.close audit: success=%v target=%q detail=%q", ok, target, detail)
	}
	if e.clk.tick() {
		e.t.Error("the supervisor is still running after the session ended")
	}
	return final
}

func TestSessionEndsWhenDisabled(t *testing.T) {
	e, ws, pid := startSupervised(t)
	e.ticks(6)
	ws.noText("error")
	e.set(settings.KeyTerminalEnabled, "false")
	e.clk.tick()
	e.clk.tick() // the setting is read every second tick
	e.expectClosed(ws, pid, endDisabled, websocket.ClosePolicyViolation)
}

func TestSessionEndsWhenUserChanges(t *testing.T) {
	e, ws, pid := startSupervised(t)
	e.ticks(4)
	// Insignificant whitespace is not a change.
	e.set(settings.KeyTerminalUser, " ubuntu ")
	e.ticks(4)
	ws.noText("error")
	e.set(settings.KeyTerminalUser, "ayse")
	e.clk.tick()
	e.clk.tick()
	e.expectClosed(ws, pid, endUserChanged, websocket.ClosePolicyViolation)
}

func TestSessionEndsWhenUserCleared(t *testing.T) {
	e, ws, pid := startSupervised(t)
	e.set(settings.KeyTerminalUser, "")
	e.clk.tick()
	e.clk.tick()
	e.expectClosed(ws, pid, endUserChanged, websocket.ClosePolicyViolation)
}

func TestSessionEndsWhenPanelSessionEnds(t *testing.T) {
	cases := []struct {
		name  string
		query string
	}{
		{"logout", `DELETE FROM sessions`},
		{"expiry", `UPDATE sessions SET expires_at = 1`},
		{"user disabled", `UPDATE users SET disabled = 1`},
		{"role changed", `UPDATE users SET role = 'user'`},
		{"user deleted", `DELETE FROM users`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, ws, pid := startSupervised(t)
			e.ticks(29)
			e.exec(c.query)
			// The check runs on the 30th tick and is confirmed once, a
			// few ticks later, before the terminal is closed.
			for i := 0; i < 6 && e.clk.tick(); i++ {
			}
			e.expectClosed(ws, pid, endSession, websocket.ClosePolicyViolation)
		})
	}
}

func TestSessionSurvivesTransientAuthFailure(t *testing.T) {
	e, ws, _ := startSupervised(t)
	e.ticks(29)
	e.exec(`UPDATE sessions SET expires_at = 1`)
	e.ticks(1) // first failed check
	e.exec(`UPDATE sessions SET expires_at = ?`, time.Now().Unix()+3600)
	e.ticks(40) // the repeated check succeeds, as does the regular one at 60
	ws.noText("error")
	if got := ws.run("echo still"); got != "still" {
		t.Errorf("shell output = %q", got)
	}
	// The failure count was reset: a later single failure does not add up.
	e.ticks(19)
	e.exec(`UPDATE sessions SET expires_at = 1`)
	e.ticks(1) // tick 90: fails once
	e.exec(`UPDATE sessions SET expires_at = ?`, time.Now().Unix()+3600)
	e.ticks(10)
	ws.noText("error")
	if got := ws.run("echo again"); got != "again" {
		t.Errorf("shell output = %q", got)
	}
}

func TestSessionRefusedWhenPanelSessionEndedAtConnect(t *testing.T) {
	// The handshake is authenticated by the middleware; the handler checks
	// again after its own validation. Removing the session in between needs
	// a hook the code does not have, so this covers the ordinary case: a
	// session that ended before the handshake is refused by the middleware.
	e := newEnv(t, true)
	admin := e.login("yonetici", auth.RoleAdmin)
	e.exec(`DELETE FROM sessions`)
	conn, res, err := e.dialRaw(admin, "http://"+e.host(), "")
	if err == nil {
		conn.Close()
		t.Fatal("the WebSocket was upgraded")
	}
	if res == nil || res.StatusCode != http.StatusUnauthorized {
		t.Errorf("response = %+v", res)
	}
}

func TestSessionIdleTimeout(t *testing.T) {
	requireRoot(t)
	e := newEnv(t, true)
	e.set(settings.KeyTerminalTimeout, "1")
	admin := e.login("yonetici", auth.RoleAdmin)
	ws := e.dial(admin, "")
	if got := ws.run("echo up"); got != "up" {
		t.Fatalf("shell output = %q", got)
	}
	pids, _ := e.spawned()

	// With a one minute limit the warning comes 30 seconds before the end.
	e.clk.advance(29 * time.Second)
	e.ticks(1)
	ws.noText("idle_warning")
	e.clk.advance(2 * time.Second) // idle for 31 s
	e.ticks(1)
	w := ws.waitText("idle_warning")
	if w.Seconds == nil || *w.Seconds != 29 {
		t.Errorf("idle warning = %+v, want 29 seconds left", w)
	}
	e.clk.advance(10 * time.Second)
	e.ticks(1)
	ws.noText("idle_warning") // warned once

	// Output alone does not count as activity, input does.
	if got := ws.run("echo active"); got != "active" {
		t.Fatalf("shell output = %q", got)
	}
	e.clk.advance(29 * time.Second) // 70 s after connecting, 29 s after the input
	e.ticks(1)
	ws.noText("idle_warning")
	ws.noText("error")
	e.clk.advance(30 * time.Second) // idle for 59 s
	e.ticks(1)
	w = ws.waitText("idle_warning") // the warning is armed again after input
	if w.Seconds == nil || *w.Seconds != 1 {
		t.Errorf("second idle warning = %+v, want 1 second left", w)
	}
	e.clk.advance(time.Second) // idle for 60 s
	e.clk.tick()
	final := e.expectClosed(ws, pids[0], endIdle, websocket.ClosePolicyViolation)
	if !strings.Contains(final.Message, "1 dakika") {
		t.Errorf("idle message = %q, want it to name 1 minute", final.Message)
	}
}

func TestSessionIdleTimeoutFollowsSetting(t *testing.T) {
	e, ws, pid := startSupervised(t) // default: 15 minutes, warning 60 s before
	e.clk.advance(14*time.Minute - time.Second)
	e.ticks(1)
	ws.noText("idle_warning")
	e.clk.advance(time.Second)
	e.ticks(1)
	w := ws.waitText("idle_warning")
	if w.Seconds == nil || *w.Seconds != 60 {
		t.Errorf("idle warning = %+v, want 60 seconds left", w)
	}
	// Control messages are not terminal input and do not postpone the end.
	ws.sendText(`{"type":"ping"}`)
	ws.waitText("pong")
	ws.sendText(`{"type":"resize","cols":90,"rows":30}`)
	ws.sendText(`{"type":"ping"}`)
	ws.waitText("pong")
	e.clk.advance(59 * time.Second)
	e.ticks(1)
	ws.noText("error")
	// Shortening the limit in Settings applies to the open terminal.
	e.set(settings.KeyTerminalTimeout, "10")
	e.clk.tick()
	final := e.expectClosed(ws, pid, endIdle, websocket.ClosePolicyViolation)
	if !strings.Contains(final.Message, "10 dakika") {
		t.Errorf("idle message = %q, want it to name 10 minutes", final.Message)
	}
}

func TestSessionMaximumLength(t *testing.T) {
	e, ws, pid := startSupervised(t)
	e.ticks(3)
	select {
	case e.clk.timers <- e.clk.now():
	case <-time.After(waitFor):
		t.Fatal("the supervisor does not wait for the lifetime limit")
	}
	final := e.expectClosed(ws, pid, endMaxLength, websocket.ClosePolicyViolation)
	if !strings.Contains(final.Message, "12 saat") {
		t.Errorf("message = %q, want it to name 12 hours", final.Message)
	}
}

func TestSessionEndsOnShutdown(t *testing.T) {
	e, ws, pid := startSupervised(t)
	ws2 := e.dial(e.login("ikinci", auth.RoleAdmin), "")
	if got := ws2.run("echo up"); got != "up" {
		t.Fatalf("shell output = %q", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.m.Start(ctx); close(done) }()
	select {
	case <-done:
		t.Fatal("Start returned before shutdown")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(waitFor):
		t.Fatal("Start did not return after shutdown")
	}
	// Start returns only after the shells were reaped.
	if e.total() != 0 {
		t.Errorf("open sessions = %d after Start returned", e.total())
	}
	ws.expectEnd(endShutdown, websocket.CloseGoingAway)
	ws2.expectEnd(endShutdown, websocket.CloseGoingAway)
	pids, _ := e.spawned()
	for _, p := range append(pids, pid) {
		e.waitGroupGone(p)
	}
	// A terminal requested during shutdown is not started.
	e.dial(e.login("ucuncu", auth.RoleAdmin), "").waitClosed()
	e.waitTotal(0)
	for _, p := range func() []int { p, _ := e.spawned(); return p }() {
		e.waitGroupGone(p)
	}
}

// ---- leaks ----

// openFDs lists the process's file descriptors. The descriptor used for
// the listing itself and the database pool's connections (opened and closed
// on demand by database/sql) are left out.
func openFDs(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("read /proc/self/fd: %v", err)
	}
	var out []string
	for _, de := range entries {
		target, err := os.Readlink("/proc/self/fd/" + de.Name())
		if err != nil || strings.Contains(target, "/data/test.db") {
			continue
		}
		out = append(out, de.Name()+"->"+target)
	}
	return out
}

func TestSessionLeavesNothingBehind(t *testing.T) {
	requireRoot(t)
	e := newEnv(t, false)
	admin := e.login("yonetici", auth.RoleAdmin)
	transport := &http.Transport{}
	client := &http.Client{Transport: transport}

	round := func(n int) {
		var open []*wsClient
		for i := 0; i < n; i++ {
			ws := e.dial(admin, "")
			if got := ws.run("stty size"); got != "24 80" {
				t.Fatalf("size = %q", got)
			}
			ws.sendText(`{"type":"resize","cols":100,"rows":30}`)
			open = append(open, ws)
		}
		// One ends by itself, one is closed by the client, one is refused.
		open[0].send("exit\n")
		open[0].expectEnd(endExit, websocket.CloseNormalClosure)
		for _, ws := range open {
			ws.conn.Close()
		}
		e.waitTotal(0)
		e.set(settings.KeyTerminalEnabled, "false")
		ws := e.dial(admin, "")
		ws.expectEnd(endDisabled, websocket.ClosePolicyViolation)
		ws.conn.Close()
		e.set(settings.KeyTerminalEnabled, "true")
		for _, ws := range open {
			ws.waitClosed()
		}
		req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/api/v1/terminal/status", nil)
		req.AddCookie(&http.Cookie{Name: "myserver_session", Value: admin})
		if res, err := client.Do(req); err == nil {
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
		transport.CloseIdleConnections()
	}
	settle := func() (fds, goroutines int) {
		for i := 0; i < 20; i++ {
			runtime.GC()
			time.Sleep(50 * time.Millisecond)
		}
		return len(openFDs(t)), runtime.NumGoroutine()
	}

	round(3) // warm-up: database connections, lazily started goroutines
	fds0, gor0 := settle()
	before := openFDs(t)
	for i := 0; i < 4; i++ {
		round(3)
	}
	var fds1, gor1 int
	deadline := time.Now().Add(waitFor)
	for {
		fds1, gor1 = settle()
		if (fds1 <= fds0 && gor1 <= gor0) || time.Now().After(deadline) {
			break
		}
	}
	if fds1 > fds0 {
		t.Errorf("file descriptors: %d before, %d after 12 sessions\nbefore: %v\nafter:  %v",
			fds0, fds1, before, openFDs(t))
	}
	if gor1 > gor0 {
		buf := make([]byte, 1<<20)
		buf = buf[:runtime.Stack(buf, true)]
		t.Errorf("goroutines: %d before, %d after 12 sessions\n%s", gor0, gor1, buf)
	}
	// The status request also runs the helper once ("ping").
	pids, args := e.spawned()
	shells := 0
	for i, pid := range pids {
		if args[i] == "terminal-shell ubuntu" {
			shells++
		}
		e.waitGroupGone(pid)
	}
	if shells != 15 {
		t.Errorf("%d shells started, want 15: %v", shells, args)
	}
}

// ---- small pieces ----

func TestMarkerBuffer(t *testing.T) {
	b := &markerBuffer{started: make(chan struct{})}
	isStarted := func() bool {
		select {
		case <-b.started:
			return true
		default:
			return false
		}
	}
	b.Write([]byte("sudo: something\n" + privileged.UserMessagePrefix + "ilk\n"))
	if isStarted() {
		t.Fatal("started without the marker")
	}
	// The marker may arrive split across writes.
	half := len(termcheck.StartedMarker) / 2
	b.Write([]byte(termcheck.StartedMarker[:half]))
	if isStarted() {
		t.Fatal("started on half a marker")
	}
	b.Write([]byte(termcheck.StartedMarker[half:] + "\n"))
	if !isStarted() {
		t.Fatal("marker not recognised")
	}
	b.Write([]byte(termcheck.StartedMarker + "\n")) // a second marker must not panic
	b.Write([]byte("  " + privileged.UserMessagePrefix + "son mesaj\r\n"))
	if got := b.userMessage(); got != "son mesaj" {
		t.Errorf("user message = %q, want the last one", got)
	}

	// The buffer is bounded, and writes always report full success.
	big := &markerBuffer{started: make(chan struct{})}
	chunk := bytes.Repeat([]byte("x"), 5000)
	for i := 0; i < 10; i++ {
		if n, err := big.Write(chunk); n != len(chunk) || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	if big.buf.Len() != stderrKeep {
		t.Errorf("buffer holds %d bytes, want %d", big.buf.Len(), stderrKeep)
	}
	if big.userMessage() != "" {
		t.Errorf("user message = %q, want none", big.userMessage())
	}
}

func TestExitCode(t *testing.T) {
	if exitCode(nil) != 0 {
		t.Error("nil error is not exit code 0")
	}
	if exitCode(errors.New("other")) != -1 {
		t.Error("an unrelated error is not reported as -1")
	}
}

func TestEndMessages(t *testing.T) {
	seen := map[string]string{}
	for _, r := range []string{endDisabled, endUserChanged, endSession, endIdle, endMaxLength, endShutdown, endIO} {
		msg := endMessage(r, 15*time.Minute)
		if msg == "" || msg == endMessage("unknown", 0) {
			t.Errorf("reason %q has no message of its own: %q", r, msg)
		}
		if prev, dup := seen[msg]; dup {
			t.Errorf("reasons %q and %q share a message", r, prev)
		}
		seen[msg] = r
	}
	if msg := endMessage(endIdle, 15*time.Minute); !strings.Contains(msg, "15 dakika") {
		t.Errorf("idle message = %q", msg)
	}
}

func TestParseSize(t *testing.T) {
	for q, want := range map[string][2]int{
		"cols=100&rows=30": {100, 30}, "cols=2&rows=2": {2, 2}, "cols=500&rows=500": {500, 500},
		"": {80, 24}, "cols=1&rows=30": {80, 24}, "cols=501&rows=30": {80, 24},
		"cols=100": {80, 24}, "cols=1e2&rows=30": {80, 24}, "cols=0x50&rows=30": {80, 24},
		"cols=100&rows=-30": {80, 24}, "cols=99999999999999999999&rows=30": {80, 24},
	} {
		r := httptest.NewRequest(http.MethodGet, "/ws?"+q, nil)
		cols, rows := parseSize(r)
		if cols != want[0] || rows != want[1] {
			t.Errorf("parseSize(%q) = %d, %d; want %d, %d", q, cols, rows, want[0], want[1])
		}
	}
}

// In production the helper runs as root through sudo, so the panel cannot
// signal it: closing the PTY is the only way to end the shell. The stand-in
// helper ignores the panel's signals to reproduce that.
func TestSessionHangsUpIdleTerminal(t *testing.T) {
	requireRoot(t)
	script := "#!/bin/sh\ntrap '' HUP TERM INT\n" + strings.TrimPrefix(helperScript, "#!/bin/sh\n")
	for _, how := range []string{"client disconnect", "terminal disabled"} {
		t.Run(how, func(t *testing.T) {
			e := newEnv(t, true)
			if err := os.WriteFile(e.helper, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			admin := e.login("yonetici", auth.RoleAdmin)
			ws := e.dial(admin, "")
			ws.t = t
			if got := ws.run("echo up"); got != "up" {
				t.Fatalf("shell output = %q", got)
			}
			pids, _ := e.spawned()
			// The shell now waits at its prompt and prints nothing.
			start := time.Now()
			if how == "client disconnect" {
				ws.conn.Close()
			} else {
				e.set(settings.KeyTerminalEnabled, "false")
				e.clk.tick()
				e.clk.tick()
			}
			e.waitTotal(0)
			took := time.Since(start)
			e.waitGroupGone(pids[0])
			if took >= killGrace {
				t.Errorf("the session took %v to end: closing the PTY did not hang up the shell", took)
			}
			detail, ok, _, _ := e.lastAudit("terminal.close")
			if !ok || strings.Contains(detail, "çıkış_kodu=-1") {
				t.Errorf("terminal.close audit: success=%v detail=%q", ok, detail)
			}
		})
	}
}
