package apps

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
	"myserver/internal/logbuf"
	"myserver/internal/module"
	"myserver/internal/notify"
	"myserver/internal/privileged"
	"myserver/internal/settings"
	"myserver/migrations"
)

const testHost = "panel.test:8080"

/* ---------- manifests used by the HTTP and job tests ---------- */

const manifestTek = `name: Tek
slug: tek
description: Tek servisli deneme uygulaması
category: tools
icon: tek.svg
version: "1.0"
docker:
  image: example/tek:1.0
env:
  - name: ADMIN_PASSWORD
    label: Yönetici parolası
    secret: true
    required: true
  - name: DB_PASSWORD
    secret: true
    generate: password
  - name: TZ
    label: Saat dilimi
    default: Europe/Istanbul
ports:
  - host: 18080
    container: 80
    key: web
    label: Web arayüzü
    web_ui: { scheme: http, path: / }
volumes:
  - source: config
    target: /config
  - type: bind
    key: media
    target: /media
    label: Medya klasörü
options:
  - key: rawnet
    label: Ham ağ erişimi
    cap_add: [NET_RAW]
`

const manifestCift = `name: Çift
slug: cift
description: İki servisli deneme uygulaması
services:
  - name: web
    image: example/web:2
    depends_on: [db]
    env:
      - name: DB_PASS
        key: dbpass
        secret: true
        generate: password
    ports:
      - host: 18081
        container: 8080
        key: web
  - name: db
    image: example/db:16
    env:
      - name: POSTGRES_PASSWORD
        key: dbpass
        secret: true
        generate: password
    volumes:
      - source: db
        target: /var/lib/postgresql/data
`

const manifestSoket = `name: Soket
slug: soket
description: Docker soketini bağlayan uygulama
docker:
  image: example/soket:1
volumes:
  - type: system
    source: /var/run/docker.sock
    target: /var/run/docker.sock
  - source: data
    target: /data
`

const manifestKol = `name: Kol
slug: kol
description: Yalnızca arm64 destekleyen uygulama
architectures: [arm64]
docker:
  image: example/kol:1
`

const manifestAgci = `name: Ağcı
slug: agci
description: Sunucu ağını kullanan uygulama
docker:
  image: example/agci:1
  network_mode: host
  cap_add: [NET_ADMIN]
ports:
  - host: 18053
    container: 18053
    key: dns
`

/* ---------- environment ---------- */

type creds struct {
	name  string
	token string
	csrf  string
}

type testEnv struct {
	t       *testing.T
	db      *sql.DB
	mod     *Module
	cfg     *config.Config
	store   *settings.Store
	notify  *notify.Center
	h       http.Handler
	mux     *http.ServeMux
	admin   *creds
	user    *creds
	root    string // the only allowed root for bind paths
	iconDir string
}

