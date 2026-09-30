package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"myserver/internal/apps"
	"myserver/internal/auth"
	"myserver/internal/httpx"
)

/* ---------- overview and lists ---------- */

type appSummary struct {
	Slug        string           `json:"slug"`
	Name        string           `json:"name"`
	Version     string           `json:"version"`
	Volumes     []apps.AppVolume `json:"volumes"`
	BackupCount int              `json:"backup_count"`
	TotalSize   int64            `json:"total_size"`
	// LastBackup is the newest attempt (successful or not); LastSuccess
	// the newest backup that can be restored.
	LastBackup  *Record   `json:"last_backup"`
	LastSuccess *Record   `json:"last_success"`
	Schedule    *Schedule `json:"schedule"`
}

type encryptionInfo struct {
	Enabled bool   `json:"enabled"`
	SetAt   int64  `json:"set_at"`
	Cipher  string `json:"cipher"`
	KDF     string `json:"kdf"`
	KeyFile string `json:"key_file"`
	// Error is set when the key file exists but cannot be read.
	Error string `json:"error"`
}

type overview struct {
	AppsAvailable bool         `json:"apps_available"`
	AppsError     string       `json:"apps_error"`
	Apps          []appSummary `json:"apps"`
	// Orphans are applications that have backups but are not installed.
	Orphans    []appSummary   `json:"orphans"`
	Dir        string         `json:"dir"`
	FreeBytes  *int64         `json:"free_bytes"`
	TotalBytes *int64         `json:"total_bytes"`
	UsedBytes  int64          `json:"used_bytes"`
	Encryption encryptionInfo `json:"encryption"`
	Jobs       []JobView      `json:"jobs"`
}

func (m *Module) encryptionInfo() encryptionInfo {
	info := encryptionInfo{
		Cipher: "AES-256-GCM", KDF: "Argon2id", KeyFile: m.keys.path,
	}
	km, at, err := m.keys.current()
	if err != nil {
		info.Error = "Şifreleme anahtarı dosyası okunamıyor. Parolayı yeniden belirleyin."
		return info
	}
	info.Enabled, info.SetAt = km != nil, at
	return info
}

// markMissing flags records whose archive is no longer on disk.
func (m *Module) markMissing(recs []*Record) {
	for _, r := range recs {
		if r.Status != statusSuccess {
			continue
		}
		p, err := m.recordPath(r)
		if err != nil {
			r.FileMissing = true
			continue
		}
		if _, err := os.Stat(p); err != nil {
			r.FileMissing = true
		}
	}
}

func (m *Module) scheduleFor(ctx context.Context, slug string) (*Schedule, error) {
	sc, err := m.store.schedule(ctx, slug)
	if err != nil {
		return nil, err
	}
	if sc == nil {
		sc = &Schedule{
			Slug: slug, Frequency: freqDaily, Hour: 3, Minute: 0, Weekday: 1, Monthday: 1,
			KeepLast:     m.deps.Settings.Int(KeyDefaultRetention, 7),
			Live:         m.deps.Settings.Get(KeyDefaultConsistency) == consistencyLive,
			IncludeBinds: m.deps.Settings.Get(KeyDefaultBinds) != "false",
		}
	}
	if sc.Enabled {
		if t := nextDue(sc, m.now()); !t.IsZero() {
			sc.NextRunAt = t.Unix()
		}
	}
	return sc, nil
}

