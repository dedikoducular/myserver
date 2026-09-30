package docker

import (
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"myserver/internal/module"
)

/* ---------- route enumeration ---------- */

// The router does not expose what was registered, so the routes are read
// from the source of Register itself. A route added there later is picked up
// by every test below without anyone having to remember it.
//
//go:embed docker.go
var moduleSource string

type route struct {
	method  string
	pattern string // below /api/v1, e.g. /docker/containers/{id}
	ws      bool
}

func (r route) key() string { return r.method + " " + r.pattern }

var (
	registerRe = regexp.MustCompile(`(?s)func \(m \*Module\) Register\(api, ws \*httpx\.Router\) \{\n(.*?)\n\}\n`)
	groupRe    = regexp.MustCompile(`^(\w+) := api\.Group\("([^"]*)"`)
	routeRe    = regexp.MustCompile(`^(\w+)\.(Get|Post|Put|Delete)\("([^"]*)"`)
	rawRe      = regexp.MustCompile(`^(\w+)\.Raw\(http\.Method(\w+), "([^"]*)"`)
	callRe     = regexp.MustCompile(`\.(Get|Post|Put|Delete|Handle|Raw)\(`)
)

func registeredRoutes(t *testing.T) []route {
	t.Helper()
	m := registerRe.FindStringSubmatch(moduleSource)
	if m == nil {
		t.Fatal("Register function not found in docker.go")
	}
	prefixes := map[string]string{"ws": ""}
	var out []route
	for _, line := range strings.Split(m[1], "\n") {
		line = strings.TrimSpace(line)
		if g := groupRe.FindStringSubmatch(line); g != nil {
			prefixes[g[1]] = g[2]
			continue
		}
		var r route
		var group string
		if x := routeRe.FindStringSubmatch(line); x != nil {
			group, r = x[1], route{method: strings.ToUpper(x[2]), pattern: x[3]}
		} else if x := rawRe.FindStringSubmatch(line); x != nil {
			group, r = x[1], route{method: strings.ToUpper(x[2]), pattern: x[3], ws: x[1] == "ws"}
		} else {
			if callRe.MatchString(line) && !strings.HasPrefix(line, "//") {
				t.Fatalf("route registration not understood by the test: %s", line)
			}
			continue
		}
		prefix, ok := prefixes[group]
		if !ok {
			t.Fatalf("unknown router %q in: %s", group, line)
		}
		r.pattern = prefix + r.pattern
		out = append(out, r)
	}
	if len(out) < 20 {
		t.Fatalf("only %d routes found, the parser is broken", len(out))
	}
	return out
}

// requestFor fills a route pattern with valid identifiers and a valid body,
// so that the request passes input validation and reaches Docker.
func requestFor(t *testing.T, r route) (path, body string) {
	t.Helper()
	path = r.pattern
	switch {
	case strings.HasPrefix(path, "/docker/images/pull/"):
		path = strings.ReplaceAll(path, "{job}", "0123456789abcdef")
	case strings.HasPrefix(path, "/docker/images/"):
		path = strings.ReplaceAll(path, "{id}", "1a2b3c4d5e6f")
	}
	path = strings.NewReplacer("{id}", "web", "{name}", "data").Replace(path)
	if strings.ContainsAny(path, "{}") {
		t.Fatalf("test does not know how to fill %q", r.pattern)
	}
	switch r.key() {
	case "POST /docker/containers/{id}/remove":
		body = `{"force":false,"remove_volumes":false}`
	case "POST /docker/images/pull":
		body = `{"image":"nginx:latest"}`
	case "POST /docker/volumes/prune":
		body = `{"names":["data"],"confirm":true}`
	}
	return "/api/v1" + path, body
}

func TestRouteEnumerationMatchesTheRouter(t *testing.T) {
	e := newTestEnv(t, "unix:///nonexistent/docker.sock")
	seen := map[string]bool{}
	for _, r := range registeredRoutes(t) {
		if seen[r.key()] {
			t.Errorf("route %s listed twice", r.key())
		}
		seen[r.key()] = true
		path, _ := requestFor(t, r)
		req := httptest.NewRequest(r.method, "http://"+testHost+path, nil)
		_, pattern := e.routerMux().Handler(req)
		if want := r.method + " /api/v1" + r.pattern; pattern != want {
			t.Errorf("%s resolves to pattern %q, want %q", r.key(), pattern, want)
		}
	}
}

