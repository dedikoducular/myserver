package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
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

// The secret stored outside the allowed root. No response body and no file
// inside the root may ever contain it.
const secret = "TOPSECRET-outside-the-root-0123456789"

type testCreds struct{ token, csrf string }

// testEnv is a file manager module wired to a temp database, with one
// allowed root and a sibling directory that is NOT allowed.
//
//	base/root      allowed root
//	base/roots     prefix sibling of the root, not allowed
//	base/outside   not allowed, holds the secret
type testEnv struct {
	t       *testing.T
	base    string
	root    string
	sibling string
	outside string
	handler http.Handler
	store   *settings.Store
	mod     *Module
	admin   *testCreds
	user    *testCreds
}

func newEnv(t *testing.T, extraRoots ...string) *testEnv {
	t.Helper()
	dataDir := t.TempDir()
	base := t.TempDir()
	real, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	base = real
	// t.TempDir is 0700; ownership tests need other users to traverse it.
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	e := &testEnv{
		t: t, base: base,
		root:    filepath.Join(base, "root"),
		sibling: filepath.Join(base, "roots"),
		outside: filepath.Join(base, "outside"),
	}
	for _, d := range []string{e.root, e.sibling, e.outside} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e.write(filepath.Join(e.outside, "secret.txt"), secret)
	e.write(filepath.Join(e.sibling, "secret.txt"), secret)

	ctx := context.Background()
	db, err := database.Open(filepath.Join(dataDir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	store, err := settings.NewStore(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	roots := []string{e.root}
	for _, r := range extraRoots {
		if !filepath.IsAbs(r) {
			r = filepath.Join(base, r)
		}
		roots = append(roots, r)
	}
	if err := store.SetStrings(ctx, settings.KeyAllowedRoots, roots); err != nil {
		t.Fatal(err)
	}
	al := audit.New(db)
	cfg := &config.Config{DataDir: dataDir}
	priv := privileged.New("/nonexistent/myserver-helper")
	authSvc, err := auth.NewService(db, cfg, store, al, priv)
	if err != nil {
		t.Fatal(err)
	}
	deps := module.Deps{
		Cfg: cfg, DB: db, Settings: store, Audit: al,
		Notify: notify.New(db), Logs: logbuf.NewBuffer(16), Priv: priv, Auth: authSvc,
	}
	mod, err := New(deps, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.mod = mod.(*Module)
	e.store = store

	mux := http.NewServeMux()
	api := httpx.NewRouter(mux).Group("/api/v1", authSvc.Require)
	ws := httpx.NewRouter(mux).Group("/api/v1", authSvc.RequireWebSocket)
	mod.Register(api, ws)
	e.handler = mux

	mkUser := func(name, role string) *testCreds {
		res, err := db.ExecContext(ctx, `INSERT INTO users (username, password_hash, role, disabled, created_at)
			VALUES (?, 'x', ?, 0, ?)`, name, role, time.Now().Unix())
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		c := &testCreds{token: "token-" + name, csrf: "csrf-" + name}
		sum := sha256.Sum256([]byte(c.token))
		now := time.Now().Unix()
		if _, err := db.ExecContext(ctx, `INSERT INTO sessions
			(token_hash, user_id, csrf_token, ip, user_agent, created_at, last_seen_at, expires_at)
			VALUES (?, ?, ?, '127.0.0.1', 'test', ?, ?, ?)`,
			hex.EncodeToString(sum[:]), id, c.csrf, now, now, now+3600); err != nil {
			t.Fatal(err)
		}
		return c
	}
	e.admin = mkUser("yonetici", auth.RoleAdmin)
	e.user = mkUser("kullanici", auth.RoleUser)

	t.Cleanup(func() {
		e.mod.jobs.cancelAll()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			running := false
			for _, v := range e.mod.jobs.views("", true) {
				if v.Status == jobRunning {
					running = true
				}
			}
			if !running {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(20 * time.Millisecond) // let the last audit record land
		db.Close()
	})
	return e
}

/* ---------- filesystem helpers ---------- */

func (e *testEnv) write(p, content string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) mkdir(p string) {
	e.t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) symlink(target, link string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) in(parts ...string) string {
	return filepath.Join(append([]string{e.root}, parts...)...)
}

func (e *testEnv) setting(key, value string) {
	e.t.Helper()
	if err := e.store.Set(context.Background(), key, value); err != nil {
		e.t.Fatal(err)
	}
}

// snapshot describes a tree without following symlinks: one line per entry
// with type, permissions, owner and content hash or link target.
func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		owner := ""
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			owner = fmt.Sprintf("%d:%d", st.Uid, st.Gid)
		}
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			lines = append(lines, fmt.Sprintf("%s L %s -> %s", rel, owner, target))
		case fi.IsDir():
			lines = append(lines, fmt.Sprintf("%s D %o %s", rel, fi.Mode().Perm(), owner))
		case fi.Mode().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(b)
			lines = append(lines, fmt.Sprintf("%s F %o %s %d %x", rel, fi.Mode().Perm(), owner, len(b), sum[:8]))
		default:
			lines = append(lines, fmt.Sprintf("%s O %s", rel, fi.Mode().String()))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", dir, err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// guard snapshots everything that is outside the allowed root and returns a
// function that fails the test if any of it changed.
func (e *testEnv) guard() func() {
	e.t.Helper()
	before := snapshot(e.t, e.outside) + "\n--\n" + snapshot(e.t, e.sibling)
	return func() {
		e.t.Helper()
		after := snapshot(e.t, e.outside) + "\n--\n" + snapshot(e.t, e.sibling)
		if before != after {
			e.t.Errorf("ESCAPE: data outside the allowed root changed.\nbefore:\n%s\nafter:\n%s", before, after)
		}
		entries, err := os.ReadDir(e.base)
		if err != nil {
			e.t.Fatal(err)
		}
		for _, en := range entries {
			switch en.Name() {
			case "root", "roots", "outside":
			default:
				if !strings.HasPrefix(en.Name(), "allowed-") {
					e.t.Errorf("ESCAPE: unexpected entry %q created next to the root", en.Name())
				}
			}
		}
	}
}

// regularFilesWith returns the regular files below dir whose content
// contains needle. Symlinks are not followed.
func regularFilesWith(t *testing.T, dir, needle string) []string {
	t.Helper()
	var found []string
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || !fi.Mode().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err == nil && bytes.Contains(b, []byte(needle)) {
			found = append(found, p)
		}
		return nil
	})
	return found
}

// leftovers returns temporary files left below dir.
func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && strings.HasPrefix(fi.Name(), tempPrefix) {
			found = append(found, p)
		}
		return nil
	})
	return found
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, en := range entries {
		out = append(out, en.Name())
	}
	return out
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

