package apps

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

/* ---------- route enumeration ---------- */

// The router does not expose what was registered, so the routes are read
// from the source of Register. A route added later is covered by the
// authorisation test without anyone having to remember it.
//
//go:embed module.go
var moduleSource string

type route struct {
	method, pattern string
	admin           bool
}

func (r route) key() string { return r.method + " " + r.pattern }

var (
	registerRe = regexp.MustCompile(`(?s)func \(m \*Module\) Register\(api, _ \*httpx\.Router\) \{\n(.*?)\n\}\n`)
	groupRe    = regexp.MustCompile(`^(\w+) := api\.Group\("([^"]*)"(, auth\.RequireAdmin)?\)$`)
	routeRe    = regexp.MustCompile(`^(\w+)\.(Get|Post|Put|Delete)\("([^"]*)", `)
)

func registeredRoutes(t *testing.T) []route {
	t.Helper()
	m := registerRe.FindStringSubmatch(moduleSource)
	if m == nil {
		t.Fatal("Register not found in module.go")
	}
	type group struct {
		prefix string
		admin  bool
	}
	groups := map[string]group{}
	var out []route
	for _, line := range strings.Split(m[1], "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if g := groupRe.FindStringSubmatch(line); g != nil {
			groups[g[1]] = group{g[2], g[3] != ""}
			continue
		}
		x := routeRe.FindStringSubmatch(line)
		if x == nil {
			t.Fatalf("line of Register not understood by the test: %s", line)
		}
		g, ok := groups[x[1]]
		if !ok {
			t.Fatalf("unknown router in: %s", line)
		}
		out = append(out, route{method: strings.ToUpper(x[2]), pattern: g.prefix + x[3], admin: g.admin})
	}
	if len(out) < 15 {
		t.Fatalf("only %d routes found, the parser is broken", len(out))
	}
	return out
}

const someJobID = "0123456789abcdef01234567"

func fill(t *testing.T, pattern string) string {
	t.Helper()
	p := strings.NewReplacer("{slug}", "tek", "{id}", someJobID, "{name}", "tek.svg").Replace(pattern)
	if strings.ContainsAny(p, "{}") {
		t.Fatalf("test does not know how to fill %q", pattern)
	}
	return p
}

// openRoutes is everything a signed-in user without the admin role may call.
var openRoutes = map[string]bool{
	"GET /apps/catalog":          true,
	"GET /apps/catalog/{slug}":   true,
	"GET /apps/installed":        true,
	"GET /apps/installed/{slug}": true,
	"GET /apps/events":           true,
	"GET /apps/icons/{name}":     true,
}

func TestAuthorisation(t *testing.T) {
	captureLogs(t)
	fake := newFakeDocker(t)
	e := newTestEnv(t, fake.host(), map[string]string{"tek.yaml": manifestTek})
	seedInstalled(t, e, fake, "tek", Inputs{Env: map[string]string{"ADMIN_PASSWORD": "x"}})
	fake.reset()

	seen := map[string]bool{}
	for _, r := range registeredRoutes(t) {
		seen[r.key()] = true
		path := fill(t, r.pattern)

		// The pattern is really mounted.
		req := httptest.NewRequest(r.method, "http://"+testHost+"/api/v1"+path, nil)
		if _, pattern := e.mux.Handler(req); pattern != r.method+" /api/v1"+r.pattern {
			t.Errorf("%s resolves to %q", r.key(), pattern)
		}
		if r.admin == openRoutes[r.key()] {
			t.Errorf("%s: admin=%v does not match the list of open routes", r.key(), r.admin)
		}
		if r.method != http.MethodGet && !r.admin {
			t.Errorf("%s changes state and is not admin only", r.key())
		}

		// Nobody.
		if w := e.do(r.method, path, nil, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s anonymous: %d, want 401", r.key(), w.Code)
		}
		if !r.admin {
			continue
		}
		// A signed-in user who is not an administrator.
		body := ""
		if r.method != http.MethodGet {
			body = `{}`
		}
		w := e.do(r.method, path, e.user, body)
		wantError(t, w, http.StatusForbidden, "forbidden")
		// A state-changing request without the CSRF token.
		if r.method != http.MethodGet {
			req := httptest.NewRequest(r.method, "http://"+testHost+"/api/v1"+path, strings.NewReader(body))
			req.AddCookie(&http.Cookie{Name: "myserver_session", Value: e.admin.token})
			rec := httptest.NewRecorder()
			e.h.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s without CSRF token: %d, want 403", r.key(), rec.Code)
			}
		}
	}
	for k := range openRoutes {
		if !seen[k] {
			t.Errorf("open route %s is not registered", k)
		}
	}
	for _, k := range []string{"GET /apps/installed/{slug}/logs", "GET /apps/jobs/{id}/stream", "POST /apps/catalog/{slug}/install",
		"POST /apps/installed/{slug}/uninstall", "PUT /apps/installed/{slug}/settings", "POST /apps/reload"} {
		if !seen[k] {
			t.Errorf("expected route %s is not registered", k)
		}
	}
	if ops := fake.seen(); len(ops) != 0 {
		t.Errorf("refused requests reached Docker: %v", ops)
	}
	if it, _ := e.mod.store.get(context.Background(), "tek"); it == nil {
		t.Error("a refused request removed the application")
	}
	for _, a := range e.auditRows() {
		if a.Username == e.user.name {
			t.Errorf("a refused request was recorded as an action: %+v", a)
		}
	}
}

