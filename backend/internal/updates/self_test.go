package updates

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"myserver/internal/config"
)

const hostileNotes = "## Yenilikler\n\n- <b>kalın</b> <img src=x onerror=alert(1)>\n- <script>alert('xss')</script>\n" +
	"- [bağlantı](javascript:alert(1))\n\tgirintili satır & \"tırnak\""

/* ---------- semantic versions ---------- */

func TestSemverOrdering(t *testing.T) {
	// Each version is newer than the one before it (semver.org, section 11).
	chain := []string{"0.0.1", "0.1.0", "0.9.9", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta",
		"1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0-rc.2", "1.0.0-rc.10", "1.0.0", "1.0.1", "1.0.10",
		"1.1.0", "1.9.0", "1.10.0", "1.10.1", "2.0.0-dev", "2.0.0", "10.0.0"}
	for i, a := range chain {
		for j, b := range chain {
			if got, want := isNewer(a, b), i > j; got != want {
				t.Errorf("isNewer(%s, %s) = %v", a, b, got)
			}
		}
	}
	same := [][2]string{
		{"1.2.3", "v1.2.3"}, {"1.2.3", " 1.2.3 "}, {"1.2.3+build.5", "1.2.3"}, {"1.2.3+a", "1.2.3+b"},
		{"1.0.0-rc.1+build", "1.0.0-rc.1"}, {"v1.0.0-rc.1", "1.0.0-rc.1"},
	}
	for _, c := range same {
		if isNewer(c[0], c[1]) || isNewer(c[1], c[0]) {
			t.Errorf("%q and %q are not treated as the same version", c[0], c[1])
		}
		a, ok1 := parseSemver(c[0])
		b, ok2 := parseSemver(c[1])
		if !ok1 || !ok2 || compareSemver(a, b) != 0 {
			t.Errorf("compare(%q, %q)", c[0], c[1])
		}
	}
	if !isNewer("1.2.4+build", "1.2.3") || !isNewer("1.2.0", "1.2.0-dev") || isNewer("1.2.0-dev", "1.2.0") {
		t.Error("build metadata or pre-release handling")
	}
}

func TestSemverRejectsInvalidVersions(t *testing.T) {
	bad := []string{"", "v", "1", "1.2", "1.2.3.4", "1.2.x", "a.b.c", "latest", "01.2.3", "1.02.3", "1.2.03",
		"-1.2.3", "1.-2.3", "+1.2.3", "1.+2.3", "1.2.3-", "1.2.3-rc..1", "1.2.3-.rc", "1.2.3-rc.", "1..3", ".1.2.3",
		"1.2.3.", "1,2,3", "1.2.3 4", "1.2.3\n9.9.9", "0x1.2.3", "1e3.0.0", "١.٢.٣", "vv1.2.3", "V1.2.3",
		"1.2.3-rc_1", "1.2.3-rc/1", "1.2.3-é", "99999999999999999999.0.0", "1.2.3+", "1.2.3+a+b", "1.2.3+a b",
		"9.9.9;reboot", "9.9.9 --skip-verify", "--help"}
	for _, v := range bad {
		if _, ok := parseSemver(v); ok {
			t.Errorf("parseSemver(%q) succeeded", v)
		}
		// Whatever the other side is, an invalid version is never an update.
		for _, installed := range []string{"0.0.1", "1.0.0", "1.2.0-dev"} {
			if isNewer(v, installed) {
				t.Errorf("%q is offered as an update of %s", v, installed)
			}
			if isNewer("9.9.9", v) {
				t.Errorf("an update is offered over the invalid installed version %q", v)
			}
		}
	}
}

/* ---------- release server ---------- */

type releaseServer struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	requests []string
	headers  []http.Header
	handler  http.HandlerFunc
}

func newReleaseServer(t *testing.T) *releaseServer {
	t.Helper()
	s := &releaseServer{t: t}
	s.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r.Method+" "+r.URL.RequestURI())
		s.headers = append(s.headers, r.Header.Clone())
		h := s.handler
		s.mu.Unlock()
		if h == nil {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// use makes the module's release client trust this server. Call it after
// newEnv, which resets the seams.
func (s *releaseServer) use() {
	releaseTransport = s.srv.Client().Transport
	githubAPI = s.srv.URL
}

func (s *releaseServer) serve(h http.HandlerFunc) {
	s.mu.Lock()
	s.handler = h
	s.mu.Unlock()
}

func (s *releaseServer) serveBody(contentType, body string) {
	s.serve(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		io.WriteString(w, body)
	})
}

func (s *releaseServer) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func (s *releaseServer) tarball(arch string) string {
	return s.srv.URL + "/releases/download/v1.3.0/myserver-linux-" + arch + ".tar.gz"
}

var (
	sumAMD = strings.Repeat("a1", 32)
	sumARM = strings.Repeat("b2", 32)
)

func (s *releaseServer) manifest(version string) string {
	return mustJSON(map[string]any{
		"version": version, "notes": hostileNotes, "published_at": "2026-09-01T10:00:00Z",
		"url": s.srv.URL + "/releases/" + version,
		"assets": map[string]any{
			"amd64": map[string]string{"url": s.tarball("amd64"), "sha256": sumAMD},
			"arm64": map[string]string{"url": s.tarball("arm64"), "sha256": strings.ToUpper(sumARM)},
			"riscv": map[string]string{"url": s.tarball("riscv"), "sha256": sumAMD},
		},
	})
}