func (m *Module) handleOverview(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	out := overview{Apps: []appSummary{}, Orphans: []appSummary{}, Dir: m.dir,
		Encryption: m.encryptionInfo(), Jobs: m.jobs.list()}

	probe := m.dir
	if _, err := os.Stat(probe); err != nil {
		probe = m.deps.Cfg.DataDir
	}
	if free, total, err := diskSpace(probe); err == nil {
		f, t := int64(free), int64(total)
		out.FreeBytes, out.TotalBytes = &f, &t
	}

	recs, err := m.store.list(ctx, "")
	if err != nil {
		return httpx.Internal(err)
	}
	m.markMissing(recs)
	bySlug := map[string][]*Record{}
	for _, rec := range recs {
		bySlug[rec.Slug] = append(bySlug[rec.Slug], rec)
		if rec.Status == statusSuccess && !rec.FileMissing {
			out.UsedBytes += rec.Size
		}
	}
	summarize := func(s *appSummary) error {
		for _, rec := range bySlug[s.Slug] {
			if s.LastBackup == nil {
				s.LastBackup = rec
			}
			if rec.Status == statusSuccess && !rec.FileMissing {
				s.BackupCount++
				s.TotalSize += rec.Size
				if s.LastSuccess == nil {
					s.LastSuccess = rec
				}
			}
		}
		sc, err := m.scheduleFor(ctx, s.Slug)
		if err != nil {
			return httpx.Internal(err)
		}
		s.Schedule = sc
		return nil
	}

	installed := map[string]bool{}
	if api, err := m.appsAPI(); err != nil {
		out.AppsError = appsUnavailableMsg
	} else if list, err := api.InstalledApps(ctx); err != nil {
		out.AppsError = "Kurulu uygulamalar okunamadı: " + messageOf(err)
	} else {
		out.AppsAvailable = true
		for _, a := range list {
			installed[a.Slug] = true
			s := appSummary{Slug: a.Slug, Name: a.Name, Version: a.Version, Volumes: []apps.AppVolume{}}
			for _, v := range a.Volumes {
				if v.Type == apps.VolumeNamed || v.Type == apps.VolumeBind {
					s.Volumes = append(s.Volumes, v)
				}
			}
			if err := summarize(&s); err != nil {
				return err
			}
			out.Apps = append(out.Apps, s)
		}
	}
	if out.AppsAvailable {
		seen := map[string]bool{}
		for _, rec := range recs {
			if installed[rec.Slug] || seen[rec.Slug] {
				continue
			}
			seen[rec.Slug] = true
			s := appSummary{Slug: rec.Slug, Name: rec.AppName, Version: rec.AppVersion, Volumes: []apps.AppVolume{}}
			if err := summarize(&s); err != nil {
				return err
			}
			s.Schedule = nil
			out.Orphans = append(out.Orphans, s)
		}
	}
	httpx.OK(w, out)
	return nil
}

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) error {
	slug := r.URL.Query().Get("slug")
	if slug != "" && !apps.ValidSlug(slug) {
		return httpx.BadRequest("Uygulama kimliği geçersiz.")
	}
	recs, err := m.store.list(r.Context(), slug)
	if err != nil {
		return httpx.Internal(err)
	}
	m.markMissing(recs)
	httpx.OK(w, recs)
	return nil
}

/* ---------- create ---------- */