// seedInstalled records an application as installed and puts its running
// containers, network and volumes into the fake.
func seedInstalled(t *testing.T, e *testEnv, f *fakeDocker, slug string, in Inputs) *Installed {
	t.Helper()
	m := e.mod.catalog.Get(slug)
	if m == nil {
		t.Fatalf("manifest %s not loaded", slug)
	}
	cfg, norm, err := Resolve(m, in, nil, e.mod.roots())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	images := map[string]ImageInfo{}
	f.mu.Lock()
	for _, s := range cfg.Services {
		f.images[s.Image] = "sha256:" + strings.Repeat("a", 60) + "-" + s.Name
		images[s.Name] = ImageInfo{Image: s.Image, ID: f.images[s.Image], Digest: s.Image + "@" + f.images[s.Image]}
		for _, v := range s.Volumes {
			if v.Type == VolumeNamed {
				f.volumes[v.Source] = managed(slug, "")
			}
		}
	}
	if cfg.Network != "" {
		f.networks[cfg.Network] = managed(slug, "")
	}
	f.mu.Unlock()
	for _, s := range cfg.Services {
		var ports []fakePort
		for _, p := range s.Ports {
			ports = append(ports, fakePort{IP: p.HostIP, PrivatePort: p.Container, PublicPort: p.Host, Type: p.Protocol})
		}
		f.addContainer(s.ContainerName, s.Image, "running", managed(slug, s.Name), ports...)
	}
	if err := e.mod.store.save(context.Background(), cfg, norm, images); err != nil {
		t.Fatal(err)
	}
	it, err := e.mod.store.get(context.Background(), slug)
	if err != nil || it == nil {
		t.Fatalf("seed: %v", err)
	}
	return it
}

/* ---------- what non-admins see ---------- */