/* ---------- HTTP helpers ---------- */

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type envelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   *apiError       `json:"error"`
}

func (e *testEnv) send(c *testCreds, r *http.Request) *httptest.ResponseRecorder {
	if c != nil {
		r.AddCookie(&http.Cookie{Name: "myserver_session", Value: c.token})
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			r.Header.Set("X-CSRF-Token", c.csrf)
		}
	}
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, r)
	return w
}

func apiURL(endpoint string, q url.Values) string {
	u := "/api/v1/files" + endpoint
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

func (e *testEnv) get(c *testCreds, endpoint string, paths ...string) *httptest.ResponseRecorder {
	q := url.Values{}
	for _, p := range paths {
		q.Add("path", p)
	}
	return e.send(c, httptest.NewRequest(http.MethodGet, apiURL(endpoint, q), nil))
}

func (e *testEnv) call(c *testCreds, method, endpoint string, body any) *httptest.ResponseRecorder {
	b, err := json.Marshal(body)
	if err != nil {
		e.t.Fatal(err)
	}
	r := httptest.NewRequest(method, apiURL(endpoint, nil), bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	return e.send(c, r)
}

func (e *testEnv) post(endpoint string, body any) *httptest.ResponseRecorder {
	return e.call(e.admin, http.MethodPost, endpoint, body)
}

func decode(t *testing.T, w *httptest.ResponseRecorder) envelope {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not an envelope (status %d): %q", w.Code, w.Body.String())
	}
	return env
}

func errCode(w *httptest.ResponseRecorder) string {
	var env envelope
	if json.Unmarshal(w.Body.Bytes(), &env) == nil && env.Error != nil {
		return env.Error.Code
	}
	return ""
}

// refused asserts that a request was rejected with a client error and that
// the secret did not leak into the response.
func refused(t *testing.T, what string, w *httptest.ResponseRecorder, allowed ...int) {
	t.Helper()
	if len(allowed) == 0 {
		allowed = []int{400, 403, 404}
	}
	ok := false
	for _, s := range allowed {
		if w.Code == s {
			ok = true
		}
	}
	if !ok {
		t.Errorf("%s: status %d, want one of %v; body: %.300s", what, w.Code, allowed, w.Body.String())
	}
	noSecret(t, what, w)
}

func noSecret(t *testing.T, what string, w *httptest.ResponseRecorder) {
	t.Helper()
	if bytes.Contains(w.Body.Bytes(), []byte(secret)) {
		t.Errorf("%s: LEAK: the response contains data from outside the root", what)
	}
}

// job waits for the job started by a 202 response and returns its final
// state. started is false when the request was refused up front.
func (e *testEnv) job(w *httptest.ResponseRecorder) (v JobView, started bool) {
	e.t.Helper()
	if w.Code != http.StatusAccepted {
		return JobView{}, false
	}
	env := decode(e.t, w)
	if err := json.Unmarshal(env.Data, &v); err != nil || v.ID == "" {
		e.t.Fatalf("no job in response: %s", w.Body.String())
	}
	j := e.mod.jobs.get(v.ID)
	if j == nil {
		e.t.Fatalf("job %s not found", v.ID)
	}
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		v = j.view()
		if v.Status != jobRunning {
			return v, true
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.t.Fatalf("job %s (%s) did not finish: %+v", v.ID, v.Kind, v)
	return v, true
}

// mustRun runs a job request that has to succeed.
func (e *testEnv) mustRun(endpoint string, body any) JobView {
	e.t.Helper()
	w := e.post(endpoint, body)
	v, ok := e.job(w)
	if !ok {
		e.t.Fatalf("%s refused: %d %s", endpoint, w.Code, w.Body.String())
	}
	if v.Status != jobDone {
		e.t.Fatalf("%s job ended as %s: %s", endpoint, v.Status, v.Message)
	}
	return v
}

// mustNotRun runs a job request that must be refused, either up front or by
// a failing job.
func (e *testEnv) mustNotRun(what, endpoint string, body any) {
	e.t.Helper()
	w := e.post(endpoint, body)
	noSecret(e.t, what, w)
	v, ok := e.job(w)
	if !ok {
		if w.Code < 400 || w.Code >= 500 {
			e.t.Errorf("%s: status %d, want a 4xx refusal; body %.300s", what, w.Code, w.Body.String())
		}
		return
	}
	if v.Status != jobFailed {
		e.t.Errorf("%s: job ended as %q (%s), want it to fail", what, v.Status, v.Message)
	}
}

/* ---------- uploads ---------- */

const testBoundary = "----myserver-test-boundary-7f3a"

// multipartBody builds a multipart body by hand so that awkward file names
// reach the server exactly as written.
func multipartBody(filename string, data []byte) []byte {
	esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(filename)
	var b bytes.Buffer
	fmt.Fprintf(&b, "--%s\r\nContent-Disposition: form-data; name=\"file\"; filename=\"%s\"\r\n"+
		"Content-Type: application/octet-stream\r\n\r\n", testBoundary, esc)
	b.Write(data)
	fmt.Fprintf(&b, "\r\n--%s--\r\n", testBoundary)
	return b.Bytes()
}

func (e *testEnv) uploadRaw(c *testCreds, dir string, overwrite bool, body io.Reader, length int64) *httptest.ResponseRecorder {
	q := url.Values{"path": {dir}}
	if overwrite {
		q.Set("overwrite", "true")
	}
	r := httptest.NewRequest(http.MethodPost, apiURL("/upload", q), body)
	r.ContentLength = length
	r.Header.Set("Content-Type", "multipart/form-data; boundary="+testBoundary)
	return e.send(c, r)
}

func (e *testEnv) upload(dir, filename string, data []byte, overwrite bool) *httptest.ResponseRecorder {
	b := multipartBody(filename, data)
	return e.uploadRaw(e.admin, dir, overwrite, bytes.NewReader(b), int64(len(b)))
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var zeroTime time.Time

func timeout(seconds int) <-chan time.Time {
	return time.After(time.Duration(seconds) * time.Second)
}