func (m *Module) handleCreate(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Slug         string `json:"slug"`
		Live         *bool  `json:"live"`
		IncludeBinds *bool  `json:"include_binds"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	if !apps.ValidSlug(req.Slug) {
		return httpx.BadRequest("Uygulama kimliği geçersiz.")
	}
	opts := backupOptions{
		Slug:         req.Slug,
		Live:         m.deps.Settings.Get(KeyDefaultConsistency) == consistencyLive,
		IncludeBinds: m.deps.Settings.Get(KeyDefaultBinds) != "false",
		Trigger:      triggerManual,
		Actor:        auth.ActorFrom(r),
	}
	if req.Live != nil {
		opts.Live = *req.Live
	}
	if req.IncludeBinds != nil {
		opts.IncludeBinds = *req.IncludeBinds
	}
	view, err := m.startBackup(r.Context(), opts)
	if err != nil {
		m.deps.Audit.Log(r.Context(), opts.Actor, "backup.create", req.Slug, messageOf(err), false)
		return httpError(err)
	}
	httpx.JSON(w, http.StatusAccepted, view)
	return nil
}

/* ---------- one backup ---------- */

// record loads the record named by the path. The file name always comes
// from the database, never from the client.
func (m *Module) record(r *http.Request) (*Record, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return nil, httpx.BadRequest("Yedek kimliği geçersiz.")
	}
	rec, err := m.store.get(r.Context(), id)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	if rec == nil {
		return nil, httpx.NotFound("Yedek bulunamadı.")
	}
	return rec, nil
}

// usable returns the archive path of a record that can be read.
func (m *Module) usable(rec *Record) (string, error) {
	if rec.Status != statusSuccess || rec.FileName == "" {
		return "", httpx.Conflict("Bu yedekleme tamamlanmadığı için dosyası yok.")
	}
	p, err := m.recordPath(rec)
	if err != nil {
		return "", err
	}
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return "", httpx.NotFound("Yedek dosyası diskte bulunamadı. Silinmiş veya taşınmış olabilir.")
	}
	return p, nil
}

func (m *Module) handleDelete(w http.ResponseWriter, r *http.Request) error {
	rec, err := m.record(r)
	if err != nil {
		return err
	}
	actor := auth.ActorFrom(r)
	if rec.Status == statusRunning || m.jobs.busy(rec.Slug) != "" {
		return httpx.Conflict("Bu uygulama için bir işlem sürerken yedek silinemez.")
	}
	if err := m.deleteRecord(r.Context(), rec); err != nil {
		m.deps.Audit.Log(r.Context(), actor, "backup.delete", rec.Slug, auditDetail(rec.FileName, err), false)
		return httpError(err)
	}
	m.deps.Audit.Log(r.Context(), actor, "backup.delete", rec.Slug, rec.FileName, true)
	m.invalidateHealth()
	httpx.OK(w, map[string]any{"deleted": rec.ID})
	return nil
}

func (m *Module) handleDownload(w http.ResponseWriter, r *http.Request) error {
	rec, err := m.record(r)
	if err != nil {
		return err
	}
	actor := auth.ActorFrom(r)
	p, err := m.usable(rec)
	if err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return httpx.NotFound("Yedek dosyası açılamadı.")
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return httpx.NotFound("Yedek dosyası açılamadı.")
	}
	m.deps.Audit.Log(r.Context(), actor, "backup.download", rec.Slug, rec.FileName, true)
	h := w.Header()
	// fileNameRe limits the name to letters, digits, "-" and ".".
	h.Set("Content-Disposition", `attachment; filename="`+rec.FileName+`"`)
	h.Set("Content-Type", "application/octet-stream")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", fi.ModTime(), f)
	return nil
}

const maxPassphrase = 256

func cleanPassphrase(p string) (string, error) {
	if len(p) > maxPassphrase || !utf8.ValidString(p) || strings.ContainsRune(p, 0) {
		return "", httpx.BadRequest("Parola geçersiz.")
	}
	return p, nil
}

func (m *Module) handleVerify(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Passphrase string `json:"passphrase"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	pass, err := cleanPassphrase(req.Passphrase)
	if err != nil {
		return err
	}
	rec, err := m.record(r)
	if err != nil {
		return err
	}
	p, err := m.usable(rec)
	if err != nil {
		return err
	}
	if _, err := m.checkPassphrase(p, pass); err != nil {
		return httpError(err)
	}
	view, err := m.startVerify(rec, p, pass, auth.ActorFrom(r))
	if err != nil {
		return httpError(err)
	}
	httpx.JSON(w, http.StatusAccepted, view)
	return nil
}