// github returns what api.github.com answers for releases/latest, reduced
// to a realistic selection of its fields.
func (s *releaseServer) github(withDigests bool) string {
	asset := func(name, sum string) map[string]any {
		a := map[string]any{
			"url": "https://api.github.com/repos/owner/myserver/releases/assets/1",
			"id":  1, "node_id": "RA_kwDO", "name": name, "label": "", "content_type": "application/gzip",
			"state": "uploaded", "size": 18234567, "download_count": 42,
			"uploader":             map[string]any{"login": "owner", "id": 1, "type": "User", "site_admin": false},
			"created_at":           "2026-09-01T09:58:00Z",
			"updated_at":           "2026-09-01T09:58:30Z",
			"browser_download_url": s.srv.URL + "/releases/download/v1.3.0/" + name,
		}
		if withDigests && sum != "" {
			a["digest"] = "sha256:" + sum
		} else {
			a["digest"] = nil
		}
		return a
	}
	return mustJSON(map[string]any{
		"url":      "https://api.github.com/repos/owner/myserver/releases/1",
		"html_url": s.srv.URL + "/owner/myserver/releases/tag/v1.3.0",
		"id":       1, "tag_name": "v1.3.0", "target_commitish": "main", "name": "MyServer 1.3.0",
		"draft": false, "prerelease": false, "immutable": false,
		"author":       map[string]any{"login": "owner", "id": 1, "type": "User"},
		"created_at":   "2026-09-01T09:50:00Z",
		"published_at": "2026-09-01T10:00:00Z",
		"assets": []any{
			asset("myserver-linux-arm64.tar.gz", sumARM),
			asset("SHA256SUMS", ""),
			asset("myserver-linux-amd64.tar.gz", sumAMD),
			asset("myserver-linux-amd64.tar.gz.sig", ""),
			asset("myserver-windows-amd64.zip", sumAMD),
		},
		"tarball_url": "https://api.github.com/repos/owner/myserver/tarball/v1.3.0",
		"zipball_url": "https://api.github.com/repos/owner/myserver/zipball/v1.3.0",
		"body":        hostileNotes,
	})
}

func useManifest(e *testEnv, s *releaseServer) {
	s.use()
	e.set(KeySource, sourceURL)
	e.set(KeyManifestURL, s.srv.URL+"/myserver/manifest.json")
}

func selfOf(t *testing.T, e *testEnv) selfView {
	t.Helper()
	var v selfView
	res := e.do("GET", "/updates/self", "", "user")
	if res.Status != 200 {
		t.Fatalf("status %d: %s", res.Status, res.Body)
	}
	res.into(t, &v)
	return v
}

/* ---------- sources ---------- */

func TestSelfNoSourceConfigured(t *testing.T) {
	s := newReleaseServer(t)
	e := newEnv(t)
	s.use()
	e.set(KeyGitHubRepo, "owner/myserver")
	e.set(KeyManifestURL, s.srv.URL+"/myserver/manifest.json")
	// The addresses are filled in, but the source is "none".
	e.set(KeySource, sourceNone)
	v := selfOf(t, e)
	if v.Configured || v.Source != "none" || v.Latest != nil || v.UpdateAvailable || v.Installable ||
		v.CheckedAt != nil || v.Error != nil || v.Installed != "1.2.0" || v.Arch != runtime.GOARCH {
		t.Errorf("view = %+v", v)
	}
	res := e.do("POST", "/updates/self/check", "", "admin")
	if res.Status != http.StatusConflict || res.code() != "source_not_configured" {
		t.Errorf("check: status %d code %q", res.Status, res.code())
	}
	res = e.do("POST", "/updates/self/apply", `{"version":"1.3.0","confirm":true}`, "admin")
	if res.Status != http.StatusConflict || res.code() != "source_not_configured" {
		t.Errorf("apply: status %d code %q", res.Status, res.code())
	}
	e.mod.runSchedule(context.Background())
	var sum summaryView
	e.do("GET", "/updates/summary", "", "user").into(t, &sum)
	if sum.Self.Configured || sum.Self.Count != 0 || sum.Self.Latest != "" {
		t.Errorf("summary = %+v", sum.Self)
	}
	if got := s.seen(); len(got) != 0 {
		t.Errorf("a server was contacted although no source is configured: %q", got)
	}
	for _, a := range e.helperActions() {
		if a == "updates-self" {
			t.Error("the helper was asked to update")
		}
	}
}

func TestSelfSourceIncomplete(t *testing.T) {
	e := newEnv(t)
	// The GitHub repository has a default; clear it to test the incomplete case.
	e.set(KeyGitHubRepo, "")
	for _, src := range []string{sourceGitHub, sourceURL} {
		e.set(KeySource, src)
		v := selfOf(t, e)
		if v.Configured || v.Error == nil || v.Error.Code != "source_incomplete" {
			t.Errorf("%s: view = %+v", src, v)
		}
		if res := e.do("POST", "/updates/self/check", "", "admin"); res.Status != http.StatusConflict {
			t.Errorf("%s: status %d", src, res.Status)
		}
	}
	// Values that did not pass the validator (edited in the database).
	e.set(KeySource, sourceGitHub)
	for _, repo := range []string{"owner", "owner/repo/../../x", "owner/repo?x", "../x"} {
		e.set(KeyGitHubRepo, repo)
		if e.mod.source() != nil {
			t.Errorf("repository %q is used", repo)
		}
	}
	e.set(KeySource, sourceURL)
	for _, u := range []string{"http://example.com/m.json", "example.com", "file:///etc/passwd", "https://"} {
		e.set(KeyManifestURL, u)
		if e.mod.source() != nil {
			t.Errorf("address %q is used", u)
		}
	}
}

