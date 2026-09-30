// Package backup makes, verifies and restores application-level backups:
// the configuration of an installed application together with the contents
// of its Docker volumes and host folders, in one archive per backup.
package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"time"

	"myserver/internal/apps"
	"myserver/internal/audit"
	"myserver/internal/auth"
	"myserver/internal/httpx"
	"myserver/internal/module"
	"myserver/internal/notify"
	"myserver/internal/settings"
)

// Settings keys.
const (
	KeyDefaultRetention   = "backup.default_retention"
	KeyDefaultConsistency = "backup.default_consistency"
	KeyDefaultBinds       = "backup.default_include_binds"
	KeyMinFreeGB          = "backup.min_free_gb"
)

// AppsAPI is the part of the application module the backup module uses.
// *apps.Module implements it.
type AppsAPI interface {
	InstalledApps(ctx context.Context) ([]apps.InstalledApp, error)
	AppConfig(ctx context.Context, slug string) (*apps.Config, error)
	AppRunning(ctx context.Context, slug string) (bool, error)
	StopApp(ctx context.Context, actor audit.Actor, slug string) error
	StartApp(ctx context.Context, actor audit.Actor, slug string) error
	RecreateApp(ctx context.Context, actor audit.Actor, cfg *apps.Config) error
}

const appsUnavailableMsg = "Uygulama modülü kullanılamıyor. Yedekleme ve geri yükleme işlemleri yapılamıyor."

var errAppsUnavailable = httpx.Unavailable("apps_unavailable", appsUnavailableMsg)

var schedulerActor = audit.Actor{Username: "zamanlayıcı", IP: "-"}

type Module struct {
	deps  module.Deps
	store *store
	jobs  *jobManager
	eng   *engine
	keys  *keyring
	dir   string

	mu   sync.RWMutex
	apps AppsAPI
	base context.Context

	// clock is the time source of the scheduler; tests replace it.
	clock func() time.Time

	healthMu sync.Mutex
	healthAt time.Time
	health   []module.HealthCheck
}

func New(deps module.Deps, st *settings.API) (module.Module, error) {
	settings.RegisterDefault(KeyDefaultRetention, "7")
	settings.RegisterDefault(KeyDefaultConsistency, consistencyStopped)
	settings.RegisterDefault(KeyDefaultBinds, "true")
	settings.RegisterDefault(KeyMinFreeGB, "5")
	if st != nil {
		st.Allow(KeyDefaultRetention, settings.IntRange(1, 365), nil)
		st.Allow(KeyDefaultConsistency, settings.OneOf(consistencyStopped, consistencyLive), nil)
		st.Allow(KeyDefaultBinds, settings.Bool, nil)
		st.Allow(KeyMinFreeGB, settings.IntRange(1, 100000), nil)
	}
	if deps.Cfg == nil {
		return nil, errors.New("backup: configuration is missing")
	}
	return &Module{
		deps:  deps,
		store: &store{db: deps.DB},
		jobs:  newJobManager(),
		eng:   &engine{host: deps.Cfg.DockerHost},
		keys:  newKeyring(deps.Cfg.DataDir),
		dir:   deps.Cfg.BackupDir(),
		base:  context.Background(),
	}, nil
}

func (m *Module) Name() string { return "backup" }

func (m *Module) now() time.Time {
	if m.clock != nil {
		return m.clock()
	}
	return time.Now()
}

// SetApps connects the application module. The core calls it after both
// modules were constructed; until then (or when it is given nil) every
// operation that needs applications fails with a clear message.
func (m *Module) SetApps(a AppsAPI) {
	if a != nil {
		if v := reflect.ValueOf(a); v.Kind() == reflect.Pointer && v.IsNil() {
			a = nil
		}
	}
	m.mu.Lock()
	m.apps = a
	m.mu.Unlock()
}

func (m *Module) appsAPI() (AppsAPI, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.apps == nil {
		return nil, errAppsUnavailable
	}
	return m.apps, nil
}

func (m *Module) baseCtx() context.Context {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.base
}