func (m *Module) handleRestore(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		// Confirm must repeat the application slug.
		Confirm      string `json:"confirm"`
		Passphrase   string `json:"passphrase"`
		SafetyBackup *bool  `json:"safety_backup"`
		RestoreBinds *bool  `json:"restore_binds"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	pass, err := cleanPassphrase(req.Passphrase)
	if err != nil {
		return err
	}
	rec, err := m.record(r)
	if err != nil {
		return err
	}
	actor := auth.ActorFrom(r)
	if req.Confirm != rec.Slug {
		return httpx.BadRequest("Geri yüklemeyi onaylamak için uygulama kimliğini (" + rec.Slug + ") yazmalısınız.")
	}
	p, err := m.usable(rec)
	if err != nil {
		return err
	}
	if _, err := m.checkPassphrase(p, pass); err != nil {
		return httpError(err)
	}
	opts := restoreOptions{Passphrase: pass, SafetyBackup: true, RestoreBinds: true, Actor: actor}
	if req.SafetyBackup != nil {
		opts.SafetyBackup = *req.SafetyBackup
	}
	if req.RestoreBinds != nil {
		opts.RestoreBinds = *req.RestoreBinds
	}
	view, err := m.startRestore(rec, p, opts)
	if err != nil {
		m.deps.Audit.Log(r.Context(), actor, "backup.restore", rec.Slug, auditDetail(rec.FileName, err), false)
		return httpError(err)
	}
	httpx.JSON(w, http.StatusAccepted, view)
	return nil
}

/* ---------- upload and import ---------- */

var gzipMagic = []byte{0x1f, 0x8b}

type uploadResult struct {
	UploadID  string `json:"upload_id"`
	Size      int64  `json:"size"`
	Encrypted bool   `json:"encrypted"`
	// NeedsPassphrase is true when the file is encrypted with a key other
	// than the one stored on this server.
	NeedsPassphrase bool `json:"needs_passphrase"`
}

// handleUpload stores an uploaded file in the staging folder. Nothing is
// accepted as a backup here: that happens in the import job, after the file
// was verified.
func (m *Module) handleUpload(w http.ResponseWriter, r *http.Request) error {
	actor := auth.ActorFrom(r)
	if err := os.MkdirAll(m.incomingDir(), 0o700); err != nil {
		return httpError(userErr("Yedek klasörü oluşturulamadı.", err))
	}
	free, _, spaceErr := diskSpace(m.incomingDir())
	const reserve = 64 << 20
	if spaceErr == nil && r.ContentLength > 0 && uint64(r.ContentLength)+reserve > free {
		return httpx.NewError(http.StatusInsufficientStorage, "no_space",
			fmt.Sprintf("Yedek klasöründe yeterli boş alan yok (%s boş).", humanBytes(int64(free))))
	}
	mr, err := r.MultipartReader()
	if err != nil {
		return httpx.BadRequest("Yükleme isteği geçersiz.")
	}
	var part io.Reader
	for {
		p, err := mr.NextPart()
		if err != nil {
			return httpx.BadRequest("Yüklenecek dosya gönderilmedi.")
		}
		if p.FormName() == "file" {
			part = p
			break
		}
	}
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return httpx.Internal(err)
	}
	id := hex.EncodeToString(b)
	staged := m.stagedPath(id)
	f, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return httpError(userErr("Yüklenen dosya kaydedilemedi.", err))
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(staged)
		}
	}()
	limit := int64(1) << 50
	if spaceErr == nil && free > reserve {
		limit = int64(free - reserve)
	}
	n, err := io.Copy(f, io.LimitReader(part, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		if r.Context().Err() != nil {
			return httpx.BadRequest("Yükleme yarıda kesildi.")
		}
		return httpError(userErr("Yüklenen dosya kaydedilemedi. "+messageOf(err), err))
	}
	if n > limit {
		return httpx.NewError(http.StatusInsufficientStorage, "no_space", "Yedek klasöründe bu dosya için yeterli boş alan yok.")
	}

	head := make([]byte, encHeaderLen)
	hf, err := os.Open(staged)
	if err != nil {
		return httpx.Internal(err)
	}
	hn, _ := io.ReadFull(hf, head)
	hf.Close()
	head = head[:hn]
	res := uploadResult{UploadID: id, Size: n}
	switch {
	case isEncrypted(head):
		h, err := parseEncHeader(head)
		if err != nil {
			return httpError(err)
		}
		res.Encrypted = true
		km, _, _ := m.keys.current()
		res.NeedsPassphrase = km == nil || km.KDF != h.kdf || !bytes.Equal(km.Salt, h.salt)
	case bytes.HasPrefix(head, gzipMagic):
	default:
		m.deps.Audit.Log(r.Context(), actor, "backup.import", "", "Dosya bir MyServer yedeği değil.", false)
		return httpx.NewError(http.StatusBadRequest, "invalid_backup", "Dosya bir MyServer yedeği değil.")
	}
	keep = true
	httpx.OK(w, res)
	return nil
}

func (m *Module) handleImport(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		UploadID   string `json:"upload_id"`
		Passphrase string `json:"passphrase"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	if !uploadIDRe.MatchString(req.UploadID) {
		return httpx.BadRequest("Yükleme kimliği geçersiz.")
	}
	pass, err := cleanPassphrase(req.Passphrase)
	if err != nil {
		return err
	}
	staged := m.stagedPath(req.UploadID)
	if fi, err := os.Lstat(staged); err != nil || !fi.Mode().IsRegular() {
		return httpx.NotFound("Yüklenen dosya bulunamadı. Dosyayı yeniden yükleyin.")
	}
	if _, err := m.checkPassphrase(staged, pass); err != nil {
		return httpError(err)
	}
	view, err := m.startImport(req.UploadID, pass, auth.ActorFrom(r))
	if err != nil {
		return httpError(err)
	}
	httpx.JSON(w, http.StatusAccepted, view)
	return nil
}

