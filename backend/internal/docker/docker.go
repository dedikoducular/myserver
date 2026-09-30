// Package docker manages the Docker Engine through its official API (Go SDK).
// The panel never shells out to the docker CLI and the browser never talks to
// the Docker socket; every operation goes through this package.
//
// The module stays usable when Docker is stopped or not installed: the client
// is created lazily, every request reports "docker_unavailable" (HTTP 503)
// while the daemon cannot be reached, and because the SDK dials per request
// the connection recovers by itself once Docker is back.
package docker

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/client"

	"myserver/internal/auth"
	"myserver/internal/httpx"
	"myserver/internal/module"
	"myserver/internal/settings"
)

// AppLabel is the container label the apps module sets to the app slug.
const AppLabel = "io.myserver.app"

const (
	listTimeout   = 15 * time.Second
	actionTimeout = 90 * time.Second
)

// Module is the docker feature module.
type Module struct {
	deps module.Deps

	// ctx lives as long as the module; background work derives from it.
	ctx    context.Context
	cancel context.CancelFunc

	cliMu sync.Mutex
	cli   *client.Client

	stats    *statsHub
	changes  *changeHub
	pulls    *pullManager
	expected *recentSet // containers the panel itself stopped/killed/removed
	started  *startCache
}

// New never fails because Docker is unreachable; the condition is reported
// per request.
func New(deps module.Deps, _ *settings.API) (module.Module, error) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Module{
		deps:     deps,
		ctx:      ctx,
		cancel:   cancel,
		changes:  newChangeHub(),
		expected: newRecentSet(2 * time.Minute),
		started:  newStartCache(),
	}
	m.stats = newStatsHub(m)
	m.pulls = newPullManager(m)
	return m, nil
}

func (m *Module) Name() string { return "docker" }

func (m *Module) Register(api, ws *httpx.Router) {
	// Read-only listings: any signed-in user.
	g := api.Group("/docker")
	g.Get("/info", m.handleInfo)
	g.Get("/containers", m.handleContainers)
	g.Get("/stats/stream", m.handleStatsStream)
	g.Get("/images", m.handleImages)
	g.Get("/volumes", m.handleVolumes)
	g.Get("/networks", m.handleNetworks)

	// Everything that changes state or may reveal secrets: admin only.
	a := api.Group("/docker", auth.RequireAdmin)
	a.Get("/containers/{id}", m.handleInspect)
	a.Get("/containers/{id}/logs/stream", m.handleLogStream)
	a.Post("/containers/{id}/start", m.containerAction("start"))
	a.Post("/containers/{id}/stop", m.containerAction("stop"))
	a.Post("/containers/{id}/restart", m.containerAction("restart"))
	a.Post("/containers/{id}/kill", m.containerAction("kill"))
	a.Post("/containers/{id}/remove", m.handleContainerRemove)

	a.Post("/images/pull", m.handleImagePull)
	a.Get("/images/pull/{job}/stream", m.handlePullStream)
	a.Post("/images/prune", m.handleImagePrune)
	a.Delete("/images/{id}", m.handleImageRemove)

	a.Get("/volumes/unused", m.handleVolumesUnused)
	a.Post("/volumes/prune", m.handleVolumesPrune)
	a.Delete("/volumes/{name}", m.handleVolumeRemove)

	a.Delete("/networks/{id}", m.handleNetworkRemove)

	// Admin role is checked inside the handler (WebSocket upgrade).
	ws.Raw(http.MethodGet, "/docker/containers/{id}/exec/ws", http.HandlerFunc(m.handleExec))
}

// Start runs the Docker event watcher until ctx is cancelled.
func (m *Module) Start(ctx context.Context) {
	defer m.cancel()
	m.watchEvents(ctx)
}