// routerMux digs the ServeMux out of the handler chain built by newTestEnv.
func (e *testEnv) routerMux() *http.ServeMux { return e.mux }

/* ---------- authorisation ---------- */

// openRoutes is the complete list of what a signed-in user without the admin
// role may call: read-only listings that contain no secrets (labels are
// masked). Everything else Register mounts must be admin only.
var openRoutes = map[string]bool{
	"GET /docker/info":         true,
	"GET /docker/containers":   true,
	"GET /docker/stats/stream": true,
	"GET /docker/images":       true,
	"GET /docker/volumes":      true,
	"GET /docker/networks":     true,
}

func TestAuthorisation(t *testing.T) {
	fake := newFakeDocker(t)
	e := newTestEnv(t, fake.host())
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	origin := srv.URL

	// call sends a real HTTP request and returns status and error code.
	// Streams are closed as soon as the status is known.
	call := func(r route, who *creds) (int, string) {
		t.Helper()
		path, body := requestFor(t, r)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, r.method, srv.URL+path, rd)
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if r.ws {
			req.Header.Set("Origin", origin)
		}
		sign(req, who)
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatalf("%s: %v", r.key(), err)
		}
		defer resp.Body.Close()
		code := ""
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			var env envelope
			if json.Unmarshal(b, &env) == nil {
				code = errorCode(env)
			}
		}
		return resp.StatusCode, code
	}

	routes := registeredRoutes(t)
	mustBeAdminOnly := []string{
		"GET /docker/containers/{id}",
		"GET /docker/containers/{id}/logs/stream",
		"GET /docker/containers/{id}/exec/ws",
	}
	for _, k := range mustBeAdminOnly {
		found := false
		for _, r := range routes {
			found = found || r.key() == k
		}
		if !found {
			t.Errorf("route %s is not registered any more; update the test", k)
		}
	}
	for k := range openRoutes {
		found := false
		for _, r := range routes {
			found = found || r.key() == k
		}
		if !found {
			t.Errorf("open route %s is not registered any more; update the test", k)
		}
	}

	t.Run("anonymous", func(t *testing.T) {
		for _, r := range routes {
			status, code := call(r, nil)
			if status != 401 || code != "unauthorized" {
				t.Errorf("%s without a session: status %d code %q, want 401 unauthorized", r.key(), status, code)
			}
		}
		if got := fake.seen(); len(got) != 0 {
			t.Errorf("anonymous requests reached Docker: %v", got)
		}
	})

	t.Run("normal user is refused", func(t *testing.T) {
		n := 0
		for _, r := range routes {
			if openRoutes[r.key()] {
				continue
			}
			n++
			if r.method != "GET" && !strings.Contains("POST DELETE PUT", r.method) {
				t.Errorf("unexpected method in %s", r.key())
			}
			status, code := call(r, e.user)
			if status != 403 || code != "forbidden" {
				t.Errorf("%s as a normal user: status %d code %q, want 403 forbidden", r.key(), status, code)
			}
		}
		if n < 15 {
			t.Errorf("only %d admin routes were checked", n)
		}
		if got := fake.seen(); len(got) != 0 {
			t.Errorf("refused requests reached Docker: %v", got)
		}
		for _, a := range e.auditRows() {
			t.Errorf("refused request left an audit record of an action: %+v", a)
		}
	})

	t.Run("state changing routes are never open", func(t *testing.T) {
		for k := range openRoutes {
			if !strings.HasPrefix(k, "GET ") {
				t.Errorf("open route %s changes state", k)
			}
		}
	})

	t.Run("csrf token is required from admins too", func(t *testing.T) {
		for _, r := range routes {
			if r.method == "GET" {
				continue
			}
			noCSRF := &creds{token: e.admin.token, csrf: "wrong"}
			status, code := call(r, noCSRF)
			if status != 403 || code != "csrf_invalid" {
				t.Errorf("%s with a wrong CSRF token: status %d code %q", r.key(), status, code)
			}
		}
		if got := fake.seen(); len(got) != 0 {
			t.Errorf("requests without CSRF token reached Docker: %v", got)
		}
	})

	t.Run("normal user may read listings", func(t *testing.T) {
		fake.handleJSON("GET /containers/json", 200, `[]`)
		fake.handleJSON("GET /images/json", 200, `[]`)
		fake.handleJSON("GET /volumes", 200, `{"Volumes":[],"Warnings":null}`)
		fake.handleJSON("GET /networks", 200, `[]`)
		fake.handleJSON("GET /info", 200, `{"ServerVersion":"27.3.1","NCPU":4}`)
		for _, r := range routes {
			if !openRoutes[r.key()] {
				continue
			}
			if status, code := call(r, e.user); status != 200 {
				t.Errorf("%s as a normal user: status %d code %q, want 200", r.key(), status, code)
			}
		}
	})

	t.Run("admin passes the role check", func(t *testing.T) {
		for _, r := range routes {
			status, code := call(r, e.admin)
			if status == 401 || status == 403 {
				t.Errorf("%s as admin: status %d code %q", r.key(), status, code)
			}
		}
	})
}