func (m *Module) Register(api, _ *httpx.Router) {
	a := api.Group("/backup", auth.RequireAdmin)
	a.Get("/overview", m.handleOverview)
	a.Get("/backups", m.handleList)
	a.Post("/backups", m.handleCreate)
	a.Delete("/backups/{id}", m.handleDelete)
	a.Get("/backups/{id}/download", m.handleDownload)
	a.Post("/backups/{id}/verify", m.handleVerify)
	a.Post("/backups/{id}/restore", m.handleRestore)
	a.Post("/upload", m.handleUpload)
	a.Post("/imports", m.handleImport)
	a.Get("/jobs", m.handleJobs)
	a.Get("/jobs/{id}", m.handleJob)
	a.Post("/jobs/{id}/cancel", m.handleJobCancel)
	a.Get("/stream", m.handleStream)
	a.Get("/schedules/{slug}", m.handleScheduleGet)
	a.Put("/schedules/{slug}", m.handleSchedulePut)
	a.Get("/encryption", m.handleEncryptionGet)
	a.Put("/encryption", m.handleEncryptionSet)
	a.Delete("/encryption", m.handleEncryptionClear)
}

/* ---------- paths ---------- */

func (m *Module) appDir(slug string) string   { return filepath.Join(m.dir, slug) }
func (m *Module) incomingDir() string         { return filepath.Join(m.dir, ".incoming") }
func (m *Module) stagedPath(id string) string { return filepath.Join(m.incomingDir(), id+".upload") }

// recordPath returns the archive of a record after checking that the stored
// names cannot point outside the backup directory.
func (m *Module) recordPath(r *Record) (string, error) {
	if !apps.ValidSlug(r.Slug) || !fileNameRe.MatchString(r.FileName) || filepath.Base(r.FileName) != r.FileName {
		return "", httpx.NotFound("Bu yedeğin dosyası yok.")
	}
	return filepath.Join(m.appDir(r.Slug), r.FileName), nil
}

func (m *Module) ensureDirs(slug string) error {
	for _, d := range []string{m.dir, m.appDir(slug)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return userErr("Yedek klasörü oluşturulamadı: "+d, err)
		}
	}
	return nil
}

/* ---------- errors ---------- */

// messageOf returns the Turkish message of an error for job results,
// notifications and the audit log. It never contains internal details.
func messageOf(err error) string {
	var he *httpx.Error
	var ue *userError
	var ae *archiveError
	var se *unsafeEntryError
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errPassphraseRequired):
		return "Bu yedek şifreli. Açmak için yedeğin alındığı sırada geçerli olan parolayı girin."
	case errors.Is(err, errWrongPassphrase):
		return "Parola yanlış: yedek bu parolayla açılamıyor."
	case errors.Is(err, errEncHeader):
		return "Şifreli yedeğin başlığı geçersiz; dosya bozuk veya bir MyServer yedeği değil."
	case errors.As(err, &ue):
		return ue.Message
	case errors.As(err, &ae):
		return ae.Message
	case errors.As(err, &se):
		return "Yedek dosyası güvenli olmayan bir kayıt içeriyor (" + se.Reason + "). İşlem reddedildi."
	case errors.Is(err, errEncCorrupt):
		return "Şifreli yedek bozuk, eksik veya değiştirilmiş; doğrulama başarısız."
	case errors.Is(err, errCorruptTar):
		return "Yedekteki veri akışı bozuk."
	case errors.As(err, &he):
		return he.Message
	case errors.Is(err, context.Canceled):
		return "İşlem iptal edildi."
	case errors.Is(err, context.DeadlineExceeded):
		return "İşlem zaman aşımına uğradı."
	case errors.Is(err, syscall.ENOSPC):
		return "Diskte yeterli boş alan yok."
	}
	return "Beklenmeyen bir hata oluştu. Ayrıntılar panel loglarında."
}

// httpError converts an error into an API error.
func httpError(err error) error {
	var he *httpx.Error
	var ae *archiveError
	var se *unsafeEntryError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &he):
		return err
	case errors.Is(err, errPassphraseRequired):
		return httpx.NewError(http.StatusBadRequest, "passphrase_required", messageOf(err))
	case errors.Is(err, errWrongPassphrase):
		return httpx.NewError(http.StatusBadRequest, "wrong_passphrase", messageOf(err))
	case errors.As(err, &ae), errors.As(err, &se), errors.Is(err, errEncHeader),
		errors.Is(err, errEncCorrupt), errors.Is(err, errCorruptTar):
		return httpx.NewError(http.StatusBadRequest, "invalid_backup", messageOf(err)).Wrap(err)
	}
	var ue *userError
	if errors.As(err, &ue) {
		return httpx.NewError(http.StatusBadGateway, "backup_error", ue.Message).Wrap(err)
	}
	return httpx.Internal(err)
}