func TestSelfManifestSource(t *testing.T) {
	s := newReleaseServer(t)
	e := newEnv(t)
	useManifest(e, s)
	s.serveBody("application/json", s.manifest("1.3.0"))

	res := e.do("POST", "/updates/self/check", "", "admin")
	if res.Status != 200 {
		t.Fatalf("status %d: %s", res.Status, res.Body)
	}
	var v selfView
	res.into(t, &v)
	if !v.Configured || !v.UpdateAvailable || !v.Installable || v.InstallBlocker != "" || v.Error != nil ||
		v.Installed != "1.2.0" || v.Source != "url" || v.SourceLabel != strings.TrimPrefix(s.srv.URL, "https://") {
		t.Errorf("view = %+v", v)
	}
	if v.Latest == nil || v.Latest.Version != "1.3.0" || v.Latest.PublishedAt == nil || *v.Latest.PublishedAt != 1788256800 ||
		v.Latest.URL != s.srv.URL+"/releases/1.3.0" {
		t.Fatalf("latest = %+v", v.Latest)
	}
	want := map[string]Asset{
		"amd64": {URL: s.tarball("amd64"), SHA256: sumAMD},
		"arm64": {URL: s.tarball("arm64"), SHA256: sumARM},
	}
	if !reflect.DeepEqual(v.Latest.Assets, want) {
		t.Errorf("assets = %+v", v.Latest.Assets)
	}
	if v.Arch != runtime.GOARCH || v.Asset == nil || *v.Asset != want[runtime.GOARCH] {
		t.Errorf("asset for %s = %+v", runtime.GOARCH, v.Asset)
	}
	// Release notes are plain text: a string, passed on unchanged.
	var raw struct {
		Latest map[string]json.RawMessage `json:"latest"`
	}
	res.into(t, &raw)
	var notes any
	if err := json.Unmarshal(raw.Latest["notes"], &notes); err != nil {
		t.Fatal(err)
	}
	if text, ok := notes.(string); !ok || text != hostileNotes {
		t.Errorf("notes = %#v", notes)
	}
	for _, k := range []string{"notes_html", "html", "body_html"} {
		if _, ok := raw.Latest[k]; ok {
			t.Errorf("the API offers %s", k)
		}
	}

	if got, want := s.seen(), []string{"GET /myserver/manifest.json"}; !reflect.DeepEqual(got, want) {
		t.Errorf("requests = %q", got)
	}
	if ua := s.headers[0].Get("User-Agent"); ua != "MyServer/1.2.0" {
		t.Errorf("User-Agent = %q", ua)
	}
	if s.headers[0].Get("Cookie") != "" || s.headers[0].Get("Authorization") != "" {
		t.Error("credentials were sent to the release server")
	}
	if got, want := e.auditRows(), []auditRow{{"yonetici", "updates.self_check", e.mod.sourceID(), "", true}}; !reflect.DeepEqual(got, want) {
		t.Errorf("audit = %+v", got)
	}
	var sum summaryView
	e.do("GET", "/updates/summary", "", "user").into(t, &sum)
	if sum.Total != 1 || sum.Self.Count != 1 || sum.Self.Latest != "1.3.0" || !sum.Self.Configured || sum.Self.Installed != "1.2.0" {
		t.Errorf("summary = %+v", sum.Self)
	}
	e.wantNoHelperCalls("checking for a release")
}

func TestSelfGitHubSource(t *testing.T) {
	for _, withDigests := range []bool{true, false} {
		name := "asset digests"
		if !withDigests {
			name = "SHA256SUMS"
		}
		t.Run(name, func(t *testing.T) {
			s := newReleaseServer(t)
			e := newEnv(t)
			s.use()
			e.set(KeySource, sourceGitHub)
			e.set(KeyGitHubRepo, "owner/myserver")
			s.serve(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/owner/myserver/releases/latest":
					w.Header().Set("Content-Type", "application/json; charset=utf-8")
					io.WriteString(w, s.github(withDigests))
				case "/releases/download/v1.3.0/SHA256SUMS":
					w.Header().Set("Content-Type", "application/octet-stream")
					io.WriteString(w, sumARM+"  myserver-linux-arm64.tar.gz\n"+
						strings.ToUpper(sumAMD)+" *myserver-linux-amd64.tar.gz\n"+
						"not-a-digest  myserver-linux-amd64.tar.gz.sig\n"+
						strings.Repeat("c3", 32)+"  myserver-windows-amd64.zip\n\n")
				default:
					http.NotFound(w, r)
				}
			})
			res := e.do("POST", "/updates/self/check", "", "admin")
			if res.Status != 200 {
				t.Fatalf("status %d: %s", res.Status, res.Body)
			}
			var v selfView
			res.into(t, &v)
			if !v.UpdateAvailable || !v.Installable || v.SourceLabel != "github.com/owner/myserver" || v.Source != "github" {
				t.Errorf("view = %+v", v)
			}
			if v.Latest == nil || v.Latest.Version != "1.3.0" || v.Latest.Notes != hostileNotes ||
				v.Latest.URL != s.srv.URL+"/owner/myserver/releases/tag/v1.3.0" || v.Latest.PublishedAt == nil {
				t.Fatalf("latest = %+v", v.Latest)
			}
			want := map[string]Asset{
				"amd64": {URL: s.srv.URL + "/releases/download/v1.3.0/myserver-linux-amd64.tar.gz", SHA256: sumAMD},
				"arm64": {URL: s.srv.URL + "/releases/download/v1.3.0/myserver-linux-arm64.tar.gz", SHA256: sumARM},
			}
			if !reflect.DeepEqual(v.Latest.Assets, want) {
				t.Errorf("assets = %+v", v.Latest.Assets)
			}
			if v.Asset == nil || *v.Asset != want[runtime.GOARCH] {
				t.Errorf("asset = %+v", v.Asset)
			}
			wantRequests := []string{"GET /repos/owner/myserver/releases/latest"}
			if !withDigests {
				wantRequests = append(wantRequests, "GET /releases/download/v1.3.0/SHA256SUMS")
			}
			// With digests on the assets the checksum file is not needed;
			// fetching it anyway is allowed, anything else is not.
			got := s.seen()
			if withDigests && len(got) == 2 {
				wantRequests = append(wantRequests, "GET /releases/download/v1.3.0/SHA256SUMS")
			}
			if !reflect.DeepEqual(got, wantRequests) {
				t.Errorf("requests = %q", got)
			}
			if got := s.headers[0].Get("Accept"); got != "application/vnd.github+json" {
				t.Errorf("Accept = %q", got)
			}
		})
	}
}

