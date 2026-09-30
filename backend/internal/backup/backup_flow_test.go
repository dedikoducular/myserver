package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"myserver/internal/apps"
	"myserver/internal/audit"
	"myserver/internal/httpx"
)

var testActor = audit.Actor{Username: "yonetici", IP: "192.0.2.10"}

func (e *testEnv) backup(opts backupOptions) JobView {
	e.t.Helper()
	if opts.Slug == "" {
		opts.Slug = "fotolar"
	}
	if opts.Trigger == "" {
		opts.Trigger = triggerManual
	}
	opts.Actor = testActor
	v, err := e.m.startBackup(context.Background(), opts)
	if err != nil {
		e.t.Fatalf("startBackup: %v", err)
	}
	return e.wait(v.ID)
}

// noPartialFiles asserts that the backup directory holds exactly the named
// archives and nothing else (no temporary file in particular).
func (e *testEnv) wantFiles(want ...string) {
	e.t.Helper()
	got := e.files()
	if fmt.Sprint(got) != fmt.Sprint(want) && !(len(got) == 0 && len(want) == 0) {
		e.t.Fatalf("backup directory holds %q, want %q", got, want)
	}
}

func (e *testEnv) wantNoHelpers() {
	e.t.Helper()
	if left := e.docker.liveContainers(); len(left) != 0 {
		e.t.Fatalf("containers left behind: %q", left)
	}
}

func checkHelper(t *testing.T, c *fakeContainer, readOnly bool) {
	t.Helper()
	if c.labels["io.myserver.managed"] != "true" || c.labels["io.myserver.role"] != "backup-helper" {
		t.Errorf("helper labels = %v", c.labels)
	}
	if !strings.HasPrefix(c.name, "myserver-backup-helper-") {
		t.Errorf("helper name = %q", c.name)
	}
	if c.ro != readOnly {
		t.Errorf("helper mounts the volume read-only=%v, want %v", c.ro, readOnly)
	}
	hc, _ := c.body["HostConfig"].(map[string]any)
	if hc == nil {
		t.Fatalf("helper without HostConfig: %v", c.body)
	}
	if hc["NetworkMode"] != "none" || c.body["NetworkDisabled"] != true {
		t.Errorf("helper has network access: mode %v disabled %v", hc["NetworkMode"], c.body["NetworkDisabled"])
	}
	for _, key := range []string{"Privileged", "CapAdd", "Devices", "Binds", "PidMode", "IpcMode", "UsernsMode", "VolumesFrom", "SecurityOpt"} {
		switch v := hc[key].(type) {
		case nil:
		case bool:
			if v {
				t.Errorf("helper HostConfig.%s is set", key)
			}
		case string:
			if v != "" {
				t.Errorf("helper HostConfig.%s = %q", key, v)
			}
		case []any:
			if len(v) != 0 {
				t.Errorf("helper HostConfig.%s = %v", key, v)
			}
		default:
			t.Errorf("helper HostConfig.%s = %v", key, v)
		}
	}
	mounts, _ := hc["Mounts"].([]any)
	if len(mounts) != 1 {
		t.Fatalf("helper mounts = %v", hc["Mounts"])
	}
	m := mounts[0].(map[string]any)
	if m["Type"] != "volume" || m["Target"] != helperMount {
		t.Errorf("helper mount = %v", m)
	}
}