func isNotFound(err error) bool {
	var he *httpx.Error
	return errors.As(err, &he) && he.Status == http.StatusNotFound
}

/* ---------- jobs ---------- */

const busyMsg = "Bu uygulama için başka bir yedekleme veya geri yükleme işlemi sürüyor. Bitmesini bekleyin."

// launch registers a job under the lock key and runs fn in the background.
func (m *Module) launch(key string, j *Job, fn func(ctx context.Context, j *Job) error) (JobView, error) {
	ctx, release, ok := m.jobs.create(m.baseCtx(), key, j)
	if !ok {
		return JobView{}, httpx.Conflict(busyMsg)
	}
	go m.runJob(ctx, release, j, fn)
	return j.view(), nil
}

func (m *Module) runJob(ctx context.Context, release func(), j *Job, fn func(ctx context.Context, j *Job) error) {
	defer release()
	var err error
	func() {
		defer func() {
			if v := recover(); v != nil {
				err = fmt.Errorf("panic: %v", v)
			}
		}()
		err = fn(ctx, j)
	}()
	switch {
	case err == nil:
		j.finish(statusSuccess, "")
	case errors.Is(err, context.Canceled):
		j.finish(statusCancelled, messageOf(err))
	default:
		slog.Error("yedekleme işi başarısız", "kind", j.Kind, "slug", j.Slug, "error", err.Error())
		j.finish(statusFailed, messageOf(err))
	}
}

/* ---------- background ---------- */

// Start cleans up after an interrupted run and then runs the scheduler
// until ctx is cancelled.
func (m *Module) Start(ctx context.Context) {
	m.mu.Lock()
	m.base = ctx
	m.mu.Unlock()

	m.recoverState(ctx)

	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	lastSweep := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		m.runSchedules(ctx)
		if time.Since(lastSweep) >= time.Hour {
			lastSweep = time.Now()
			m.sweepIncoming(6 * time.Hour)
		}
	}
}

// recoverState repairs what a crash or restart in the middle of an operation
// left behind: helper containers, temporary files, records that still say
// "running", and applications that a backup had stopped.
func (m *Module) recoverState(ctx context.Context) {
	m.eng.removeLeftoverHelpers(ctx)
	m.sweepIncoming(0)
	if entries, err := os.ReadDir(m.dir); err == nil {
		for _, e := range entries {
			if !e.IsDir() || !apps.ValidSlug(e.Name()) {
				continue
			}
			files, err := os.ReadDir(m.appDir(e.Name()))
			if err != nil {
				continue
			}
			for _, f := range files {
				if tmpNameRe.MatchString(f.Name()) {
					_ = os.Remove(filepath.Join(m.appDir(e.Name()), f.Name()))
				}
			}
		}
	}
	if m.deps.DB == nil {
		return
	}
	stale, err := m.store.query(ctx, `status = 'running'`)
	if err != nil {
		slog.Error("yarım kalan yedekler okunamadı", "error", err.Error())
		return
	}
	for _, r := range stale {
		restart := r.AppWasRunning
		r.Status = statusFailed
		r.Error = "Panel yeniden başlatıldığı için yedekleme yarıda kaldı."
		r.FileName, r.Size, r.AppWasRunning = "", 0, false
		if err := m.store.finish(ctx, r); err != nil {
			slog.Error("yarım kalan yedek kaydı güncellenemedi", "id", r.ID, "error", err.Error())
		}
		m.deps.Notify.Publish(ctx, notify.Error, "backup", "Yedekleme başarısız",
			r.AppName+": "+r.Error)
		if !restart {
			continue
		}
		// The backup had stopped the application; start it again.
		go m.restartAfterCrash(ctx, r.Slug, r.AppName)
	}
}

