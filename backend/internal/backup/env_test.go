package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"myserver/internal/apps"
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

/* ---------- event log shared by the fakes ---------- */

type eventLog struct {
	mu   sync.Mutex
	list []string
}

func (e *eventLog) add(format string, args ...any) {
	e.mu.Lock()
	e.list = append(e.list, fmt.Sprintf(format, args...))
	e.mu.Unlock()
}

func (e *eventLog) all() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string{}, e.list...)
}

func (e *eventLog) reset() {
	e.mu.Lock()
	e.list = nil
	e.mu.Unlock()
}

// index returns the position of the first event with the prefix, or -1.
func (e *eventLog) index(prefix string) int {
	for i, ev := range e.all() {
		if strings.HasPrefix(ev, prefix) {
			return i
		}
	}
	return -1
}

func (e *eventLog) count(prefix string) int {
	n := 0
	for _, ev := range e.all() {
		if strings.HasPrefix(ev, prefix) {
			n++
		}
	}
	return n
}

/* ---------- fake application module ---------- */

type fakeApps struct {
	ev *eventLog

	mu          sync.Mutex
	cfgs        map[string]*apps.Config
	running     map[string]bool
	stopErr     error
	startErr    error
	recreateErr error
	runningErr  error
	listErr     error
	recreated   []*apps.Config
	actors      []audit.Actor
}

func newFakeApps(ev *eventLog) *fakeApps {
	return &fakeApps{ev: ev, cfgs: map[string]*apps.Config{}, running: map[string]bool{}}
}

func (f *fakeApps) install(cfg *apps.Config, running bool) {
	f.mu.Lock()
	f.cfgs[cfg.Slug] = cfg
	f.running[cfg.Slug] = running
	f.mu.Unlock()
}

func (f *fakeApps) isRunning(slug string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running[slug]
}

func (f *fakeApps) InstalledApps(context.Context) ([]apps.InstalledApp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := []apps.InstalledApp{}
	for _, c := range f.cfgs {
		a := apps.InstalledApp{Slug: c.Slug, Name: c.Name, Version: c.Version, Images: map[string]apps.ImageInfo{}}
		for _, s := range c.Services {
			for _, v := range s.Volumes {
				a.Volumes = append(a.Volumes, apps.AppVolume{Service: s.Name, Type: v.Type, Source: v.Source, Target: v.Target})
			}
		}
		out = append(out, a)
	}
	return out, nil
}

func (f *fakeApps) AppConfig(_ context.Context, slug string) (*apps.Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.cfgs[slug]
	if !ok {
		return nil, httpx.NotFound("Uygulama kurulu değil.")
	}
	return c, nil
}

func (f *fakeApps) AppRunning(_ context.Context, slug string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.runningErr != nil {
		return false, f.runningErr
	}
	if _, ok := f.cfgs[slug]; !ok {
		return false, httpx.NotFound("Uygulama kurulu değil.")
	}
	return f.running[slug], nil
}

func (f *fakeApps) StopApp(ctx context.Context, actor audit.Actor, slug string) error {
	f.ev.add("apps.stop %s", slug)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actors = append(f.actors, actor)
	if f.stopErr != nil {
		return f.stopErr
	}
	f.running[slug] = false
	return nil
}

func (f *fakeApps) StartApp(ctx context.Context, actor audit.Actor, slug string) error {
	f.ev.add("apps.start %s cancelled=%v", slug, ctx.Err() != nil)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return f.startErr
	}
	f.running[slug] = true
	return nil
}

func (f *fakeApps) RecreateApp(_ context.Context, _ audit.Actor, cfg *apps.Config) error {
	f.ev.add("apps.recreate %s", cfg.Slug)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recreated = append(f.recreated, cfg)
	if f.recreateErr != nil {
		return f.recreateErr
	}
	f.cfgs[cfg.Slug] = cfg
	f.running[cfg.Slug] = true
	return nil
}