func TestBackupOfARunningApp(t *testing.T) {
	e := newEnv(t)
	e.standardApp(true)
	v := e.backup(backupOptions{})
	if v.Status != statusSuccess {
		t.Fatalf("job: %s %s; events %q", v.Status, v.Error, e.ev.all())
	}
	stop, create, read, remove, start := e.ev.index("apps.stop fotolar"), e.ev.index("docker.create"), e.ev.index("docker.read"),
		e.ev.index("docker.remove"), e.ev.index("apps.start fotolar")
	if !(stop >= 0 && stop < create && create < read && read < remove && remove < start) {
		t.Fatalf("order of operations is wrong: %q", e.ev.all())
	}
	if e.ev.count("apps.stop") != 1 || e.ev.count("apps.start") != 1 {
		t.Fatalf("events %q", e.ev.all())
	}
	if !e.apps.isRunning("fotolar") {
		t.Fatal("the application is not running after the backup")
	}
	helpers := e.docker.helpers()
	if len(helpers) != 1 {
		t.Fatalf("helpers created: %d", len(helpers))
	}
	checkHelper(t, helpers[0], true)
	e.wantNoHelpers()

	recs := e.records("fotolar")
	if len(recs) != 1 {
		t.Fatalf("records: %d", len(recs))
	}
	rec := recs[0]
	if rec.Status != statusSuccess || rec.Consistency != consistencyStopped || rec.Trigger != triggerManual || rec.Encrypted ||
		rec.AppWasRunning || rec.Error != "" || rec.AppName != "Fotolar" || rec.AppVersion != "1.2.3" || v.BackupID != rec.ID {
		t.Fatalf("record %+v", rec)
	}
	if !fileNameRe.MatchString(rec.FileName) || !strings.HasPrefix(rec.FileName, "fotolar-") || !strings.HasSuffix(rec.FileName, ".tar.gz") {
		t.Fatalf("file name %q", rec.FileName)
	}
	e.wantFiles("fotolar/" + rec.FileName)
	path := filepath.Join(e.m.dir, "fotolar", rec.FileName)
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode().Perm() != 0o600 || fi.Size() != rec.Size {
		t.Fatalf("archive: mode %v size %d (record %d) err %v", fi.Mode(), fi.Size(), rec.Size, err)
	}
	for _, d := range []string{e.m.dir, filepath.Join(e.m.dir, "fotolar")} {
		if di, _ := os.Stat(d); di.Mode().Perm() != 0o700 {
			t.Errorf("directory %s has mode %v, want 0700", d, di.Mode().Perm())
		}
	}
	data, _ := os.ReadFile(path)
	got, err := scanBytes(t, data, nil, "")
	if err != nil {
		t.Fatalf("the archive does not verify: %v", err)
	}
	man := got.res.Manifest
	if man.App.Slug != "fotolar" || !man.AppStopped || man.Consistency != consistencyStopped || len(man.Entries) != 1 ||
		man.Entries[0].Source != "myserver-fotolar-data" || man.Entries[0].Files != 2 {
		t.Fatalf("manifest %+v", man)
	}
	if got.res.Config.Services[0].Env[0].Value != "cok-gizli-veritabani-parolasi" {
		t.Fatal("the configuration in the archive lacks the secret values")
	}
	entries := listTar(t, got.streams[man.Entries[0].Archive])
	if h := entries["alt/kasa.kdbx"]; h == nil || h.PAXRecords["test.body"] != "parola kasası" || h.Mode&0o777 != 0o600 {
		t.Fatalf("archive content: %v", entries)
	}
	if h := entries["foto.jpg"]; h == nil || h.Uid != 1000 || h.Gid != 1000 {
		t.Fatalf("ownership lost: %+v", h)
	}
	a := e.auditOf("backup.create")
	if len(a) != 1 || !a[0].Success || a[0].Target != "fotolar" || a[0].User != "yonetici" || a[0].Detail != rec.FileName {
		t.Fatalf("audit %+v", a)
	}
}

func TestBackupLeavesAStoppedAppStopped(t *testing.T) {
	e := newEnv(t)
	e.standardApp(false)
	v := e.backup(backupOptions{})
	if v.Status != statusSuccess {
		t.Fatalf("job: %s %s", v.Status, v.Error)
	}
	if n := e.ev.count("apps."); n != 0 {
		t.Fatalf("the application was touched: %q", e.ev.all())
	}
	if e.apps.isRunning("fotolar") {
		t.Fatal("the application was started")
	}
	if rec := e.records("fotolar")[0]; rec.Consistency != consistencyStopped || len(rec.Warnings) != 0 {
		t.Fatalf("record %+v", rec)
	}
}

