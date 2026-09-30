// Package services manages systemd service units: listing, state, guarded
// start/stop/enable/disable through the root helper, and journald logs.
package services

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os/exec"
	"sync"
	"time"

	"myserver/internal/auth"
	"myserver/internal/httpx"
	"myserver/internal/module"
	"myserver/internal/notify"
	"myserver/internal/privileged"
	"myserver/internal/services/servicescheck"
	"myserver/internal/settings"
)

// systemctlPath is a variable only so that tests can point it at a fake that
// prints fixtures; nothing else assigns it.
var systemctlPath = "/usr/bin/systemctl"

const (
	cacheTTL      = 3 * time.Second
	readTimeout   = 8 * time.Second
	actionTimeout = 100 * time.Second
	checkInterval = 60 * time.Second
	showChunk     = 300
	showProps     = "Id,Description,UnitFileState,MainPID,MemoryCurrent,ActiveEnterTimestamp,CanReload,TriggeredBy"
)

type snapshot struct {
	at       time.Time
	services []Service
	byUnit   map[string]Service
}

// Module is the services feature module.
type Module struct {
	deps module.Deps

	mu   sync.Mutex
	snap *snapshot

	failed    map[string]bool
	followers chan struct{}
}

func New(deps module.Deps, st *settings.API) (module.Module, error) {
	def, err := validateFeatured(mustJSON(defaultFeatured))
	if err != nil {
		return nil, err
	}
	settings.RegisterDefault(settingFeatured, def)
	st.Allow(settingFeatured, validateFeatured, nil)
	return &Module{
		deps:      deps,
		failed:    map[string]bool{},
		followers: make(chan struct{}, maxFollowers),
	}, nil
}

func (m *Module) Name() string { return "services" }

func (m *Module) Register(api, _ *httpx.Router) {
	g := api.Group("/services")
	g.Get("", m.handleList)
	g.Get("/featured", m.handleFeatured)

	a := api.Group("/services", auth.RequireAdmin)
	a.Get("/{unit}/logs", m.handleLogs)
	a.Get("/{unit}/logs/stream", m.handleLogStream)
	a.Post("/{unit}/{action}", m.handleAction)
}

// Start watches the featured services and notifies when one fails.
func (m *Module) Start(ctx context.Context) {
	t := time.NewTicker(checkInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.checkFeatured(ctx)
		}
	}
}

func (m *Module) checkFeatured(ctx context.Context) {
	snap, err := m.snapshot(ctx)
	if err != nil {
		return
	}
	now := map[string]bool{}
	for _, s := range resolveFeatured(m.deps.Settings.Strings(settingFeatured), snap.byUnit) {
		if s.State != StateFailed {
			continue
		}
		now[s.Unit] = true
		if !m.failed[s.Unit] {
			m.deps.Notify.PublishOnce(ctx, notify.Error, "services", "Servis hata verdi",
				s.Name+" ("+s.Unit+") servisi hata durumuna geçti. Ayrıntılar için servis loglarına bakın.",
				"services.failed."+s.Unit, time.Hour)
			slog.Warn("öne çıkan servis hata durumunda", "unit", s.Unit)
		}
	}
	m.failed = now
}

func runSystemctl(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, systemctlPath, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "LANG=C", "SYSTEMD_COLORS=0", "SYSTEMD_PAGER="}
	cmd.WaitDelay = 2 * time.Second
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.String(), err
}

// snapshot returns the cached service list, rebuilding it when stale. The
// lock is held while rebuilding so concurrent requests share one rebuild.
func (m *Module) snapshot(ctx context.Context) (*snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.snap != nil && time.Since(m.snap.at) < cacheTTL {
		return m.snap, nil
	}
	// A cancelled request must not abort a rebuild others will reuse.
	s, err := build(context.WithoutCancel(ctx))
	if err != nil {
		return nil, err
	}
	m.snap = s
	return s, nil
}

func (m *Module) invalidate() {
	m.mu.Lock()
	m.snap = nil
	m.mu.Unlock()
}

