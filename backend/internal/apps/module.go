package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"

	"myserver/internal/audit"
	"myserver/internal/auth"
	"myserver/internal/httpx"
	"myserver/internal/module"
	"myserver/internal/notify"
	"myserver/internal/settings"
)

// Module is the application system. Other modules (backup) obtain it by
// asserting the module.Module returned by New to *apps.Module and use the
// exported methods documented in api.go.
type Module struct {
	deps    module.Deps
	catalog *Catalog
	store   *store
	eng     *engine
	jobs    *jobManager
	hub     *hub

	mu      sync.Mutex
	busy    map[string]string // slug -> running operation
	baseCtx context.Context

	healthMu    sync.Mutex
	healthAt    time.Time
	healthCache []module.HealthCheck
}

// New builds the module. It never fails because Docker is unavailable; that
// condition is reported per request.
func New(deps module.Deps, _ *settings.API) (module.Module, error) {
	m := &Module{
		deps:    deps,
		catalog: NewCatalog(deps.Cfg.ManifestDir),
		store:   &store{db: deps.DB},
		eng:     &engine{host: deps.Cfg.DockerHost, dataDir: deps.Cfg.DataDir},
		jobs:    newJobManager(),
		hub:     newHub(),
		busy:    map[string]string{},
		baseCtx: context.Background(),
	}
	m.catalog.Reload()
	lctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	m.loadCustom(lctx)
	cancel()
	return m, nil
}

func (m *Module) Name() string { return "apps" }

func (m *Module) Register(api, _ *httpx.Router) {
	g := api.Group("/apps")
	g.Get("/catalog", m.handleCatalog)
	g.Get("/catalog/{slug}", m.handleCatalogApp)
	g.Get("/installed", m.handleInstalled)
	g.Get("/installed/{slug}", m.handleInstalledApp)
	g.Get("/events", m.handleEvents)
	g.Get("/icons/{name}", m.handleIcon)

	a := api.Group("/apps", auth.RequireAdmin)
	a.Post("/reload", m.handleReload)
	a.Post("/custom/preview", m.handleCustomPreview)
	a.Post("/custom", m.handleCustomCreate)
	a.Delete("/custom/{slug}", m.handleCustomDelete)
	a.Post("/catalog/{slug}/install", m.handleInstall)
	a.Post("/installed/{slug}/start", m.handleLifecycle("start"))
	a.Post("/installed/{slug}/stop", m.handleLifecycle("stop"))
	a.Post("/installed/{slug}/restart", m.handleLifecycle("restart"))
	a.Post("/installed/{slug}/update", m.handleUpdate)
	a.Put("/installed/{slug}/settings", m.handleSettings)
	a.Post("/installed/{slug}/uninstall", m.handleUninstall)
	a.Get("/installed/{slug}/logs", m.handleLogs)
	a.Get("/jobs", m.handleJobs)
	a.Get("/jobs/{id}", m.handleJob)
	a.Get("/jobs/{id}/stream", m.handleJobStream)
}

// Start watches Docker for container state changes and tells connected
// browsers to refresh. It returns when ctx is cancelled.
func (m *Module) Start(ctx context.Context) {
	m.mu.Lock()
	m.baseCtx = ctx
	m.mu.Unlock()

	wait := func(d time.Duration) bool {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			return true
		}
	}
	for ctx.Err() == nil {
		cli, err := m.eng.ready(ctx)
		if err != nil {
			if !wait(20 * time.Second) {
				return
			}
			continue
		}
		m.hub.notify()
		m.watch(ctx, cli)
		m.hub.notify()
		if !wait(5 * time.Second) {
			return
		}
	}
}

func (m *Module) watch(ctx context.Context, cli *client.Client) {
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	f := filters.NewArgs(
		filters.Arg("type", string(events.ContainerEventType)),
		filters.Arg("label", LabelManaged+"=true"),
	)
	msgs, errs := cli.Events(wctx, events.ListOptions{Filters: f})
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-errs:
			if err != nil && ctx.Err() == nil {
				slog.Warn("Docker olay akışı kesildi", "error", err.Error())
			}
			return
		case ev := <-msgs:
			switch ev.Action {
			case events.ActionCreate, events.ActionStart, events.ActionStop, events.ActionDie,
				events.ActionDestroy, events.ActionRestart, events.ActionPause, events.ActionUnPause,
				events.ActionKill, events.ActionRename, events.ActionOOM,
				events.ActionHealthStatusHealthy, events.ActionHealthStatusUnhealthy, events.ActionHealthStatusRunning:
				m.hub.notify()
			}
		}
	}
}