func TestLiveBackupDoesNotStopTheApp(t *testing.T) {
	e := newEnv(t)
	e.standardApp(true)
	v := e.backup(backupOptions{Live: true})
	if v.Status != statusSuccess {
		t.Fatalf("job: %s %s", v.Status, v.Error)
	}
	if n := e.ev.count("apps."); n != 0 {
		t.Fatalf("the application was touched: %q", e.ev.all())
	}
	rec := e.records("fotolar")[0]
	if rec.Consistency != consistencyLive || len(rec.Warnings) == 0 || !strings.Contains(rec.Warnings[0], "çalışırken") {
		t.Fatalf("record %+v", rec)
	}
}

func TestBackupNoticesAnAppStartedMeanwhile(t *testing.T) {
	e := newEnv(t)
	e.standardApp(false)
	e.docker.setHook("read", func(*http.Request) { e.apps.install(testConfig("fotolar"), true) })
	v := e.backup(backupOptions{})
	rec := e.records("fotolar")[0]
	if v.Status != statusSuccess || rec.Consistency != consistencyLive || len(rec.Warnings) == 0 {
		t.Fatalf("a backup taken while the application was started claims to be consistent: %+v", rec)
	}
}

func TestEncryptedBackup(t *testing.T) {
	e := newEnv(t)
	e.standardApp(true)
	if err := e.m.keys.set(testPass); err != nil {
		t.Fatal(err)
	}
	v := e.backup(backupOptions{})
	if v.Status != statusSuccess {
		t.Fatalf("job: %s %s", v.Status, v.Error)
	}
	rec := e.records("fotolar")[0]
	if !rec.Encrypted || !strings.HasSuffix(rec.FileName, ".tar.gz.enc") {
		t.Fatalf("record %+v", rec)
	}
	data, _ := os.ReadFile(filepath.Join(e.m.dir, "fotolar", rec.FileName))
	if !isEncrypted(data) || bytes.Contains(data, []byte("cok-gizli")) || bytes.Contains(data, []byte("parola kasası")) {
		t.Fatal("the archive is not encrypted")
	}
	km, _, _ := e.m.keys.current()
	if _, err := scanBytes(t, data, km, ""); err != nil {
		t.Fatalf("stored key: %v", err)
	}
	if _, err := scanBytes(t, data, nil, testPass); err != nil {
		t.Fatalf("passphrase: %v", err)
	}
}