func TestWebSocketRouteNeedsSameOrigin(t *testing.T) {
	e := newTestEnv(t, "unix:///nonexistent/docker.sock")
	for _, origin := range []string{"", "http://evil.example", "http://panel.test"} {
		r := httptest.NewRequest("GET", "http://"+testHost+"/api/v1/docker/containers/web/exec/ws", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		sign(r, e.admin)
		w := httptest.NewRecorder()
		e.h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Errorf("origin %q: status %d, want 403", origin, w.Code)
		}
	}
}

/* ---------- Docker unreachable ---------- */

// Endpoints that cannot answer 503, with the reason:
//   - the stats stream is an SSE stream that stays open and reports
//     "available": false, so the page recovers without reconnecting;
//   - the pull progress stream never talks to Docker, it reads a job kept in
//     memory (and a job cannot be created while Docker is down);
//   - the exec endpoint is a WebSocket, whose failure is reported inside the
//     socket because a browser cannot read a rejected handshake.
//
// Each of them has its own test below.
var notPlainHTTP = map[string]bool{
	"GET /docker/stats/stream":             true,
	"GET /docker/images/pull/{job}/stream": true,
	"GET /docker/containers/{id}/exec/ws":  true,
}

func unreachableHosts(t *testing.T) map[string]string {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-socket")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"socket does not exist":      "unix://" + filepath.Join(dir, "missing", "docker.sock"),
		"path is a regular file":     "unix://" + file,
		"tcp port refuses":           refusedTCPHost(t),
		"host value cannot be used":  "bogus://???",
		"host value is empty string": "unix://",
	}
}

func TestEveryEndpointAnswers503WhenDockerIsUnreachable(t *testing.T) {
	for name, host := range unreachableHosts(t) {
		t.Run(name, func(t *testing.T) {
			e := newTestEnv(t, host) // the module constructs and registers
			if e.mod.Name() != "docker" {
				t.Errorf("Name() = %q", e.mod.Name())
			}
			checked := 0
			for _, r := range registeredRoutes(t) {
				if notPlainHTTP[r.key()] {
					continue
				}
				checked++
				path, body := requestFor(t, r)
				start := time.Now()
				w := e.do(r.method, path, e.admin, body)
				took := time.Since(start)
				env := parseEnvelope(t, w.Body.Bytes())
				if w.Code != 503 || errorCode(env) != "docker_unavailable" {
					t.Errorf("%s: status %d body %s", r.key(), w.Code, strings.TrimSpace(w.Body.String()))
					continue
				}
				if env.Error.Message != "Docker servisine ulaşılamıyor." {
					t.Errorf("%s: message %q", r.key(), env.Error.Message)
				}
				if env.Success || string(env.Data) != "null" {
					t.Errorf("%s: envelope %s", r.key(), w.Body)
				}
				if took > 3*time.Second {
					t.Errorf("%s: answered after %v", r.key(), took)
				}
			}
			if checked < 18 {
				t.Errorf("only %d endpoints checked", checked)
			}
		})
	}
}

func TestReadOnlyEndpointsAnswer503ForNormalUsers(t *testing.T) {
	e := newTestEnv(t, "unix:///nonexistent/docker.sock")
	for _, p := range []string{"info", "containers", "images", "volumes", "networks"} {
		w := e.do("GET", "/api/v1/docker/"+p, e.user, "")
		if w.Code != 503 || errorCode(parseEnvelope(t, w.Body.Bytes())) != "docker_unavailable" {
			t.Errorf("%s: status %d body %s", p, w.Code, w.Body)
		}
	}
}

