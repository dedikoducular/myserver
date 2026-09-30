package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The embedded files depend on whether (and when) the frontend was built, so
// the tests discover what is embedded instead of assuming names.

func embedded(t *testing.T) (index string, asset string) {
	t.Helper()
	if b, err := fs.ReadFile(dist, "dist/index.html"); err == nil {
		index = string(b)
	}
	entries, _ := fs.ReadDir(dist, "dist/assets")
	for _, e := range entries {
		if !e.IsDir() {
			asset = e.Name()
			break
		}
	}
	return
}

func request(method, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/", nil)
	// Set the path directly: the handler must be safe on its own, without
	// relying on the mux to clean the URL first.
	r.URL.Path = path
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, r)
	return rec
}

// isIndex reports whether the response is the application shell (or the
// notice that replaces it in a build without the frontend).
func isIndex(t *testing.T, rec *httptest.ResponseRecorder, index string) bool {
	t.Helper()
	if index == "" {
		return rec.Code == http.StatusServiceUnavailable && strings.Contains(rec.Body.String(), "MyServer")
	}
	return rec.Code == http.StatusOK && rec.Body.String() == index
}

func TestRootServesIndex(t *testing.T) {
	index, _ := embedded(t)
	rec := request("GET", "/")
	if !isIndex(t, rec, index) {
		t.Fatalf("GET /: %d %q", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("index is cached: Cache-Control %q", cc)
	}
	if index != "" && !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Errorf("Content-Type %q", rec.Header().Get("Content-Type"))
	}
}

func TestClientRoutesFallBackToIndex(t *testing.T) {
	index, _ := embedded(t)
	for _, p := range []string{"/docker", "/files", "/storage", "/settings", "/apps/nextcloud", "/terminal/", "/no/such/page",
		"/settings.html", "/assets", "/assetsx/y.js", "/favicon.ico"} {
		rec := request("GET", p)
		if !isIndex(t, rec, index) {
			t.Errorf("GET %s: %d %.80q, want the application shell", p, rec.Code, rec.Body.String())
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("GET %s: Cache-Control %q", p, cc)
		}
	}
}

func TestMissingAssetsAre404(t *testing.T) {
	index, _ := embedded(t)
	for _, p := range []string{"/assets/no-such-file.js", "/assets/index-00000000.css", "/assets/sub/x.js", "/assets/../assets/none.js",
		"/x/../assets/none.js"} {
		rec := request("GET", p)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", p, rec.Code)
		}
		if index != "" && rec.Body.String() == index {
			t.Errorf("GET %s: the application shell was served for a missing asset", p)
		}
	}
}

func TestAssetsAreServedAndCached(t *testing.T) {
	_, asset := embedded(t)
	if asset == "" {
		t.Skip("no assets embedded: the frontend is not built")
	}
	want, err := fs.ReadFile(dist, "dist/assets/"+asset)
	if err != nil {
		t.Fatal(err)
	}
	rec := request("GET", "/assets/"+asset)
	if rec.Code != 200 || rec.Body.String() != string(want) {
		t.Fatalf("GET /assets/%s: %d, %d bytes (want %d)", asset, rec.Code, rec.Body.Len(), len(want))
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") || !strings.Contains(cc, "max-age=31536000") {
		t.Errorf("Cache-Control %q", cc)
	}
	head := request("HEAD", "/assets/"+asset)
	if head.Code != 200 || head.Body.Len() != 0 {
		t.Errorf("HEAD: %d with %d body bytes", head.Code, head.Body.Len())
	}
}

func TestOtherMethodsAreRefused(t *testing.T) {
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH", "OPTIONS", "TRACE", "CONNECT"} {
		for _, p := range []string{"/", "/docker", "/assets/x.js"} {
			if rec := request(m, p); rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: status %d, want 405", m, p, rec.Code)
			}
		}
	}
}

func TestPathTraversal(t *testing.T) {
	index, _ := embedded(t)
	// The Go source of this package lies next to dist/; the system files
	// are the classic targets.
	paths := []string{
		"/../webui.go", "/../../webui/webui.go", "/assets/../../webui.go", "/assets/../../../../../../etc/passwd",
		"/../../../../../../etc/passwd", "/..", "/../", "/./../etc/passwd", "//etc/passwd", "/etc/passwd",
		"/assets/..%2f..%2fwebui.go", "/..%2fwebui.go", "/%2e%2e/webui.go", `/..\webui.go`, `/assets\..\..\webui.go`,
		"/assets/....//....//webui.go", "/dist/index.html", "/../dist/index.html", "/index.html/../../webui.go",
		"/assets/\x00/../../webui.go", "/C:/Windows/win.ini", "/proc/self/environ",
	}
	for _, p := range paths {
		rec := request("GET", p)
		body := rec.Body.String()
		if strings.Contains(body, "package webui") || strings.Contains(body, "go:embed") {
			t.Errorf("GET %q served the package source", p)
		}
		if strings.Contains(body, "root:") || strings.Contains(body, "/bin/") || strings.Contains(body, "[fonts]") || strings.Contains(body, "PATH=") {
			t.Errorf("GET %q served a system file: %.100q", p, body)
		}
		switch {
		case isIndex(t, rec, index):
		case rec.Code == http.StatusNotFound:
		case rec.Code == http.StatusBadRequest:
			// net/http refuses paths with ".." elements outright.
		case rec.Code >= 300 && rec.Code < 400:
			// A relative redirect stays on the panel; one that names a
			// host would be an open redirect.
			if loc := rec.Header().Get("Location"); strings.Contains(loc, "//") || strings.Contains(loc, ":") {
				t.Errorf("GET %q: redirect to %q", p, loc)
			}
		default:
			t.Errorf("GET %q: unexpected status %d: %.100q", p, rec.Code, body)
		}
	}
}

func TestDirectoriesAreNotListed(t *testing.T) {
	_, asset := embedded(t)
	for _, p := range []string{"/assets", "/assets/", "/"} {
		rec := request("GET", p)
		body := rec.Body.String()
		if asset != "" && strings.Contains(body, `href="`+asset) {
			t.Errorf("GET %s lists the directory", p)
		}
		if strings.Contains(body, "<pre>") {
			t.Errorf("GET %s returned a directory listing", p)
		}
	}
}