/* ---------- jobs ---------- */

func (m *Module) handleJobs(w http.ResponseWriter, _ *http.Request) error {
	httpx.OK(w, m.jobs.list())
	return nil
}

func (m *Module) job(r *http.Request) (*Job, error) {
	j := m.jobs.get(r.PathValue("id"))
	if j == nil {
		return nil, httpx.NotFound("İşlem bulunamadı. Panel yeniden başlatılmış olabilir.")
	}
	return j, nil
}

func (m *Module) handleJob(w http.ResponseWriter, r *http.Request) error {
	j, err := m.job(r)
	if err != nil {
		return err
	}
	httpx.OK(w, j.view())
	return nil
}

func (m *Module) handleJobCancel(w http.ResponseWriter, r *http.Request) error {
	j, err := m.job(r)
	if err != nil {
		return err
	}
	if !j.requestCancel() {
		v := j.view()
		if v.Status != statusRunning {
			return httpx.Conflict("İşlem zaten sona erdi.")
		}
		return httpx.Conflict("Bu işlem artık iptal edilemez: veriler yazılmaya başlandı. Yarıda kesmek uygulamayı bozuk bir durumda bırakırdı; işlemin bitmesini bekleyin.")
	}
	m.deps.Audit.Log(r.Context(), auth.ActorFrom(r), "backup.cancel", j.view().Slug, j.Kind, true)
	httpx.OK(w, j.view())
	return nil
}

// handleStream sends the list of jobs whenever it changes, and once a
// second while a job is running (for the progress counters).
func (m *Module) handleStream(w http.ResponseWriter, r *http.Request) error {
	f, ok := w.(http.Flusher)
	if !ok {
		return httpx.Internal(errors.New("response writer does not support streaming"))
	}
	ch, cancel := m.jobs.subscribe()
	defer cancel()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": bağlandı\n\n")
	f.Flush()

	var last []byte
	send := func() error {
		b, err := json.Marshal(m.jobs.list())
		if err != nil || bytes.Equal(b, last) {
			return nil
		}
		last = b
		if _, err := fmt.Fprintf(w, "event: jobs\ndata: %s\n\n", b); err != nil {
			return err
		}
		f.Flush()
		return nil
	}
	if err := send(); err != nil {
		return nil
	}
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return nil
		case <-ch:
			// Coalesce bursts of log lines.
			select {
			case <-r.Context().Done():
				return nil
			case <-time.After(150 * time.Millisecond):
			}
			if err := send(); err != nil {
				return nil
			}
		case <-tick.C:
			if m.jobs.anyRunning() {
				if err := send(); err != nil {
					return nil
				}
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return nil
			}
			f.Flush()
		}
	}
}

/* ---------- schedules ---------- */

func (m *Module) handleScheduleGet(w http.ResponseWriter, r *http.Request) error {
	slug := r.PathValue("slug")
	if !apps.ValidSlug(slug) {
		return httpx.BadRequest("Uygulama kimliği geçersiz.")
	}
	sc, err := m.scheduleFor(r.Context(), slug)
	if err != nil {
		return httpx.Internal(err)
	}
	httpx.OK(w, sc)
	return nil
}