func TestSelfNotInstallable(t *testing.T) {
	type mutate func(m map[string]any, s *releaseServer)
	cases := []struct {
		name      string
		change    mutate
		blocker   string
		hasAsset  bool
		available bool
	}{
		{"no asset for this architecture", func(m map[string]any, _ *releaseServer) {
			delete(m["assets"].(map[string]any), runtime.GOARCH)
		}, "kurulum arşivi", false, true},
		{"no assets at all", func(m map[string]any, _ *releaseServer) { delete(m, "assets") }, "kurulum arşivi", false, true},
		{"no checksum", func(m map[string]any, s *releaseServer) {
			m["assets"].(map[string]any)[runtime.GOARCH] = map[string]string{"url": s.tarball(runtime.GOARCH)}
		}, "SHA-256", true, true},
		{"malformed checksum", func(m map[string]any, s *releaseServer) {
			m["assets"].(map[string]any)[runtime.GOARCH] = map[string]string{"url": s.tarball(runtime.GOARCH), "sha256": sumAMD[:60]}
		}, "SHA-256", true, true},
		{"checksum of another kind", func(m map[string]any, s *releaseServer) {
			m["assets"].(map[string]any)[runtime.GOARCH] = map[string]string{"url": s.tarball(runtime.GOARCH), "sha256": "md5:" + sumAMD[:32]}
		}, "SHA-256", true, true},
		{"plain http tarball", func(m map[string]any, s *releaseServer) {
			m["assets"].(map[string]any)[runtime.GOARCH] = map[string]string{
				"url": strings.Replace(s.tarball(runtime.GOARCH), "https://", "http://", 1), "sha256": sumAMD}
		}, "kurulum arşivi", false, true},
		{"tarball address with a query", func(m map[string]any, s *releaseServer) {
			m["assets"].(map[string]any)[runtime.GOARCH] = map[string]string{"url": s.tarball(runtime.GOARCH) + "?token=x", "sha256": sumAMD}
		}, "kurulum arşivi", false, true},
		{"tarball address with credentials", func(m map[string]any, _ *releaseServer) {
			m["assets"].(map[string]any)[runtime.GOARCH] = map[string]string{"url": "https://u:p@example.com/x.tar.gz", "sha256": sumAMD}
		}, "kurulum arşivi", false, true},
		{"tarball address that is an option", func(m map[string]any, _ *releaseServer) {
			m["assets"].(map[string]any)[runtime.GOARCH] = map[string]string{"url": "--output=/etc/cron.d/x", "sha256": sumAMD}
		}, "kurulum arşivi", false, true},
		{"same version", func(m map[string]any, _ *releaseServer) { m["version"] = "1.2.0" }, "", false, false},
		{"older version", func(m map[string]any, _ *releaseServer) { m["version"] = "1.1.9" }, "", false, false},
		{"pre-release of the installed version", func(m map[string]any, _ *releaseServer) { m["version"] = "1.2.0-rc.1" }, "", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newReleaseServer(t)
			e := newEnv(t)
			useManifest(e, s)
			var m map[string]any
			if err := json.Unmarshal([]byte(s.manifest("1.3.0")), &m); err != nil {
				t.Fatal(err)
			}
			c.change(m, s)
			s.serveBody("application/json", mustJSON(m))
			res := e.do("POST", "/updates/self/check", "", "admin")
			if res.Status != 200 {
				t.Fatalf("status %d: %s", res.Status, res.Body)
			}
			var v selfView
			res.into(t, &v)
			if v.Installable || v.UpdateAvailable != c.available || (v.Asset != nil) != c.hasAsset {
				t.Errorf("view = %+v", v)
			}
			if c.blocker == "" && v.InstallBlocker != "" || !strings.Contains(v.InstallBlocker, c.blocker) {
				t.Errorf("blocker = %q", v.InstallBlocker)
			}
			for _, version := range []string{m["version"].(string), "1.3.0", "9.9.9"} {
				res = e.do("POST", "/updates/self/apply", `{"version":"`+version+`","confirm":true}`, "admin")
				if res.Status != http.StatusConflict {
					t.Errorf("apply %s: status %d: %s", version, res.Status, res.Body)
				}
				if c.available && (res.code() != "not_installable" || res.message() != v.InstallBlocker) {
					t.Errorf("apply %s: code %q message %q", version, res.code(), res.message())
				}
			}
			e.wantNoHelperCalls("a release that cannot be installed")
			if !c.available && len(e.notifications()) != 0 {
				t.Errorf("notifications = %q", e.notificationTitles())
			}
		})
	}
}