/* ---------- fake Docker Engine API ---------- */

type fakeVolume struct {
	labels map[string]string
	files  []tarFile // exported with the helper prefix
	raw    []byte    // when set, sent instead of files
	stored []byte    // the last tar stream written into the volume
	size   int64     // reported by /system/df; -1 leaves the volume out
}

type fakeContainer struct {
	id     string
	name   string
	labels map[string]string
	state  string
	body   map[string]any // the create request
	volume string
	ro     bool
}

type fakeDocker struct {
	t   *testing.T
	ev  *eventLog
	srv *httptest.Server

	mu         sync.Mutex
	volumes    map[string]*fakeVolume
	containers map[string]*fakeContainer
	images     map[string]bool
	created    []*fakeContainer // every container ever created
	nextID     int
	allowPull  bool
	// fail maps an operation to the HTTP status it answers with.
	fail map[string]int
	// hook runs inside the handler of an operation, before it answers.
	hook map[string]func(r *http.Request)
}

var versionPrefix = regexp.MustCompile(`^/v[0-9]+\.[0-9]+`)

func newFakeDocker(t *testing.T, ev *eventLog) *fakeDocker {
	d := &fakeDocker{t: t, ev: ev, volumes: map[string]*fakeVolume{}, containers: map[string]*fakeContainer{},
		images: map[string]bool{}, fail: map[string]int{}, hook: map[string]func(*http.Request){}}
	d.srv = httptest.NewServer(http.HandlerFunc(d.serve))
	t.Cleanup(d.srv.Close)
	return d
}

func (d *fakeDocker) host() string { return "tcp://" + strings.TrimPrefix(d.srv.URL, "http://") }

func (d *fakeDocker) addVolume(name, slug string, files ...tarFile) *fakeVolume {
	v := &fakeVolume{labels: map[string]string{apps.LabelApp: slug, apps.LabelManaged: "true"}, files: files, size: 1 << 20}
	d.mu.Lock()
	d.volumes[name] = v
	d.mu.Unlock()
	return v
}

func (d *fakeDocker) setFail(op string, status int) {
	d.mu.Lock()
	d.fail[op] = status
	d.mu.Unlock()
}

func (d *fakeDocker) setHook(op string, fn func(r *http.Request)) {
	d.mu.Lock()
	d.hook[op] = fn
	d.mu.Unlock()
}

func (d *fakeDocker) liveContainers() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, c := range d.containers {
		out = append(out, c.name)
	}
	return out
}

func (d *fakeDocker) helpers() []*fakeContainer {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*fakeContainer{}, d.created...)
}

func dockerError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"message": msg})
}

func dockerJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// step applies the hook and the injected failure of an operation.
func (d *fakeDocker) step(op string, w http.ResponseWriter, r *http.Request) bool {
	d.mu.Lock()
	hook, status := d.hook[op], d.fail[op]
	d.mu.Unlock()
	if hook != nil {
		hook(r)
	}
	if status != 0 {
		dockerError(w, status, "injected failure: "+op)
		return false
	}
	return true
}

func volumeTar(t *testing.T, v *fakeVolume) []byte {
	if v.raw != nil {
		return v.raw
	}
	files := []tarFile{{name: helperPrefix + "/", typ: tar.TypeDir}}
	for _, f := range v.files {
		f.name = helperPrefix + "/" + f.name
		if f.typ == tar.TypeLink {
			f.link = helperPrefix + "/" + f.link
		}
		files = append(files, f)
	}
	return tarStream(t, files...)
}

