package httpx

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	secret      = "S3CRET-dsn-password=hunter2"
	internalMsg = `{"success":false,"data":null,"error":{"code":"internal_error","message":"Beklenmeyen bir sunucu hatası oluştu."}}` + "\n"
)

func TestSuccessEnvelopeShape(t *testing.T) {
	cases := []struct {
		data any
		want string
	}{
		{map[string]int{"a": 1}, `{"success":true,"data":{"a":1},"error":null}`},
		{nil, `{"success":true,"data":null,"error":null}`},
		{[]string{}, `{"success":true,"data":[],"error":null}`},
		{"metin", `{"success":true,"data":"metin","error":null}`},
		{false, `{"success":true,"data":false,"error":null}`},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		OK(rec, c.data)
		if rec.Code != 200 {
			t.Errorf("status %d", rec.Code)
		}
		if got := rec.Body.String(); got != c.want+"\n" {
			t.Errorf("body %q, want %q", got, c.want)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
			t.Errorf("Content-Type %q", ct)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("Cache-Control %q", cc)
		}
	}
	rec := httptest.NewRecorder()
	JSON(rec, http.StatusCreated, 1)
	if rec.Code != http.StatusCreated {
		t.Errorf("JSON status %d", rec.Code)
	}
}

func TestErrorEnvelopeShape(t *testing.T) {
	cases := []struct {
		err    error
		status int
		want   string
	}{
		{BadRequest("Disk adı geçersiz."), 400, `{"success":false,"data":null,"error":{"code":"bad_request","message":"Disk adı geçersiz."}}`},
		{Unauthorized(), 401, `{"success":false,"data":null,"error":{"code":"unauthorized","message":"Oturum açmanız gerekiyor."}}`},
		{Forbidden(), 403, `{"success":false,"data":null,"error":{"code":"forbidden","message":"Yetkiniz bulunmuyor."}}`},
		{NotFound(""), 404, `{"success":false,"data":null,"error":{"code":"not_found","message":"Kayıt bulunamadı."}}`},
		{Conflict("Zaten var."), 409, `{"success":false,"data":null,"error":{"code":"conflict","message":"Zaten var."}}`},
		{TooManyRequests("Yavaş."), 429, `{"success":false,"data":null,"error":{"code":"rate_limited","message":"Yavaş."}}`},
		{Unavailable("docker_down", "Docker servisine ulaşılamıyor."), 503, `{"success":false,"data":null,"error":{"code":"docker_down","message":"Docker servisine ulaşılamıyor."}}`},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		Fail(rec, httptest.NewRequest("GET", "/x", nil), c.err)
		if rec.Code != c.status {
			t.Errorf("%v: status %d, want %d", c.err, rec.Code, c.status)
		}
		if got := rec.Body.String(); got != c.want+"\n" {
			t.Errorf("body %q, want %q", got, c.want)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("Cache-Control %q", cc)
		}
	}
}

func TestFailNeverExposesInternalErrors(t *testing.T) {
	cause := errors.New(secret + " at /var/lib/myserver/myserver.db")
	cases := map[string]struct {
		err  error
		want string
	}{
		"raw error":         {cause, internalMsg},
		"wrapped raw error": {fmt.Errorf("query failed: %w", cause), internalMsg},
		"Internal":          {Internal(cause), internalMsg},
		"user error with cause": {BadRequest("Geçersiz istek.").Wrap(cause),
			`{"success":false,"data":null,"error":{"code":"bad_request","message":"Geçersiz istek."}}` + "\n"},
		"user error inside a wrapped error": {fmt.Errorf("%s: %w", secret, NotFound("Yok.")),
			`{"success":false,"data":null,"error":{"code":"not_found","message":"Yok."}}` + "\n"},
	}
	for name, c := range cases {
		rec := httptest.NewRecorder()
		Fail(rec, httptest.NewRequest("POST", "/x", nil), c.err)
		body := rec.Body.String()
		if body != c.want {
			t.Errorf("%s: body %q, want %q", name, body, c.want)
		}
		if strings.Contains(body, "S3CRET") || strings.Contains(body, "hunter2") || strings.Contains(body, "/var/lib") {
			t.Errorf("%s: internal error text in the response: %s", name, body)
		}
	}
}

func TestWrapDoesNotModifyOriginal(t *testing.T) {
	base := BadRequest("x")
	w := base.Wrap(errors.New("cause"))
	if base.Err != nil {
		t.Fatal("Wrap modified the shared error value")
	}
	if !errors.Is(w, w.Err) || w.Message != "x" {
		t.Fatal("wrapped error lost its parts")
	}
}

func newMux(register func(rt *Router)) http.Handler {
	mux := http.NewServeMux()
	register(NewRouter(mux))
	return Recover(SecurityHeaders(mux))
}

