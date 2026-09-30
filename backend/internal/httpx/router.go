package httpx

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
)

// HandlerFunc is a handler that reports failure by returning an error.
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// Middleware wraps a handler.
type Middleware func(http.Handler) http.Handler

// Router is a thin prefix/middleware layer over http.ServeMux.
type Router struct {
	mux    *http.ServeMux
	prefix string
	mws    []Middleware
}

func NewRouter(mux *http.ServeMux) *Router { return &Router{mux: mux} }

// Group returns a router for a sub-path that inherits this router's
// middleware and adds its own.
func (rt *Router) Group(prefix string, mws ...Middleware) *Router {
	all := make([]Middleware, 0, len(rt.mws)+len(mws))
	all = append(all, rt.mws...)
	all = append(all, mws...)
	return &Router{mux: rt.mux, prefix: rt.prefix + prefix, mws: all}
}

// Handle registers an error-returning handler.
func (rt *Router) Handle(method, path string, h HandlerFunc, mws ...Middleware) {
	rt.Raw(method, path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			Fail(w, r, err)
		}
	}), mws...)
}

func (rt *Router) Get(path string, h HandlerFunc, mws ...Middleware) {
	rt.Handle(http.MethodGet, path, h, mws...)
}

func (rt *Router) Post(path string, h HandlerFunc, mws ...Middleware) {
	rt.Handle(http.MethodPost, path, h, mws...)
}

func (rt *Router) Put(path string, h HandlerFunc, mws ...Middleware) {
	rt.Handle(http.MethodPut, path, h, mws...)
}

func (rt *Router) Delete(path string, h HandlerFunc, mws ...Middleware) {
	rt.Handle(http.MethodDelete, path, h, mws...)
}

// Raw registers a plain http.Handler; used for streams and WebSockets.
func (rt *Router) Raw(method, path string, h http.Handler, mws ...Middleware) {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	for i := len(rt.mws) - 1; i >= 0; i-- {
		h = rt.mws[i](h)
	}
	full := rt.prefix + path
	if full != "/" {
		full = strings.TrimSuffix(full, "/")
	}
	rt.mux.Handle(method+" "+full, h)
}

// Recover turns a panic in one handler into a 500 for that request only, so
// a bug in one module cannot take the panel down.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				slog.Error("handler panic",
					"path", r.URL.Path, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
				Fail(w, r, Internal(fmt.Errorf("panic: %v", v)))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// SecurityHeaders sets conservative browser security headers.
func SecurityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; " +
		"font-src 'self' data:; connect-src 'self' ws: wss:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", csp)
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}