func TestStoredValuesAreForAdminsOnly(t *testing.T) {
	captureLogs(t)
	fake := newFakeDocker(t)
	e := newTestEnv(t, fake.host(), map[string]string{"tek.yaml": manifestTek, "bad.yaml": "name: [\n"})
	media := filepath.Join(e.root, "ozel-klasor")
	it := seedInstalled(t, e, fake, "tek", Inputs{
		Env:   map[string]string{"ADMIN_PASSWORD": "cok-gizli-parola", "TZ": "Asia/Tokyo"},
		Paths: map[string]string{"media": media}, Ports: map[string]int{"web": 19090},
		Options: map[string]bool{"rawnet": true}, BindAddress: BindLoopback,
	})
	generated := it.Inputs.Env["DB_PASSWORD"]

	type detail struct {
		Fields       fieldsView `json:"fields"`
		BindAddress  string     `json:"bind_address"`
		InstalledApp *struct {
			Slug string `json:"slug"`
		} `json:"installed_app"`
	}
	w := e.do("GET", "/apps/catalog/tek", e.user, "")
	var d detail
	decodeData(t, w, &d)
	body := w.Body.String()
	for _, secret := range []string{"cok-gizli-parola", generated, "Asia/Tokyo", "ozel-klasor"} {
		if strings.Contains(body, secret) {
			t.Errorf("a non-admin sees the stored value %q", secret)
		}
	}
	for _, f := range d.Fields.Env {
		if f.HasValue || (f.Secret && f.Value != "") {
			t.Errorf("non-admin env field: %+v", f)
		}
	}
	if d.Fields.Ports[0].Value != 18080 || d.Fields.Options[0].Value || d.BindAddress != BindAll {
		t.Errorf("non-admin sees stored choices: %+v %+v %s", d.Fields.Ports, d.Fields.Options, d.BindAddress)
	}
	if d.InstalledApp == nil || d.InstalledApp.Slug != "tek" {
		t.Error("the installed state itself is visible to every user")
	}

	// The administrator gets the plain values, and has_value for secrets.
	w = e.do("GET", "/apps/catalog/tek", e.admin, "")
	d = detail{}
	decodeData(t, w, &d)
	body = w.Body.String()
	for _, secret := range []string{"cok-gizli-parola", generated} {
		if strings.Contains(body, secret) {
			t.Errorf("the detail contains the secret %q", secret)
		}
	}
	byKey := map[string]envField{}
	for _, f := range d.Fields.Env {
		byKey[f.Key] = f
	}
	for _, k := range []string{"ADMIN_PASSWORD", "DB_PASSWORD"} {
		if f := byKey[k]; !f.Secret || !f.HasValue || f.Value != "" || f.Default != "" {
			t.Errorf("%s: %+v", k, f)
		}
	}
	if !byKey["DB_PASSWORD"].Generated {
		t.Error("generated flag missing")
	}
	if f := byKey["TZ"]; f.Value != "Asia/Tokyo" || f.Default != "Europe/Istanbul" || f.Secret {
		t.Errorf("TZ: %+v", f)
	}
	if d.Fields.Ports[0].Value != 19090 || !d.Fields.Options[0].Value || d.BindAddress != BindLoopback ||
		d.Fields.Paths[0].Value != media {
		t.Errorf("admin does not see the stored choices: %+v", d)
	}

	// Broken manifests are listed to administrators only.
	var cat struct {
		Invalid []InvalidManifest `json:"invalid"`
		Apps    []catalogView     `json:"apps"`
	}
	decodeData(t, e.do("GET", "/apps/catalog", e.user, ""), &cat)
	if len(cat.Invalid) != 0 || len(cat.Apps) != 1 || !cat.Apps[0].Installed {
		t.Errorf("catalog for a user: %+v", cat)
	}
	decodeData(t, e.do("GET", "/apps/catalog", e.admin, ""), &cat)
	if len(cat.Invalid) != 1 || cat.Invalid[0].File != "bad.yaml" {
		t.Errorf("catalog for an admin: %+v", cat.Invalid)
	}

	// Neither list of installed applications carries values.
	for _, who := range []*creds{e.user, e.admin} {
		for _, p := range []string{"/apps/installed", "/apps/installed/tek"} {
			w := e.do("GET", p, who, "")
			if w.Code != 200 {
				t.Fatalf("%s: %d", p, w.Code)
			}
			for _, secret := range []string{"cok-gizli-parola", generated, "Asia/Tokyo", "ADMIN_PASSWORD"} {
				if strings.Contains(w.Body.String(), secret) {
					t.Errorf("%s contains %q", p, secret)
				}
			}
		}
	}
}

/* ---------- architecture and risk acceptance ---------- */

