// Package server assembles the HTTP server from the core services and the
// feature modules.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"myserver/internal/auth"
	"myserver/internal/httpx"
	"myserver/internal/module"
	"myserver/internal/notify"
	"myserver/internal/settings"
	"myserver/internal/webui"
)

// ModuleState is the load state of a module, shown in the panel.
type ModuleState struct {
	Name   string `json:"name"`
	Loaded bool   `json:"loaded"`
	Error  string `json:"error,omitempty"`
}

type Server struct {
	deps     module.Deps
	settings *settings.API
	modules  []module.Module

	mu     sync.Mutex
	states []ModuleState
	health healthCache
}

func New(deps module.Deps, settingsAPI *settings.API, modules []module.Module) *Server {
	return &Server{deps: deps, settings: settingsAPI, modules: modules}
}

// Handler builds the root handler. Call it once.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	root := httpx.NewRouter(mux)
	public := root.Group("/api/v1")
	api := root.Group("/api/v1", s.deps.Auth.Require)
	ws := root.Group("/api/v1", s.deps.Auth.RequireWebSocket)

	s.deps.Auth.RegisterPublic(public)
	s.deps.Auth.RegisterPrivate(api)
	s.settings.Register(api, auth.RequireAdmin)
	s.registerCore(api)

	for _, m := range s.modules {
		st := ModuleState{Name: m.Name(), Loaded: true}
		if err := safeRegister(m, api, ws); err != nil {
			st.Loaded = false
			st.Error = "Modül yüklenemedi."
			slog.Error("modül yüklenemedi", "module", m.Name(), "error", err.Error())
		}
		s.states = append(s.states, st)
	}

	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.Fail(w, r, httpx.NotFound("İstenen API adresi bulunamadı."))
	}))
	mux.Handle("/", webui.Handler())

	return httpx.Recover(httpx.SecurityHeaders(mux))
}

// safeRegister isolates a module's route registration: a panic (including a
// duplicate route pattern) disables that module only.
func safeRegister(m module.Module, api, ws *httpx.Router) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("panic: %v", v)
		}
	}()
	m.Register(api, ws)
	return nil
}

// Run serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	handler := s.Handler()

	var wg sync.WaitGroup
	start := func(name string, fn func(context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.supervise(ctx, name, fn)
		}()
	}
	start("auth", s.deps.Auth.Start)
	start("maintenance", s.maintenance)
	for _, m := range s.modules {
		if st, ok := m.(module.Starter); ok && s.loaded(m.Name()) {
			start(m.Name(), st.Start)
		}
	}

	srv := &http.Server{
		Addr:              s.deps.Cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	errCh := make(chan error, 1)
	go func() {
		slog.Info("panel dinlemede", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errCh:
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = srv.Close()
	}
	wg.Wait()
	return runErr
}

func (s *Server) loaded(name string) bool {
	for _, st := range s.states {
		if st.Name == name {
			return st.Loaded
		}
	}
	return false
}

// supervise runs background work for one module. A panic is contained,
// reported, and the work is restarted with backoff a limited number of
// times so a crash loop cannot burn CPU.
func (s *Server) supervise(ctx context.Context, name string, fn func(context.Context)) {
	const maxRestarts = 5
	backoff := 5 * time.Second
	for attempt := 0; ; attempt++ {
		panicked := func() (p bool) {
			defer func() {
				if v := recover(); v != nil {
					p = true
					slog.Error("arka plan görevi çöktü", "module", name,
						"panic", fmt.Sprint(v), "stack", string(debug.Stack()))
				}
			}()
			fn(ctx)
			return false
		}()
		if !panicked || ctx.Err() != nil {
			return
		}
		if attempt >= maxRestarts {
			s.deps.Notify.Publish(ctx, notify.Error, name, "Modül durduruldu",
				"'"+name+"' modülünün arka plan görevi tekrarlayan hatalar nedeniyle durduruldu.")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
	}
}

// maintenance prunes old records daily.
func (s *Server) maintenance(ctx context.Context) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		if err := s.deps.Audit.Prune(ctx, 365*24*time.Hour); err != nil && ctx.Err() == nil {
			slog.Warn("denetim kayıtları temizlenemedi", "error", err.Error())
		}
		if err := s.deps.Notify.Prune(ctx, 90*24*time.Hour); err != nil && ctx.Err() == nil {
			slog.Warn("bildirimler temizlenemedi", "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