func (d *fakeDocker) serve(w http.ResponseWriter, r *http.Request) {
	p := versionPrefix.ReplaceAllString(r.URL.Path, "")
	switch {
	case p == "/_ping":
		if !d.step("ping", w, r) {
			return
		}
		w.Header().Set("API-Version", "1.47")
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(200)
		if r.Method == http.MethodGet {
			io.WriteString(w, "OK")
		}

	case r.Method == http.MethodGet && p == "/system/df":
		if !d.step("df", w, r) {
			return
		}
		type usage struct {
			Size     int64
			RefCount int64
		}
		type vol struct {
			Name      string
			Driver    string
			UsageData *usage
		}
		out := struct{ Volumes []vol }{}
		d.mu.Lock()
		for name, v := range d.volumes {
			if v.size >= 0 {
				out.Volumes = append(out.Volumes, vol{Name: name, Driver: "local", UsageData: &usage{Size: v.size, RefCount: 1}})
			}
		}
		d.mu.Unlock()
		dockerJSON(w, 200, out)

	case r.Method == http.MethodGet && strings.HasPrefix(p, "/volumes/"):
		name := strings.TrimPrefix(p, "/volumes/")
		if !d.step("volume-inspect", w, r) {
			return
		}
		d.mu.Lock()
		v, ok := d.volumes[name]
		d.mu.Unlock()
		if !ok {
			dockerError(w, 404, "get "+name+": no such volume")
			return
		}
		dockerJSON(w, 200, map[string]any{"Name": name, "Driver": "local", "Labels": v.labels, "Mountpoint": "/var/lib/docker/volumes/" + name + "/_data", "Scope": "local"})

	case r.Method == http.MethodDelete && strings.HasPrefix(p, "/volumes/"):
		name := strings.TrimPrefix(p, "/volumes/")
		d.ev.add("docker.volume.remove %s", name)
		if !d.step("volume-remove", w, r) {
			return
		}
		d.mu.Lock()
		delete(d.volumes, name)
		d.mu.Unlock()
		w.WriteHeader(204)

	case r.Method == http.MethodPost && p == "/volumes/create":
		var req struct {
			Name   string
			Labels map[string]string
		}
		json.NewDecoder(r.Body).Decode(&req)
		d.ev.add("docker.volume.create %s", req.Name)
		if !d.step("volume-create", w, r) {
			return
		}
		d.mu.Lock()
		d.volumes[req.Name] = &fakeVolume{labels: req.Labels, size: 0}
		d.mu.Unlock()
		dockerJSON(w, 201, map[string]any{"Name": req.Name, "Driver": "local", "Labels": req.Labels})

	case r.Method == http.MethodPost && p == "/containers/create":
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		var typed struct {
			Image      string
			Labels     map[string]string
			HostConfig struct {
				Mounts []struct {
					Source   string
					ReadOnly bool
				}
			}
		}
		json.Unmarshal(raw, &typed)
		c := &fakeContainer{name: r.URL.Query().Get("name"), labels: typed.Labels, state: "created", body: body}
		if len(typed.HostConfig.Mounts) > 0 {
			c.volume, c.ro = typed.HostConfig.Mounts[0].Source, typed.HostConfig.Mounts[0].ReadOnly
		}
		d.ev.add("docker.create image=%s volume=%s ro=%v", typed.Image, c.volume, c.ro)
		if !d.step("create", w, r) {
			return
		}
		d.mu.Lock()
		if !d.images[typed.Image] {
			d.mu.Unlock()
			dockerError(w, 404, "No such image: "+typed.Image)
			return
		}
		d.nextID++
		c.id = fmt.Sprintf("%064x", d.nextID)
		d.containers[c.id] = c
		d.created = append(d.created, c)
		d.mu.Unlock()
		dockerJSON(w, 201, map[string]any{"Id": c.id, "Warnings": []string{}})

	case r.Method == http.MethodGet && p == "/containers/json":
		if !d.step("list", w, r) {
			return
		}
		var f map[string]map[string]bool
		json.Unmarshal([]byte(r.URL.Query().Get("filters")), &f)
		out := []map[string]any{}
		d.mu.Lock()
		for _, c := range d.containers {
			match := true
			for want := range f["label"] {
				k, v, _ := strings.Cut(want, "=")
				if c.labels[k] != v {
					match = false
				}
			}
			if match {
				out = append(out, map[string]any{"Id": c.id, "Names": []string{"/" + c.name}, "Labels": c.labels, "State": c.state})
			}
		}
		d.mu.Unlock()
		dockerJSON(w, 200, out)

	case strings.HasPrefix(p, "/containers/"):
		rest := strings.TrimPrefix(p, "/containers/")
		id, action, _ := strings.Cut(rest, "/")
		d.mu.Lock()
		c := d.containers[id]
		d.mu.Unlock()
		if c == nil {
			dockerError(w, 404, "No such container: "+id)
			return
		}
		switch {
		case r.Method == http.MethodDelete && action == "":
			d.ev.add("docker.remove %s", c.name)
			if !d.step("remove", w, r) {
				return
			}
			d.mu.Lock()
			delete(d.containers, id)
			d.mu.Unlock()
			w.WriteHeader(204)
		case r.Method == http.MethodPost && action == "stop":
			d.ev.add("docker.stop %s", c.name)
			if !d.step("stop", w, r) {
				return
			}
			c.state = "exited"
			w.WriteHeader(204)
		case r.Method == http.MethodGet && action == "archive":
			d.ev.add("docker.read %s path=%s", c.volume, r.URL.Query().Get("path"))
			if !d.step("read", w, r) {
				return
			}
			d.mu.Lock()
			v := d.volumes[c.volume]
			d.mu.Unlock()
			if v == nil {
				dockerError(w, 404, "no such volume")
				return
			}
			stat, _ := json.Marshal(map[string]any{"name": helperPrefix, "size": 4096, "mode": uint32(os.ModeDir | 0o755), "mtime": time.Unix(1_700_000_000, 0)})
			w.Header().Set("X-Docker-Container-Path-Stat", base64.StdEncoding.EncodeToString(stat))
			w.Header().Set("Content-Type", "application/x-tar")
			w.WriteHeader(200)
			data := volumeTar(d.t, v)
			d.mu.Lock()
			mid := d.hook["read-body"]
			d.mu.Unlock()
			if mid != nil {
				half := len(data) / 2
				w.Write(data[:half])
				if fl, ok := w.(http.Flusher); ok {
					fl.Flush()
				}
				mid(r)
				data = data[half:]
			}
			w.Write(data)
		case r.Method == http.MethodPut && action == "archive":
			d.ev.add("docker.write %s path=%s", c.volume, r.URL.Query().Get("path"))
			if !d.step("write", w, r) {
				io.Copy(io.Discard, r.Body)
				return
			}
			if c.ro {
				dockerError(w, 403, "container rootfs is marked read-only")
				return
			}
			data, err := io.ReadAll(r.Body)
			if err != nil {
				dockerError(w, 500, err.Error())
				return
			}
			d.mu.Lock()
			if v := d.volumes[c.volume]; v != nil {
				v.stored = data
			}
			d.mu.Unlock()
			w.WriteHeader(200)
		default:
			// Above all: a helper container must never be started.
			d.t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL.Path)
			d.ev.add("docker.UNEXPECTED %s %s", r.Method, p)
			dockerError(w, 500, "unexpected request")
		}

	case r.Method == http.MethodGet && strings.HasPrefix(p, "/images/") && strings.HasSuffix(p, "/json"):
		ref := strings.TrimSuffix(strings.TrimPrefix(p, "/images/"), "/json")
		if !d.step("image-inspect", w, r) {
			return
		}
		d.mu.Lock()
		ok := d.images[ref]
		d.mu.Unlock()
		if !ok {
			dockerError(w, 404, "No such image: "+ref)
			return
		}
		dockerJSON(w, 200, map[string]any{"Id": "sha256:" + strings.Repeat("a", 64), "RepoTags": []string{ref}})

	case r.Method == http.MethodPost && p == "/images/create":
		ref := r.URL.Query().Get("fromImage")
		if tag := r.URL.Query().Get("tag"); tag != "" {
			ref += ":" + tag
		}
		d.ev.add("docker.pull %s", ref)
		d.mu.Lock()
		allowed := d.allowPull
		if allowed {
			d.images[ref] = true
		}
		d.mu.Unlock()
		if !allowed {
			d.t.Errorf("unexpected image pull: %s", ref)
			dockerError(w, 500, "pull not expected")
			return
		}
		w.WriteHeader(200)
		io.WriteString(w, `{"status":"Pulling"}`+"\n"+`{"status":"Done"}`+"\n")

	default:
		d.t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL.Path)
		d.ev.add("docker.UNEXPECTED %s %s", r.Method, p)
		dockerError(w, 500, "unexpected request")
	}
}