// Health reports installed applications that are not healthy.
func (m *Module) Health(ctx context.Context) []module.HealthCheck {
	m.healthMu.Lock()
	defer m.healthMu.Unlock()
	if time.Since(m.healthAt) < 30*time.Second && m.healthCache != nil {
		return m.healthCache
	}
	out := []module.HealthCheck{}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	views, dockerOK, err := m.installedViews(cctx)
	if err == nil && dockerOK {
		for _, v := range views {
			if m.jobs.running(v.Slug) != nil {
				continue
			}
			switch v.State {
			case StateUnhealthy:
				out = append(out, module.HealthCheck{ID: "apps." + v.Slug, Name: v.Name,
					Status: module.WarningLevel, Message: v.Name + " sağlık denetimini geçemiyor."})
			case StatePartial:
				out = append(out, module.HealthCheck{ID: "apps." + v.Slug, Name: v.Name,
					Status: module.WarningLevel, Message: v.Name + " uygulamasının bazı servisleri çalışmıyor."})
			case StateMissing:
				out = append(out, module.HealthCheck{ID: "apps." + v.Slug, Name: v.Name,
					Status: module.WarningLevel, Message: v.Name + " uygulamasının konteynerleri bulunamadı."})
			}
		}
	}
	m.healthAt, m.healthCache = time.Now(), out
	return out
}

/* ---------- change notifications ---------- */

type hub struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func newHub() *hub { return &hub{subs: map[chan struct{}]struct{}{}} }

func (h *hub) notify() {
	h.mu.Lock()
	for ch := range h.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	h.mu.Unlock()
}

func (h *hub) subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

/* ---------- operations ---------- */

var errBusy = httpx.Conflict("Bu uygulama için devam eden bir işlem var. Bitmesini bekleyin.")

// lock reserves an application for one operation at a time.
func (m *Module) lock(slug, op string) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.busy[slug]; ok {
		return nil, errBusy
	}
	m.busy[slug] = op
	return func() {
		m.mu.Lock()
		delete(m.busy, slug)
		m.mu.Unlock()
		m.hub.notify()
	}, nil
}

func (m *Module) operation(slug string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.busy[slug]
}

func (m *Module) background() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.baseCtx
}

func (m *Module) roots() []string {
	return m.deps.Settings.Strings(settings.KeyAllowedRoots)
}

// errNotInstalled is returned for an application that is not installed.
var errNotInstalled = httpx.NotFound("Uygulama kurulu değil.")

func (m *Module) installed(ctx context.Context, slug string) (*Installed, error) {
	if !ValidSlug(slug) {
		return nil, httpx.BadRequest("Uygulama adı geçersiz.")
	}
	it, err := m.store.get(ctx, slug)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	if it == nil {
		return nil, errNotInstalled
	}
	return it, nil
}

// runJob runs fn in the background as a job. release is called when the job
// ends. The job does not depend on the request that started it.
func (m *Module) runJob(job *Job, release func(), fn func(ctx context.Context) error) {
	go func() {
		defer release()
		var err error
		func() {
			defer func() {
				if v := recover(); v != nil {
					slog.Error("uygulama işi çöktü", "job", job.Kind, "app", job.Slug,
						"panic", fmt.Sprint(v), "stack", string(debug.Stack()))
					err = errors.New("panic")
				}
			}()
			ctx, cancel := context.WithTimeout(m.background(), 2*time.Hour)
			defer cancel()
			err = fn(ctx)
		}()
		if err != nil {
			slog.Error("uygulama işi başarısız", "job", job.Kind, "app", job.Slug, "error", err.Error())
			job.finish(userMessage(err))
		} else {
			job.finish("")
		}
		m.hub.notify()
	}()
}

func (m *Module) audit(actor audit.Actor, action, target, detail string, ok bool) {
	m.deps.Audit.Log(context.Background(), actor, action, target, detail, ok)
}

func (m *Module) publish(sev notify.Severity, title, message string) {
	m.deps.Notify.Publish(context.Background(), sev, "apps", title, message)
}

// prepare resolves and validates a configuration and checks its ports.
func (m *Module) prepare(ctx context.Context, cli *client.Client, man *Manifest, in Inputs, prev *Inputs) (*Config, *Inputs, error) {
	cfg, norm, err := Resolve(man, in, prev, m.roots())
	if err != nil {
		var ie *InputError
		if errors.As(err, &ie) {
			return nil, nil, httpx.NewError(http.StatusBadRequest, "invalid_input", ie.Message)
		}
		return nil, nil, httpx.Internal(err)
	}
	if err := checkProtectedMounts(cfg, protectedDirs(m.eng.dataDir)); err != nil {
		return nil, nil, httpx.NewError(http.StatusBadRequest, "invalid_input", userMessage(err))
	}
	conflicts, err := m.eng.checkPorts(ctx, cli, cfg)
	if err != nil {
		return nil, nil, err
	}
	if len(conflicts) > 0 {
		return nil, nil, portConflictError(conflicts)
	}
	return cfg, norm, nil
}