func (m *Module) restartAfterCrash(ctx context.Context, slug, name string) {
	// The application module may be connected shortly after Start.
	var api AppsAPI
	for i := 0; i < 30; i++ {
		var err error
		if api, err = m.appsAPI(); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	if api == nil {
		m.deps.Notify.Publish(ctx, notify.Error, "backup", "Uygulama başlatılamadı",
			name+" yarıda kalan bir yedekleme için durdurulmuştu ve yeniden başlatılamadı. Uygulamalar sayfasından başlatın.")
		return
	}
	sctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := api.StartApp(sctx, schedulerActor, slug); err != nil {
		m.deps.Notify.Publish(ctx, notify.Error, "backup", "Uygulama başlatılamadı",
			name+" yarıda kalan bir yedekleme için durdurulmuştu ve yeniden başlatılamadı: "+messageOf(err))
		return
	}
	m.deps.Notify.Publish(ctx, notify.Info, "backup", "Uygulama yeniden başlatıldı",
		name+" yarıda kalan bir yedekleme için durdurulmuştu; yeniden başlatıldı.")
}

// sweepIncoming deletes uploaded files that were never imported.
func (m *Module) sweepIncoming(olderThan time.Duration) {
	entries, err := os.ReadDir(m.incomingDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		id := trimUploadSuffix(e.Name())
		if id != "" && m.jobs.busy("import:"+id) != "" {
			continue
		}
		if time.Since(fi.ModTime()) >= olderThan {
			_ = os.Remove(filepath.Join(m.incomingDir(), e.Name()))
		}
	}
}

/* ---------- health ---------- */

func (m *Module) Health(ctx context.Context) []module.HealthCheck {
	m.healthMu.Lock()
	defer m.healthMu.Unlock()
	if m.health != nil && time.Since(m.healthAt) < time.Minute {
		return m.health
	}
	out := []module.HealthCheck{}

	sched := module.HealthCheck{ID: "backup.scheduled", Name: "Zamanlanmış yedekler", Status: module.Healthy,
		Message: "Son zamanlanmış yedeklemeler başarılı."}
	if m.deps.DB != nil {
		qctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		last, err := m.store.lastScheduled(qctx)
		cancel()
		switch {
		case err != nil:
			sched.Status, sched.Message = module.WarningLevel, "Yedek kayıtları okunamadı."
		case len(last) == 0:
			sched.Message = "Henüz zamanlanmış yedekleme çalışmadı."
		default:
			failed := ""
			n := 0
			for _, r := range last {
				if r.Status == statusSuccess {
					continue
				}
				// Only applications that still have an active
				// schedule matter.
				if sc, err := m.store.schedule(ctx, r.Slug); err != nil || sc == nil || !sc.Enabled {
					continue
				}
				if n > 0 {
					failed += ", "
				}
				failed += r.AppName
				n++
			}
			if n > 0 {
				sched.Status = module.WarningLevel
				sched.Message = "Son zamanlanmış yedekleme başarısız oldu: " + failed + "."
			}
		}
	}
	out = append(out, sched)

	space := module.HealthCheck{ID: "backup.space", Name: "Yedek klasörü boş alanı", Status: module.Healthy}
	probe := m.dir
	if _, err := os.Stat(probe); err != nil {
		probe = m.deps.Cfg.DataDir
	}
	if free, total, err := diskSpace(probe); err != nil {
		space.Message = "Yedek klasörünün boş alanı okunamadı."
		space.Status = module.WarningLevel
	} else {
		minFree := uint64(m.deps.Settings.Int(KeyMinFreeGB, 5)) << 30
		space.Message = fmt.Sprintf("Yedek klasöründe %s boş alan var.", humanBytes(int64(free)))
		if free < minFree || (total > 0 && free*100/total < 5) {
			space.Status = module.WarningLevel
			space.Message = fmt.Sprintf("Yedek klasöründe yalnızca %s boş alan kaldı.", humanBytes(int64(free)))
		}
	}
	out = append(out, space)

	if _, _, err := m.keys.current(); err != nil {
		out = append(out, module.HealthCheck{ID: "backup.key", Name: "Yedek şifreleme anahtarı",
			Status:  module.WarningLevel,
			Message: "Şifreleme anahtarı dosyası okunamıyor; yedekleme yapılamaz. Ayarlardan parolayı yeniden belirleyin."})
	}
	m.health, m.healthAt = out, time.Now()
	return out
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %s", float64(n)/float64(div), []string{"KB", "MB", "GB", "TB", "PB"}[exp])
}
