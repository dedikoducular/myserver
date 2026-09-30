package system

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"myserver/internal/httpx"
	"myserver/internal/module"
	"myserver/internal/settings"
)

// Module is the system metrics module.
type Module struct {
	deps    module.Deps
	col     *collector
	sampler *sampler
	units   unitsCache
}

// New builds the module. It never fails: a missing sensor or facility is
// reported per request.
func New(deps module.Deps, st *settings.API) (module.Module, error) {
	settings.RegisterDefault(KeyDiskWarning, "85")
	settings.RegisterDefault(KeyDiskCritical, "95")
	settings.RegisterDefault(KeyTempWarning, "80")
	settings.RegisterDefault(KeyTempCritical, "90")
	if st != nil {
		st.Allow(KeyDiskWarning, settings.IntRange(50, 99), nil)
		st.Allow(KeyDiskCritical, settings.IntRange(51, 100), nil)
		st.Allow(KeyTempWarning, settings.IntRange(40, 110), nil)
		st.Allow(KeyTempCritical, settings.IntRange(41, 120), nil)
	}
	col := newCollector()
	return &Module{deps: deps, col: col, sampler: newSampler(col)}, nil
}

func (m *Module) Name() string { return "system" }

func (m *Module) Register(api, _ *httpx.Router) {
	g := api.Group("/system")
	g.Get("/info", m.handleInfo)
	g.Get("/metrics", m.handleMetrics)
	g.Get("/stream", m.handleStream)
	g.Get("/network/history", m.handleNetworkHistory)
}

// Start runs the sampler and the slow watch loop until ctx is cancelled.
func (m *Module) Start(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.sampler.run(ctx)
	}()
	m.watch(ctx)
	<-done
}

func (m *Module) handleInfo(w http.ResponseWriter, _ *http.Request) error {
	info := m.col.info()
	if info.Kernel == "" && info.OS == "" {
		return httpx.Unavailable("system_unreadable", "Sistem bilgileri okunamadı.")
	}
	httpx.OK(w, info)
	return nil
}

type metricsResponse struct {
	Snapshot *Snapshot   `json:"snapshot"`
	Interval int         `json:"interval"`
	History  []FinePoint `json:"history"`
}

func (m *Module) handleMetrics(w http.ResponseWriter, r *http.Request) error {
	m.sampler.touch()
	snap, fine := m.sampler.current(2 * sampleInterval)
	if snap == nil {
		// The sampler was idle: wait for its first sample.
		ch, _, _, cancel := m.sampler.subscribe()
		timer := time.NewTimer(3 * time.Second)
	wait:
		for {
			select {
			case <-r.Context().Done():
				break wait
			case <-timer.C:
				break wait
			case ev := <-ch:
				if ev.name == "metrics" {
					break wait
				}
			}
		}
		timer.Stop()
		cancel()
		snap, fine = m.sampler.current(2 * sampleInterval)
	}
	if snap == nil {
		return httpx.Unavailable("metrics_unavailable", "Sistem ölçümleri okunamadı.")
	}
	httpx.OK(w, metricsResponse{Snapshot: snap, Interval: int(sampleInterval.Seconds()), History: fine})
	return nil
}

var historyRanges = map[string]time.Duration{
	"5m":  5 * time.Minute,
	"15m": 15 * time.Minute,
	"30m": 30 * time.Minute,
	"1h":  time.Hour,
}

func (m *Module) handleNetworkHistory(w http.ResponseWriter, r *http.Request) error {
	name := r.URL.Query().Get("range")
	if name == "" {
		name = "1h"
	}
	d, ok := historyRanges[name]
	if !ok {
		return httpx.BadRequest("Zaman aralığı geçersiz.")
	}
	httpx.OK(w, map[string]any{
		"range":    name,
		"interval": int(netInterval.Seconds()),
		"points":   m.sampler.hist.since(d),
	})
	return nil
}

// handleStream pushes every sample over SSE. Events: "history" once after
// connecting, "metrics" per sample, "network" per coarse history point.
func (m *Module) handleStream(w http.ResponseWriter, r *http.Request) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return httpx.Internal(fmt.Errorf("response writer does not support streaming"))
	}
	ch, latest, fine, cancel := m.sampler.subscribe()
	defer cancel()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	first, err := json.Marshal(metricsResponse{Snapshot: latest, Interval: int(sampleInterval.Seconds()), History: fine})
	if err != nil {
		return nil
	}
	if _, err := fmt.Fprintf(w, "event: history\ndata: %s\n\n", first); err != nil {
		return nil
	}
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
		case ev := <-ch:
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.name, ev.data); err != nil {
				return nil
			}
			flusher.Flush()
		}
	}
}