func (m *Module) startInstall(actor audit.Actor, cli *client.Client, cfg *Config, in *Inputs) (*Job, error) {
	release, err := m.lock(cfg.Slug, JobInstall)
	if err != nil {
		return nil, err
	}
	// A custom definition may have been deleted while the request was
	// prepared; the delete holds the same lock.
	if IsCustomSlug(cfg.Slug) && m.catalog.Get(cfg.Slug) == nil {
		release()
		return nil, httpx.NotFound("Bu özel uygulamanın tanımı silinmiş.")
	}
	job, err := m.jobs.create(cfg.Slug, cfg.Name, JobInstall, actor)
	if err != nil {
		release()
		return nil, httpx.Internal(err)
	}
	m.runJob(job, release, func(ctx context.Context) error {
		err := m.install(ctx, cli, cfg, in, job)
		if err != nil {
			m.audit(actor, "apps.install", cfg.Slug, userMessage(err), false)
			m.publish(notify.Error, "Uygulama kurulamadı", cfg.Name+" kurulamadı: "+userMessage(err))
			return err
		}
		m.audit(actor, "apps.install", cfg.Slug, "", true)
		m.publish(notify.Success, "Uygulama kuruldu", cfg.Name+" başarıyla kuruldu.")
		return nil
	})
	return job, nil
}

func (m *Module) install(ctx context.Context, cli *client.Client, cfg *Config, in *Inputs, rep reporter) error {
	// Containers left behind by an installation that was interrupted (for
	// example by a restart of the panel) carry our labels but belong to no
	// installed application.
	left, err := m.eng.appContainers(ctx, cli, cfg.Slug)
	if err != nil {
		return err
	}
	if len(left) > 0 {
		rep.Step("Önceki yarım kurulumun kalıntıları temizleniyor")
		if err := m.eng.removeContainers(ctx, cli, cfg.Slug); err != nil {
			return err
		}
	}
	rep.Step("Görüntüler indiriliyor")
	images, err := m.eng.pullAll(ctx, cli, cfg, true, rep)
	if err != nil {
		return err
	}
	made := &created{}
	if err := m.eng.up(ctx, cli, cfg, m.roots(), rep, made); err != nil {
		m.eng.cleanup(cli, made, rep)
		return err
	}
	rep.Step("Kurulum kaydediliyor")
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := m.store.save(sctx, cfg, in, images); err != nil {
		m.eng.cleanup(cli, made, rep)
		return httpx.Internal(err)
	}
	if err := m.store.dropRetained(sctx, cfg.Slug); err != nil {
		slog.Warn("saklanan uygulama değerleri silinemedi", "app", cfg.Slug, "error", err.Error())
	}
	return nil
}

func sameConfig(a, b *Config) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(x) == string(y)
}