func TestSelfSourceFailures(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a plain HTTP server was contacted: %s %s", r.Method, r.URL)
		io.WriteString(w, `{"version":"9.9.9"}`)
	}))
	defer plain.Close()
	stop := make(chan struct{})
	defer close(stop)

	cases := []struct {
		name    string
		handler func(s *releaseServer) http.HandlerFunc
		code    string
	}{
		{"redirect to http", func(*releaseServer) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, plain.URL+"/manifest.json", http.StatusFound)
			}
		}, "source_unreachable"},
		{"redirect to http (301)", func(*releaseServer) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, plain.URL+"/manifest.json", http.StatusMovedPermanently)
			}
		}, "source_unreachable"},
		{"redirect loop", func(s *releaseServer) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, s.srv.URL+r.URL.Path+"x", http.StatusFound)
			}
		}, "source_unreachable"},
		{"oversized", func(s *releaseServer) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"version":"1.3.0","notes":"`+strings.Repeat("a", maxReleaseBody)+`"}`)
			}
		}, "source_too_large"},
		{"endless", func(s *releaseServer) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"version":"1.3.0","notes":"`)
				chunk := strings.Repeat("a", 64<<10)
				for i := 0; i < 64; i++ {
					if _, err := io.WriteString(w, chunk); err != nil {
						return
					}
				}
			}
		}, "source_too_large"},
		{"slow", func(*releaseServer) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-r.Context().Done():
				case <-stop:
				}
			}
		}, "source_unreachable"},
		{"slow body", func(*releaseServer) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"version":"1.3.0",`)
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
				case <-stop:
				}
			}
		}, "source_unreachable"},
		{"invalid JSON", func(s *releaseServer) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"version":"1.3.0",`) }
		}, "source_invalid"},
		{"JSON of another shape", func(s *releaseServer) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `["1.3.0"]`) }
		}, "source_invalid"},
		{"wrong field types", func(s *releaseServer) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"version":1.3,"assets":"x"}`) }
		}, "source_invalid"},
		{"HTML page", func(s *releaseServer) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				io.WriteString(w, "<!DOCTYPE html><html><body><h1>Giriş yapın</h1></body></html>")
			}
		}, "source_invalid"},
		{"HTML error page", func(s *releaseServer) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusBadGateway)
				io.WriteString(w, "<html><body><h1>502 Bad Gateway</h1></body></html>")
			}
		}, "source_error"},
		{"empty body", func(s *releaseServer) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) {}
		}, "source_invalid"},
		{"null", func(s *releaseServer) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `null`) }
		}, "version_invalid"},
		{"no version", func(s *releaseServer) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"notes":"x"}`) }
		}, "version_invalid"},
		{"invalid version", func(s *releaseServer) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"version":"9.9.9 --skip-verify"}`) }
		}, "version_invalid"},
		{"version with build metadata", func(s *releaseServer) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"version":"1.3.0+evil"}`) }
		}, "version_invalid"},
		{"not found", func(s *releaseServer) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }
		}, "no_release"},
		{"rate limited", func(s *releaseServer) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				io.WriteString(w, `{"message":"API rate limit exceeded for 203.0.113.7."}`)
			}
		}, "source_rate_limited"},
		{"too many requests", func(s *releaseServer) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) }
		}, "source_rate_limited"},
		{"server error", func(s *releaseServer) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }
		}, "source_error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newReleaseServer(t)
			e := newEnv(t)
			useManifest(e, s)
			releaseTimeout = 300 * time.Millisecond
			// A release found earlier must not survive a failed check as installable.
			s.serveBody("application/json", s.manifest("1.3.0"))
			if res := e.do("POST", "/updates/self/check", "", "admin"); res.Status != 200 {
				t.Fatalf("first check: %d %s", res.Status, res.Body)
			}
			s.serve(c.handler(s))
			res := e.do("POST", "/updates/self/check", "", "admin")
			if res.Status != http.StatusBadGateway || res.code() != c.code {
				t.Errorf("status %d code %q: %s", res.Status, res.code(), res.Body)
			}
			for _, leak := range []string{"127.0.0.1", "x509", "json:", "dial", "redirect", "<html", "deadline", "unmarshal"} {
				if strings.Contains(res.Body, leak) {
					t.Errorf("internal error leaked (%s): %s", leak, res.Body)
				}
			}
			v := selfOf(t, e)
			if v.Error == nil || v.Error.Code != c.code || v.Error.Message != res.message() || v.UpdateAvailable || v.Installable {
				t.Errorf("view = %+v", v)
			}
			res = e.do("POST", "/updates/self/apply", `{"version":"1.3.0","confirm":true}`, "admin")
			if res.Status != http.StatusConflict {
				t.Errorf("apply after a failed check: status %d: %s", res.Status, res.Body)
			}
			e.wantNoHelperCalls("a failed check")
			rows := e.auditRows()
			if last := rows[len(rows)-1]; last.Action != "updates.self_check" || last.Success {
				t.Errorf("audit = %+v", last)
			}
		})
	}
}

