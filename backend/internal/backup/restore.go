package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"myserver/internal/apps"
	"myserver/internal/audit"
	"myserver/internal/notify"
	"myserver/internal/settings"
)

var volumeSuffixRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)

// Preview tells the administrator what a backup contains and what
// restoring it would overwrite. It never contains environment values.
type Preview struct {
	Slug         string         `json:"slug"`
	AppName      string         `json:"app_name"`
	AppVersion   string         `json:"app_version"`
	CreatedAt    int64          `json:"created_at"`
	PanelVersion string         `json:"panel_version"`
	Consistency  string         `json:"consistency"`
	Encrypted    bool           `json:"encrypted"`
	Images       []string       `json:"images"`
	Entries      []PreviewEntry `json:"entries"`
	// Untouched lists data locations of the stored configuration that
	// have no data in the backup; a restore leaves them as they are.
	Untouched []string `json:"untouched"`
	Warnings  []string `json:"warnings"`
	// State of the server at the time of the verification.
	AppsAvailable    bool   `json:"apps_available"`
	AppInstalled     bool   `json:"app_installed"`
	AppRunning       bool   `json:"app_running"`
	InstalledVersion string `json:"installed_version"`
	SecretCount      int    `json:"secret_count"`
}

type PreviewEntry struct {
	Kind     string `json:"kind"`
	Source   string `json:"source"`
	Service  string `json:"service"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
	Files    int64  `json:"files"`
	Bytes    int64  `json:"bytes"`
	// Exists reports whether the volume or folder exists now, in which
	// case its contents are deleted and replaced.
	Exists bool `json:"exists"`
}

type progressReader struct {
	r  io.Reader
	fn func(int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.fn(int64(n))
	}
	return n, err
}

func (m *Module) roots() []string {
	return m.deps.Settings.Strings(settings.KeyAllowedRoots)
}

// validateBackup checks the manifest and configuration of a backup. Both
// come from a file that may have been uploaded or modified, so nothing in
// them is trusted: names and paths are checked here, and the configuration
// is checked again by the application module when the application is
// recreated.
func validateBackup(res *scanResult, roots []string) error {
	man, cfg := res.Manifest, res.Config
	if !apps.ValidSlug(man.App.Slug) || cfg.Slug != man.App.Slug {
		return badArchive("Yedekteki uygulama kimliği geçersiz.", nil)
	}
	if strings.TrimSpace(cfg.Name) == "" || len(cfg.Services) == 0 || len(cfg.Services) > 12 {
		return badArchive("Yedekteki uygulama yapılandırması geçersiz.", nil)
	}
	if man.Consistency != consistencyStopped && man.Consistency != consistencyLive {
		return badArchive("manifest.json geçersiz (tutarlılık bilgisi).", nil)
	}
	for _, s := range cfg.Services {
		if !apps.ValidImage(s.Image) {
			return badArchive("Yedekteki görüntü adı geçersiz: "+s.Name, nil)
		}
	}
	known := map[string]bool{}
	for _, s := range collectSources(cfg, true) {
		known[s.Kind+"\x00"+s.Source] = true
	}
	seen := map[string]bool{}
	for _, e := range man.Entries {
		k := e.Kind + "\x00" + e.Source
		if !known[k] || seen[k] {
			return badArchive("Yedekte, uygulama yapılandırmasında bulunmayan bir veri konumu var: "+e.Source, nil)
		}
		seen[k] = true
		switch e.Kind {
		case kindVolume:
			prefix := "myserver-" + cfg.Slug + "-"
			if !strings.HasPrefix(e.Source, prefix) || !volumeSuffixRe.MatchString(strings.TrimPrefix(e.Source, prefix)) {
				return badArchive("Yedekteki veri birimi adı geçersiz: "+e.Source, nil)
			}
		case kindBind:
			if err := apps.CheckBindPath(e.Source, roots); err != nil {
				var ie *apps.InputError
				if errors.As(err, &ie) {
					return badArchive("Yedekteki klasör bu sunucuda kullanılamıyor. "+ie.Message, nil)
				}
				return badArchive("Yedekteki klasör yolu geçersiz: "+e.Source, err)
			}
		}
	}
	return nil
}

// verifyFile reads a whole backup file: structure, decryption, checksums
// and every tar entry name.
func (m *Module) verifyFile(ctx context.Context, path, passphrase string, j *Job) (*scanResult, error) {
	km, _, _ := m.keys.current()
	ar, err := openArchive(path, km, passphrase)
	if err != nil {
		return nil, err
	}
	defer ar.close()
	j.resetProgress(0)
	res, err := scanArchive(ctx, ar, func(_ string, r io.Reader) error {
		_, err := readTar(ctx, &progressReader{r: r, fn: j.progress}, "", nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := validateBackup(res, m.roots()); err != nil {
		return nil, err
	}
	return res, nil
}

// checkPassphrase fails fast, before a job is started, when an encrypted
// backup cannot be opened with the stored key or the given passphrase.
func (m *Module) checkPassphrase(path, passphrase string) (encrypted bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return false, badArchive("Yedek dosyası açılamadı. Dosya silinmiş veya taşınmış olabilir.", err)
	}
	defer f.Close()
	head := make([]byte, len(encMagic))
	n, _ := io.ReadFull(f, head)
	if !isEncrypted(head[:n]) {
		return false, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return true, err
	}
	km, _, _ := m.keys.current()
	if _, err := newEncReader(f, km, passphrase); err != nil {
		return true, err
	}
	return true, nil
}

func (m *Module) preview(ctx context.Context, res *scanResult) *Preview {
	man, cfg := res.Manifest, res.Config
	p := &Preview{
		Slug: man.App.Slug, AppName: cfg.Name, AppVersion: cfg.Version, CreatedAt: man.CreatedAt,
		PanelVersion: man.PanelVersion, Consistency: man.Consistency, Encrypted: res.Encrypted,
		Images: []string{}, Entries: []PreviewEntry{}, Untouched: []string{}, Warnings: []string{},
	}
	for i, w := range man.Warnings {
		if i >= 50 {
			break
		}
		if len(w) > 500 {
			w = w[:500]
		}
		p.Warnings = append(p.Warnings, w)
	}
	seenImg := map[string]bool{}
	for _, s := range cfg.Services {
		if !seenImg[s.Image] {
			seenImg[s.Image] = true
			p.Images = append(p.Images, s.Image)
		}
		for _, e := range s.Env {
			if e.Secret {
				p.SecretCount++
			}
		}
	}
	cli, derr := m.eng.ready(ctx)
	inBackup := map[string]bool{}
	for _, e := range man.Entries {
		inBackup[e.Kind+"\x00"+e.Source] = true
		pe := PreviewEntry{Kind: e.Kind, Source: e.Source, Service: e.Service, Target: e.Target,
			ReadOnly: e.ReadOnly, Files: e.Files, Bytes: e.ContentBytes}
		switch e.Kind {
		case kindVolume:
			if derr == nil {
				pe.Exists, _ = volumeExists(ctx, cli, e.Source)
			}
		case kindBind:
			if fi, err := os.Stat(e.Source); err == nil && fi.IsDir() {
				pe.Exists = true
			}
		}
		p.Entries = append(p.Entries, pe)
	}
	for _, s := range collectSources(cfg, true) {
		if !inBackup[s.Kind+"\x00"+s.Source] {
			p.Untouched = append(p.Untouched, s.Source)
		}
	}
	if api, err := m.appsAPI(); err == nil {
		p.AppsAvailable = true
		if cur, err := api.AppConfig(ctx, p.Slug); err == nil {
			p.AppInstalled = true
			p.InstalledVersion = cur.Version
			p.AppRunning, _ = api.AppRunning(ctx, p.Slug)
		}
	}
	return p
}

/* ---------- verify ---------- */

func (m *Module) startVerify(rec *Record, path, passphrase string, actor audit.Actor) (JobView, error) {
	j := &Job{Kind: jobVerify, Slug: rec.Slug, Name: rec.AppName, Trigger: triggerManual, BackupID: rec.ID,
		Actor: actor, cancellable: true}
	return m.launch(rec.Slug, j, func(ctx context.Context, j *Job) error {
		j.Step("Yedek doğrulanıyor: " + rec.FileName)
		res, err := m.verifyFile(ctx, path, passphrase, j)
		m.recordVerification(rec.ID, err)
		m.deps.Audit.Log(ctx, actor, "backup.verify", rec.Slug, auditDetail(rec.FileName, err), err == nil)
		if err != nil {
			return err
		}
		j.Log("Dosya okunabilir, sağlama toplamları eşleşiyor.")
		j.setResult(m.preview(ctx, res))
		return nil
	})
}

func auditDetail(name string, err error) string {
	if err != nil {
		return name + ": " + messageOf(err)
	}
	return name
}

// recordVerification stores the result of a verification, unless the
// verification did not take place (cancelled, passphrase missing).
func (m *Module) recordVerification(id int64, err error) {
	if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, errPassphraseRequired) || errors.Is(err, errWrongPassphrase)) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if derr := m.store.setVerified(ctx, id, err == nil); derr != nil {
		slog.Error("doğrulama sonucu kaydedilemedi", "id", id, "error", derr.Error())
	}
}

/* ---------- restore ---------- */

type restoreOptions struct {
	Passphrase   string
	SafetyBackup bool
	RestoreBinds bool
	Actor        audit.Actor
}

// RestoreResult is the report of a finished restore.
type RestoreResult struct {
	Restored     []string `json:"restored"`
	Skipped      []string `json:"skipped"`
	Warnings     []string `json:"warnings"`
	SafetyBackup string   `json:"safety_backup"`
	SafetyID     int64    `json:"safety_backup_id"`
	WasInstalled bool     `json:"was_installed"`
}

func (m *Module) startRestore(rec *Record, path string, opts restoreOptions) (JobView, error) {
	if _, err := m.appsAPI(); err != nil {
		return JobView{}, err
	}
	j := &Job{Kind: jobRestore, Slug: rec.Slug, Name: rec.AppName, Trigger: triggerManual, BackupID: rec.ID,
		Actor: opts.Actor, cancellable: true}
	return m.launch(rec.Slug, j, func(ctx context.Context, j *Job) error {
		result := &RestoreResult{Restored: []string{}, Skipped: []string{}, Warnings: []string{}}
		err := m.runRestore(ctx, j, rec, path, opts, result)
		j.setResult(result)
		actx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		m.deps.Audit.Log(actx, opts.Actor, "backup.restore", rec.Slug, auditDetail(rec.FileName, err), err == nil)
		switch {
		case err == nil:
			m.deps.Notify.Publish(actx, notify.Success, "backup", "Geri yükleme tamamlandı",
				rec.AppName+" uygulaması "+rec.FileName+" yedeğinden geri yüklendi.")
		case !errors.Is(err, context.Canceled):
			m.deps.Notify.Publish(actx, notify.Error, "backup", "Geri yükleme başarısız",
				rec.AppName+": "+messageOf(err))
		}
		return err
	})
}

func (m *Module) runRestore(ctx context.Context, j *Job, rec *Record, path string, opts restoreOptions, result *RestoreResult) error {
	api, err := m.appsAPI()
	if err != nil {
		return err
	}
	warn := func(msg string) {
		result.Warnings = append(result.Warnings, msg)
		j.Warn(msg)
	}

	// 1. Verify everything before anything is touched.
	j.Step("Yedek doğrulanıyor")
	res, err := m.verifyFile(ctx, path, opts.Passphrase, j)
	m.recordVerification(rec.ID, err)
	if err != nil {
		return err
	}
	man, cfg := res.Manifest, res.Config
	slug := man.App.Slug
	if slug != rec.Slug {
		return badArchive("Yedek dosyası başka bir uygulamaya ait.", nil)
	}
	roots := m.roots()
	images := map[string]string{}
	for _, s := range collectSources(cfg, true) {
		images[s.Kind+"\x00"+s.Source] = s.Image
	}
	byArchive := map[string]DataEntry{}
	var total int64
	hasBinds := false
	for _, e := range man.Entries {
		byArchive[e.Archive] = e
		if e.Kind == kindBind {
			if !opts.RestoreBinds {
				continue
			}
			hasBinds = true
			if err := checkBindTarget(e.Source, roots); err != nil {
				return err
			}
		}
		total += e.ContentBytes
	}

	cli, err := m.eng.ready(ctx)
	if err != nil {
		return err
	}
	j.Step("Uygulama görüntüleri denetleniyor")
	for _, e := range man.Entries {
		if e.Kind != kindVolume {
			continue
		}
		// Refused here, while nothing has been changed yet, rather than
		// after the containers of the application were removed.
		if err := checkVolumeOwner(ctx, cli, e.Source, slug); err != nil {
			return err
		}
		if err := ensureImage(ctx, cli, images[e.Kind+"\x00"+e.Source]); err != nil {
			return err
		}
	}

	// 2. Current state.
	installed, wasRunning := false, false
	if _, err := api.AppConfig(ctx, slug); err == nil {
		installed = true
	} else if !isNotFound(err) {
		return err
	}
	result.WasInstalled = installed
	if installed {
		if wasRunning, err = api.AppRunning(ctx, slug); err != nil {
			return err
		}
	}
	restartOnAbort := false
	abort := func(err error) error {
		if restartOnAbort {
			j.Step("Uygulama yeniden başlatılıyor")
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
			defer cancel()
			if serr := api.StartApp(sctx, opts.Actor, slug); serr != nil {
				warn("Uygulama yeniden başlatılamadı: " + messageOf(serr))
			}
		}
		return err
	}
	if wasRunning {
		j.Step("Uygulama durduruluyor")
		restartOnAbort = true
		if err := api.StopApp(ctx, opts.Actor, slug); err != nil {
			return abort(err)
		}
	}

	// 3. Safety backup of the current state.
	if opts.SafetyBackup && installed {
		j.Step("Mevcut durumun güvenlik yedeği alınıyor")
		srec, err := m.runBackup(ctx, j, backupOptions{
			Slug: slug, IncludeBinds: hasBinds, Trigger: triggerSafety, Actor: opts.Actor, assumeStopped: true,
		})
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return abort(err)
			}
			return abort(userErr("Güvenlik yedeği alınamadı, bu yüzden geri yükleme başlatılmadı ve hiçbir veri değiştirilmedi. Neden: "+messageOf(err), err))
		}
		result.SafetyBackup, result.SafetyID = srec.FileName, srec.ID
		for _, w := range srec.Warnings {
			warn("Güvenlik yedeği: " + w)
		}
	} else if installed {
		warn("Güvenlik yedeği alınmadı; bu geri yükleme geri alınamaz.")
	}
	if err := ctx.Err(); err != nil {
		return abort(err)
	}

	// 4. Point of no return.
	j.setCancellable(false)
	ctx = context.WithoutCancel(ctx)
	j.Warn("Bu noktadan sonra işlem iptal edilemez: uygulamanın verilerinin üzerine yazılıyor.")
	midway := func(err error) error {
		msg := "Geri yükleme yarıda kaldı: " + messageOf(err) + " Uygulamanın verileri eksik olabilir ve uygulama çalışmıyor olabilir."
		if result.SafetyBackup != "" {
			msg += " Önceki duruma dönmek için şu güvenlik yedeğini geri yükleyin: " + result.SafetyBackup
		}
		return userErr(msg, err)
	}
	j.Step("Uygulamanın konteynerleri kaldırılıyor")
	if err := removeAppContainers(ctx, cli, slug); err != nil {
		return midway(err)
	}

	km, _, _ := m.keys.current()
	ar, err := openArchive(path, km, opts.Passphrase)
	if err != nil {
		return midway(err)
	}
	defer ar.close()
	j.resetProgress(total)
	_, err = scanArchive(ctx, ar, func(archive string, r io.Reader) error {
		e, ok := byArchive[archive]
		if !ok {
			return badArchive("Yedek dosyası doğrulamadan sonra değişti.", nil)
		}
		switch e.Kind {
		case kindVolume:
			j.Step("Veri birimi geri yükleniyor: " + e.Source)
			if err := resetVolume(ctx, cli, e.Source, slug); err != nil {
				return err
			}
			st, err := importVolume(ctx, cli, images[e.Kind+"\x00"+e.Source], e.Source, r, j.progress)
			if err != nil {
				return err
			}
			j.Log(fmt.Sprintf("%s: %d dosya, %s", e.Source, st.Files, humanBytes(st.Bytes)))
			result.Restored = append(result.Restored, e.Source)
		case kindBind:
			if !opts.RestoreBinds {
				result.Skipped = append(result.Skipped, e.Source)
				return nil
			}
			j.Step("Klasör geri yükleniyor: " + e.Source)
			st, rep, err := importBind(ctx, e.Source, roots, r, j.progress)
			for _, w := range rep.warnings(e.Source) {
				warn(w)
			}
			if err != nil {
				return err
			}
			j.Log(fmt.Sprintf("%s: %d dosya, %s", e.Source, st.Files, humanBytes(st.Bytes)))
			result.Restored = append(result.Restored, e.Source)
		}
		return nil
	})
	if err != nil {
		return midway(err)
	}

	// 5. Recreate and start.
	j.Step("Uygulama yedekteki yapılandırmayla yeniden oluşturuluyor ve başlatılıyor")
	if err := api.RecreateApp(ctx, opts.Actor, cfg); err != nil {
		msg := "Veriler geri yüklendi ancak uygulama yeniden oluşturulamadı: " + messageOf(err)
		if result.SafetyBackup != "" {
			msg += " Önceki duruma dönmek için şu güvenlik yedeğini geri yükleyin: " + result.SafetyBackup
		}
		return userErr(msg, err)
	}
	j.Log("Uygulama çalışıyor.")
	return nil
}

/* ---------- import ---------- */

func (m *Module) startImport(uploadID, passphrase string, actor audit.Actor) (JobView, error) {
	staged := m.stagedPath(uploadID)
	j := &Job{Kind: jobImport, Name: "İçe aktarma", Trigger: triggerImported, Actor: actor, cancellable: true}
	return m.launch("import:"+uploadID, j, func(ctx context.Context, j *Job) error {
		rec, err := m.runImport(ctx, j, staged, passphrase)
		actx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		target, detail := "", messageOf(err)
		if rec != nil {
			target, detail = rec.Slug, auditDetail(rec.FileName, err)
		}
		m.deps.Audit.Log(actx, actor, "backup.import", target, detail, err == nil)
		if err != nil && !errors.Is(err, errPassphraseRequired) && !errors.Is(err, errWrongPassphrase) {
			// A file that failed validation is not kept.
			_ = os.Remove(staged)
		}
		return err
	})
}

func (m *Module) runImport(ctx context.Context, j *Job, staged, passphrase string) (*Record, error) {
	j.Step("Yüklenen dosya doğrulanıyor")
	res, err := m.verifyFile(ctx, staged, passphrase, j)
	if err != nil {
		return nil, err
	}
	man := res.Manifest
	slug := man.App.Slug
	j.setSlug(slug, res.Config.Name)
	fi, err := os.Stat(staged)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	created := time.Unix(man.CreatedAt, 0)
	if man.CreatedAt <= 0 || created.After(now.Add(24*time.Hour)) {
		created = now
	}
	if err := m.ensureDirs(slug); err != nil {
		return nil, err
	}
	name, err := m.uniqueName(ctx, slug, created, res.Encrypted)
	if err != nil {
		return nil, err
	}
	rec := &Record{
		Slug: slug, AppName: res.Config.Name, AppVersion: res.Config.Version, FileName: name, Size: fi.Size(),
		CreatedAt: created.Unix(), Status: statusSuccess, Consistency: man.Consistency,
		Encrypted: res.Encrypted, IncludesBinds: man.IncludesBinds, Trigger: triggerImported,
		Warnings: []string{},
	}
	for i, w := range man.Warnings {
		if i >= 50 {
			break
		}
		if len(w) > 500 {
			w = w[:500]
		}
		rec.Warnings = append(rec.Warnings, w)
	}
	j.Step("Yedek, yedek klasörüne taşınıyor")
	final := filepath.Join(m.appDir(slug), name)
	if err := os.Rename(staged, final); err != nil {
		return rec, userErr("Yüklenen dosya yedek klasörüne taşınamadı.", err)
	}
	_ = os.Chmod(final, 0o600)
	rec.ID, err = m.store.insert(ctx, rec)
	if err != nil {
		_ = os.Remove(final)
		return rec, err
	}
	m.recordVerification(rec.ID, nil)
	j.setBackupID(rec.ID)
	j.setResult(m.preview(ctx, res))
	j.Log("Yedek içe aktarıldı: " + name)
	return rec, nil
}