/* ---------- the environment ---------- */

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

const (
	testHost      = "panel.test:8080"
	adminPassword = "yonetici-parolasi-1"
	userPassword  = "kullanici-parolasi-1"
)

type testEnv struct {
	t        *testing.T
	dir      string
	root     string // allowed root for host folders
	db       *sql.DB
	cfg      *config.Config
	settings *settings.Store
	m        *Module
	apps     *fakeApps
	docker   *fakeDocker
	ev       *eventLog
	logs     *syncBuffer
	handler  http.Handler
	admin    *session
	user     *session
	// responses collects every response body, for the secrecy checks.
	respMu    sync.Mutex
	responses []string
}

type session struct {
	cookie *http.Cookie
	csrf   string
}

// newEnvIn builds the module over a temporary database with fakes for the
// application module and Docker. dataDir may be "" for a fresh directory.
func newEnvIn(t *testing.T, dataDir string) *testEnv {
	t.Helper()
	if dataDir == "" {
		dataDir = t.TempDir()
	}
	e := &testEnv{t: t, dir: dataDir, root: t.TempDir(), ev: &eventLog{}, logs: &syncBuffer{}}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := database.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	e.db = db
	e.settings, err = settings.NewStore(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.settings.SetStrings(ctx, settings.KeyAllowedRoots, []string{e.root}); err != nil {
		t.Fatal(err)
	}
	if err := e.settings.Set(ctx, settings.KeySetupComplete, "true"); err != nil {
		t.Fatal(err)
	}
	e.apps = newFakeApps(e.ev)
	e.docker = newFakeDocker(t, e.ev)
	e.cfg = &config.Config{
		DataDir:     dataDir,
		ManifestDir: filepath.Join(t.TempDir(), "manifests"),
		HelperPath:  filepath.Join(dataDir, "no-such-helper"),
		DockerHost:  e.docker.host(),
	}
	al := audit.New(db)
	svc, err := auth.NewService(db, e.cfg, e.settings, al, privileged.New(e.cfg.HelperPath))
	if err != nil {
		t.Fatal(err)
	}
	mod, err := New(module.Deps{Cfg: e.cfg, DB: db, Settings: e.settings, Audit: al, Notify: notify.New(db), Auth: svc}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.m = mod.(*Module)
	e.m.SetApps(e.apps)
	// Jobs must not outlive the test that started them.
	base, cancel := context.WithCancel(context.Background())
	e.m.base = base
	t.Cleanup(func() {
		cancel()
		deadline := time.Now().Add(10 * time.Second)
		for e.m.jobs.anyRunning() && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
	})

	mux := http.NewServeMux()
	rt := httpx.NewRouter(mux)
	svc.RegisterPublic(rt.Group("/api/v1"))
	e.m.Register(rt.Group("/api/v1", svc.Require), rt.Group("/api/v1", svc.RequireWebSocket))
	e.handler = httpx.Recover(mux)

	for name, u := range map[string]struct{ pass, role string }{"yonetici": {adminPassword, auth.RoleAdmin}, "misafir": {userPassword, auth.RoleUser}} {
		hash, err := auth.HashPassword(u.pass)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO users (username, password_hash, role, created_at) VALUES (?, ?, ?, ?)`,
			name, hash, u.role, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
	}
	e.admin = e.login("yonetici", adminPassword)
	e.user = e.login("misafir", userPassword)
	return e
}

func newEnv(t *testing.T) *testEnv { return newEnvIn(t, "") }

type apiResponse struct {
	Status  int
	Header  http.Header
	Body    []byte
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (r *apiResponse) code() string {
	if r.Error == nil {
		return ""
	}
	return r.Error.Code
}

func (r *apiResponse) message() string {
	if r.Error == nil {
		return ""
	}
	return r.Error.Message
}

func (e *testEnv) serve(req *http.Request, s *session) *apiResponse {
	e.t.Helper()
	req.Host = testHost
	req.RemoteAddr = "192.0.2.10:40000"
	if s != nil {
		req.AddCookie(&http.Cookie{Name: s.cookie.Name, Value: s.cookie.Value})
		if req.Header.Get("X-CSRF-Token") == "" {
			req.Header.Set("X-CSRF-Token", s.csrf)
		}
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	res := &apiResponse{Status: rec.Code, Header: rec.Header(), Body: rec.Body.Bytes()}
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(res.Body, res); err != nil {
			e.t.Fatalf("%s %s: response is not an envelope: %v\n%s", req.Method, req.URL.Path, err, res.Body)
		}
	}
	e.respMu.Lock()
	e.responses = append(e.responses, fmt.Sprintf("%s %s -> %d %v\n%s", req.Method, req.URL.Path, rec.Code, rec.Header(), res.Body))
	e.respMu.Unlock()
	return res
}

// do sends a JSON request below /api/v1/backup.
func (e *testEnv) do(s *session, method, path string, body any) *apiResponse {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, "http://"+testHost+"/api/v1/backup"+path, rd)
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return e.serve(req, s)
}

func (e *testEnv) login(username, password string) *session {
	e.t.Helper()
	raw, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req := httptest.NewRequest("POST", "http://"+testHost+"/api/v1/auth/login", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Host = testHost
	req.RemoteAddr = "192.0.2.10:40000"
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		e.t.Fatalf("login %s: %d %s", username, rec.Code, rec.Body)
	}
	s := &session{}
	for _, c := range rec.Result().Cookies() {
		if c.Value != "" {
			s.cookie = c
		}
	}
	var env struct {
		Data struct {
			CSRF string `json:"csrf_token"`
		} `json:"data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &env)
	s.csrf = env.Data.CSRF
	if s.cookie == nil || s.csrf == "" {
		e.t.Fatalf("login %s: no session", username)
	}
	return s
}

// upload posts a file to /upload as multipart form data.
func (e *testEnv) upload(s *session, data []byte) *apiResponse {
	e.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "yedek.tar.gz")
	fw.Write(data)
	mw.Close()
	req := httptest.NewRequest("POST", "http://"+testHost+"/api/v1/backup/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return e.serve(req, s)
}

func (e *testEnv) jobFrom(res *apiResponse) JobView {
	e.t.Helper()
	if res.Status != http.StatusAccepted {
		e.t.Fatalf("job was not started: %d %s", res.Status, res.Body)
	}
	var v JobView
	if err := json.Unmarshal(res.Data, &v); err != nil || v.ID == "" {
		e.t.Fatalf("job view: %v %s", err, res.Data)
	}
	return v
}

// wait blocks until the job ended and returns its final view.
func (e *testEnv) wait(id string) JobView {
	e.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		j := e.m.jobs.get(id)
		if j == nil {
			e.t.Fatalf("job %s does not exist", id)
		}
		held := false
		e.m.jobs.mu.Lock()
		for _, holder := range e.m.jobs.locks {
			held = held || holder == id
		}
		e.m.jobs.mu.Unlock()
		if v := j.view(); v.Status != statusRunning && !held {
			return v
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.t.Fatalf("job %s did not end; events: %q", id, e.ev.all())
	return JobView{}
}

// waitFor blocks until cond holds.
func (e *testEnv) waitFor(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.t.Fatalf("timeout waiting for %s; events: %q", what, e.ev.all())
}

func (e *testEnv) records(slug string) []*Record {
	e.t.Helper()
	recs, err := e.m.store.list(context.Background(), slug)
	if err != nil {
		e.t.Fatal(err)
	}
	return recs
}

// files lists the files below the backup directory, relative to it.
func (e *testEnv) files() []string {
	e.t.Helper()
	var out []string
	filepath.Walk(e.m.dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			rel, _ := filepath.Rel(e.m.dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}

type auditRow struct {
	User, Action, Target, Detail string
	Success                      bool
}

func (e *testEnv) audit() []auditRow {
	e.t.Helper()
	rows, err := e.db.Query(`SELECT username, action, target, detail, success FROM audit_log ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		if err := rows.Scan(&r.User, &r.Action, &r.Target, &r.Detail, &r.Success); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func (e *testEnv) auditOf(action string) []auditRow {
	var out []auditRow
	for _, r := range e.audit() {
		if r.Action == action {
			out = append(out, r)
		}
	}
	return out
}

func (e *testEnv) notifications() []string {
	e.t.Helper()
	rows, err := e.db.Query(`SELECT severity || ': ' || title || ': ' || message FROM notifications ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

// standardApp installs the test application with one volume holding two
// files, and makes its image available.
func (e *testEnv) standardApp(running bool) *apps.Config {
	cfg := testConfig("fotolar")
	e.apps.install(cfg, running)
	e.docker.images[cfg.Services[0].Image] = true
	e.docker.addVolume("myserver-fotolar-data", "fotolar",
		tarFile{name: "foto.jpg", body: "JPEG-verisi", uid: 1000, gid: 1000},
		tarFile{name: "alt/", typ: tar.TypeDir},
		tarFile{name: "alt/kasa.kdbx", body: "parola kasası", mode: 0o600},
	)
	return cfg
}

// putArchive stores a backup file as if the module had made it and returns
// its record.
func (e *testEnv) putArchive(slug string, data []byte, encrypted bool) *Record {
	e.t.Helper()
	if err := e.m.ensureDirs(slug); err != nil {
		e.t.Fatal(err)
	}
	name, err := e.m.uniqueName(context.Background(), slug, time.Unix(1_750_000_000, 0), encrypted)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.m.appDir(slug), name), data, 0o600); err != nil {
		e.t.Fatal(err)
	}
	rec := &Record{Slug: slug, AppName: "Fotolar", AppVersion: "1.2.3", FileName: name, Size: int64(len(data)),
		CreatedAt: 1_750_000_000, Status: statusSuccess, Consistency: consistencyStopped, Encrypted: encrypted,
		IncludesBinds: true, Trigger: triggerManual}
	rec.ID, err = e.m.store.insert(context.Background(), rec)
	if err != nil {
		e.t.Fatal(err)
	}
	return rec
}

// mutating lists the events that change the state of the server.
func mutating(events []string) []string {
	var out []string
	for _, ev := range events {
		for _, p := range []string{"apps.stop", "apps.start", "apps.recreate", "docker.create", "docker.remove", "docker.stop",
			"docker.write", "docker.volume", "docker.pull", "docker.UNEXPECTED"} {
			if strings.HasPrefix(ev, p) {
				out = append(out, ev)
			}
		}
	}
	return out
}

// listTar returns the entries of a tar stream by name; the body of a file
// is kept under the PAX key "test.body".
func listTar(t *testing.T, stream []byte) map[string]*tar.Header {
	t.Helper()
	out := map[string]*tar.Header{}
	tr := tar.NewReader(bytes.NewReader(stream))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		b, _ := io.ReadAll(tr)
		h.PAXRecords = map[string]string{"test.body": string(b)}
		out[h.Name] = h
	}
}