func TestSelfRefusesPlainHTTPAndUntrustedTLS(t *testing.T) {
	var hits int
	var mu sync.Mutex
	count := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		io.WriteString(w, `{"version":"9.9.9"}`)
	})
	plain := httptest.NewServer(count)
	defer plain.Close()
	untrusted := httptest.NewTLSServer(count)
	defer untrusted.Close()
	newEnv(t)

	ctx := context.Background()
	for _, u := range []string{plain.URL + "/manifest.json", "HTTP://" + strings.TrimPrefix(plain.URL, "http://") + "/m.json",
		"ftp://example.com/m.json", "file:///etc/passwd", "//example.com/m.json", "example.com/m.json", "", "https://", "https:///m.json"} {
		_, err := manifestSource{url: u}.Latest(ctx)
		var se *sourceError
		if !errors.As(err, &se) || se.Code != "insecure_source" {
			t.Errorf("%q: error %v", u, err)
		}
		if _, err := fetchBytes(ctx, u, nil, 100); err == nil {
			t.Errorf("%q fetched", u)
		}
	}
	// A certificate nobody vouches for: the default transport is in use.
	_, err := manifestSource{url: untrusted.URL + "/manifest.json"}.Latest(ctx)
	var se *sourceError
	if !errors.As(err, &se) || se.Code != "source_unreachable" {
		t.Errorf("untrusted certificate: error %v", err)
	}
	if hits != 0 {
		t.Errorf("%d requests were answered", hits)
	}
}

func TestParseSums(t *testing.T) {
	got := parseSums(sumAMD + "  myserver-linux-amd64.tar.gz\n" + strings.ToUpper(sumARM) + " *myserver-linux-arm64.tar.gz\r\n" +
		"kısa  x\n" + sumAMD + "\n" + sumAMD + "  a  b\n\n# yorum\n" + sumAMD[:63] + "g  y\n")
	want := map[string]string{"myserver-linux-amd64.tar.gz": sumAMD, "myserver-linux-arm64.tar.gz": sumARM}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
	var many strings.Builder
	for i := 0; i < 1000; i++ {
		many.WriteString(sumAMD + "  file" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + "\n")
	}
	if got := parseSums(many.String()); len(got) > 64 {
		t.Errorf("%d entries", len(got))
	}
}