func TestBackupFailsRatherThanWritingUnencrypted(t *testing.T) {
	e := newEnv(t)
	e.standardApp(true)
	if err := e.m.keys.set(testPass); err != nil {
		t.Fatal(err)
	}
	// A new process finds a damaged key file.
	if err := os.WriteFile(e.m.keys.path, []byte("{bozuk"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.m.keys = newKeyring(e.cfg.DataDir)
	v := e.backup(backupOptions{})
	if v.Status != statusFailed || !strings.Contains(v.Error, "Şifreleme anahtarı okunamadı") {
		t.Fatalf("job: %s %q", v.Status, v.Error)
	}
	e.wantFiles()
	if m := mutating(e.ev.all()); len(m) != 0 {
		t.Fatalf("the server was touched: %q", m)
	}
	if rec := e.records("fotolar")[0]; rec.Status != statusFailed || rec.FileName != "" {
		t.Fatalf("record %+v", rec)
	}
	res := e.do(e.admin, "GET", "/encryption", nil)
	if !strings.Contains(string(res.Data), "okunamıyor") || strings.Contains(string(res.Data), `"enabled":true`) {
		t.Fatalf("encryption info: %s", res.Data)
	}
	found := false
	for _, h := range e.m.Health(context.Background()) {
		found = found || h.ID == "backup.key"
	}
	if !found {
		t.Fatal("the health monitor does not report the unreadable key")
	}
}

// failure is one way for a backup to fail after the application was stopped.
type failure struct {
	name   string
	inject func(e *testEnv)
	// stopped reports whether the application is stopped by the backup
	// before the failure occurs.
	stopped bool
	message string
}

func backupFailures() []failure {
	return []failure{
		{"stopping the application fails", func(e *testEnv) { e.apps.stopErr = httpx.Conflict("Uygulama için başka bir işlem sürüyor.") }, true, "başka bir işlem"},
		{"helper cannot be created", func(e *testEnv) { e.docker.setFail("create", 500) }, true, "yardımcı konteyner"},
		{"image of the helper is missing", func(e *testEnv) { delete(e.docker.images, "ghcr.io/ornek/fotolar:1.2.3") }, true, "yardımcı konteyner"},
		{"volume cannot be read", func(e *testEnv) { e.docker.setFail("read", 500) }, true, "okunamadı"},
		{"disk full inside docker", func(e *testEnv) { e.docker.setFail("read", 507) }, true, ""},
		{"stream ends early", func(e *testEnv) {
			// Cut inside the content of the last file.
			v := e.docker.volumes["myserver-fotolar-data"]
			v.files = append(v.files, tarFile{name: "son.bin", body: strings.Repeat("x", 5000)})
			full := volumeTar(e.t, v)
			v.raw = full[:len(full)-1024-3000]
		}, true, ""},
		{"stream is not a tar archive", func(e *testEnv) {
			e.docker.volumes["myserver-fotolar-data"].raw = bytes.Repeat([]byte("çöp"), 1000)
		}, true, "bozuk"},
		{"docker sends an entry outside the volume", func(e *testEnv) {
			e.docker.volumes["myserver-fotolar-data"].raw = endOfTar(rawHeader(helperPrefix+"/", tar.TypeDir, 0, ""), rawFile("etc/shadow", "x"))
		}, true, "güvenli olmayan"},
		{"docker sends a parent reference", func(e *testEnv) {
			e.docker.volumes["myserver-fotolar-data"].raw = endOfTar(rawFile(helperPrefix+"/../../etc/shadow", "x"))
		}, true, "güvenli olmayan"},
		{"docker sends nothing", func(e *testEnv) {
			e.docker.volumes["myserver-fotolar-data"].raw = make([]byte, 1024)
		}, true, "boş bir yanıt"},
		{"backup directory of the application is a file", func(e *testEnv) {
			os.MkdirAll(e.m.dir, 0o700)
			os.WriteFile(e.m.appDir("fotolar"), []byte("x"), 0o600)
		}, false, "Yedek klasörü oluşturulamadı"},
		{"docker is unreachable", func(e *testEnv) { e.docker.srv.Close() }, false, "Docker servisine ulaşılamıyor"},
		{"docker ping fails", func(e *testEnv) { e.docker.setFail("ping", 500) }, false, "Docker servisine ulaşılamıyor"},
		{"volume cannot be inspected", func(e *testEnv) { e.docker.setFail("volume-inspect", 500) }, false, ""},
		{"state of the application is unknown", func(e *testEnv) {
			e.apps.runningErr = httpx.Unavailable("docker_unavailable", "Docker servisine ulaşılamıyor.")
		}, false, "Docker servisine ulaşılamıyor"},
	}
}

func TestAppIsStartedAgainWhenTheBackupFails(t *testing.T) {
	for _, f := range backupFailures() {
		t.Run(f.name, func(t *testing.T) {
			e := newEnv(t)
			e.standardApp(true)
			f.inject(e)
			v := e.backup(backupOptions{})
			if v.Status != statusFailed {
				t.Fatalf("job: %s %q; events %q", v.Status, v.Error, e.ev.all())
			}
			if v.Error == "" || strings.HasPrefix(v.Error, "Beklenmeyen") || !strings.Contains(v.Error, f.message) {
				t.Errorf("error message %q, want one containing %q", v.Error, f.message)
			}
			if f.stopped {
				stop, start := e.ev.index("apps.stop"), e.ev.index("apps.start")
				if stop < 0 || start < stop || e.ev.count("apps.start") != 1 {
					t.Fatalf("the application was stopped and not started again: %q", e.ev.all())
				}
				if !strings.Contains(e.ev.all()[start], "cancelled=false") {
					t.Fatalf("the application was started with a dead context: %q", e.ev.all()[start])
				}
			} else if e.ev.count("apps.stop") != 0 {
				t.Fatalf("the application was stopped although the backup could not begin: %q", e.ev.all())
			}
			if !e.apps.isRunning("fotolar") && e.apps.stopErr == nil {
				t.Fatal("the application is not running after the failed backup")
			}
			if f.name != "docker is unreachable" {
				e.wantNoHelpers()
			}
			for _, name := range e.files() {
				if name != "fotolar" { // the file the test itself put in the way
					t.Fatalf("a file was left behind: %q", e.files())
				}
			}
			recs := e.records("fotolar")
			if len(recs) != 1 || recs[0].Status != statusFailed || recs[0].FileName != "" || recs[0].Size != 0 ||
				recs[0].Error != v.Error || recs[0].AppWasRunning {
				t.Fatalf("record %+v", recs[0])
			}
			a := e.auditOf("backup.create")
			if len(a) != 1 || a[0].Success || a[0].Detail != v.Error {
				t.Fatalf("audit %+v", a)
			}
			if n := e.notifications(); len(n) != 1 || !strings.HasPrefix(n[0], "ERROR: Yedekleme başarısız") {
				t.Fatalf("notifications %q", n)
			}
		})
	}
}

func TestBackupReportsAnAppThatCannotBeStartedAgain(t *testing.T) {
	e := newEnv(t)
	e.standardApp(true)
	e.apps.startErr = httpx.NewError(502, "start_failed", "Konteyner başlatılamadı: port kullanımda.")
	v := e.backup(backupOptions{})
	rec := e.records("fotolar")[0]
	if v.Status != statusSuccess || rec.Status != statusSuccess {
		t.Fatalf("the backup itself succeeded; job %s record %s", v.Status, rec.Status)
	}
	joined := strings.Join(rec.Warnings, "\n")
	if !strings.Contains(joined, "yeniden başlatılamadı") || !strings.Contains(joined, "port kullanımda") {
		t.Fatalf("warnings %q", rec.Warnings)
	}
	if n := strings.Join(e.notifications(), "\n"); !strings.Contains(n, "Uygulama başlatılamadı") {
		t.Fatalf("notifications %q", n)
	}
}

func TestCancelledBackup(t *testing.T) {
	for _, point := range []string{"read", "read-body"} {
		t.Run(point, func(t *testing.T) {
			e := newEnv(t)
			e.standardApp(true)
			// Large enough that the response is still being copied.
			e.docker.volumes["myserver-fotolar-data"].files = append(e.docker.volumes["myserver-fotolar-data"].files,
				tarFile{name: "buyuk.bin", body: string(randomBytes(t, 3<<20))})
			reached := make(chan struct{})
			e.docker.setHook(point, func(r *http.Request) {
				close(reached)
				select {
				case <-r.Context().Done():
				case <-time.After(20 * time.Second):
				}
			})
			v, err := e.m.startBackup(context.Background(), backupOptions{Slug: "fotolar", Trigger: triggerManual, Actor: testActor})
			if err != nil {
				t.Fatal(err)
			}
			<-reached
			// The temporary file exists under a name that is not an archive.
			for _, name := range e.files() {
				if base := filepath.Base(name); !tmpNameRe.MatchString(base) || fileNameRe.MatchString(base) {
					t.Fatalf("while the backup runs the directory holds %q", e.files())
				}
			}
			if len(e.files()) != 1 {
				t.Fatalf("expected the temporary file, have %q", e.files())
			}
			res := e.do(e.admin, "POST", "/jobs/"+v.ID+"/cancel", nil)
			if res.Status != 200 {
				t.Fatalf("cancel: %d %s", res.Status, res.Body)
			}
			final := e.wait(v.ID)
			if final.Status != statusCancelled || !final.CancelAsked {
				t.Fatalf("job: %+v", final)
			}
			if e.ev.count("apps.start") != 1 || !e.apps.isRunning("fotolar") {
				t.Fatalf("the application was not started again: %q", e.ev.all())
			}
			if !strings.Contains(e.ev.all()[e.ev.index("apps.start")], "cancelled=false") {
				t.Fatal("the application was started with the cancelled context")
			}
			e.wantNoHelpers()
			e.wantFiles()
			rec := e.records("fotolar")[0]
			if rec.Status != statusCancelled || rec.FileName != "" || rec.AppWasRunning {
				t.Fatalf("record %+v", rec)
			}
			if a := e.auditOf("backup.cancel"); len(a) != 1 || a[0].Target != "fotolar" {
				t.Fatalf("audit %+v", a)
			}
			if res := e.do(e.admin, "POST", "/jobs/"+v.ID+"/cancel", nil); res.Status != 409 {
				t.Fatalf("cancel of a finished job: %d", res.Status)
			}
		})
	}
}

func TestPanelShutdownDuringABackupRestartsTheApp(t *testing.T) {
	e := newEnv(t)
	e.standardApp(true)
	base, shutdown := context.WithCancel(context.Background())
	e.m.base = base
	reached := make(chan struct{})
	e.docker.setHook("read", func(r *http.Request) {
		close(reached)
		<-r.Context().Done()
	})
	v, err := e.m.startBackup(context.Background(), backupOptions{Slug: "fotolar", Trigger: triggerManual, Actor: testActor})
	if err != nil {
		t.Fatal(err)
	}
	<-reached
	shutdown()
	e.wait(v.ID)
	if e.ev.count("apps.start") != 1 || !e.apps.isRunning("fotolar") {
		t.Fatalf("the application stays stopped after a shutdown: %q", e.ev.all())
	}
	e.wantFiles()
	e.wantNoHelpers()
}

func TestFreeSpaceIsCheckedBeforeTheAppIsStopped(t *testing.T) {
	e := newEnv(t)
	e.standardApp(true)
	e.docker.volumes["myserver-fotolar-data"].size = 1 << 55
	v := e.backup(backupOptions{})
	if v.Status != statusFailed || !strings.Contains(v.Error, "yeterli boş alan yok") {
		t.Fatalf("job: %s %q", v.Status, v.Error)
	}
	if m := mutating(e.ev.all()); len(m) != 0 {
		t.Fatalf("the server was touched before the refusal: %q", m)
	}
	if !e.apps.isRunning("fotolar") {
		t.Fatal("the application was stopped")
	}
	e.wantFiles()
}

func TestBackupOnAFullDisk(t *testing.T) {
	small := os.Getenv("MSTEST_SMALLFS")
	if small == "" {
		t.Skip("MSTEST_SMALLFS is not set (needs a small tmpfs)")
	}
	dir := filepath.Join(small, "full")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	e := newEnvIn(t, dir)
	e.standardApp(true)
	// Less than the reserve of 64 MiB is left.
	filler := filepath.Join(dir, "dolgu")
	if err := os.WriteFile(filler, make([]byte, 48<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	v := e.backup(backupOptions{})
	if v.Status != statusFailed || !strings.Contains(v.Error, "yeterli boş alan yok") {
		t.Fatalf("job: %s %q", v.Status, v.Error)
	}
	if m := mutating(e.ev.all()); len(m) != 0 {
		t.Fatalf("the server was touched: %q", m)
	}
	e.wantFiles()
}

func TestMissingVolumeIsReported(t *testing.T) {
	e := newEnv(t)
	cfg := e.standardApp(false)
	cfg.Services[0].Volumes = append(cfg.Services[0].Volumes, apps.Mount{Type: apps.VolumeNamed, Source: "myserver-fotolar-yok", Target: "/yok"},
		apps.Mount{Type: apps.VolumeSystem, Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"},
		apps.Mount{Type: apps.VolumeBind, Key: "medya", Source: filepath.Join(e.root, "yok"), Target: "/medya"})
	v := e.backup(backupOptions{IncludeBinds: true})
	rec := e.records("fotolar")[0]
	if v.Status != statusSuccess || len(rec.Warnings) != 2 {
		t.Fatalf("job %s, warnings %q", v.Status, rec.Warnings)
	}
	data, _ := os.ReadFile(filepath.Join(e.m.dir, "fotolar", rec.FileName))
	got, err := scanBytes(t, data, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.res.Manifest.Entries) != 1 {
		t.Fatalf("entries %+v: a system mount or a missing location was stored", got.res.Manifest.Entries)
	}
}

func TestBackupWithHostFolder(t *testing.T) {
	e := newEnv(t)
	media := filepath.Join(e.root, "medya")
	if err := os.MkdirAll(filepath.Join(media, "alt"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(media, "alt", "film.mkv"), []byte("video"), 0o644)
	cfg := e.standardApp(true)
	cfg.Services[0].Volumes = append(cfg.Services[0].Volumes, apps.Mount{Type: apps.VolumeBind, Key: "medya", Source: media, Target: "/medya"})

	for _, include := range []bool{true, false} {
		v := e.backup(backupOptions{IncludeBinds: include})
		if v.Status != statusSuccess {
			t.Fatalf("include=%v: %s %s", include, v.Status, v.Error)
		}
		rec := e.records("fotolar")[0]
		data, _ := os.ReadFile(filepath.Join(e.m.dir, "fotolar", rec.FileName))
		ar, err := openArchive(writeTemp(t, data), nil, "")
		if err != nil {
			t.Fatal(err)
		}
		res, err := scanArchive(context.Background(), ar, nil)
		ar.close()
		if err != nil {
			t.Fatal(err)
		}
		if err := validateBackup(res, []string{e.root}); err != nil {
			t.Fatal(err)
		}
		kinds := ""
		for _, en := range res.Manifest.Entries {
			kinds += en.Kind + ":" + en.Source + " "
		}
		want := "volume:myserver-fotolar-data "
		if include {
			want += "bind:" + media + " "
		}
		if kinds != want || rec.IncludesBinds != include || res.Manifest.IncludesBinds != include {
			t.Fatalf("include=%v: entries %q, record %+v", include, kinds, rec)
		}
		time.Sleep(1100 * time.Millisecond) // file names have a resolution of one second
	}
}

func TestTwoBackupsInTheSameSecondGetDifferentNames(t *testing.T) {
	e := newEnv(t)
	e.standardApp(false)
	names := map[string]bool{}
	for i := 0; i < 3; i++ {
		v := e.backup(backupOptions{})
		if v.Status != statusSuccess {
			t.Fatalf("job: %s %s", v.Status, v.Error)
		}
	}
	for _, r := range e.records("fotolar") {
		if names[r.FileName] || !fileNameRe.MatchString(r.FileName) {
			t.Fatalf("file name %q repeated or invalid", r.FileName)
		}
		names[r.FileName] = true
	}
	if len(e.files()) != 3 {
		t.Fatalf("files %q", e.files())
	}
}

func TestBackupOfAnAppThatIsNotInstalled(t *testing.T) {
	e := newEnv(t)
	_, err := e.m.startBackup(context.Background(), backupOptions{Slug: "yok", Trigger: triggerManual})
	if !isNotFound(err) {
		t.Fatalf("err = %v", err)
	}
	e.m.SetApps(nil)
	_, err = e.m.startBackup(context.Background(), backupOptions{Slug: "fotolar", Trigger: triggerManual})
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != "apps_unavailable" {
		t.Fatalf("err = %v", err)
	}
	var typedNil *apps.Module
	e.m.SetApps(typedNil)
	if _, err := e.m.appsAPI(); err == nil {
		t.Fatal("a nil *apps.Module was accepted as the application module")
	}
	if len(e.records("")) != 0 {
		t.Fatal("records were created")
	}
}