func TestErrorsDoNotExposeInternals(t *testing.T) {
	host := "unix:///nonexistent/secret-path/docker.sock"
	e := newTestEnv(t, host)
	w := e.do("GET", "/api/v1/docker/containers", e.admin, "")
	for _, leak := range []string{"secret-path", "dial", "no such file", "docker.sock", "goroutine"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Errorf("response %s contains %q", w.Body, leak)
		}
	}
}

func TestStatsStreamWhenDockerIsUnreachable(t *testing.T) {
	e := newTestEnv(t, "unix:///nonexistent/docker.sock")
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/docker/stats/stream", nil)
	sign(req, e.user)
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	ev := readSSEEvent(t, resp.Body, "stats")
	var snap StatsSnapshot
	if err := json.Unmarshal([]byte(ev.Data), &snap); err != nil {
		t.Fatalf("bad stats event %q: %v", ev.Data, err)
	}
	if snap.Available {
		t.Error("available must be false")
	}
	if !strings.Contains(ev.Data, `"items":[]`) {
		t.Errorf("items must be an empty list: %s", ev.Data)
	}
}

func TestPullStreamOfUnknownJob(t *testing.T) {
	e := newTestEnv(t, "unix:///nonexistent/docker.sock")
	w := e.do("GET", "/api/v1/docker/images/pull/0123456789abcdef/stream", e.admin, "")
	env := parseEnvelope(t, w.Body.Bytes())
	if w.Code != 404 || errorCode(env) != "not_found" {
		t.Errorf("status %d body %s", w.Code, w.Body)
	}
	w = e.do("GET", "/api/v1/docker/images/pull/not-a-job/stream", e.admin, "")
	if w.Code != 400 {
		t.Errorf("invalid job id: status %d body %s", w.Code, w.Body)
	}
}

func TestPullWhileDockerIsUnreachableCreatesNoJob(t *testing.T) {
	e := newTestEnv(t, "unix:///nonexistent/docker.sock")
	w := e.do("POST", "/api/v1/docker/images/pull", e.admin, `{"image":"nginx:latest"}`)
	if w.Code != 503 {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	e.mod.pulls.mu.Lock()
	n := len(e.mod.pulls.jobs)
	e.mod.pulls.mu.Unlock()
	if n != 0 {
		t.Errorf("%d pull jobs exist", n)
	}
}

func dialExec(t *testing.T, srv *httptest.Server, who *creds, id string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/docker/containers/" + id + "/exec/ws"
	h := http.Header{}
	h.Set("Origin", srv.URL)
	h.Set("Cookie", "myserver_session="+who.token)
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	return d.Dial(url, h)
}

func TestExecWhenDockerIsUnreachable(t *testing.T) {
	e := newTestEnv(t, "unix:///nonexistent/docker.sock")
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	conn, _, err := dialExec(t, srv, e.admin, "web")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	kind, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var msg execControl
	if kind != websocket.TextMessage || json.Unmarshal(data, &msg) != nil {
		t.Fatalf("message kind %d: %q", kind, data)
	}
	if msg.Type != "error" || msg.Message != "Docker servisine ulaşılamıyor." {
		t.Errorf("message = %+v", msg)
	}
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Error("the socket must be closed after the error")
	}
}

func TestExecRefusesNormalUsersAndBadIdentifiers(t *testing.T) {
	e := newTestEnv(t, "unix:///nonexistent/docker.sock")
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	if conn, resp, err := dialExec(t, srv, e.user, "web"); err == nil {
		conn.Close()
		t.Error("a normal user opened a container terminal")
	} else if resp == nil || resp.StatusCode != 403 {
		t.Errorf("normal user: %v, response %v", err, resp)
	}
	if conn, resp, err := dialExec(t, srv, e.admin, "-rf"); err == nil {
		conn.Close()
		t.Error("an option-like identifier was accepted")
	} else if resp == nil || resp.StatusCode != 400 {
		t.Errorf("bad identifier: %v, response %v", err, resp)
	}
	for _, a := range e.auditRows() {
		t.Errorf("unexpected audit record %+v", a)
	}
}

