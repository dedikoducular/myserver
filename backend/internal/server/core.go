package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"myserver/internal/auth"
	"myserver/internal/httpx"
	"myserver/internal/logbuf"
	"myserver/internal/module"
)

// registerCore mounts the cross-cutting endpoints: notifications, audit
// log, recent logs, health and module states.
func (s *Server) registerCore(api *httpx.Router) {
	n := api.Group("/notifications")
	n.Get("", s.handleNotifications)
	n.Get("/stream", s.handleNotificationStream)
	n.Post("/read-all", s.handleNotificationsReadAll)
	n.Post("/{id}/read", s.handleNotificationRead)
	n.Delete("/{id}", s.handleNotificationDelete, auth.RequireAdmin)
	n.Delete("", s.handleNotificationsClear, auth.RequireAdmin)

	api.Get("/audit", s.handleAudit, auth.RequireAdmin)
	api.Get("/logs", s.handleLogs, auth.RequireAdmin)
	api.Get("/health", s.handleHealth)
	api.Get("/modules", s.handleModules)
}

func intQuery(r *http.Request, key string, def int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return def
	}
	return n
}

func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) error {
	items, err := s.deps.Notify.List(r.Context(), intQuery(r, "limit", 50), r.URL.Query().Get("unread") == "true")
	if err != nil {
		return httpx.Internal(err)
	}
	unread, err := s.deps.Notify.UnreadCount(r.Context())
	if err != nil {
		return httpx.Internal(err)
	}
	httpx.OK(w, map[string]any{"items": items, "unread": unread})
	return nil
}

// handleNotificationStream pushes new notifications over SSE.
func (s *Server) handleNotificationStream(w http.ResponseWriter, r *http.Request) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return httpx.Internal(fmt.Errorf("response writer does not support streaming"))
	}
	ch, cancel := s.deps.Notify.Subscribe()
	defer cancel()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": bağlandı\n\n")
	flusher.Flush()
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return nil
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return nil
			}
			flusher.Flush()
		case n := <-ch:
			b, err := json.Marshal(n)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: notification\ndata: %s\n\n", b); err != nil {
				return nil
			}
			flusher.Flush()
		}
	}
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, httpx.BadRequest("Geçersiz kayıt numarası.")
	}
	return id, nil
}

func (s *Server) handleNotificationRead(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	if err := s.deps.Notify.MarkRead(r.Context(), id); err != nil {
		return httpx.Internal(err)
	}
	httpx.OK(w, nil)
	return nil
}

func (s *Server) handleNotificationsReadAll(w http.ResponseWriter, r *http.Request) error {
	if err := s.deps.Notify.MarkAllRead(r.Context()); err != nil {
		return httpx.Internal(err)
	}
	httpx.OK(w, nil)
	return nil
}

func (s *Server) handleNotificationDelete(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	if err := s.deps.Notify.Delete(r.Context(), id); err != nil {
		return httpx.Internal(err)
	}
	httpx.OK(w, nil)
	return nil
}

func (s *Server) handleNotificationsClear(w http.ResponseWriter, r *http.Request) error {
	if err := s.deps.Notify.Clear(r.Context()); err != nil {
		return httpx.Internal(err)
	}
	s.deps.Audit.Log(r.Context(), auth.ActorFrom(r), "notifications.clear", "", "", true)
	httpx.OK(w, nil)
	return nil
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) error {
	items, total, err := s.deps.Audit.List(r.Context(), intQuery(r, "limit", 100), intQuery(r, "offset", 0))
	if err != nil {
		return httpx.Internal(err)
	}
	httpx.OK(w, map[string]any{"items": items, "total": total})
	return nil
}

// handleLogs returns the panel's own recent log lines (already redacted),
// never raw system logs.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) error {
	limit := intQuery(r, "limit", 50)
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	entries := s.deps.Logs.Recent(limit)
	if entries == nil {
		entries = []logbuf.Entry{}
	}
	httpx.OK(w, entries)
	return nil
}

func (s *Server) handleModules(w http.ResponseWriter, _ *http.Request) error {
	httpx.OK(w, s.states)
	return nil
}

// HealthReport is the aggregated system health.
type HealthReport struct {
	Status    module.HealthStatus  `json:"status"`
	Checks    []module.HealthCheck `json:"checks"`
	CheckedAt int64                `json:"checked_at"`
}

type healthCache struct {
	mu     sync.Mutex
	report HealthReport
	at     time.Time
}

const healthTTL = 15 * time.Second

func severity(s module.HealthStatus) int {
	switch s {
	case module.CriticalLvl:
		return 2
	case module.WarningLevel:
		return 1
	}
	return 0
}

// Health aggregates the health checks of every loaded module, cached
// briefly so dashboards cannot trigger probe storms.
func (s *Server) Health(ctx context.Context) HealthReport {
	s.health.mu.Lock()
	defer s.health.mu.Unlock()
	if !s.health.at.IsZero() && time.Since(s.health.at) < healthTTL {
		return s.health.report
	}
	rep := HealthReport{Status: module.Healthy, Checks: []module.HealthCheck{}}
	for _, st := range s.states {
		if !st.Loaded {
			rep.Checks = append(rep.Checks, module.HealthCheck{
				ID: "module." + st.Name, Name: "Modül: " + st.Name,
				Status: module.WarningLevel, Message: "Modül yüklenemedi.",
			})
		}
	}
	for _, m := range s.modules {
		hr, ok := m.(module.HealthReporter)
		if !ok || !s.loaded(m.Name()) {
			continue
		}
		rep.Checks = append(rep.Checks, safeHealth(ctx, m.Name(), hr)...)
	}
	for _, c := range rep.Checks {
		if severity(c.Status) > severity(rep.Status) {
			rep.Status = c.Status
		}
	}
	rep.CheckedAt = time.Now().Unix()
	s.health.report, s.health.at = rep, time.Now()
	return rep
}

func safeHealth(ctx context.Context, name string, hr module.HealthReporter) (out []module.HealthCheck) {
	defer func() {
		if v := recover(); v != nil {
			slog.Error("sağlık denetimi çöktü", "module", name, "panic", fmt.Sprint(v))
			out = []module.HealthCheck{{
				ID: "module." + name, Name: "Modül: " + name,
				Status: module.WarningLevel, Message: "Sağlık denetimi çalıştırılamadı.",
			}}
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return hr.Health(ctx)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) error {
	httpx.OK(w, s.Health(r.Context()))
	return nil
}