func shortTag(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// startReplace runs an update or a settings change: pull, then replace the
// containers, keeping volumes and configuration.
func (m *Module) startReplace(actor audit.Actor, cli *client.Client, kind string, it *Installed, cfg *Config, in *Inputs) (*Job, error) {
	release, err := m.lock(cfg.Slug, kind)
	if err != nil {
		return nil, err
	}
	job, err := m.jobs.create(cfg.Slug, cfg.Name, kind, actor)
	if err != nil {
		release()
		return nil, httpx.Internal(err)
	}
	action, okTitle, failTitle := "apps.update", "Uygulama güncellendi", "Uygulama güncellenemedi"
	if kind == JobSettings {
		action, okTitle, failTitle = "apps.settings", "Uygulama ayarları değişti", "Uygulama ayarları uygulanamadı"
	}
	m.runJob(job, release, func(ctx context.Context) error {
		changed, err := m.replace(ctx, cli, kind, it, cfg, in, job)
		if err != nil {
			m.audit(actor, action, cfg.Slug, userMessage(err), false)
			m.publish(notify.Error, failTitle, cfg.Name+": "+userMessage(err)+" Önceki sürüm çalışmaya devam ediyor.")
			return err
		}
		switch {
		case kind == JobUpdate && !changed:
			job.Log("Yeni bir sürüm bulunamadı; uygulama zaten güncel.")
			m.audit(actor, action, cfg.Slug, "zaten güncel", true)
			m.publish(notify.Info, "Uygulama zaten güncel", cfg.Name+" için yeni bir sürüm bulunamadı.")
		case kind == JobUpdate:
			m.audit(actor, action, cfg.Slug, "", true)
			m.publish(notify.Success, okTitle, cfg.Name+" en son sürüme güncellendi.")
		default:
			m.audit(actor, action, cfg.Slug, "", true)
			m.publish(notify.Success, okTitle, cfg.Name+" yeni ayarlarla yeniden başlatıldı.")
		}
		return nil
	})
	return job, nil
}

func (m *Module) replace(ctx context.Context, cli *client.Client, kind string, it *Installed, cfg *Config, in *Inputs, rep reporter) (bool, error) {
	if kind == JobUpdate {
		rep.Step("Yeni sürüm denetleniyor ve indiriliyor")
	} else {
		rep.Step("Görüntüler denetleniyor")
	}
	images, err := m.eng.pullAll(ctx, cli, cfg, kind == JobUpdate, rep)
	if err != nil {
		return false, err
	}
	if kind == JobUpdate && it != nil && sameConfig(it.Config, cfg) {
		same := len(images) == len(it.Images)
		for name, img := range images {
			if old, ok := it.Images[name]; !ok || old.ID != img.ID {
				same = false
			}
		}
		if same {
			return false, nil
		}
	}
	if err := m.eng.recreate(ctx, cli, cfg, m.roots(), shortTag(jobTag(rep)), rep); err != nil {
		return false, err
	}
	rep.Step("Yapılandırma kaydediliyor")
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := m.store.save(sctx, cfg, in, images); err != nil {
		return false, httpx.Internal(err)
	}
	return true, nil
}

func jobTag(rep reporter) string {
	if j, ok := rep.(*Job); ok {
		return j.ID
	}
	return fmt.Sprintf("%x", time.Now().UnixNano())
}

// UninstallResult reports what an uninstall did with the data volumes.
type UninstallResult struct {
	RemovedVolumes []string `json:"removed_volumes"`
	KeptVolumes    []string `json:"kept_volumes"`
	FailedVolumes  []string `json:"failed_volumes"`
	KeptPaths      []string `json:"kept_paths"`
}

func (m *Module) uninstall(ctx context.Context, it *Installed, deleteData bool) (*UninstallResult, error) {
	cli, err := m.eng.ready(ctx)
	if err != nil {
		return nil, err
	}
	release, err := m.lock(it.Slug, "uninstall")
	if err != nil {
		return nil, err
	}
	defer release()
	if err := m.eng.removeContainers(ctx, cli, it.Slug); err != nil {
		return nil, err
	}
	m.eng.removeNetwork(ctx, cli, it.Slug)

	res := &UninstallResult{RemovedVolumes: []string{}, KeptVolumes: []string{}, FailedVolumes: []string{}, KeptPaths: []string{}}
	seen := map[string]bool{}
	for _, s := range it.Config.Services {
		for _, v := range s.Volumes {
			if seen[v.Source] {
				continue
			}
			seen[v.Source] = true
			switch v.Type {
			case VolumeNamed:
				if !deleteData {
					res.KeptVolumes = append(res.KeptVolumes, v.Source)
				}
			case VolumeBind:
				res.KeptPaths = append(res.KeptPaths, v.Source)
			}
		}
	}
	if deleteData {
		removed, failed := m.eng.removeVolumes(ctx, cli, it.Config)
		if removed != nil {
			res.RemovedVolumes = removed
		}
		if failed != nil {
			res.FailedVolumes = failed
		}
		if err := m.store.dropRetained(ctx, it.Slug); err != nil {
			slog.Warn("saklanan uygulama değerleri silinemedi", "app", it.Slug, "error", err.Error())
		}
	} else if err := m.store.retain(ctx, it.Slug, it.Inputs); err != nil {
		slog.Warn("uygulama değerleri saklanamadı", "app", it.Slug, "error", err.Error())
	}
	if err := m.store.remove(ctx, it.Slug); err != nil {
		return nil, httpx.Internal(err)
	}
	return res, nil
}

func (m *Module) lifecycle(ctx context.Context, slug, action string) (*Installed, error) {
	it, err := m.installed(ctx, slug)
	if err != nil {
		return nil, err
	}
	cli, err := m.eng.ready(ctx)
	if err != nil {
		return nil, err
	}
	release, err := m.lock(slug, action)
	if err != nil {
		return nil, err
	}
	defer release()
	if action != "stop" {
		conflicts, err := m.eng.checkPorts(ctx, cli, it.Config)
		if err != nil {
			return nil, err
		}
		if len(conflicts) > 0 {
			return nil, portConflictError(conflicts)
		}
	}
	return it, m.eng.lifecycle(ctx, cli, it.Config, action)
}