func TestCleanText(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"düz metin", 100, "düz metin"},
		{"  boşluk  \n", 100, "boşluk"},
		{"a\r\nb\rc", 100, "a\nbc"},
		{"a\x00b\x1b[31mc\x7f\x08d", 100, "ab[31mcd"},
		{"geçersiz \xff\xfe bayt", 100, "geçersiz  bayt"},
		{"<b>x</b>", 100, "<b>x</b>"},
		{strings.Repeat("a", 50), 10, strings.Repeat("a", 10)},
		{"", 10, ""},
	}
	for _, c := range cases {
		if got := cleanText(c.in, c.max); got != c.want {
			t.Errorf("cleanText(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
	long := cleanText(strings.Repeat("ş", maxNotesLength), maxNotesLength)
	if len(long) > maxNotesLength+4 || !strings.HasPrefix(long, "şş") || strings.ContainsRune(long, '�') {
		t.Errorf("%d bytes", len(long))
	}
}

/* ---------- applying ---------- */

func TestSelfApply(t *testing.T) {
	s := newReleaseServer(t)
	e := newEnv(t)
	useManifest(e, s)
	s.serveBody("application/json", s.manifest("v1.3.0"))
	if res := e.do("POST", "/updates/self/check", "", "admin"); res.Status != 200 {
		t.Fatalf("check: %d %s", res.Status, res.Body)
	}
	requests := len(s.seen())

	refused := []string{
		"", `{}`, `{"version":"1.3.0"}`, `{"version":"1.3.0","confirm":false}`, `{"version":"1.3.0","confirm":"true"}`,
		`{"confirm":true}`, `{"version":"","confirm":true}`, `{"version":"1.3.1","confirm":true}`,
		`{"version":"1.2.0","confirm":true}`, `{"version":"1.3","confirm":true}`, `{"version":"9.9.9","confirm":true}`,
		`{"version":"1.3.0-rc.1","confirm":true}`, `{"version":"1.3.0 --skip-verify","confirm":true}`,
		`{"version":"--from=/tmp/x","confirm":true}`, `{"version":"latest","confirm":true}`,
		`{"version":"1.3.0","confirm":true,"url":"https://evil.example/x.tar.gz"}`,
		`{"version":"1.3.0","confirm":true,"sha256":"` + sumARM + `"}`,
		`{"version":"1.3.0","confirm":true,"skip_verify":true}`, `{"version":["1.3.0"],"confirm":true}`,
	}
	for _, body := range refused {
		res := e.do("POST", "/updates/self/apply", body, "admin")
		if res.Status != http.StatusBadRequest {
			t.Errorf("body %q: status %d: %s", body, res.Status, res.Body)
		}
	}
	e.wantNoHelperCalls("refused self-update requests")

	for _, file := range []string{"units-active", "units-activating"} {
		for _, unit := range []string{"myserver-apt-upgrade.service", "myserver-update.service"} {
			e.write(file, unit+"\n", 0o644)
			res := e.do("POST", "/updates/self/apply", `{"version":"1.3.0","confirm":true}`, "admin")
			if res.Status != http.StatusConflict || res.code() != "conflict" {
				t.Errorf("%s %s: status %d code %q", file, unit, res.Status, res.code())
			}
			os.Remove(e.path(file))
		}
	}
	e.mod.jobs.add(newJob(kindApt, "x", "", "yonetici"))
	if res := e.do("POST", "/updates/self/apply", `{"version":"1.3.0","confirm":true}`, "admin"); res.Status != http.StatusConflict {
		t.Errorf("during an apt job: status %d", res.Status)
	}
	e.mod.jobs = jobSet{}
	e.wantNoHelperCalls("self-update during another update")
	if rows := e.auditRows(); len(rows) != 1 {
		t.Errorf("refused requests were audited as actions: %+v", rows[1:])
	}

	for i, version := range []string{"1.3.0", "v1.3.0", " 1.3.0 "} {
		res := e.do("POST", "/updates/self/apply", `{"version":"`+version+`","confirm":true}`, "admin")
		if res.Status != http.StatusAccepted {
			t.Fatalf("%q: status %d: %s", version, res.Status, res.Body)
		}
		var out struct {
			Started bool   `json:"started"`
			Version string `json:"version"`
		}
		res.into(t, &out)
		if !out.Started || out.Version != "1.3.0" {
			t.Errorf("response = %+v", out)
		}
		calls := e.helperCalls()
		want := []string{"updates-self", "1.3.0", sumAMD, s.tarball("amd64")}
		if len(calls) != i+1 || !reflect.DeepEqual(calls[i], want) {
			t.Errorf("helper calls = %q\nwant %q", calls, want)
		}
	}
	rows := e.auditRows()
	if last := rows[len(rows)-1]; last != (auditRow{"yonetici", "updates.self_update", "1.2.0 -> 1.3.0", "başlatıldı", true}) {
		t.Errorf("audit = %+v", last)
	}
	// Applying uses what the last check found; it does not ask again.
	if got := len(s.seen()); got != requests {
		t.Errorf("%d requests were sent while applying", got-requests)
	}
}

func TestSelfApplyAfterSourceChange(t *testing.T) {
	s := newReleaseServer(t)
	e := newEnv(t)
	useManifest(e, s)
	s.serveBody("application/json", s.manifest("1.3.0"))
	if res := e.do("POST", "/updates/self/check", "", "admin"); res.Status != 200 {
		t.Fatalf("check: %d %s", res.Status, res.Body)
	}
	// The release belongs to the source it was found at.
	e.set(KeyManifestURL, s.srv.URL+"/other/manifest.json")
	v := selfOf(t, e)
	if v.Latest != nil || v.UpdateAvailable || v.Installable || v.CheckedAt != nil {
		t.Errorf("view = %+v", v)
	}
	res := e.do("POST", "/updates/self/apply", `{"version":"1.3.0","confirm":true}`, "admin")
	if res.Status != http.StatusConflict {
		t.Errorf("status %d: %s", res.Status, res.Body)
	}
	e.set(KeySource, sourceNone)
	res = e.do("POST", "/updates/self/apply", `{"version":"1.3.0","confirm":true}`, "admin")
	if res.Status != http.StatusConflict || res.code() != "source_not_configured" {
		t.Errorf("status %d code %q", res.Status, res.code())
	}
	e.wantNoHelperCalls("a release of another source")
}

func TestSelfApplyAfterThePanelWasUpdated(t *testing.T) {
	s := newReleaseServer(t)
	e := newEnv(t)
	useManifest(e, s)
	s.serveBody("application/json", s.manifest("1.3.0"))
	if res := e.do("POST", "/updates/self/check", "", "admin"); res.Status != 200 {
		t.Fatalf("check: %d %s", res.Status, res.Body)
	}
	for _, installed := range []string{"1.3.0", "1.3.1", "2.0.0", "gecersiz"} {
		config.Version = installed
		if v := selfOf(t, e); v.UpdateAvailable || v.Installable {
			t.Errorf("installed %s: view = %+v", installed, v)
		}
		res := e.do("POST", "/updates/self/apply", `{"version":"1.3.0","confirm":true}`, "admin")
		if res.Status != http.StatusConflict {
			t.Errorf("installed %s: status %d: %s", installed, res.Status, res.Body)
		}
	}
	e.wantNoHelperCalls("a downgrade")
}

func TestSelfApplyFailure(t *testing.T) {
	s := newReleaseServer(t)
	e := newEnv(t)
	useManifest(e, s)
	s.serveBody("application/json", s.manifest("1.3.0"))
	if res := e.do("POST", "/updates/self/check", "", "admin"); res.Status != 200 {
		t.Fatalf("check: %d %s", res.Status, res.Body)
	}
	e.write("updates-self.err", "Güncelleme betiğinin sahipliği veya izinleri güvenli değil; güncelleme başlatılmadı.", 0o644)
	res := e.do("POST", "/updates/self/apply", `{"version":"1.3.0","confirm":true}`, "admin")
	if res.Status != http.StatusBadGateway || res.code() != "self_update_failed" ||
		!strings.Contains(res.message(), "güvenli değil") {
		t.Errorf("status %d code %q message %q", res.Status, res.code(), res.message())
	}
	want := []string{"INFO Güncelleme mevcut", "ERROR Güncelleme başarısız oldu"}
	if got := e.notificationTitles(); !reflect.DeepEqual(got, want) {
		t.Errorf("notifications = %q", got)
	}
	os.Remove(e.path("updates-self.err"))
	e.write("updates-self.crash", "", 0o644)
	res = e.do("POST", "/updates/self/apply", `{"version":"1.3.0","confirm":true}`, "admin")
	if res.Status != http.StatusBadGateway || res.message() != "MyServer güncellemesi başlatılamadı." ||
		strings.Contains(res.Body, "/etc/secret") {
		t.Errorf("status %d: %s", res.Status, res.Body)
	}
	rows := e.auditRows()
	if last := rows[len(rows)-1]; last.Action != "updates.self_update" || last.Success {
		t.Errorf("audit = %+v", last)
	}
}

func TestSelfNotifiesOncePerRelease(t *testing.T) {
	s := newReleaseServer(t)
	e := newEnv(t)
	useManifest(e, s)
	s.serveBody("application/json", s.manifest("1.3.0"))
	for i := 0; i < 4; i++ {
		if err := e.mod.checkSelf(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	notes := e.notifications()
	if len(notes) != 1 || notes[0].Title != "Güncelleme mevcut" ||
		notes[0].Message != "MyServer 1.3.0 sürümü yayınlandı (kurulu sürüm: 1.2.0)." {
		t.Fatalf("notifications = %+v", notes)
	}
	s.serveBody("application/json", s.manifest("1.4.0"))
	if err := e.mod.checkSelf(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.serveBody("application/json", s.manifest("1.2.0"))
	if err := e.mod.checkSelf(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.notificationTitles(); !reflect.DeepEqual(got, []string{"INFO Güncelleme mevcut", "INFO Güncelleme mevcut"}) {
		t.Errorf("notifications = %q", got)
	}
}

/* ---------- update.status ---------- */

func TestReadLastUpdate(t *testing.T) {
	e := newEnv(t)
	if v := selfOf(t, e); v.LastUpdate != nil {
		t.Errorf("no status file: %+v", v.LastUpdate)
	}
	ts := int64(1790000000)
	cases := []struct {
		name    string
		content string
		want    *lastUpdate
	}{
		{"success", "state=success\ntarget=v1.2.0\nfrom_version=1.1.0\nto_version=1.2.0\ntime=1790000000\nmessage=MyServer 1.2.0 sürümüne güncellendi.\n",
			&lastUpdate{"success", "1.1.0", "1.2.0", &ts, "MyServer 1.2.0 sürümüne güncellendi."}},
		{"running, target only", "state=running\ntarget=v1.2.0\nfrom_version=v1.1.0\nto_version=\ntime=1790000000\nmessage=Güncelleme başladı.\n",
			&lastUpdate{"running", "1.1.0", "1.2.0", &ts, "Güncelleme başladı."}},
		{"rolled back", "state=rolled_back\r\ntarget=v1.2.0\r\nfrom_version=1.1.0\r\nto_version=1.2.0\r\ntime=1790000000\r\nmessage=Geri dönüldü.\r\n",
			&lastUpdate{"rolled_back", "1.1.0", "1.2.0", &ts, "Geri dönüldü."}},
		{"failed, invalid fields", "state=failed\ntarget=/tmp/dist\nfrom_version=<script>\nto_version=1.2\ntime=yarın\nmessage=<img src=x onerror=alert(1)>\x1b[31m\x00 bozuk\n",
			&lastUpdate{"failed", "", "", nil, "<img src=x onerror=alert(1)>[31m bozuk"}},
		{"negative time", "state=failed\ntime=-5\n", &lastUpdate{State: "failed"}},
		{"huge time", "state=failed\ntime=99999999999999999999999\n", &lastUpdate{State: "failed"}},
		{"long message", "state=failed\nmessage=" + strings.Repeat("ç", 5000) + "\n",
			&lastUpdate{State: "failed", Message: strings.Repeat("ç", 250)}},
		{"duplicate keys", "state=failed\nstate=success\nextra=1\n", &lastUpdate{State: "success"}},
		{"unknown state", "state=done\nto_version=1.2.0\n", nil},
		{"state in another case", "state=SUCCESS\n", nil},
		{"state with padding", "state= success\n", nil},
		{"no state", "to_version=1.2.0\nmessage=x\n", nil},
		{"empty", "", nil},
		{"garbage", "\x00\x01\x02\xff\xfe PK\x03\x04 =\n==\n", nil},
		{"JSON", `{"state":"success"}`, nil},
		{"state after an oversized prefix", strings.Repeat("x=1\n", 5000) + "state=success\n", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e.write("update.status", c.content, 0o640)
			got := e.mod.readLastUpdate()
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got  %+v\nwant %+v", got, c.want)
			}
			res := e.do("GET", "/updates/self", "", "user")
			if res.Status != 200 {
				t.Fatalf("status %d", res.Status)
			}
			if c.want != nil {
				var raw struct {
					LastUpdate map[string]any `json:"last_update"`
				}
				res.into(t, &raw)
				if _, ok := raw.LastUpdate["message"].(string); !ok {
					t.Errorf("message = %#v", raw.LastUpdate["message"])
				}
			}
		})
	}
	os.Remove(e.path("update.status"))
	e.write("elsewhere", "state=success\nto_version=9.9.9\n", 0o644)
	if err := os.Symlink(e.path("elsewhere"), e.path("update.status")); err != nil {
		t.Fatal(err)
	}
	if got := e.mod.readLastUpdate(); got != nil {
		t.Errorf("a symbolic link was followed: %+v", got)
	}
	os.Remove(e.path("update.status"))
	if err := os.Mkdir(e.path("update.status"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := e.mod.readLastUpdate(); got != nil {
		t.Errorf("a directory was read: %+v", got)
	}
}