// client returns the shared SDK client, creating it on first use. Creating
// the client does not connect; connections are made per request.
func (m *Module) client() (*client.Client, error) {
	m.cliMu.Lock()
	defer m.cliMu.Unlock()
	if m.cli != nil {
		return m.cli, nil
	}
	c, err := client.NewClientWithOpts(
		client.WithHost(m.deps.Cfg.DockerHost),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, errUnavailable().Wrap(err)
	}
	m.cli = c
	return c, nil
}

const unavailableMessage = "Docker servisine ulaşılamıyor."

func errUnavailable() *httpx.Error {
	return httpx.Unavailable("docker_unavailable", unavailableMessage)
}

// isUnavailable reports whether err means the daemon could not be reached
// (stopped, not installed, socket permission), as opposed to the daemon
// answering with an error.
func isUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if client.IsErrConnectionFailed(err) {
		return true
	}
	var nerr net.Error
	return errors.As(err, &nerr)
}

// errText holds the Turkish messages for the outcomes of one operation.
type errText struct {
	notFound string
	conflict string
	fallback string
}

// apiError converts an SDK error into a user-facing error. The raw cause is
// only attached for logging.
func apiError(err error, t errText) error {
	var he *httpx.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &he):
		return he
	case errors.Is(err, context.Canceled):
		return httpx.NewError(http.StatusRequestTimeout, "cancelled", "İstek iptal edildi.")
	case errors.Is(err, context.DeadlineExceeded):
		return httpx.NewError(http.StatusGatewayTimeout, "docker_timeout", "Docker zamanında yanıt vermedi.").Wrap(err)
	case isUnavailable(err):
		return errUnavailable().Wrap(err)
	case cerrdefs.IsNotFound(err):
		msg := t.notFound
		if msg == "" {
			msg = "Kayıt bulunamadı."
		}
		return httpx.NotFound(msg)
	case cerrdefs.IsConflict(err):
		msg := t.conflict
		if msg == "" {
			msg = t.fallback
		}
		return httpx.Conflict(msg).Wrap(err)
	case cerrdefs.IsPermissionDenied(err), cerrdefs.IsUnauthorized(err):
		return httpx.NewError(http.StatusForbidden, "docker_denied", "Docker bu işleme izin vermedi.").Wrap(err)
	}
	return httpx.NewError(http.StatusBadGateway, "docker_error", t.fallback).Wrap(err)
}

// userMessage extracts the Turkish message of an error produced by apiError.
func userMessage(err error, fallback string) string {
	var he *httpx.Error
	if errors.As(err, &he) && he.Message != "" {
		return he.Message
	}
	return fallback
}

// recentSet remembers keys for a limited time.
type recentSet struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[string]time.Time
}

func newRecentSet(ttl time.Duration) *recentSet {
	return &recentSet{ttl: ttl, items: map[string]time.Time{}}
}

func (s *recentSet) mark(key string) {
	now := time.Now()
	s.mu.Lock()
	for k, at := range s.items {
		if now.Sub(at) > s.ttl {
			delete(s.items, k)
		}
	}
	s.items[key] = now
	s.mu.Unlock()
}

func (s *recentSet) unmark(key string) {
	s.mu.Lock()
	delete(s.items, key)
	s.mu.Unlock()
}

func (s *recentSet) has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.items[key]
	return ok && time.Since(at) <= s.ttl
}

// changeEvent tells subscribers that a Docker object changed, so lists can
// be reloaded without polling.
type changeEvent struct {
	Kind   string `json:"kind"` // container, image, volume, network
	Action string `json:"action"`
	ID     string `json:"id"`
}

type changeHub struct {
	mu   sync.Mutex
	subs map[chan changeEvent]struct{}
}

func newChangeHub() *changeHub {
	return &changeHub{subs: map[chan changeEvent]struct{}{}}
}

func (h *changeHub) subscribe() (<-chan changeEvent, func()) {
	ch := make(chan changeEvent, 32)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

func (h *changeHub) publish(ev changeEvent) {
	h.mu.Lock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default: // slow subscriber: drop, the next event reloads anyway
		}
	}
	h.mu.Unlock()
}