func TestHealthWhenDockerIsUnreachable(t *testing.T) {
	cases := []struct {
		name, host string
		status     module.HealthStatus
		message    string
	}{
		{"missing socket", "unix:///nonexistent/docker.sock", module.WarningLevel, "Docker kurulu değil veya hiç başlatılmamış."},
		{"refusing port", refusedTCPHost(t), module.CriticalLvl, "Docker servisine ulaşılamıyor."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newTestEnv(t, c.host)
			start := time.Now()
			got := e.mod.Health(context.Background())
			if time.Since(start) > 3*time.Second {
				t.Errorf("health probe took %v", time.Since(start))
			}
			if len(got) != 1 || got[0].ID != "docker.engine" || got[0].Status != c.status || got[0].Message != c.message {
				t.Errorf("health = %+v", got)
			}
		})
	}
}

func TestStartReturnsWhenCancelled(t *testing.T) {
	e := newTestEnv(t, "unix:///nonexistent/docker.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.mod.Start(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after its context was cancelled")
	}
}

/* ---------- input validation at the HTTP level ---------- */

func TestHostileIdentifiersNeverReachDocker(t *testing.T) {
	fake := newFakeDocker(t)
	e := newTestEnv(t, fake.host())
	ids := []string{"-rf", "--help", "..%2F..%2Fetc", "a%20b", "web%0A", "%2Fweb", "a%3Bb", strings.Repeat("a", 300), ".hidden"}
	templates := []struct{ method, path, body string }{
		{"GET", "/docker/containers/ID", ""},
		{"GET", "/docker/containers/ID/logs/stream", ""},
		{"POST", "/docker/containers/ID/start", ""},
		{"POST", "/docker/containers/ID/stop", ""},
		{"POST", "/docker/containers/ID/restart", ""},
		{"POST", "/docker/containers/ID/kill", ""},
		{"POST", "/docker/containers/ID/remove", `{"force":true,"remove_volumes":true}`},
		{"DELETE", "/docker/images/ID", ""},
		{"DELETE", "/docker/volumes/ID", ""},
		{"DELETE", "/docker/networks/ID", ""},
	}
	for _, tp := range templates {
		for _, id := range ids {
			path := "/api/v1" + strings.Replace(tp.path, "ID", id, 1)
			w := e.do(tp.method, path, e.admin, tp.body)
			if w.Code != 400 {
				t.Errorf("%s %s: status %d body %s", tp.method, path, w.Code, strings.TrimSpace(w.Body.String()))
			}
		}
	}
	// A container name is not an image ID.
	if w := e.do("DELETE", "/api/v1/docker/images/nginx", e.admin, ""); w.Code != 400 {
		t.Errorf("image removal by name: status %d", w.Code)
	}
	for _, body := range []string{
		`{"image":"--help"}`, `{"image":"-v /:/host"}`, `{"image":""}`, `{"image":"nginx latest"}`,
		`{"image":"../x"}`, `{"image":"nginx","extra":1}`, `{}`, ``, `not json`,
	} {
		if w := e.do("POST", "/api/v1/docker/images/pull", e.admin, body); w.Code != 400 {
			t.Errorf("pull %s: status %d body %s", body, w.Code, w.Body)
		}
	}
	for _, body := range []string{
		`{"names":["data"]}`, `{"names":["data"],"confirm":false}`, `{"names":[],"confirm":true}`,
		`{"confirm":true}`, `{"names":["-f"],"confirm":true}`, `{"names":["ok","../x"],"confirm":true}`,
	} {
		if w := e.do("POST", "/api/v1/docker/volumes/prune", e.admin, body); w.Code != 400 {
			t.Errorf("volume prune %s: status %d body %s", body, w.Code, w.Body)
		}
	}
	for _, body := range []string{`{}`, `{"force":true}`, `{"remove_volumes":true}`, ``} {
		if w := e.do("POST", "/api/v1/docker/containers/web/remove", e.admin, body); w.Code != 400 {
			t.Errorf("container remove %q: status %d body %s", body, w.Code, w.Body)
		}
	}
	if got := fake.seen(); len(got) != 0 {
		t.Errorf("invalid input reached Docker: %v", got)
	}
}

/* ---------- fake Docker Engine API ---------- */