func TestUnsupportedArchitecture(t *testing.T) {
	captureLogs(t)
	fake := newFakeDocker(t)
	other := "arm64"
	if runtime.GOARCH == "arm64" {
		other = "amd64"
	}
	e := newTestEnv(t, fake.host(), map[string]string{
		"kol.yaml":  strings.Replace(manifestKol, "[arm64]", "["+other+", riscv64]", 1),
		"bura.yaml": strings.Replace(strings.Replace(manifestKol, "[arm64]", "["+runtime.GOARCH+"]", 1), "slug: kol", "slug: bura", 1),
	})
	w := e.do("POST", "/apps/catalog/kol/install", e.admin, `{}`)
	wantError(t, w, http.StatusConflict, "unsupported_architecture",
		"işlemci mimarisini", runtime.GOARCH, "desteklemiyor", other+", riscv64")
	if ops := fake.seen(); len(ops) != 0 {
		t.Errorf("Docker was asked for an unsupported application: %v", ops)
	}
	var d struct {
		Installable       bool     `json:"installable"`
		UnsupportedReason string   `json:"unsupported_reason"`
		Architectures     []string `json:"architectures"`
	}
	decodeData(t, e.do("GET", "/apps/catalog/kol", e.admin, ""), &d)
	if d.Installable || !strings.Contains(d.UnsupportedReason, "desteklemiyor") || len(d.Architectures) != 2 {
		t.Errorf("detail: %+v", d)
	}
	decodeData(t, e.do("GET", "/apps/catalog/bura", e.admin, ""), &d)
	if !d.Installable || d.UnsupportedReason != "" {
		t.Errorf("supported application: %+v", d)
	}
	job := e.startJob("POST", "/apps/catalog/bura/install", `{}`)
	if job.Status != JobSuccess {
		t.Errorf("supported application did not install: %+v", job)
	}
}