func build(ctx context.Context) (*snapshot, error) {
	unitsOut, err := runSystemctl(ctx, "list-units", "--type=service", "--all", "--no-legend", "--plain", "--no-pager")
	if err != nil {
		return nil, err
	}
	filesOut, err := runSystemctl(ctx, "list-unit-files", "--type=service", "--no-legend", "--plain", "--no-pager")
	if err != nil {
		slog.Warn("servis dosyası listesi alınamadı", "error", err.Error())
	}
	listed := parseListUnits(unitsOut)
	if len(listed) == 0 {
		return nil, errors.New("services: systemctl list-units returned no units")
	}
	var names []string
	for _, u := range listed {
		if u.Load != "not-found" && !servicescheck.IsTemplate(u.Unit) {
			names = append(names, u.Unit)
		}
	}
	details := map[string]map[string]string{}
	for i := 0; i < len(names); i += showChunk {
		chunk := names[i:min(i+showChunk, len(names))]
		base := []string{"show", "--no-pager", "--property=" + showProps}
		out, err := runSystemctl(ctx, append(append(append([]string{}, base...), "--timestamp=unix", "--"), chunk...)...)
		if err != nil && out == "" {
			// Older systemd without --timestamp.
			out, err = runSystemctl(ctx, append(append(append([]string{}, base...), "--"), chunk...)...)
		}
		if err != nil && out == "" {
			slog.Warn("servis ayrıntıları alınamadı", "error", err.Error())
			continue
		}
		for id, props := range parseShow(out) {
			details[id] = props
		}
	}
	list := assemble(listed, parseUnitFiles(filesOut), details, time.Local)
	byUnit := make(map[string]Service, len(list))
	for _, s := range list {
		byUnit[s.Unit] = s
	}
	return &snapshot{at: time.Now(), services: list, byUnit: byUnit}, nil
}

var errUnavailable = httpx.Unavailable("systemd_unavailable",
	"Servis listesi alınamadı: systemd servis yöneticisine ulaşılamıyor.")

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) error {
	snap, err := m.snapshot(r.Context())
	if err != nil {
		return errUnavailable.Wrap(err)
	}
	featured := resolveFeatured(m.deps.Settings.Strings(settingFeatured), snap.byUnit)
	mark := map[string]bool{}
	for _, f := range featured {
		mark[f.Unit] = true
	}
	all := make([]Service, len(snap.services))
	copy(all, snap.services)
	for i := range all {
		all[i].Featured = mark[all[i].Unit]
	}
	httpx.OK(w, map[string]any{
		"featured":   featured,
		"services":   all,
		"updated_at": snap.at.Unix(),
	})
	return nil
}

func (m *Module) handleFeatured(w http.ResponseWriter, r *http.Request) error {
	snap, err := m.snapshot(r.Context())
	if err != nil {
		return errUnavailable.Wrap(err)
	}
	httpx.OK(w, resolveFeatured(m.deps.Settings.Strings(settingFeatured), snap.byUnit))
	return nil
}

var verbFailed = map[string]string{
	servicescheck.VerbStart:   "Servis başlatılamadı.",
	servicescheck.VerbStop:    "Servis durdurulamadı.",
	servicescheck.VerbRestart: "Servis yeniden başlatılamadı.",
	servicescheck.VerbReload:  "Servis yapılandırması yeniden yüklenemedi.",
	servicescheck.VerbEnable:  "Servis açılışta başlayacak şekilde etkinleştirilemedi.",
	servicescheck.VerbDisable: "Servis devre dışı bırakılamadı.",
}