const containerListJSON = `[
 {"Id":"` + idDB + `","Names":["/db"],"Image":"postgres:16","ImageID":"sha256:` + idDB + `",
  "Command":"docker-entrypoint.sh postgres","Created":1714550000,"Ports":[],
  "Labels":{"com.example.db.password":"hunter2"},
  "State":"exited","Status":"Exited (137) 3 hours ago",
  "NetworkSettings":{"Networks":{}},"Mounts":[]},
 {"Id":"` + idWeb + `","Names":["/web"],"Image":"nginx:latest","ImageID":"sha256:` + idAlpha + `",
  "Command":"nginx -g 'daemon off;'","Created":1714557000,
  "Ports":[{"PrivatePort":443,"Type":"tcp"},
           {"IP":"0.0.0.0","PrivatePort":80,"PublicPort":8080,"Type":"tcp"}],
  "Labels":{"io.myserver.app":"nginx","API_TOKEN":"tok-12345","maintainer":"someone"},
  "State":"running","Status":"Up 3 hours",
  "NetworkSettings":{"Networks":{
     "frontend":{"NetworkID":"n2","IPAddress":"172.18.0.5"},
     "bridge":{"NetworkID":"n1","IPAddress":"172.17.0.2"}}},
  "Mounts":[]},
 {"Id":"` + idAlpha + `","Names":["/Alpha"],"Image":"alpine","ImageID":"sha256:` + idAlpha + `",
  "Command":"sleep 1d","Created":1714556000,"Ports":[],"Labels":{},
  "State":"running","Status":"Up 1 hour",
  "NetworkSettings":{"Networks":{}},"Mounts":[]}
]`