// newTestEnv mounts the module the way internal/server does: real router,
// real auth middleware, real database. manifests maps file name to content.
func newTestEnv(t *testing.T, dockerHost string, manifests map[string]string) *testEnv {
	t.Helper()
	dir := t.TempDir()
	manifestDir := filepath.Join(dir, "apps", "manifests")
	iconDir := filepath.Join(dir, "apps", "icons")
	root := filepath.Join(dir, "veri")
	dataDir := filepath.Join(dir, "panel")
	for _, d := range []string{manifestDir, iconDir, root, dataDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	for name, body := range manifests {
		if err := os.WriteFile(filepath.Join(manifestDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
	}
	db, err := database.Open(filepath.Join(dataDir, "test.db"))
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
	if err := store.SetStrings(ctx, settings.KeyAllowedRoots, []string{root}); err != nil {
		t.Fatalf("allowed roots: %v", err)
	}
	cfg := &config.Config{
		DataDir:     dataDir,
		ManifestDir: manifestDir,
		HelperPath:  filepath.Join(dir, "no-such-helper"),
		DockerHost:  dockerHost,
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
	mux := http.NewServeMux()
	rt := httpx.NewRouter(mux)
	api := rt.Group("/api/v1", svc.Require)
	ws := rt.Group("/api/v1", svc.RequireWebSocket)
	m.Register(api, ws)

	e := &testEnv{t: t, db: db, mod: m, cfg: cfg, store: store, notify: nc,
		h: httpx.Recover(mux), mux: mux, root: root, iconDir: iconDir}
	e.admin = e.addSession("yonetici", auth.RoleAdmin)
	e.user = e.addSession("kullanici", auth.RoleUser)
	return e
}

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

func sign(r *http.Request, who *creds) {
	if who == nil {
		return
	}
	r.AddCookie(&http.Cookie{Name: "myserver_session", Value: who.token})
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		r.Header.Set("X-CSRF-Token", who.csrf)
	}
}

func (e *testEnv) do(method, path string, who *creds, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, "http://"+testHost+"/api/v1"+path, rd)
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

func parseEnvelope(t *testing.T, w *httptest.ResponseRecorder) envelope {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not an envelope (status %d): %v\n%s", w.Code, err, w.Body.String())
	}
	return env
}

// wantError asserts status, code and that the message contains each part.
func wantError(t *testing.T, w *httptest.ResponseRecorder, status int, code string, parts ...string) {
	t.Helper()
	env := parseEnvelope(t, w)
	if w.Code != status || env.Error == nil || env.Error.Code != code {
		t.Fatalf("got %d %s, want %d %s", w.Code, w.Body.String(), status, code)
	}
	for _, p := range parts {
		if !strings.Contains(env.Error.Message, p) {
			t.Errorf("message %q does not contain %q", env.Error.Message, p)
		}
	}
}

func decodeData(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	env := parseEnvelope(t, w)
	if !env.Success {
		t.Fatalf("request failed: %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		t.Fatalf("data: %v\n%s", err, env.Data)
	}
}

// startJob sends a request that starts a job and waits for the job to end.
func (e *testEnv) startJob(method, path, body string) JobView {
	e.t.Helper()
	w := e.do(method, path, e.admin, body)
	if w.Code != http.StatusAccepted {
		e.t.Fatalf("%s %s: got %d %s, want 202", method, path, w.Code, w.Body.String())
	}
	var v JobView
	decodeData(e.t, w, &v)
	return e.waitJob(v.ID)
}

func (e *testEnv) waitJob(id string) JobView {
	e.t.Helper()
	j := e.mod.jobs.get(id)
	if j == nil {
		e.t.Fatalf("job %s not found", id)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if v := j.view(); v.Status != JobRunning {
			// The lock is released right after the job finishes.
			for e.mod.operation(v.Slug) != "" && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			return v
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("job %s did not finish", id)
	return JobView{}
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

func (e *testEnv) findAudit(action string) []auditRow {
	var out []auditRow
	for _, a := range e.auditRows() {
		if a.Action == action {
			out = append(out, a)
		}
	}
	return out
}

/* ---------- log capture ---------- */

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// captureLogs redirects slog (all levels) into a buffer for the test.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buf
}

/* ---------- fake Docker Engine API ---------- */

type fakeContainer struct {
	ID          string
	Name        string
	Image       string
	State       string // created, running, exited
	Status      string
	Labels      map[string]string
	NetworkMode string
	Ports       []fakePort
	ExitCode    int
	// Body is the decoded body of the create request.
	Body createBody
}

type fakePort struct {
	IP          string `json:"IP"`
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort"`
	Type        string `json:"Type"`
}

type createBody struct {
	Image       string   `json:"Image"`
	Cmd         []string `json:"Cmd"`
	User        string   `json:"User"`
	Healthcheck *struct {
		Test []string `json:"Test"`
	} `json:"Healthcheck"`
	Env          []string            `json:"Env"`
	Labels       map[string]string   `json:"Labels"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts"`
	HostConfig   struct {
		ShmSize      int64             `json:"ShmSize"`
		Tmpfs        map[string]string `json:"Tmpfs"`
		Privileged   bool              `json:"Privileged"`
		CapAdd       []string          `json:"CapAdd"`
		NetworkMode  string            `json:"NetworkMode"`
		PortBindings map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"PortBindings"`
		Mounts []struct {
			Type     string `json:"Type"`
			Source   string `json:"Source"`
			Target   string `json:"Target"`
			ReadOnly bool   `json:"ReadOnly"`
		} `json:"Mounts"`
		Devices []struct {
			PathOnHost string `json:"PathOnHost"`
		} `json:"Devices"`
	} `json:"HostConfig"`
}

// fakeDocker is a small stateful Docker Engine API. It implements only what
// the application system calls; any other request fails the test.
type fakeDocker struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	ops        []string
	containers []*fakeContainer
	volumes    map[string]map[string]string // name -> labels
	networks   map[string]map[string]string
	images     map[string]string // reference -> id
	// fail maps an operation ("container.start myserver-tek") to the
	// message of the 500 response it gets.
	fail map[string]string
	// exitOnStart lists container names that exit right after starting.
	exitOnStart map[string]bool
	// block, when set for an operation, is waited on before it answers.
	block  map[string]chan struct{}
	nextID int
	logs   string
}

func newFakeDocker(t *testing.T) *fakeDocker {
	t.Helper()
	f := &fakeDocker{
		t: t, volumes: map[string]map[string]string{}, networks: map[string]map[string]string{},
		images: map[string]string{}, fail: map[string]string{}, exitOnStart: map[string]bool{},
		block: map[string]chan struct{}{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDocker) host() string { return "tcp://" + f.srv.Listener.Addr().String() }

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

// addContainer puts an existing container into the fake.
func (f *fakeDocker) addContainer(name, image, state string, labels map[string]string, ports ...fakePort) *fakeContainer {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	c := &fakeContainer{
		ID: fmt.Sprintf("%064x", f.nextID), Name: name, Image: image, State: state,
		Labels: labels, NetworkMode: "bridge", Ports: ports,
	}
	if state == "running" {
		c.Status = "Up 2 hours"
	} else {
		c.Status = "Exited (0) 1 hour ago"
	}
	f.containers = append(f.containers, c)
	return c
}

func (f *fakeDocker) find(ref string) *fakeContainer {
	for _, c := range f.containers {
		if c.ID == ref || c.Name == ref {
			return c
		}
	}
	return nil
}

// seen returns every operation in order.
func (f *fakeDocker) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ops...)
}

// changes returns the operations that change something, in order.
func (f *fakeDocker) changes() []string {
	var out []string
	for _, op := range f.seen() {
		verb, _, _ := strings.Cut(op, " ")
		if strings.HasSuffix(verb, ".inspect") || strings.HasSuffix(verb, ".list") || strings.HasSuffix(verb, ".logs") {
			continue
		}
		out = append(out, op)
	}
	return out
}

func (f *fakeDocker) reset() {
	f.mu.Lock()
	f.ops = nil
	f.mu.Unlock()
}

func (f *fakeDocker) created(name string) *fakeContainer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.find(name)
}

func (f *fakeDocker) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for _, c := range f.containers {
		out = append(out, c.Name)
	}
	return out
}

var (
	versionPrefixRe = regexp.MustCompile(`^/v1\.[0-9]+`)
	containerRe     = regexp.MustCompile(`^/containers/([^/]+)(?:/(json|start|stop|rename|logs))?$`)
	imageRe2        = regexp.MustCompile(`^/images/(.+)/json$`)
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func notFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such object"})
}

// record notes an operation and applies the configured block and failure.
// It returns false when the response has been written.
func (f *fakeDocker) record(w http.ResponseWriter, op string) bool {
	f.mu.Lock()
	f.ops = append(f.ops, op)
	ch := f.block[op]
	msg, failing := f.fail[op]
	f.mu.Unlock()
	if ch != nil {
		<-ch
	}
	if failing {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": msg})
		return false
	}
	return true
}

func matchLabels(c *fakeContainer, filters string) bool {
	if filters == "" {
		return true
	}
	var f map[string]map[string]bool
	if err := json.Unmarshal([]byte(filters), &f); err != nil {
		return false
	}
	for want := range f["label"] {
		k, v, _ := strings.Cut(want, "=")
		if c.Labels[k] != v {
			return false
		}
	}
	return true
}

func (f *fakeDocker) inspectBody(c *fakeContainer) map[string]any {
	state := map[string]any{
		"Status": c.State, "Running": c.State == "running", "Restarting": false, "ExitCode": c.ExitCode,
	}
	if c.State == "running" {
		// Healthy, so that waitReady does not wait for its settle time.
		state["Health"] = map[string]any{"Status": "healthy"}
	}
	return map[string]any{
		"Id": c.ID, "Name": "/" + c.Name, "State": state,
		"Config": map[string]any{"Labels": c.Labels, "Image": c.Image},
	}
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
	q := r.URL.Query()
	key := r.Method + " " + path
	switch {
	case key == "GET /containers/json":
		if !f.record(w, "container.list") {
			return
		}
		f.mu.Lock()
		list := []map[string]any{}
		for _, c := range f.containers {
			if q.Get("all") != "1" && q.Get("all") != "true" && c.State != "running" {
				continue
			}
			if !matchLabels(c, q.Get("filters")) {
				continue
			}
			ports := c.Ports
			if ports == nil {
				ports = []fakePort{}
			}
			list = append(list, map[string]any{
				"Id": c.ID, "Names": []string{"/" + c.Name}, "Image": c.Image, "State": c.State,
				"Status": c.Status, "Labels": c.Labels, "Ports": ports,
				"HostConfig": map[string]string{"NetworkMode": c.NetworkMode},
			})
		}
		f.mu.Unlock()
		writeJSON(w, 200, list)

	case key == "POST /images/create":
		// The client sends the normalised name; the daemon knows the image
		// under the short one as well.
		ref := q.Get("fromImage")
		if tag := q.Get("tag"); strings.HasPrefix(tag, "sha256:") {
			ref += "@" + tag
		} else if tag != "" {
			ref += ":" + tag
		}
		ref = canonicalRef(ref)
		if !f.record(w, "image.pull "+ref) {
			return
		}
		f.mu.Lock()
		if _, ok := f.images[ref]; !ok {
			sum := sha256.Sum256([]byte(ref))
			f.images[ref] = "sha256:" + hex.EncodeToString(sum[:])
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		io.WriteString(w, `{"status":"Pulling from `+ref+`"}`+"\n"+`{"status":"Status: Downloaded newer image"}`+"\n")

	case r.Method == http.MethodGet && imageRe2.MatchString(path):
		ref := canonicalRef(imageRe2.FindStringSubmatch(path)[1])
		if !f.record(w, "image.inspect "+ref) {
			return
		}
		f.mu.Lock()
		id, ok := f.images[ref]
		f.mu.Unlock()
		if !ok {
			notFound(w)
			return
		}
		writeJSON(w, 200, map[string]any{"Id": id, "RepoDigests": []string{ref + "@" + id}})

	case key == "POST /networks/create":
		var body struct {
			Name   string
			Labels map[string]string
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !f.record(w, "network.create "+body.Name) {
			return
		}
		f.mu.Lock()
		f.networks[body.Name] = body.Labels
		f.mu.Unlock()
		writeJSON(w, 201, map[string]string{"Id": "net-" + body.Name})

	case strings.HasPrefix(path, "/networks/") && (r.Method == http.MethodGet || r.Method == http.MethodDelete):
		name := strings.TrimPrefix(path, "/networks/")
		verb := "network.inspect "
		if r.Method == http.MethodDelete {
			verb = "network.remove "
		}
		if !f.record(w, verb+name) {
			return
		}
		f.mu.Lock()
		labels, ok := f.networks[name]
		if ok && r.Method == http.MethodDelete {
			delete(f.networks, name)
		}
		f.mu.Unlock()
		if !ok {
			notFound(w)
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(204)
			return
		}
		writeJSON(w, 200, map[string]any{"Name": name, "Id": "net-" + name, "Labels": labels})

	case key == "POST /volumes/create":
		var body struct {
			Name   string
			Labels map[string]string
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !f.record(w, "volume.create "+body.Name) {
			return
		}
		f.mu.Lock()
		f.volumes[body.Name] = body.Labels
		f.mu.Unlock()
		writeJSON(w, 201, map[string]any{"Name": body.Name, "Labels": body.Labels})

	case strings.HasPrefix(path, "/volumes/") && (r.Method == http.MethodGet || r.Method == http.MethodDelete):
		name := strings.TrimPrefix(path, "/volumes/")
		verb := "volume.inspect "
		if r.Method == http.MethodDelete {
			verb = "volume.remove "
		}
		if !f.record(w, verb+name) {
			return
		}
		f.mu.Lock()
		labels, ok := f.volumes[name]
		if ok && r.Method == http.MethodDelete {
			delete(f.volumes, name)
		}
		f.mu.Unlock()
		if !ok {
			notFound(w)
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(204)
			return
		}
		writeJSON(w, 200, map[string]any{"Name": name, "Labels": labels})

	case key == "POST /containers/create":
		name := q.Get("name")
		var body createBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Errorf("fake docker: create body: %v", err)
		}
		if !f.record(w, "container.create "+name) {
			return
		}
		f.mu.Lock()
		if f.find(name) != nil {
			f.mu.Unlock()
			writeJSON(w, 409, map[string]string{"message": "Conflict. The container name is already in use"})
			return
		}
		f.nextID++
		c := &fakeContainer{
			ID: fmt.Sprintf("%064x", f.nextID), Name: name, Image: body.Image, State: "created",
			Status: "Created", Labels: body.Labels, NetworkMode: body.HostConfig.NetworkMode, Body: body,
		}
		f.containers = append(f.containers, c)
		f.mu.Unlock()
		writeJSON(w, 201, map[string]any{"Id": c.ID, "Warnings": []string{}})

	case containerRe.MatchString(path):
		m := containerRe.FindStringSubmatch(path)
		ref, action := m[1], m[2]
		f.mu.Lock()
		c := f.find(ref)
		name := ref
		if c != nil {
			name = c.Name
		}
		f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && action == "json":
			if !f.record(w, "container.inspect "+name) {
				return
			}
			if c == nil {
				notFound(w)
				return
			}
			f.mu.Lock()
			body := f.inspectBody(c)
			f.mu.Unlock()
			writeJSON(w, 200, body)
		case r.Method == http.MethodGet && action == "logs":
			if !f.record(w, "container.logs "+name) {
				return
			}
			if c == nil {
				notFound(w)
				return
			}
			f.mu.Lock()
			text := f.logs
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
			w.WriteHeader(200)
			if text != "" {
				w.Write(frame(1, text))
			}
		case r.Method == http.MethodPost && (action == "start" || action == "stop"):
			if !f.record(w, "container."+action+" "+name) {
				return
			}
			if c == nil {
				notFound(w)
				return
			}
			f.mu.Lock()
			if action == "start" {
				if f.exitOnStart[c.Name] {
					c.State, c.Status, c.ExitCode = "exited", "Exited (1) 1 second ago", 1
				} else {
					c.State, c.Status = "running", "Up 1 second (healthy)"
				}
			} else {
				c.State, c.Status = "exited", "Exited (0) 1 second ago"
			}
			f.mu.Unlock()
			w.WriteHeader(204)
		case r.Method == http.MethodPost && action == "rename":
			to := q.Get("name")
			if !f.record(w, "container.rename "+name+" "+to) {
				return
			}
			if c == nil {
				notFound(w)
				return
			}
			f.mu.Lock()
			c.Name = to
			f.mu.Unlock()
			w.WriteHeader(204)
		case r.Method == http.MethodDelete && action == "":
			if !f.record(w, "container.remove "+name) {
				return
			}
			if c == nil {
				notFound(w)
				return
			}
			f.mu.Lock()
			for i, x := range f.containers {
				if x == c {
					f.containers = append(f.containers[:i], f.containers[i+1:]...)
					break
				}
			}
			f.mu.Unlock()
			w.WriteHeader(204)
		default:
			f.unexpected(w, r)
		}

	default:
		f.unexpected(w, r)
	}
}

// canonicalRef names an image the way the daemon knows it, whichever of the
// equivalent spellings was used: "postgres", "library/postgres:latest" and
// "docker.io/library/postgres" are one image.
func canonicalRef(ref string) string {
	ref = strings.TrimPrefix(ref, "docker.io/")
	if !strings.Contains(strings.SplitN(ref, "@", 2)[0], "/") || strings.HasPrefix(ref, "library/") {
		ref = strings.TrimPrefix(ref, "library/")
	}
	if !strings.Contains(ref, "@") && strings.LastIndex(ref, ":") <= strings.LastIndex(ref, "/") {
		ref += ":latest"
	}
	return ref
}

func (f *fakeDocker) unexpected(w http.ResponseWriter, r *http.Request) {
	f.t.Errorf("fake docker: unexpected request %s %s", r.Method, r.URL.RequestURI())
	writeJSON(w, http.StatusNotImplemented, map[string]string{"message": "not implemented by the test"})
}

// frame builds one frame of Docker's multiplexed log stream.
func frame(kind byte, text string) []byte {
	b := []byte{kind, 0, 0, 0, byte(len(text) >> 24), byte(len(text) >> 16), byte(len(text) >> 8), byte(len(text))}
	return append(b, text...)
}

func managed(slug, service string) map[string]string {
	return map[string]string{LabelApp: slug, LabelService: service, LabelManaged: "true"}
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func indexOf(list []string, v string) int {
	for i, s := range list {
		if s == v {
			return i
		}
	}
	return -1
}

func auditActor(name string) audit.Actor { return audit.Actor{Username: name} }