// riskMessage explains why an action needs an explicit acknowledgement.
func riskMessage(unit, kind, verb string) string {
	tail := " Devam etmek için servis adını (" + unit + ") yazarak onaylayın."
	switch kind {
	case servicescheck.KindSSH:
		return "SSH servisini durdurmak, devre dışı bırakmak veya yeniden başlatmak sunucuya uzaktan erişimi kesebilir. " +
			"Sunucuya fiziksel erişiminiz yoksa bu panel oturumu sunucuya ulaşmanın tek yolu olabilir." + tail
	case servicescheck.KindPanel:
		if verb == servicescheck.VerbRestart {
			return "MyServer paneli yeniden başlatılacak ve kısa bir süre kullanılamayacak." + tail
		}
		return "MyServer paneli durdurulursa veya devre dışı bırakılırsa panel kullanılamaz; yeniden açmak için SSH ya da konsol erişimi gerekir." + tail
	case servicescheck.KindNetwork:
		return "Bu servis sunucunun ağ bağlantısını veya ad çözümlemesini yönetir; işlem sunucuya erişimi kesebilir." + tail
	case servicescheck.KindContainer:
		return "Bu servis durursa çalışan tüm konteynerler ve onlara bağlı uygulamalar da durur." + tail
	default:
		return "Bu bir çekirdek sistem servisidir; işlem sistemin kararlılığını bozabilir." + tail
	}
}

func (m *Module) handleAction(w http.ResponseWriter, r *http.Request) error {
	unit, verb := r.PathValue("unit"), r.PathValue("action")
	if !servicescheck.ValidUnit(unit) || servicescheck.IsTemplate(unit) {
		return httpx.BadRequest("Servis adı geçersiz.")
	}
	if !servicescheck.ValidVerb(verb) {
		return httpx.BadRequest("Bilinmeyen servis işlemi.")
	}
	var req struct {
		Confirm string `json:"confirm"`
	}
	if r.ContentLength != 0 {
		if err := httpx.Decode(w, r, &req); err != nil {
			return err
		}
	}
	snap, err := m.snapshot(r.Context())
	if err != nil {
		return errUnavailable.Wrap(err)
	}
	svc, ok := snap.byUnit[unit]
	if !ok || !svc.Installed {
		return httpx.NotFound("Servis bu sunucuda kurulu değil: " + unit)
	}
	actor := auth.ActorFrom(r)
	action := "services." + verb

	if servicescheck.Denied(verb, unit) {
		m.deps.Audit.Log(r.Context(), actor, action, unit, "korumalı servis, reddedildi", false)
		return httpx.NewError(http.StatusForbidden, "protected_unit",
			"Bu çekirdek sistem servisi panelden durdurulamaz, devre dışı bırakılamaz veya yeniden başlatılamaz.")
	}
	if svc.State == StateMasked && verb != servicescheck.VerbStop && verb != servicescheck.VerbDisable {
		return httpx.Conflict("Servis maskelenmiş durumda. Panel maskeyi değiştirmez; önce sunucuda maskeyi kaldırın.")
	}
	if verb == servicescheck.VerbReload && !svc.CanReload {
		return httpx.BadRequest("Bu servis yapılandırmayı yeniden yüklemeyi desteklemiyor.")
	}
	if servicescheck.NeedsConfirm(verb, unit) && req.Confirm != unit {
		return httpx.NewError(http.StatusConflict, "confirmation_required", riskMessage(unit, svc.RiskKind, verb))
	}

	ctx, cancel := context.WithTimeout(r.Context(), actionTimeout)
	defer cancel()
	_, err = m.deps.Priv.Run(ctx, "services-control", verb, unit)
	m.invalidate()
	if err != nil {
		m.deps.Audit.Log(r.Context(), actor, action, unit, "başarısız", false)
		return httpx.NewError(http.StatusBadGateway, "service_action_failed",
			privileged.UserMessage(err, verbFailed[verb])).Wrap(err)
	}
	detail := ""
	if servicescheck.NeedsConfirm(verb, unit) {
		detail = "kritik servis, kullanıcı onayı alındı"
	}
	m.deps.Audit.Log(r.Context(), actor, action, unit, detail, true)

	// The panel is about to go away; do not spawn processes for a refresh.
	if unit == servicescheck.PanelUnit && servicescheck.Disruptive(verb) && verb != servicescheck.VerbDisable {
		httpx.OK(w, svc)
		return nil
	}
	if fresh, err := m.snapshot(r.Context()); err == nil {
		if s, ok := fresh.byUnit[unit]; ok {
			svc = s
		}
	}
	httpx.OK(w, svc)
	return nil
}