func (m *Module) handleSchedulePut(w http.ResponseWriter, r *http.Request) error {
	slug := r.PathValue("slug")
	if !apps.ValidSlug(slug) {
		return httpx.BadRequest("Uygulama kimliği geçersiz.")
	}
	var req struct {
		Enabled      bool   `json:"enabled"`
		Frequency    string `json:"frequency"`
		Hour         int    `json:"hour"`
		Minute       int    `json:"minute"`
		Weekday      int    `json:"weekday"`
		Monthday     int    `json:"monthday"`
		KeepLast     int    `json:"keep_last"`
		PruneManual  bool   `json:"prune_manual"`
		Live         bool   `json:"live"`
		IncludeBinds bool   `json:"include_binds"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	switch {
	case req.Frequency != freqDaily && req.Frequency != freqWeekly && req.Frequency != freqMonthly:
		return httpx.BadRequest("Sıklık günlük, haftalık veya aylık olmalıdır.")
	case req.Hour < 0 || req.Hour > 23 || req.Minute < 0 || req.Minute > 59:
		return httpx.BadRequest("Saat geçersiz.")
	case req.Weekday < 0 || req.Weekday > 6:
		return httpx.BadRequest("Haftanın günü geçersiz.")
	case req.Monthday < 1 || req.Monthday > 31:
		return httpx.BadRequest("Ayın günü 1 ile 31 arasında olmalıdır.")
	case req.KeepLast < 1 || req.KeepLast > 365:
		return httpx.BadRequest("Saklanacak yedek sayısı 1 ile 365 arasında olmalıdır.")
	}
	api, err := m.appsAPI()
	if err != nil {
		return err
	}
	if _, err := api.AppConfig(r.Context(), slug); err != nil {
		return httpError(err)
	}
	actor := auth.ActorFrom(r)
	sc := &Schedule{
		Slug: slug, Enabled: req.Enabled, Frequency: req.Frequency, Hour: req.Hour, Minute: req.Minute,
		Weekday: req.Weekday, Monthday: req.Monthday, KeepLast: req.KeepLast, PruneManual: req.PruneManual,
		Live: req.Live, IncludeBinds: req.IncludeBinds, UpdatedAt: time.Now().Unix(),
	}
	if err := m.store.saveSchedule(r.Context(), sc); err != nil {
		m.deps.Audit.Log(r.Context(), actor, "backup.schedule_update", slug, "başarısız", false)
		return httpx.Internal(err)
	}
	detail := "kapalı"
	if sc.Enabled {
		detail = fmt.Sprintf("%s %02d:%02d, son %d yedek saklanır", sc.Frequency, sc.Hour, sc.Minute, sc.KeepLast)
	}
	m.deps.Audit.Log(r.Context(), actor, "backup.schedule_update", slug, detail, true)
	m.invalidateHealth()
	out, err := m.scheduleFor(r.Context(), slug)
	if err != nil {
		return httpx.Internal(err)
	}
	httpx.OK(w, out)
	return nil
}

/* ---------- encryption ---------- */

func (m *Module) handleEncryptionGet(w http.ResponseWriter, _ *http.Request) error {
	httpx.OK(w, m.encryptionInfo())
	return nil
}

func (m *Module) handleEncryptionSet(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Passphrase string `json:"passphrase"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	pass, err := cleanPassphrase(req.Passphrase)
	if err != nil {
		return err
	}
	if utf8.RuneCountInString(pass) < 12 {
		return httpx.BadRequest("Parola en az 12 karakter olmalıdır.")
	}
	actor := auth.ActorFrom(r)
	if m.jobs.anyRunning() {
		return httpx.Conflict("Bir yedekleme işlemi sürerken parola değiştirilemez.")
	}
	if err := m.keys.set(pass); err != nil {
		m.deps.Audit.Log(r.Context(), actor, "backup.encryption_set", "", "başarısız", false)
		return httpError(userErr("Şifreleme anahtarı kaydedilemedi.", err))
	}
	m.deps.Audit.Log(r.Context(), actor, "backup.encryption_set", "", "", true)
	m.invalidateHealth()
	httpx.OK(w, m.encryptionInfo())
	return nil
}

func (m *Module) handleEncryptionClear(w http.ResponseWriter, r *http.Request) error {
	actor := auth.ActorFrom(r)
	if m.jobs.anyRunning() {
		return httpx.Conflict("Bir yedekleme işlemi sürerken şifreleme kapatılamaz.")
	}
	if err := m.keys.clear(); err != nil {
		m.deps.Audit.Log(r.Context(), actor, "backup.encryption_clear", "", "başarısız", false)
		return httpError(userErr("Şifreleme anahtarı silinemedi.", err))
	}
	m.deps.Audit.Log(r.Context(), actor, "backup.encryption_clear", "", "", true)
	m.invalidateHealth()
	httpx.OK(w, m.encryptionInfo())
	return nil
}