func get(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestHandlerErrorsAndPanicsAreContained(t *testing.T) {
	h := newMux(func(rt *Router) {
		g := rt.Group("/api/v1/mod")
		g.Get("/raw", func(http.ResponseWriter, *http.Request) error { return errors.New(secret) })
		g.Get("/panic-string", func(http.ResponseWriter, *http.Request) error { panic(secret) })
		g.Get("/panic-error", func(http.ResponseWriter, *http.Request) error { panic(errors.New(secret)) })
		g.Get("/panic-nil-map", func(http.ResponseWriter, *http.Request) error {
			var m map[string]string
			m[secret] = "x"
			return nil
		})
		g.Raw("GET", "/panic-raw", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(secret) }))
		g.Get("/ok", func(w http.ResponseWriter, _ *http.Request) error { OK(w, "iyi"); return nil })
	})
	for _, p := range []string{"/raw", "/panic-string", "/panic-error", "/panic-nil-map", "/panic-raw"} {
		rec := get(h, "GET", "/api/v1/mod"+p)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s: status %d, want 500", p, rec.Code)
		}
		body := rec.Body.String()
		if body != internalMsg {
			t.Errorf("%s: body %q", p, body)
		}
		for _, leak := range []string{"S3CRET", "hunter2", "goroutine", ".go:", "panic", "runtime"} {
			if strings.Contains(body, leak) {
				t.Errorf("%s: response contains %q: %s", p, leak, body)
			}
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: security headers missing on the error response", p)
		}
		// The server keeps serving after each failure.
		if ok := get(h, "GET", "/api/v1/mod/ok"); ok.Code != 200 || ok.Body.String() != `{"success":true,"data":"iyi","error":null}`+"\n" {
			t.Fatalf("after %s the server answered %d %s", p, ok.Code, ok.Body.String())
		}
	}
}

func TestRecoverPassesAbortThrough(t *testing.T) {
	h := Recover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }))
	defer func() {
		if v := recover(); v != http.ErrAbortHandler {
			t.Fatalf("recovered %v, want http.ErrAbortHandler", v)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	t.Fatal("ErrAbortHandler was swallowed")
}

func TestSecurityHeaders(t *testing.T) {
	h := newMux(func(rt *Router) {
		rt.Get("/ok", func(w http.ResponseWriter, _ *http.Request) error { OK(w, nil); return nil })
		rt.Post("/fail", func(http.ResponseWriter, *http.Request) error { return Forbidden() })
	})
	for _, target := range []string{"GET /ok", "POST /fail", "GET /missing", "DELETE /ok", "HEAD /ok"} {
		m, p, _ := strings.Cut(target, " ")
		hd := get(h, m, p).Header()
		want := map[string]string{
			"X-Content-Type-Options": "nosniff",
			"X-Frame-Options":        "DENY",
			"Referrer-Policy":        "same-origin",
			"Permissions-Policy":     "camera=(), microphone=(), geolocation=()",
		}
		for k, v := range want {
			if hd.Get(k) != v {
				t.Errorf("%s: %s = %q, want %q", target, k, hd.Get(k), v)
			}
		}
		csp := hd.Get("Content-Security-Policy")
		for _, directive := range []string{"default-src 'self'", "frame-ancestors 'none'", "base-uri 'self'", "form-action 'self'"} {
			if !strings.Contains(csp, directive) {
				t.Errorf("%s: CSP lacks %q: %q", target, directive, csp)
			}
		}
		// Scripts must not be allowed inline, from eval or from anywhere.
		for _, part := range strings.Split(csp, ";") {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "script-src") || strings.HasPrefix(part, "default-src") {
				if strings.Contains(part, "unsafe-inline") || strings.Contains(part, "unsafe-eval") || strings.Contains(part, "*") {
					t.Errorf("%s: CSP allows unsafe scripts: %q", target, part)
				}
			}
		}
	}
}

func TestRouterGroupsAndMiddlewareOrder(t *testing.T) {
	var order []string
	mw := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	deny := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { Fail(w, r, Forbidden()) })
	}
	ran := false
	mux := http.NewServeMux()
	root := NewRouter(mux)
	api := root.Group("/api", mw("outer"))
	g := api.Group("/mod", mw("group"))
	g.Get("/item/{id}", func(w http.ResponseWriter, r *http.Request) error {
		order = append(order, "handler:"+r.PathValue("id"))
		OK(w, nil)
		return nil
	}, mw("route"))
	g.Get("", func(w http.ResponseWriter, _ *http.Request) error { OK(w, "root"); return nil })
	api.Group("/locked", deny).Post("/x", func(w http.ResponseWriter, _ *http.Request) error {
		ran = true
		return nil
	})
	// A sibling group must not inherit another group's middleware.
	api.Group("/open").Get("/x", func(w http.ResponseWriter, _ *http.Request) error { OK(w, nil); return nil })

	if rec := get(mux, "GET", "/api/mod/item/42"); rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if got := strings.Join(order, ","); got != "outer,group,route,handler:42" {
		t.Fatalf("middleware order %q", got)
	}
	if rec := get(mux, "GET", "/api/mod"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "root") {
		t.Fatalf("group root: %d %s", rec.Code, rec.Body.String())
	}
	if rec := get(mux, "POST", "/api/locked/x"); rec.Code != 403 || ran {
		t.Fatalf("guarded handler: status %d ran=%v", rec.Code, ran)
	}
	order = nil
	if rec := get(mux, "GET", "/api/open/x"); rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if got := strings.Join(order, ","); got != "outer" {
		t.Fatalf("sibling group ran middleware %q", got)
	}
	// The method is part of the route.
	if rec := get(mux, "DELETE", "/api/mod/item/42"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method: status %d", rec.Code)
	}
}