func TestContainerListAgainstFakeEngine(t *testing.T) {
	fake := newFakeDocker(t)
	fake.handleJSON("GET /containers/json", 200, containerListJSON)
	fake.handleJSON("GET /containers/"+idWeb+"/json", 200, inspectJSON(idWeb, "web", false))
	fake.handleJSON("GET /containers/"+idAlpha+"/json", 200, inspectJSON(idAlpha, "Alpha", false))
	e := newTestEnv(t, fake.host())

	w := e.do("GET", "/api/v1/docker/containers", e.user, "")
	if w.Code != 200 {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	env := parseEnvelope(t, w.Body.Bytes())
	if !env.Success || env.Error != nil {
		t.Fatalf("envelope %s", w.Body)
	}
	var list []Container
	if err := json.Unmarshal(env.Data, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d containers", len(list))
	}
	// Running first, then by name without regard to case.
	if list[0].Name != "Alpha" || list[1].Name != "web" || list[2].Name != "db" {
		t.Errorf("order = %s, %s, %s", list[0].Name, list[1].Name, list[2].Name)
	}

	web := list[1]
	if web.ID != idWeb || web.ShortID != idWeb[:12] || web.Image != "nginx:latest" ||
		web.State != "running" || web.Status != "Up 3 hours" || web.CreatedAt != 1714557000 {
		t.Errorf("web = %+v", web)
	}
	if web.ExitCode != nil {
		t.Errorf("running container has exit code %d", *web.ExitCode)
	}
	if web.StartedAt == nil || *web.StartedAt != 1714557600 {
		t.Errorf("started_at = %v, want 1714557600", web.StartedAt)
	}
	if web.UptimeSeconds == nil || *web.UptimeSeconds <= 0 {
		t.Errorf("uptime_seconds = %v", web.UptimeSeconds)
	}
	if web.App == nil || *web.App != "nginx" {
		t.Errorf("app = %v", web.App)
	}
	wantPorts := []PortMapping{
		{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"},
		{ContainerPort: 443, Protocol: "tcp"},
	}
	if len(web.Ports) != 2 || web.Ports[0] != wantPorts[0] || web.Ports[1] != wantPorts[1] {
		t.Errorf("ports = %+v", web.Ports)
	}
	wantAddr := []Address{{Network: "bridge", IP: "172.17.0.2"}, {Network: "frontend", IP: "172.18.0.5"}}
	if len(web.Addresses) != 2 || web.Addresses[0] != wantAddr[0] || web.Addresses[1] != wantAddr[1] {
		t.Errorf("addresses = %+v", web.Addresses)
	}
	if web.Labels["API_TOKEN"] != masked || web.Labels["maintainer"] != "someone" || web.Labels["io.myserver.app"] != "nginx" {
		t.Errorf("labels = %v", web.Labels)
	}

	db := list[2]
	if db.State != "exited" || db.ExitCode == nil || *db.ExitCode != 137 {
		t.Errorf("db = %+v", db)
	}
	if db.StartedAt != nil || db.UptimeSeconds != nil || db.App != nil {
		t.Errorf("stopped container has start time, uptime or app: %+v", db)
	}

	body := w.Body.String()
	for _, secret := range []string{"tok-12345", "hunter2"} {
		if strings.Contains(body, secret) {
			t.Errorf("response contains the secret %q", secret)
		}
	}
	// Empty lists are [] and absent values are null.
	for _, frag := range []string{`"ports":[]`, `"addresses":[]`, `"exit_code":null`, `"app":null`, `"started_at":null`} {
		if !strings.Contains(body, frag) {
			t.Errorf("response lacks %s", frag)
		}
	}

	line, ok := fake.seenPath("GET", "/containers/json")
	if !ok || !strings.Contains(line, "all=1") {
		t.Errorf("list request = %q, want all=1; saw %v", line, fake.seen())
	}
	if _, ok := fake.seenPath("GET", "/containers/"+idDB+"/json"); ok {
		t.Error("a stopped container was inspected for its start time")
	}

	// The start times are cached: a second listing inspects nothing.
	before := len(fake.seen())
	if w := e.do("GET", "/api/v1/docker/containers", e.user, ""); w.Code != 200 {
		t.Fatalf("second listing: %d", w.Code)
	}
	if extra := fake.seen()[before:]; len(extra) != 1 {
		t.Errorf("second listing made %v, want the list call only", extra)
	}
}

func TestEmptyContainerListIsAnEmptyArray(t *testing.T) {
	fake := newFakeDocker(t)
	fake.handleJSON("GET /containers/json", 200, `[]`)
	e := newTestEnv(t, fake.host())
	w := e.do("GET", "/api/v1/docker/containers", e.user, "")
	if w.Code != 200 || string(parseEnvelope(t, w.Body.Bytes()).Data) != "[]" {
		t.Errorf("status %d body %s", w.Code, w.Body)
	}
}

func TestStopActionAgainstFakeEngine(t *testing.T) {
	fake := newFakeDocker(t)
	fake.handleJSON("GET /containers/web/json", 200, inspectJSON(idWeb, "web", false))
	fake.handle("POST /containers/"+idWeb+"/stop", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	e := newTestEnv(t, fake.host())

	w := e.do("POST", "/api/v1/docker/containers/web/stop", e.admin, "")
	if w.Code != 200 {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	var out map[string]string
	if err := json.Unmarshal(parseEnvelope(t, w.Body.Bytes()).Data, &out); err != nil {
		t.Fatal(err)
	}
	if out["id"] != idWeb || out["name"] != "web" {
		t.Errorf("data = %v", out)
	}
	seen := fake.seen()
	if len(seen) != 2 || seen[0] != "GET /containers/web/json" || !strings.HasPrefix(seen[1], "POST /containers/"+idWeb+"/stop") {
		t.Errorf("requests = %v", seen)
	}
	rows := e.auditRows()
	if len(rows) != 1 || rows[0].Action != "docker.container_stop" || rows[0].Target != "web" ||
		rows[0].Username != "yonetici" || !rows[0].Success {
		t.Errorf("audit = %+v", rows)
	}

	// The exit that follows is not a crash.
	e.mod.handleEvent(context.Background(), dieEvent(idWeb, "web", 137), newRecentSet(signalWindow), newRecentSet(signalWindow))
	if got := e.notifications(); len(got) != 0 {
		t.Errorf("a stop from the panel was reported as a crash: %+v", got)
	}
}

func TestFailedActionAgainstFakeEngine(t *testing.T) {
	fake := newFakeDocker(t)
	fake.handleJSON("GET /containers/web/json", 200, inspectJSON(idWeb, "web", false))
	fake.handleJSON("POST /containers/"+idWeb+"/kill", 409, `{"message":"container `+idWeb+` is not running"}`)
	fake.handleJSON("POST /containers/"+idWeb+"/start", 500, `{"message":"driver failed: secret internal detail"}`)
	e := newTestEnv(t, fake.host())

	w := e.do("POST", "/api/v1/docker/containers/web/kill", e.admin, "")
	env := parseEnvelope(t, w.Body.Bytes())
	if w.Code != 409 || errorCode(env) != "conflict" || env.Error.Message != "Konteyner çalışmıyor; zorla durdurulacak bir süreç yok." {
		t.Errorf("kill: status %d body %s", w.Code, w.Body)
	}
	// A failed kill must not excuse a later crash.
	if e.mod.expected.has(idWeb) {
		t.Error("container is still marked as stopped on purpose after the action failed")
	}

	w = e.do("POST", "/api/v1/docker/containers/web/start", e.admin, "")
	env = parseEnvelope(t, w.Body.Bytes())
	if w.Code != 502 || errorCode(env) != "docker_error" || env.Error.Message != "Konteyner başlatılamadı." {
		t.Errorf("start: status %d body %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "internal detail") {
		t.Errorf("the raw Docker error leaked: %s", w.Body)
	}

	w = e.do("POST", "/api/v1/docker/containers/ghost/restart", e.admin, "")
	if w.Code != 404 {
		t.Errorf("unknown container: status %d body %s", w.Code, w.Body)
	}

	rows := e.auditRows()
	if len(rows) != 2 || rows[0].Action != "docker.container_kill" || rows[0].Success ||
		rows[1].Action != "docker.container_start" || rows[1].Success {
		t.Errorf("audit = %+v", rows)
	}
}

func TestInspectMasksSecrets(t *testing.T) {
	fake := newFakeDocker(t)
	fake.handleJSON("GET /containers/web/json", 200, `{
		"Id":"`+idWeb+`","Name":"/web","Created":"2024-05-01T09:00:00Z","RestartCount":2,
		"State":{"Status":"exited","Running":false,"ExitCode":137,"OOMKilled":true,
			"StartedAt":"2024-05-01T10:00:00Z","FinishedAt":"2024-05-01T11:00:00Z"},
		"Config":{"Image":"app:1","Tty":false,
			"Env":["PATH=/usr/bin","DB_PASSWORD=envsecret1","DATABASE_URL=postgres://app:urlsecret2@db:5432/x","mysql_pwd=envsecret3"],
			"Cmd":["serve","--api-key","argsecret4","--token=argsecret5"],
			"Entrypoint":["/entry","--password","argsecret6"],
			"Labels":{"client_secret":"labelsecret7","io.myserver.app":"demo"}},
		"HostConfig":{"RestartPolicy":{"Name":"always"},"Memory":1048576,"Privileged":true},
		"Mounts":[{"Type":"volume","Name":"data","Source":"/var/lib/docker/volumes/data/_data","Destination":"/data","RW":false}],
		"NetworkSettings":{"Ports":{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"8080"}],"53/udp":null},
			"Networks":{"bridge":{"NetworkID":"n1","IPAddress":"172.17.0.2","Gateway":"172.17.0.1"}}}
	}`)
	e := newTestEnv(t, fake.host())
	w := e.do("GET", "/api/v1/docker/containers/web", e.admin, "")
	if w.Code != 200 {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, s := range []string{"envsecret1", "urlsecret2", "envsecret3", "argsecret4", "argsecret5", "argsecret6", "labelsecret7"} {
		if strings.Contains(body, s) {
			t.Errorf("inspect response contains the secret %q", s)
		}
	}
	var d ContainerDetail
	if err := json.Unmarshal(parseEnvelope(t, w.Body.Bytes()).Data, &d); err != nil {
		t.Fatal(err)
	}
	if d.ID != idWeb || d.Name != "web" || d.State != "exited" || d.ExitCode != 137 || !d.OOMKilled ||
		d.RestartCount != 2 || d.RestartPolicy != "always" || d.MemoryLimit != 1048576 || !d.Privileged {
		t.Errorf("detail = %+v", d)
	}
	if d.StartedAt == nil || *d.StartedAt != 1714557600 || d.FinishedAt == nil || *d.FinishedAt != 1714561200 {
		t.Errorf("times = %v %v", d.StartedAt, d.FinishedAt)
	}
	if len(d.Env) != 4 || d.Env[0].Name != "DATABASE_URL" || !d.Env[0].Masked || d.Env[2].Name != "PATH" || d.Env[2].Masked {
		t.Errorf("env = %+v", d.Env)
	}
	if len(d.Mounts) != 1 || !d.Mounts[0].ReadOnly || d.Mounts[0].Destination != "/data" {
		t.Errorf("mounts = %+v", d.Mounts)
	}
	wantPorts := []PortMapping{
		{ContainerPort: 53, Protocol: "udp"},
		{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"},
	}
	if len(d.Ports) != 2 || d.Ports[0] != wantPorts[0] || d.Ports[1] != wantPorts[1] {
		t.Errorf("ports = %+v", d.Ports)
	}
}
