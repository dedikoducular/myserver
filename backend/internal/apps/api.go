package apps

// Exported Go API for other modules (written for the backup module).
//
// Obtain the module with a type assertion on the value returned by New:
//
//	appsMod, _ := mod.(*apps.Module)
//
// All methods are safe for concurrent use. Methods that change state take
// the per-application lock and fail with an *httpx.Error (409) while another
// operation on the same application is running; Docker being unreachable is
// an *httpx.Error with code "docker_unavailable" (503). Every error returned
// is either an *httpx.Error carrying a Turkish, user-facing message or an
// internal error.
//
// The configuration returned by AppConfig contains secret environment values
// in plain text: store it only inside the (protected) backup and never log
// or return it through an API.

import (
	"context"
	"errors"
	"net/http"
	"time"

	"myserver/internal/audit"
	"myserver/internal/httpx"
)

// InstalledApp summarizes one installed application.
type InstalledApp struct {
	Slug        string               `json:"slug"`
	Name        string               `json:"name"`
	Version     string               `json:"version"`
	InstalledAt int64                `json:"installed_at"`
	UpdatedAt   int64                `json:"updated_at"`
	Images      map[string]ImageInfo `json:"images"` // by service name
	// Volumes lists every data location of the application, without
	// duplicates: Docker volumes (Type "volume", Source is the volume
	// name), host folders chosen by the user (Type "bind", Source is the
	// host path) and system mounts (Type "system", not user data).
	Volumes []AppVolume `json:"volumes"`
}

// AppVolume is one data location of an application.
type AppVolume struct {
	Service  string `json:"service"`
	Type     string `json:"type"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}

func volumesOf(cfg *Config) []AppVolume {
	out := []AppVolume{}
	seen := map[string]bool{}
	for _, s := range cfg.Services {
		for _, v := range s.Volumes {
			k := v.Type + "\x00" + v.Source
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, AppVolume{Service: s.Name, Type: v.Type, Source: v.Source, Target: v.Target, ReadOnly: v.ReadOnly})
		}
	}
	return out
}

// InstalledApps lists the installed applications ordered by name.
func (m *Module) InstalledApps(ctx context.Context) ([]InstalledApp, error) {
	list, err := m.store.list(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]InstalledApp, 0, len(list))
	for _, it := range list {
		out = append(out, InstalledApp{
			Slug: it.Slug, Name: it.Name, Version: it.Version,
			InstalledAt: it.InstalledAt, UpdatedAt: it.UpdatedAt,
			Images: it.Images, Volumes: volumesOf(it.Config),
		})
	}
	return out, nil
}

// AppConfig returns the resolved configuration of an installed application:
// services, images, ports, environment (including secrets), volumes and
// bind paths. It returns a 404 *httpx.Error when the application is not
// installed.
func (m *Module) AppConfig(ctx context.Context, slug string) (*Config, error) {
	it, err := m.installed(ctx, slug)
	if err != nil {
		return nil, err
	}
	return it.Config, nil
}

// AppRunning reports whether any container of the application is running,
// so that a caller can restore the previous state after a backup.
func (m *Module) AppRunning(ctx context.Context, slug string) (bool, error) {
	it, err := m.installed(ctx, slug)
	if err != nil {
		return false, err
	}
	cli, err := m.eng.ready(ctx)
	if err != nil {
		return false, err
	}
	list, err := m.eng.appContainers(ctx, cli, slug)
	if err != nil {
		return false, err
	}
	state, _ := DeriveState(it.Config, containerInfos(list))
	return state != StateStopped && state != StateMissing, nil
}

// StopApp stops every container of an installed application, dependents
// first. The action is recorded in the audit log under actor.
func (m *Module) StopApp(ctx context.Context, actor audit.Actor, slug string) error {
	_, err := m.lifecycle(ctx, slug, "stop")
	m.audit(actor, "apps.stop", slug, failureDetail(err), err == nil)
	return err
}

// StartApp starts every container of an installed application in
// dependency order.
func (m *Module) StartApp(ctx context.Context, actor audit.Actor, slug string) error {
	_, err := m.lifecycle(ctx, slug, "start")
	m.audit(actor, "apps.start", slug, failureDetail(err), err == nil)
	return err
}

// RecreateApp (re)creates an application from a stored configuration, as
// returned earlier by AppConfig: it validates the configuration, checks the
// host ports, pulls missing images, creates the network, the missing volumes
// and the containers, starts them and records the application as installed.
// Existing volumes are reused and never emptied, so restore the volume
// contents before calling it. If the application is currently installed its
// containers are replaced (and put back if the new ones fail to start).
//
// The configuration is not trusted: privileged mode, capabilities, devices,
// host networking and system mounts are only accepted when the application's
// current manifest declares them, and bind paths must lie inside the allowed
// roots. The services, image repositories, environment names and volumes
// must be those of the manifest; command, user, tmpfs, shm size and health
// check are taken from the manifest (see ConformToManifest). An application
// whose manifest is not in the catalog is refused. cfg is updated in place.
// The call blocks until the application runs.
func (m *Module) RecreateApp(ctx context.Context, actor audit.Actor, cfg *Config) error {
	err := m.recreateApp(ctx, cfg)
	slug := ""
	if cfg != nil {
		slug = cfg.Slug
	}
	m.audit(actor, "apps.restore", slug, failureDetail(err), err == nil)
	return err
}

func (m *Module) recreateApp(ctx context.Context, cfg *Config) error {
	if cfg == nil {
		return httpx.BadRequest("Uygulama yapılandırması boş.")
	}
	var man *Manifest
	if ValidSlug(cfg.Slug) {
		man = m.catalog.Get(cfg.Slug)
	}
	if err := ValidateConfig(cfg, man, m.roots()); err != nil {
		var ie *InputError
		if errors.As(err, &ie) {
			return httpx.NewError(http.StatusBadRequest, "invalid_config", ie.Message)
		}
		return httpx.Internal(err)
	}
	// Without the manifest nothing in the configuration can be verified,
	// so it is refused rather than trusted.
	if err := ConformToManifest(cfg, man); err != nil {
		return httpx.NewError(http.StatusBadRequest, "invalid_config", userMessage(err))
	}
	if err := checkProtectedMounts(cfg, protectedDirs(m.eng.dataDir)); err != nil {
		return httpx.NewError(http.StatusBadRequest, "invalid_config", userMessage(err))
	}
	cli, err := m.eng.ready(ctx)
	if err != nil {
		return err
	}
	release, err := m.lock(cfg.Slug, JobRestore)
	if err != nil {
		return err
	}
	defer release()

	conflicts, err := m.eng.checkPorts(ctx, cli, cfg)
	if err != nil {
		return err
	}
	if len(conflicts) > 0 {
		return portConflictError(conflicts)
	}
	images, err := m.eng.pullAll(ctx, cli, cfg, false, nopReporter{})
	if err != nil {
		return err
	}
	if err := m.eng.recreate(ctx, cli, cfg, m.roots(), shortTag(jobTag(nopReporter{})), nopReporter{}); err != nil {
		return err
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := m.store.save(sctx, cfg, InputsFromConfig(cfg, man), images); err != nil {
		return httpx.Internal(err)
	}
	if err := m.store.dropRetained(sctx, cfg.Slug); err != nil {
		return httpx.Internal(err)
	}
	return nil
}

func failureDetail(err error) string {
	if err == nil {
		return ""
	}
	return userMessage(err)
}
