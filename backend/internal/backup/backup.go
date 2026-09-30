package backup

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/docker/docker/client"

	"myserver/internal/apps"
	"myserver/internal/audit"
	"myserver/internal/config"
	"myserver/internal/notify"
)

var (
	fileNameRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?-[0-9]{8}T[0-9]{6}Z(?:-[0-9]{1,4})?\.tar\.gz(?:\.enc)?$`)
	tmpNameRe  = regexp.MustCompile(`^\..+\.tmp$`)
	uploadIDRe = regexp.MustCompile(`^[a-f0-9]{24}$`)
)

func trimUploadSuffix(name string) string {
	id := strings.TrimSuffix(name, ".upload")
	if id == name || !uploadIDRe.MatchString(id) {
		return ""
	}
	return id
}

type backupOptions struct {
	Slug         string
	Live         bool
	IncludeBinds bool
	Trigger      string
	Actor        audit.Actor
	// assumeStopped is set for the safety backup taken inside a restore:
	// the restore already stopped the application and keeps it stopped.
	assumeStopped bool
}

// source is one data location of an application.
type source struct {
	Kind     string
	Source   string
	Service  string
	Target   string
	Key      string
	ReadOnly bool
	Image    string
}

// collectSources lists the volumes and host folders of a configuration,
// without duplicates. System mounts are not application data and are left
// out.
func collectSources(cfg *apps.Config, includeBinds bool) []source {
	out := []source{}
	seen := map[string]bool{}
	for _, s := range cfg.Services {
		for _, v := range s.Volumes {
			kind := ""
			switch v.Type {
			case apps.VolumeNamed:
				kind = kindVolume
			case apps.VolumeBind:
				if !includeBinds {
					continue
				}
				kind = kindBind
			default:
				continue
			}
			k := kind + "\x00" + v.Source
			if seen[k] || v.Source == "" {
				continue
			}
			seen[k] = true
			out = append(out, source{
				Kind: kind, Source: v.Source, Service: s.Name, Target: v.Target, Key: v.Key,
				ReadOnly: v.ReadOnly, Image: s.Image,
			})
		}
	}
	return out
}

// findAppManifest returns the catalog manifest file of an application, or
// nil. It is stored in the backup for reference only.
func (m *Module) findAppManifest(slug string) []byte {
	dir := m.deps.Cfg.ManifestDir
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if e.IsDir() || (ext != ".yaml" && ext != ".yml") {
			continue
		}
		fi, err := e.Info()
		if err != nil || fi.Size() > 256<<10 {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if man, err := apps.Parse(b); err == nil && man.Slug == slug {
			return b
		}
	}
	return nil
}

// uniqueName returns a file name that neither exists on disk nor in the
// database.
func (m *Module) uniqueName(ctx context.Context, slug string, at time.Time, encrypted bool) (string, error) {
	ext := ".tar.gz"
	if encrypted {
		ext += ".enc"
	}
	stem := slug + "-" + at.UTC().Format("20060102T150405Z")
	for i := 1; i < 10000; i++ {
		name := stem + ext
		if i > 1 {
			name = fmt.Sprintf("%s-%d%s", stem, i, ext)
		}
		taken, err := m.store.fileNameTaken(ctx, slug, name)
		if err != nil {
			return "", err
		}
		if _, err := os.Lstat(filepath.Join(m.appDir(slug), name)); !taken && errors.Is(err, fs.ErrNotExist) {
			return name, nil
		}
	}
	return "", errors.New("backup: no free file name")
}

// startBackup starts a backup job.
func (m *Module) startBackup(ctx context.Context, opts backupOptions) (JobView, error) {
	api, err := m.appsAPI()
	if err != nil {
		return JobView{}, err
	}
	cfg, err := api.AppConfig(ctx, opts.Slug)
	if err != nil {
		return JobView{}, err
	}
	j := &Job{Kind: jobBackup, Slug: opts.Slug, Name: cfg.Name, Trigger: opts.Trigger, Actor: opts.Actor, cancellable: true}
	return m.launch(opts.Slug, j, func(ctx context.Context, j *Job) error {
		_, err := m.runBackup(ctx, j, opts)
		return err
	})
}

// runBackup makes one backup and records its outcome in the database, the
// audit log and the notification center.
func (m *Module) runBackup(ctx context.Context, j *Job, opts backupOptions) (*Record, error) {
	start := time.Now()
	api, err := m.appsAPI()
	if err != nil {
		return nil, err
	}
	cfg, err := api.AppConfig(ctx, opts.Slug)
	if err != nil {
		return nil, err
	}
	rec := &Record{
		Slug: opts.Slug, AppName: cfg.Name, AppVersion: cfg.Version, CreatedAt: start.Unix(),
		Status: statusRunning, Consistency: consistencyStopped, IncludesBinds: opts.IncludeBinds,
		Trigger: opts.Trigger, Warnings: []string{},
	}
	if opts.Live {
		rec.Consistency = consistencyLive
	}
	rec.ID, err = m.store.insert(ctx, rec)
	if err != nil {
		return nil, err
	}
	if opts.Trigger != triggerSafety {
		j.setBackupID(rec.ID)
	}
	err = m.doBackup(ctx, j, api, cfg, rec, opts, start)
	m.concludeBackup(j, rec, opts, start, err)
	if err != nil {
		return rec, err
	}
	return rec, nil
}

func (m *Module) concludeBackup(j *Job, rec *Record, opts backupOptions, start time.Time, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rec.Duration = int64(time.Since(start).Seconds())
	switch {
	case err == nil:
		rec.Status = statusSuccess
	case errors.Is(err, context.Canceled):
		rec.Status, rec.Error = statusCancelled, messageOf(err)
	default:
		rec.Status, rec.Error = statusFailed, messageOf(err)
	}
	if err != nil {
		rec.FileName, rec.Size = "", 0
	}
	if derr := m.store.finish(ctx, rec); derr != nil {
		slog.Error("yedek kaydı güncellenemedi", "id", rec.ID, "error", derr.Error())
	}
	detail := rec.FileName
	if err != nil {
		detail = rec.Error
	}
	m.deps.Audit.Log(ctx, opts.Actor, "backup.create", rec.Slug, detail, err == nil)
	switch {
	case rec.Status == statusFailed:
		slog.Error("yedekleme başarısız", "slug", rec.Slug, "trigger", rec.Trigger, "error", err.Error())
		m.deps.Notify.Publish(ctx, notify.Error, "backup", "Yedekleme başarısız", rec.AppName+": "+rec.Error)
	case rec.Status == statusSuccess && opts.Trigger == triggerScheduled:
		msg := fmt.Sprintf("%s yedeklendi (%s).", rec.AppName, humanBytes(rec.Size))
		sev := notify.Success
		if len(rec.Warnings) > 0 {
			msg += " Uyarılar var; ayrıntılar Yedekleme sayfasında."
			sev = notify.Warning
		}
		m.deps.Notify.Publish(ctx, sev, "backup", "Zamanlanmış yedekleme tamamlandı", msg)
	}
	m.invalidateHealth()
}

func (m *Module) invalidateHealth() {
	m.healthMu.Lock()
	m.health = nil
	m.healthMu.Unlock()
}

func (m *Module) doBackup(ctx context.Context, j *Job, api AppsAPI, cfg *apps.Config, rec *Record, opts backupOptions, start time.Time) (err error) {
	warn := func(msg string) {
		rec.Warnings = append(rec.Warnings, msg)
		j.Warn(msg)
	}
	j.Step("Yedekleme hazırlanıyor")

	// Never fall back to an unencrypted archive when the key cannot be
	// read: the administrator expects encryption.
	km, _, err := m.keys.current()
	if err != nil {
		return userErr("Şifreleme anahtarı okunamadı; şifresiz yedek alınmadı. Ayarlardan parolayı yeniden belirleyin.", err)
	}
	rec.Encrypted = km != nil
	if err := m.ensureDirs(opts.Slug); err != nil {
		return err
	}

	sources := collectSources(cfg, opts.IncludeBinds)
	var cli *client.Client
	for _, s := range sources {
		if s.Kind == kindVolume {
			if cli, err = m.eng.ready(ctx); err != nil {
				return err
			}
			break
		}
	}

	// Leave out what does not exist, and say so.
	present := sources[:0]
	for _, s := range sources {
		switch s.Kind {
		case kindVolume:
			ok, err := volumeExists(ctx, cli, s.Source)
			if err != nil {
				return err
			}
			if !ok {
				warn("Veri birimi bulunamadı, yedeğe alınmadı: " + s.Source)
				continue
			}
		case kindBind:
			fi, err := os.Stat(s.Source)
			if err != nil || !fi.IsDir() {
				warn("Klasör bulunamadı, yedeğe alınmadı: " + s.Source)
				continue
			}
		}
		present = append(present, s)
	}
	sources = present

	// Free space.
	j.Step("Veri boyutu hesaplanıyor")
	var estimate int64
	known := true
	var sizes map[string]int64
	for _, s := range sources {
		switch s.Kind {
		case kindVolume:
			if sizes == nil {
				sizes = volumeSizes(ctx, cli)
			}
			if n, ok := sizes[s.Source]; ok {
				estimate += n
			} else {
				known = false
			}
		case kindBind:
			n, err := bindSize(ctx, s.Source)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				known = false
			}
			estimate += n
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if free, _, err := diskSpace(m.dir); err == nil {
		const reserve = 64 << 20
		switch {
		case free < reserve:
			return userErr(fmt.Sprintf("Yedek klasöründe yeterli boş alan yok (%s boş).", humanBytes(int64(free))), nil)
		case uint64(estimate)/3+reserve > free:
			return userErr(fmt.Sprintf("Yedek klasöründe yeterli boş alan yok: yaklaşık %s veri yedeklenecek, %s boş alan var.",
				humanBytes(estimate), humanBytes(int64(free))), nil)
		case uint64(estimate) > free:
			j.Warn("Boş alan, yedeklenecek verinin sıkıştırılmamış boyutundan az. Veri yeterince sıkışmazsa yedekleme yarıda kalır.")
		}
	} else {
		j.Warn("Yedek klasörünün boş alanı okunamadı; alan denetimi yapılamadı.")
	}
	if !known {
		estimate = 0
	}
	j.resetProgress(estimate)

	// Consistency.
	running := false
	if !opts.assumeStopped {
		if running, err = api.AppRunning(ctx, opts.Slug); err != nil {
			return err
		}
	}
	stopped := false
	switch {
	case running && !opts.Live:
		j.Step("Uygulama durduruluyor")
		rec.AppWasRunning = true
		if err := m.store.setAppWasRunning(ctx, rec.ID, true); err != nil {
			return err
		}
		stopped = true
		// Registered before stopping: a stop that fails half way may
		// already have stopped some containers.
		defer func() {
			j.Step("Uygulama yeniden başlatılıyor")
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
			defer cancel()
			if serr := api.StartApp(sctx, opts.Actor, opts.Slug); serr != nil {
				msg := "Uygulama yedeklemeden sonra yeniden başlatılamadı: " + messageOf(serr)
				warn(msg)
				slog.Error("uygulama yeniden başlatılamadı", "slug", opts.Slug, "error", serr.Error())
				m.deps.Notify.Publish(sctx, notify.Error, "backup", "Uygulama başlatılamadı", cfg.Name+": "+msg)
			}
			rec.AppWasRunning = false
		}()
		if err := api.StopApp(ctx, opts.Actor, opts.Slug); err != nil {
			return err
		}
		rec.Consistency = consistencyStopped
	case running && opts.Live:
		rec.Consistency = consistencyLive
		warn("Yedek, uygulama çalışırken alındı. Veritabanı dosyaları tutarsız bir anda kopyalanmış olabilir.")
	default:
		rec.Consistency = consistencyStopped
	}

	// Archive.
	name, err := m.uniqueName(ctx, opts.Slug, start, km != nil)
	if err != nil {
		return err
	}
	final := filepath.Join(m.appDir(opts.Slug), name)
	tmp := filepath.Join(m.appDir(opts.Slug), "."+name+".tmp")
	_ = os.Remove(tmp)
	aw, err := newArchiveWriter(tmp, km, start)
	if err != nil {
		return userErr("Yedek dosyası oluşturulamadı.", err)
	}
	done := false
	defer func() {
		if !done {
			aw.abort()
			_ = os.Remove(tmp)
		}
	}()

	man := &Manifest{
		Format: formatName, FormatVersion: formatVersion, CreatedAt: start.Unix(),
		PanelVersion: config.Version,
		App:          ManifestApp{Slug: cfg.Slug, Name: cfg.Name, Version: cfg.Version},
		Images:       map[string]apps.ImageInfo{},
		Trigger:      opts.Trigger, IncludesBinds: opts.IncludeBinds, AppStopped: stopped,
		Files: []FileEntry{}, Entries: []DataEntry{},
	}
	if list, err := api.InstalledApps(ctx); err == nil {
		for _, a := range list {
			if a.Slug == opts.Slug && a.Images != nil {
				man.Images = a.Images
			}
		}
	}
	cfgJSON, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	fe, err := aw.addFile(nameConfig, cfgJSON)
	if err != nil {
		return writeErr(err)
	}
	man.Files = append(man.Files, fe)
	if raw := m.findAppManifest(opts.Slug); raw != nil {
		fe, err := aw.addFile(nameAppManifest, raw)
		if err != nil {
			return writeErr(err)
		}
		man.Files = append(man.Files, fe)
	}

	for i, s := range sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		if i >= maxEntries {
			return userErr("Uygulamanın veri konumu sayısı desteklenen sınırı aşıyor.", nil)
		}
		base := dataArchiveName(i+1, s.Kind, path.Base(s.Source))
		pw := aw.stream(base)
		var st tarStats
		switch s.Kind {
		case kindVolume:
			j.Step("Veri birimi yedekleniyor: " + s.Source)
			st, err = exportVolume(ctx, cli, s.Image, s.Source, pw, j.progress)
		case kindBind:
			j.Step("Klasör yedekleniyor: " + s.Source)
			tw := tar.NewWriter(pw)
			var rep *bindReport
			st, rep, err = exportBind(ctx, s.Source, tw, j.progress)
			if err == nil {
				err = tw.Close()
			}
			for _, w := range rep.warnings(s.Source) {
				warn(w)
			}
		}
		if err != nil {
			return writeErr(err)
		}
		parts, size, sum, err := pw.close()
		if err != nil {
			return writeErr(err)
		}
		man.Entries = append(man.Entries, DataEntry{
			Archive: base, Kind: s.Kind, Source: s.Source, Service: s.Service, Target: s.Target,
			ReadOnly: s.ReadOnly, Parts: parts, Size: size, SHA256: sum,
			Files: st.Files, Items: st.Items, ContentBytes: st.Bytes,
		})
		j.Log(fmt.Sprintf("%s: %d dosya, %s", s.Source, st.Files, humanBytes(st.Bytes)))
	}

	// The application must still be stopped, otherwise the promise of a
	// consistent copy does not hold.
	if rec.Consistency == consistencyStopped && !opts.assumeStopped {
		if now, err := api.AppRunning(ctx, opts.Slug); err == nil && now {
			rec.Consistency = consistencyLive
			warn("Yedekleme sürerken uygulama başlatıldı. Veritabanı dosyaları tutarsız bir anda kopyalanmış olabilir.")
		}
	}
	man.Consistency = rec.Consistency
	man.Warnings = append([]string{}, rec.Warnings...)
	manJSON, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return err
	}
	if err := aw.writeEntry(nameManifest, manJSON); err != nil {
		return writeErr(err)
	}
	if err := aw.finish(); err != nil {
		return writeErr(err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		return userErr("Yedek dosyası yerine taşınamadı.", err)
	}
	done = true
	_ = os.Chmod(final, 0o600)
	rec.FileName = name
	rec.Size = aw.written()
	j.Log("Yedek dosyası yazıldı: " + name + " (" + humanBytes(rec.Size) + ")")
	return nil
}

// writeErr gives errors that occur while the archive is written a message.
func writeErr(err error) error {
	var ue *userError
	var se *unsafeEntryError
	switch {
	case errors.As(err, &ue), errors.As(err, &se),
		errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, errCorruptTar):
		return err
	}
	if msg := messageOf(err); !strings.HasPrefix(msg, "Beklenmeyen") {
		return userErr(msg, err)
	}
	return userErr("Yedek dosyası yazılamadı.", err)
}