func TestRiskAcceptance(t *testing.T) {
	captureLogs(t)
	fake := newFakeDocker(t)
	e := newTestEnv(t, fake.host(), map[string]string{"soket.yaml": manifestSoket, "tek.yaml": manifestTek})

	var d struct {
		Warnings               []Warning    `json:"warnings"`
		RequiresRiskAcceptance bool         `json:"requires_risk_acceptance"`
		WarningLevel           string       `json:"warning_level"`
		Volumes                []volumeView `json:"volumes"`
	}
	decodeData(t, e.do("GET", "/apps/catalog/soket", e.user, ""), &d)
	if !d.RequiresRiskAcceptance || d.WarningLevel != "danger" || len(d.Warnings) != 1 ||
		d.Warnings[0].Level != "danger" || d.Warnings[0].Code != "docker_socket" {
		t.Errorf("detail: %+v", d)
	}
	sys := false
	for _, v := range d.Volumes {
		if v.Type == VolumeSystem && v.Source == "/var/run/docker.sock" {
			sys = true
		}
	}
	if !sys {
		t.Errorf("the system mount is not listed: %+v", d.Volumes)
	}

	for _, body := range []string{`{}`, `{"accept_risks":false}`} {
		w := e.do("POST", "/apps/catalog/soket/install", e.admin, body)
		wantError(t, w, http.StatusBadRequest, "risk_not_accepted", "güvenlik uyarılarını")
	}
	// Anything that is not the boolean true does not count.
	for _, body := range []string{`{"accept_risks":"true"}`, `{"accept_risks":1}`, `{"acceptRisks":true}`, `{"accept_risks":null}`} {
		if w := e.do("POST", "/apps/catalog/soket/install", e.admin, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", body, w.Code)
		}
	}
	if ops := fake.seen(); len(ops) != 0 {
		t.Errorf("Docker was asked before the risks were accepted: %v", ops)
	}

	// A request cannot carry privileges: unknown fields are refused.
	for _, body := range []string{
		`{"env":{"ADMIN_PASSWORD":"x"},"privileged":true}`,
		`{"env":{"ADMIN_PASSWORD":"x"},"cap_add":["SYS_ADMIN"]}`,
		`{"env":{"ADMIN_PASSWORD":"x"},"network_mode":"host"}`,
		`{"env":{"ADMIN_PASSWORD":"x"},"devices":[{"host":"/dev/sda"}]}`,
		`{"env":{"ADMIN_PASSWORD":"x"},"volumes":[{"type":"system","source":"/","target":"/host"}]}`,
		`{"env":{"ADMIN_PASSWORD":"x"},"paths":{"sock":"/var/run/docker.sock"}}`,
		`{"env":{"ADMIN_PASSWORD":"x"},"options":{"privileged":true}}`,
		`{"env":{"ADMIN_PASSWORD":"x"},"bind_address":"0.0.0.0"}`,
	} {
		if w := e.do("POST", "/apps/catalog/tek/install", e.admin, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	if ops := fake.changes(); len(ops) != 0 {
		t.Errorf("a refused request changed Docker: %v", ops)
	}
}

/* ---------- icons ---------- */

const pngHeader = "\x89PNG\r\n\x1a\n"

func iconRequest(t *testing.T, e *testEnv, rawPath string, who *creds) *httptest.ResponseRecorder {
	t.Helper()
	u, err := url.Parse("http://" + testHost + "/api/v1/apps/icons/" + rawPath)
	if err != nil {
		return nil // cannot even be expressed as a request
	}
	r := httptest.NewRequest("GET", "http://"+testHost+"/api/v1/apps/icons/x", nil)
	r.URL = u
	r.RequestURI = u.RequestURI()
	sign(r, who)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func TestIconEndpoint(t *testing.T) {
	captureLogs(t)
	e := newTestEnv(t, "tcp://127.0.0.1:1", nil)
	const svg = `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`
	const secret = "ICON-TEST-SECRET-CONTENT"
	outside := filepath.Dir(e.iconDir)
	writeFiles(t, e.iconDir, map[string]string{
		"tek.svg":     svg,
		"resim.png":   pngHeader + "rest of the image",
		"sahte.png":   "<html><script>alert(1)</script></html>",
		"buyuk.svg":   strings.Repeat("a", maxIconBytes+1),
		"tek.txt":     secret,
		"tek.html":    secret,
		"a.b.svg":     secret,
		"tek.svg.bak": secret,
		"BUYUK.svg":   secret,
	})
	writeFiles(t, outside, map[string]string{"secret.svg": secret, "secret.png": pngHeader + secret})
	if err := os.Mkdir(filepath.Join(e.iconDir, "klasor.svg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.iconDir, "klasor.svg", "ic.svg"), []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	links := true
	for name, target := range map[string]string{
		"link.svg":     filepath.Join(outside, "secret.svg"),
		"link.png":     filepath.Join(outside, "secret.png"),
		"passwd.svg":   "/etc/passwd",
		"relative.svg": "../secret.svg",
		"inside.svg":   "tek.txt",
	} {
		if err := os.Symlink(target, filepath.Join(e.iconDir, name)); err != nil {
			links = false
		}
	}

	for _, who := range []*creds{e.admin, e.user} {
		w := iconRequest(t, e, "tek.svg", who)
		if w.Code != 200 || w.Body.String() != svg {
			t.Fatalf("tek.svg: %d %q", w.Code, w.Body.String())
		}
		h := w.Header()
		if h.Get("Content-Type") != "image/svg+xml" {
			t.Errorf("content type %q", h.Get("Content-Type"))
		}
		if h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("nosniff missing: %q", h.Get("X-Content-Type-Options"))
		}
		csp := h.Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "sandbox") {
			t.Errorf("CSP %q", csp)
		}
		for _, bad := range []string{"script-src", "unsafe-eval", "*", "allow-scripts", "allow-same-origin"} {
			if strings.Contains(csp, bad) {
				t.Errorf("CSP %q contains %q", csp, bad)
			}
		}
	}
	w := iconRequest(t, e, "resim.png", e.user)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Errorf("resim.png: %d %v", w.Code, w.Header())
	}
	if w := iconRequest(t, e, "tek.svg", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", w.Code)
	}

	refused := []string{
		// traversal, plain and encoded
		"../secret.svg", "..%2Fsecret.svg", "..%2fsecret.svg", "%2e%2e%2fsecret.svg", "%2e%2e/secret.svg",
		"..%5Csecret.svg", "..%255Csecret.svg", "..%252Fsecret.svg", "....//secret.svg", "..%2F..%2F..%2Fetc%2Fpasswd",
		"..%2F..%2Fpanel%2Ftest.db", "..%2Fmanifests%2Ftek.yaml",
		// absolute paths
		"%2Fetc%2Fpasswd", "%2Fetc%2Fpasswd.svg", "/etc/passwd.svg", filepath.ToSlash(outside)[1:] + "/secret.svg",
		url.PathEscape(filepath.Join(outside, "secret.svg")),
		// NUL and control characters
		"tek.svg%00", "tek%00.svg", "tek.txt%00.svg", "tek.svg%00.png", "tek.svg%0a", "tek.svg%0d%0aX-Injected:%201",
		// extra dots and other extensions
		"a.b.svg", "tek..svg", "tek.svg.svg", "tek.svg.bak", ".svg", "..svg", "...svg", "tek.svg.", "tek.", "tek",
		"tek.txt", "tek.html", "tek.SVG", "BUYUK.svg", "tek.svg%20", "%20tek.svg", "tek.svg/", "tek.svg/ic.svg",
		"klasor.svg/ic.svg", "klasor.svg%2Fic.svg",
		// not files, too large, wrong content, absent
		"klasor.svg", "buyuk.svg", "sahte.png", "yok.svg",
	}
	if links {
		refused = append(refused, "link.svg", "link.png", "passwd.svg", "relative.svg", "inside.svg")
	} else {
		t.Log("symlinks are not available here; the symlink cases were not run")
	}
	for _, p := range refused {
		w := iconRequest(t, e, p, e.admin)
		if w == nil {
			continue
		}
		if w.Code == 200 {
			t.Errorf("%q was served: %q", p, w.Body.String())
		}
		if strings.Contains(w.Body.String(), secret) || strings.Contains(w.Body.String(), "root:") {
			t.Errorf("%q leaked file content", p)
		}
		if w.Header().Get("X-Injected") != "" {
			t.Errorf("%q injected a header", p)
		}
		if loc := w.Header().Get("Location"); loc != "" {
			// A redirect of the router must not lead to a served file.
			u, err := url.Parse(loc)
			if err != nil {
				continue
			}
			r := httptest.NewRequest("GET", "http://"+testHost+"/x", nil)
			r.URL.Path, r.URL.RawPath = u.Path, u.RawPath
			sign(r, e.admin)
			rec := httptest.NewRecorder()
			e.h.ServeHTTP(rec, r)
			if strings.Contains(rec.Body.String(), secret) || strings.Contains(rec.Body.String(), "root:") {
				t.Errorf("%q leaked file content after the redirect to %s", p, loc)
			}
		}
	}
}

/* ---------- catalog reload over HTTP ---------- */

func TestReload(t *testing.T) {
	captureLogs(t)
	e := newTestEnv(t, "tcp://127.0.0.1:1", map[string]string{"tek.yaml": manifestTek})
	writeFiles(t, e.cfg.ManifestDir, map[string]string{"cift.yaml": manifestCift, "bozuk.yaml": "slug: [\n"})
	var cat struct {
		Apps []catalogView `json:"apps"`
	}
	decodeData(t, e.do("GET", "/apps/catalog", e.admin, ""), &cat)
	if len(cat.Apps) != 1 {
		t.Fatalf("new files are loaded only on reload: %+v", cat.Apps)
	}
	var res struct {
		Valid   int               `json:"valid"`
		Invalid []InvalidManifest `json:"invalid"`
	}
	decodeData(t, e.do("POST", "/apps/reload", e.admin, ""), &res)
	if res.Valid != 2 || len(res.Invalid) != 1 || res.Invalid[0].File != "bozuk.yaml" {
		t.Errorf("reload: %+v", res)
	}
	rows := e.findAudit("apps.reload")
	if len(rows) != 1 || !rows[0].Success || rows[0].Username != e.admin.name {
		t.Errorf("audit: %+v", rows)
	}
	raw, _ := json.Marshal(res)
	if strings.Contains(string(raw), e.cfg.ManifestDir) {
		t.Errorf("the reload result exposes server paths: %s", raw)
	}
}