func TestGroupDoesNotShareMiddlewareSlices(t *testing.T) {
	// Two groups derived from the same parent must not overwrite each
	// other's middleware through a shared backing array.
	mark := func(name string, hits *[]string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				*hits = append(*hits, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	var hits []string
	mux := http.NewServeMux()
	parent := NewRouter(mux).Group("/p", mark("p1", &hits), mark("p2", &hits))
	a := parent.Group("/a", mark("a", &hits))
	b := parent.Group("/b", mark("b", &hits))
	ok := func(w http.ResponseWriter, _ *http.Request) error { OK(w, nil); return nil }
	a.Get("/x", ok)
	b.Get("/x", ok)
	get(mux, "GET", "/p/a/x")
	if got := strings.Join(hits, ","); got != "p1,p2,a" {
		t.Fatalf("group a ran %q", got)
	}
	hits = nil
	get(mux, "GET", "/p/b/x")
	if got := strings.Join(hits, ","); got != "p1,p2,b" {
		t.Fatalf("group b ran %q", got)
	}
}

type payload struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

func decode(body string) (payload, error) {
	var p payload
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	err := Decode(httptest.NewRecorder(), r, &p)
	return p, err
}

func TestDecode(t *testing.T) {
	p, err := decode(`{"name":"a","n":3}`)
	if err != nil || p.Name != "a" || p.N != 3 {
		t.Fatalf("valid body: %+v %v", p, err)
	}
	bad := map[string]string{
		"empty":          "",
		"unknown field":  `{"name":"a","admin":true}`,
		"wrong type":     `{"name":1}`,
		"array":          `[1,2]`,
		"truncated":      `{"name":"a"`,
		"two documents":  `{"name":"a"}{"name":"b"}`,
		"trailing text":  `{"name":"a"} x`,
		"not json":       `name=a`,
		"oversized":      `{"name":"` + strings.Repeat("a", 1<<20) + `"}`,
		"oversized junk": strings.Repeat(" ", 1<<20) + `{"name":"a"}`,
	}
	for name, body := range bad {
		_, err := decode(body)
		var e *Error
		if !errors.As(err, &e) || e.Status != http.StatusBadRequest {
			t.Errorf("%s: error %v, want a 400", name, err)
			continue
		}
		if strings.Contains(e.Message, "json") || strings.Contains(e.Message, "field") {
			t.Errorf("%s: decoder text in the user message: %q", name, e.Message)
		}
	}
	if _, err := decode(`{"name":"` + strings.Repeat("a", 1<<19) + `"}`); err != nil {
		t.Errorf("a 512 KiB body was refused: %v", err)
	}
}

func TestSameOrigin(t *testing.T) {
	cases := []struct {
		host, origin string
		want         bool
	}{
		{"panel.lan:8080", "http://panel.lan:8080", true},
		{"panel.lan:8080", "https://panel.lan:8080", true},
		{"panel.lan:8080", "HTTP://PANEL.LAN:8080", true},
		{"192.168.1.10:8080", "http://192.168.1.10:8080", true},
		{"[fe80::1]:8080", "http://[fe80::1]:8080", true},
		{"panel.lan", "http://panel.lan", true},
		{"panel.lan:8080", "", false},
		{"panel.lan:8080", "null", false},
		{"panel.lan:8080", "panel.lan:8080", false},
		{"panel.lan:8080", "http://panel.lan", false},
		{"panel.lan:8080", "http://panel.lan:8081", false},
		{"panel.lan:8080", "http://evil.example", false},
		{"panel.lan:8080", "http://panel.lan:8080.evil.example", false},
		{"panel.lan:8080", "http://evil.example/panel.lan:8080", false},
		{"panel.lan:8080", "http://evil.example#panel.lan:8080", false},
		{"panel.lan:8080", "http://panel.lan:8080@evil.example", false},
		{"panel.lan:8080", "http://evil.example@panel.lan:8080/", false},
		{"panel.lan:8080", "http://xpanel.lan:8080", false},
		{"panel.lan:8080", "http://panel.lan:8080/", false},
		{"", "http://", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if got := SameOrigin(r); got != c.want {
			t.Errorf("Host %q Origin %q: %v, want %v", c.host, c.origin, got, c.want)
		}
	}
}

func TestClientIP(t *testing.T) {
	cases := map[string]string{
		"203.0.113.5:4711": "203.0.113.5",
		"[2001:db8::1]:80": "2001:db8::1",
		"203.0.113.5":      "203.0.113.5",
	}
	for addr, want := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = addr
		r.TLS = &tls.ConnectionState{}
		r.Header.Set("X-Forwarded-For", "10.0.0.1")
		r.Header.Set("X-Real-IP", "10.0.0.1")
		r.Header.Set("Forwarded", "for=10.0.0.1")
		if got := ClientIP(r); got != want {
			t.Errorf("%s: %q, want %q", addr, got, want)
		}
	}
}
