package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"myserver/internal/apps"
	"myserver/internal/httpx"
)

// restoreEnv has the application installed and running, its volume holding
// the "current" data, and a backup with different ("old") data.
func restoreEnv(t *testing.T, km *keyMaterial) (*testEnv, *Record) {
	t.Helper()
	e := newEnv(t)
	e.standardApp(true)
	b := &blueprint{streams: []streamSpec{{kind: kindVolume, source: "myserver-fotolar-data", tar: tarStream(t,
		tarFile{name: "./", typ: tar.TypeDir},
		tarFile{name: "foto.jpg", body: "yedekteki eski foto", uid: 1000, gid: 1000},
	)}}}
	rec := e.putArchive("fotolar", b.pack(t, km), km != nil)
	return e, rec
}

func (e *testEnv) restore(rec *Record, body map[string]any) *apiResponse {
	e.t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	if _, ok := body["confirm"]; !ok {
		body["confirm"] = rec.Slug
	}
	return e.do(e.admin, "POST", "/backups/"+itoa(rec.ID)+"/restore", body)
}

func restoreResult(t *testing.T, v JobView) RestoreResult {
	t.Helper()
	raw, _ := json.Marshal(v.Result)
	var r RestoreResult
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("result: %v", err)
	}
	return r
}

func storedFiles(t *testing.T, e *testEnv, volume string) map[string]*tar.Header {
	t.Helper()
	e.docker.mu.Lock()
	v := e.docker.volumes[volume]
	e.docker.mu.Unlock()
	if v == nil || v.stored == nil {
		t.Fatalf("nothing was written into %s", volume)
	}
	return listTar(t, v.stored)
}

func TestRestoreOfAnInstalledRunningApp(t *testing.T) {
	e, rec := restoreEnv(t, nil)
	v := e.wait(e.jobFrom(e.restore(rec, nil)).ID)
	if v.Status != statusSuccess {
		t.Fatalf("job: %s %q; events %q", v.Status, v.Error, e.ev.all())
	}
	ev := e.ev.all()
	order := []string{
		"apps.stop fotolar",
		"docker.create image=ghcr.io/ornek/fotolar:1.2.3 volume=myserver-fotolar-data ro=true", // safety backup
		"docker.read myserver-fotolar-data",
		"docker.remove myserver-backup-helper-",
		"docker.volume.remove myserver-fotolar-data",
		"docker.volume.create myserver-fotolar-data",
		"docker.create image=ghcr.io/ornek/fotolar:1.2.3 volume=myserver-fotolar-data ro=false",
		"docker.write myserver-fotolar-data path=" + helperMount,
		"docker.remove myserver-backup-helper-",
		"apps.recreate fotolar",
	}
	pos := 0
	for _, want := range order {
		found := false
		for ; pos < len(ev); pos++ {
			if strings.HasPrefix(ev[pos], want) {
				found = true
				pos++
				break
			}
		}
		if !found {
			t.Fatalf("operation %q is missing or out of order in %q", want, ev)
		}
	}
	if e.ev.count("apps.start") != 0 {
		t.Fatalf("the module started the application itself instead of leaving it to RecreateApp: %q", ev)
	}
	e.wantNoHelpers()
	for _, h := range e.docker.helpers() {
		checkHelper(t, h, h.ro)
	}
	files := storedFiles(t, e, "myserver-fotolar-data")
	if h := files["foto.jpg"]; h == nil || h.PAXRecords["test.body"] != "yedekteki eski foto" || h.Uid != 1000 {
		t.Fatalf("volume content after the restore: %v", files)
	}
	if _, stale := files["alt/kasa.kdbx"]; stale {
		t.Fatal("a file that is not in the backup survived the restore")
	}
	if l := e.docker.volumes["myserver-fotolar-data"].labels; l[apps.LabelApp] != "fotolar" || l[apps.LabelManaged] != "true" {
		t.Fatalf("labels of the recreated volume: %v", l)
	}

	res := restoreResult(t, v)
	if !res.WasInstalled || len(res.Restored) != 1 || res.SafetyBackup == "" || res.SafetyID == 0 {
		t.Fatalf("result %+v", res)
	}
	// The safety backup holds the state before the restore.
	var safety *Record
	for _, r := range e.records("fotolar") {
		if r.Trigger == triggerSafety {
			safety = r
		}
	}
	if safety == nil || safety.Status != statusSuccess || safety.FileName != res.SafetyBackup || safety.ID != res.SafetyID {
		t.Fatalf("safety record %+v", safety)
	}
	data, err := os.ReadFile(filepath.Join(e.m.dir, "fotolar", safety.FileName))
	if err != nil {
		t.Fatal(err)
	}
	got, err := scanBytes(t, data, nil, "")
	if err != nil {
		t.Fatalf("the safety backup does not verify: %v", err)
	}
	if h := listTar(t, got.streams[got.res.Manifest.Entries[0].Archive])["alt/kasa.kdbx"]; h == nil || h.PAXRecords["test.body"] != "parola kasası" {
		t.Fatal("the safety backup does not hold the data that was overwritten")
	}
	if v.BackupID != rec.ID {
		t.Fatalf("the job points to backup %d, want the restored one (%d)", v.BackupID, rec.ID)
	}
	after, _ := e.m.store.get(context.Background(), rec.ID)
	if after.VerifiedAt == 0 || !after.VerifyOK {
		t.Fatalf("verification was not recorded: %+v", after)
	}
	if a := e.auditOf("backup.restore"); len(a) != 1 || !a[0].Success || a[0].Target != "fotolar" || a[0].User != "yonetici" {
		t.Fatalf("audit %+v", a)
	}
	if len(e.apps.recreated) != 1 || e.apps.recreated[0].Services[0].Env[0].Value != "cok-gizli-veritabani-parolasi" {
		t.Fatal("the configuration of the backup was not handed to RecreateApp")
	}
}

func TestRestoreRequiresTheExactSlug(t *testing.T) {
	e, rec := restoreEnv(t, nil)
	for _, confirm := range []any{"", "Fotolar", "fotolar ", " fotolar", "FOTOLAR", "fotola", "fotolar\n", "evet", "true", nil, "fotolar\x00"} {
		body := map[string]any{"confirm": confirm}
		if confirm == nil {
			body = map[string]any{"safety_backup": true, "confirm": ""}
		}
		res := e.restore(rec, body)
		if res.Status != 400 || !strings.Contains(res.message(), "fotolar") {
			t.Fatalf("confirm %q: %d %s", confirm, res.Status, res.Body)
		}
	}
	if res := e.do(e.admin, "POST", "/backups/"+itoa(rec.ID)+"/restore", `{}`); res.Status != 400 {
		t.Fatalf("no confirm field: %d", res.Status)
	}
	if res := e.do(e.admin, "POST", "/backups/"+itoa(rec.ID)+"/restore", `{"confirm":true}`); res.Status != 400 {
		t.Fatalf("confirm true: %d", res.Status)
	}
	if res := e.do(e.admin, "POST", "/backups/"+itoa(rec.ID)+"/restore", ``); res.Status != 400 {
		t.Fatalf("empty body: %d", res.Status)
	}
	if len(e.ev.all()) != 0 || len(e.m.jobs.list()) != 0 {
		t.Fatalf("something happened without confirmation: %q", e.ev.all())
	}
}

// Whatever is wrong with the archive, the restore ends before anything on
// the server was stopped, removed or written.
func TestRestoreDoesNotTouchAnythingWhenVerificationFails(t *testing.T) {
	km := keyFor(t, testPass)
	good := func(t *testing.T, k *keyMaterial) []byte { return (&blueprint{}).pack(t, k) }
	hostile := func(stream []byte) func(*testing.T, *keyMaterial) []byte {
		return func(t *testing.T, k *keyMaterial) []byte {
			return (&blueprint{streams: []streamSpec{{kind: kindVolume, source: "myserver-fotolar-data", tar: stream}}}).pack(t, k)
		}
	}
	cases := map[string]struct {
		key  *keyMaterial
		data func(*testing.T, *keyMaterial) []byte
	}{
		"flipped byte": {nil, func(t *testing.T, k *keyMaterial) []byte {
			d := good(t, k)
			d[len(d)/2] ^= 1
			return d
		}},
		"truncated": {nil, func(t *testing.T, k *keyMaterial) []byte { d := good(t, k); return d[:len(d)-20] }},
		"encrypted, final chunk damaged": {km, func(t *testing.T, k *keyMaterial) []byte {
			// Several chunks, so that the earlier ones decrypt fine.
			b := &blueprint{streams: []streamSpec{{kind: kindVolume, source: "myserver-fotolar-data",
				tar: tarStream(t, tarFile{name: "./", typ: tar.TypeDir}, tarFile{name: "a.bin", body: string(randomBytes(t, 300_000))})}}}
			d := b.pack(t, k)
			d[len(d)-1] ^= 1
			return d
		}},
		"encrypted, final chunk dropped": {km, func(t *testing.T, k *keyMaterial) []byte {
			b := &blueprint{streams: []streamSpec{{kind: kindVolume, source: "myserver-fotolar-data",
				tar: tarStream(t, tarFile{name: "./", typ: tar.TypeDir}, tarFile{name: "a.bin", body: string(randomBytes(t, 300_000))})}}}
			d := b.pack(t, k)
			n := (len(d) - encHeaderLen) / (encChunkSize + gcmTagLen)
			return d[:encHeaderLen+n*(encChunkSize+gcmTagLen)]
		}},
		"encrypted, flipped byte in the middle": {km, func(t *testing.T, k *keyMaterial) []byte {
			d := good(t, k)
			d[len(d)/2] ^= 1
			return d
		}},
		"entry with a parent reference":     {nil, hostile(endOfTar(rawFile("../../etc/cron.d/x", "x")))},
		"entry with an absolute name":       {nil, hostile(endOfTar(rawFile("/etc/cron.d/x", "x")))},
		"file through a symlink":            {nil, hostile(endOfTar(rawHeader("s", tar.TypeSymlink, 0, "/etc"), rawFile("s/cron.d/x", "x")))},
		"hard link out of the volume":       {nil, hostile(endOfTar(rawHeader("h", tar.TypeLink, 0, "../../etc/shadow")))},
		"inner stream is not a tar archive": {nil, hostile(bytes.Repeat([]byte("çöp"), 1000))},
		"archive of another application": {nil, func(t *testing.T, k *keyMaterial) []byte {
			return (&blueprint{cfg: testConfig("parolalar")}).pack(t, k)
		}},
		"manifest missing": {nil, func(t *testing.T, k *keyMaterial) []byte {
			_, entries := (&blueprint{}).build(t)
			return packEntries(t, k, entries)
		}},
		"not an archive": {nil, func(t *testing.T, k *keyMaterial) []byte { return []byte("yedek değil") }},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.standardApp(true)
			if c.key != nil {
				// The server holds the key the archive was made with.
				e.m.keys.loaded, e.m.keys.km, e.m.keys.setAt = true, c.key, 1
			}
			rec := e.putArchive("fotolar", c.data(t, c.key), c.key != nil)
			res := e.restore(rec, nil)
			var final JobView
			if res.Status == http.StatusAccepted {
				final = e.wait(e.jobFrom(res).ID)
				if final.Status != statusFailed {
					t.Fatalf("job: %s %q", final.Status, final.Error)
				}
				if final.Error == "" || strings.HasPrefix(final.Error, "Beklenmeyen") {
					t.Fatalf("error message %q", final.Error)
				}
				if strings.Contains(final.Error, "yarıda kaldı") {
					t.Fatalf("the restore had begun: %q", final.Error)
				}
			} else if res.Status != 400 || res.code() != "invalid_backup" {
				t.Fatalf("response %d %s", res.Status, res.Body)
			}
			if m := mutating(e.ev.all()); len(m) != 0 {
				t.Fatalf("the server was touched although the archive is bad: %q", m)
			}
			if !e.apps.isRunning("fotolar") {
				t.Fatal("the application was stopped")
			}
			if v := e.docker.volumes["myserver-fotolar-data"]; v == nil || v.stored != nil {
				t.Fatal("the volume was changed")
			}
			if len(e.records("fotolar")) != 1 {
				t.Fatalf("records %d: a safety backup was taken for a restore that cannot happen", len(e.records("fotolar")))
			}
			// An intact archive of another application is a good file that
			// is refused; the others are recorded as failed verifications.
			if res.Status == http.StatusAccepted && name != "archive of another application" {
				after, _ := e.m.store.get(context.Background(), rec.ID)
				if after.VerifiedAt == 0 || after.VerifyOK {
					t.Fatalf("the failed verification was not recorded: %+v", after)
				}
			}
		})
	}
}

func TestRestoreWithPassphrases(t *testing.T) {
	km := keyFor(t, testPass)
	t.Run("stored key, no passphrase", func(t *testing.T) {
		e, rec := restoreEnv(t, km)
		e.m.keys.loaded, e.m.keys.km = true, km
		if v := e.wait(e.jobFrom(e.restore(rec, map[string]any{"safety_backup": false})).ID); v.Status != statusSuccess {
			t.Fatalf("job: %s %q", v.Status, v.Error)
		}
	})
	t.Run("stored key, right passphrase", func(t *testing.T) {
		e, rec := restoreEnv(t, km)
		e.m.keys.loaded, e.m.keys.km = true, km
		if v := e.wait(e.jobFrom(e.restore(rec, map[string]any{"passphrase": testPass, "safety_backup": false})).ID); v.Status != statusSuccess {
			t.Fatalf("job: %s %q", v.Status, v.Error)
		}
	})
	t.Run("stored key, wrong passphrase", func(t *testing.T) {
		e, rec := restoreEnv(t, km)
		e.m.keys.loaded, e.m.keys.km = true, km
		for _, path := range []string{"/restore", "/verify"} {
			res := e.do(e.admin, "POST", "/backups/"+itoa(rec.ID)+path, map[string]any{"confirm": "fotolar", "passphrase": "yanlis-parola-12345"})
			if path == "/verify" {
				res = e.do(e.admin, "POST", "/backups/"+itoa(rec.ID)+path, map[string]any{"passphrase": "yanlis-parola-12345"})
			}
			if res.Status != 400 || res.code() != "wrong_passphrase" || !strings.Contains(res.message(), "Parola yanlış") {
				t.Fatalf("%s with a wrong passphrase although the server holds the key: %d %s", path, res.Status, res.Body)
			}
		}
		if len(e.m.jobs.list()) != 0 || len(mutating(e.ev.all())) != 0 {
			t.Fatalf("a job was started: %q", e.ev.all())
		}
		after, _ := e.m.store.get(context.Background(), rec.ID)
		if after.VerifiedAt != 0 {
			t.Fatal("a wrong passphrase was recorded as a failed verification of the file")
		}
	})
	t.Run("no stored key", func(t *testing.T) {
		e, rec := restoreEnv(t, km)
		res := e.restore(rec, nil)
		if res.Status != 400 || res.code() != "passphrase_required" {
			t.Fatalf("no passphrase: %d %s", res.Status, res.Body)
		}
		res = e.restore(rec, map[string]any{"passphrase": "yanlis-parola-12345"})
		if res.Status != 400 || res.code() != "wrong_passphrase" {
			t.Fatalf("wrong passphrase: %d %s", res.Status, res.Body)
		}
		if len(mutating(e.ev.all())) != 0 {
			t.Fatalf("events %q", e.ev.all())
		}
		v := e.wait(e.jobFrom(e.restore(rec, map[string]any{"passphrase": testPass})).ID)
		if v.Status != statusSuccess {
			t.Fatalf("job: %s %q", v.Status, v.Error)
		}
		// The safety backup of a server without a key is not encrypted.
		if r := restoreResult(t, v); !strings.HasSuffix(r.SafetyBackup, ".tar.gz") {
			t.Fatalf("safety backup %q", r.SafetyBackup)
		}
	})
	t.Run("key of another passphrase stored", func(t *testing.T) {
		e, rec := restoreEnv(t, km)
		if err := e.m.keys.set("sunucunun-yeni-parolasi"); err != nil {
			t.Fatal(err)
		}
		if res := e.restore(rec, nil); res.code() != "passphrase_required" {
			t.Fatalf("no passphrase: %d %s", res.Status, res.Body)
		}
		if res := e.restore(rec, map[string]any{"passphrase": "sunucunun-yeni-parolasi"}); res.code() != "wrong_passphrase" {
			t.Fatalf("passphrase of the server: %d %s", res.Status, res.Body)
		}
		v := e.wait(e.jobFrom(e.restore(rec, map[string]any{"passphrase": testPass})).ID)
		if v.Status != statusSuccess {
			t.Fatalf("job: %s %q", v.Status, v.Error)
		}
		if r := restoreResult(t, v); !strings.HasSuffix(r.SafetyBackup, ".tar.gz.enc") {
			t.Fatalf("safety backup %q is not encrypted although the server has a key", r.SafetyBackup)
		}
	})
	for _, bad := range []string{strings.Repeat("a", 257), "a\x00b", "\xff\xfe"} {
		e, rec := restoreEnv(t, km)
		if res := e.restore(rec, map[string]any{"passphrase": bad}); res.Status != 400 {
			t.Fatalf("passphrase %q: %d", bad, res.Status)
		}
	}
}

func TestRestoreWithoutSafetyBackup(t *testing.T) {
	e, rec := restoreEnv(t, nil)
	v := e.wait(e.jobFrom(e.restore(rec, map[string]any{"safety_backup": false})).ID)
	if v.Status != statusSuccess {
		t.Fatalf("job: %s %q", v.Status, v.Error)
	}
	if e.ev.count("docker.read") != 0 || len(e.records("fotolar")) != 1 {
		t.Fatalf("a safety backup was taken: %q", e.ev.all())
	}
	r := restoreResult(t, v)
	if r.SafetyBackup != "" || len(r.Warnings) == 0 || !strings.Contains(r.Warnings[0], "geri alınamaz") {
		t.Fatalf("result %+v", r)
	}
}

func TestRestoreIsAbortedWhenTheSafetyBackupFails(t *testing.T) {
	e, rec := restoreEnv(t, nil)
	e.docker.setFail("read", 500)
	v := e.wait(e.jobFrom(e.restore(rec, nil)).ID)
	if v.Status != statusFailed || !strings.Contains(v.Error, "Güvenlik yedeği alınamadı") || !strings.Contains(v.Error, "hiçbir veri değiştirilmedi") {
		t.Fatalf("job: %s %q", v.Status, v.Error)
	}
	for _, ev := range e.ev.all() {
		if strings.HasPrefix(ev, "docker.volume") || strings.HasPrefix(ev, "docker.write") || strings.HasPrefix(ev, "apps.recreate") {
			t.Fatalf("data was touched: %q", e.ev.all())
		}
	}
	if !e.apps.isRunning("fotolar") || e.ev.count("apps.start") != 1 {
		t.Fatalf("the application was not started again: %q", e.ev.all())
	}
	e.wantNoHelpers()
	e.wantFiles("fotolar/" + rec.FileName)
}

func TestRestoreCanBeCancelledOnlyBeforeDataIsOverwritten(t *testing.T) {
	t.Run("during the safety backup", func(t *testing.T) {
		e, rec := restoreEnv(t, nil)
		reached := make(chan struct{})
		e.docker.setHook("read", func(r *http.Request) {
			close(reached)
			<-r.Context().Done()
		})
		job := e.jobFrom(e.restore(rec, nil))
		if !job.Cancellable {
			t.Fatal("a restore that has not begun to write is not cancellable")
		}
		<-reached
		if res := e.do(e.admin, "POST", "/jobs/"+job.ID+"/cancel", nil); res.Status != 200 {
			t.Fatalf("cancel: %d %s", res.Status, res.Body)
		}
		v := e.wait(job.ID)
		if v.Status != statusCancelled {
			t.Fatalf("job: %s %q", v.Status, v.Error)
		}
		for _, ev := range e.ev.all() {
			if strings.HasPrefix(ev, "docker.volume") || strings.HasPrefix(ev, "docker.write") || strings.HasPrefix(ev, "apps.recreate") {
				t.Fatalf("data was touched: %q", e.ev.all())
			}
		}
		if !e.apps.isRunning("fotolar") {
			t.Fatal("the application was not started again")
		}
		e.wantNoHelpers()
		e.wantFiles("fotolar/" + rec.FileName)
	})
	t.Run("while data is written", func(t *testing.T) {
		e, rec := restoreEnv(t, nil)
		reached, release := make(chan struct{}), make(chan struct{})
		e.docker.setHook("write", func(r *http.Request) {
			close(reached)
			select {
			case <-release:
			case <-r.Context().Done():
				t.Error("the write was interrupted")
			case <-time.After(20 * time.Second):
			}
		})
		job := e.jobFrom(e.restore(rec, nil))
		<-reached
		view := e.do(e.admin, "GET", "/jobs/"+job.ID, nil)
		var jv JobView
		json.Unmarshal(view.Data, &jv)
		if jv.Cancellable || jv.Status != statusRunning {
			t.Fatalf("job view while writing: cancellable=%v status=%s", jv.Cancellable, jv.Status)
		}
		found := false
		for _, l := range jv.Logs {
			found = found || (l.Level == "warning" && strings.Contains(l.Message, "iptal edilemez"))
		}
		if !found {
			t.Fatalf("the job log does not say that the restore cannot be cancelled any more: %+v", jv.Logs)
		}
		res := e.do(e.admin, "POST", "/jobs/"+job.ID+"/cancel", nil)
		if res.Status != 409 || !strings.Contains(res.message(), "artık iptal edilemez") {
			t.Fatalf("cancel after the point of no return: %d %s", res.Status, res.Body)
		}
		if len(e.auditOf("backup.cancel")) != 0 {
			t.Fatal("a refused cancel was audited as a cancel")
		}
		// Not even a shutdown of the panel interrupts the write.
		e.m.jobs.get(job.ID).cancel()
		time.Sleep(50 * time.Millisecond)
		close(release)
		v := e.wait(job.ID)
		if v.Status != statusSuccess || v.CancelAsked {
			t.Fatalf("job: %s %q", v.Status, v.Error)
		}
		if h := storedFiles(t, e, "myserver-fotolar-data")["foto.jpg"]; h == nil {
			t.Fatal("the volume was not written")
		}
	})
}

func TestRestoreFailingHalfWayNamesTheSafetyBackup(t *testing.T) {
	for _, op := range []string{"write", "volume-remove", "volume-create", "list"} {
		t.Run(op, func(t *testing.T) {
			e, rec := restoreEnv(t, nil)
			e.docker.setFail(op, 500)
			v := e.wait(e.jobFrom(e.restore(rec, nil)).ID)
			r := restoreResult(t, v)
			if v.Status != statusFailed || r.SafetyBackup == "" || !strings.Contains(v.Error, r.SafetyBackup) || !strings.Contains(v.Error, "yarıda kaldı") {
				t.Fatalf("job: %s %q (safety %q)", v.Status, v.Error, r.SafetyBackup)
			}
			e.wantNoHelpers()
			if e.ev.count("apps.recreate") != 0 {
				t.Fatal("the application was recreated over incomplete data")
			}
			if n := strings.Join(e.notifications(), "\n"); !strings.Contains(n, "Geri yükleme başarısız") {
				t.Fatalf("notifications %q", n)
			}
		})
	}
}

// The configuration inside an archive is hostile input. The module neither
// judges nor applies it: it hands it to RecreateApp, which validates it
// against the manifest of the application, and it never creates a container
// from it itself.
func TestTamperedConfigurationIsNotAppliedByTheModule(t *testing.T) {
	e := newEnv(t)
	e.standardApp(true)
	cfg := testConfig("fotolar")
	s := &cfg.Services[0]
	s.Privileged = true
	s.CapAdd = []string{"SYS_ADMIN", "NET_ADMIN"}
	s.Devices = []apps.DeviceSpec{{Host: "/dev/sda", Container: "/dev/sda", Permissions: "rwm"}}
	s.NetworkMode = "host"
	s.User = "0:0"
	s.Volumes = append(s.Volumes,
		apps.Mount{Type: apps.VolumeSystem, Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"},
		apps.Mount{Type: apps.VolumeSystem, Source: "/", Target: "/host"})
	rec := e.putArchive("fotolar", (&blueprint{cfg: cfg}).pack(t, nil), false)

	// What the real RecreateApp answers for such a configuration.
	e.apps.recreateErr = httpx.NewError(400, "invalid_config", "Servis app ayrıcalıklı kip istiyor ancak uygulama tanımı buna izin vermiyor.")
	v := e.wait(e.jobFrom(e.restore(rec, nil)).ID)
	if v.Status != statusFailed || !strings.Contains(v.Error, "ayrıcalıklı kip") {
		t.Fatalf("job: %s %q", v.Status, v.Error)
	}
	if r := restoreResult(t, v); r.SafetyBackup == "" || !strings.Contains(v.Error, r.SafetyBackup) {
		t.Fatalf("the error does not name the safety backup: %q", v.Error)
	}
	if len(e.apps.recreated) != 1 {
		t.Fatalf("RecreateApp calls: %d", len(e.apps.recreated))
	}
	got := e.apps.recreated[0].Services[0]
	if !got.Privileged || got.NetworkMode != "host" || len(got.CapAdd) != 2 || len(got.Devices) != 1 || len(got.Volumes) != 3 {
		t.Fatalf("the configuration was altered before validation: %+v", got)
	}
	// Every container the module created itself is a plain helper.
	for _, h := range e.docker.helpers() {
		checkHelper(t, h, h.ro)
		raw, _ := json.Marshal(h.body)
		for _, bad := range []string{"SYS_ADMIN", "/dev/sda", "docker.sock", `"host"`, "/host"} {
			if strings.Contains(string(raw), bad) {
				t.Fatalf("a helper container carries %q from the archive: %s", bad, raw)
			}
		}
	}
	e.wantNoHelpers()
}

func TestRestoreOfAnAppThatIsNotInstalled(t *testing.T) {
	e := newEnv(t)
	cfg := testConfig("fotolar")
	rec := e.putArchive("fotolar", (&blueprint{cfg: cfg}).pack(t, nil), false)
	e.docker.allowPull = true
	v := e.wait(e.jobFrom(e.restore(rec, nil)).ID)
	if v.Status != statusSuccess {
		t.Fatalf("job: %s %q; %q", v.Status, v.Error, e.ev.all())
	}
	if e.ev.count("apps.stop") != 0 || e.ev.count("docker.read") != 0 {
		t.Fatalf("events %q", e.ev.all())
	}
	if e.ev.index("docker.pull ghcr.io/ornek/fotolar:1.2.3") < 0 || e.ev.index("docker.pull") > e.ev.index("docker.volume.create") {
		t.Fatalf("the image must be present before data is written: %q", e.ev.all())
	}
	r := restoreResult(t, v)
	if r.WasInstalled || r.SafetyBackup != "" || len(r.Warnings) != 0 {
		t.Fatalf("result %+v", r)
	}
	if h := storedFiles(t, e, "myserver-fotolar-data")["foto.jpg"]; h == nil {
		t.Fatal("volume not written")
	}
}

func TestRestoreRefusesAForeignVolume(t *testing.T) {
	e, rec := restoreEnv(t, nil)
	e.docker.volumes["myserver-fotolar-data"].labels = map[string]string{"com.example": "baska"}
	v := e.wait(e.jobFrom(e.restore(rec, map[string]any{"safety_backup": false})).ID)
	if v.Status != statusFailed || !strings.Contains(v.Error, "Üzerine yazılmadı") {
		t.Fatalf("job: %s %q", v.Status, v.Error)
	}
	if m := mutating(e.ev.all()); len(m) != 0 {
		t.Fatalf("the refusal came after the server was changed: %q", m)
	}
	if !e.apps.isRunning("fotolar") {
		t.Fatal("the application was stopped")
	}
}

func TestRestoreOfHostFolders(t *testing.T) {
	setup := func(t *testing.T) (*testEnv, *Record, string) {
		e := newEnv(t)
		media := filepath.Join(e.root, "medya")
		os.MkdirAll(media, 0o755)
		os.WriteFile(filepath.Join(media, "bugunku.txt"), []byte("bugünkü"), 0o644)
		cfg := e.standardApp(true)
		cfg.Services[0].Volumes = append(cfg.Services[0].Volumes, apps.Mount{Type: apps.VolumeBind, Key: "medya", Source: media, Target: "/medya"})
		b := &blueprint{cfg: cfg, streams: []streamSpec{
			{kind: kindVolume, source: "myserver-fotolar-data", tar: simpleVolume(t)},
			{kind: kindBind, source: media, tar: tarStream(t, tarFile{name: "./", typ: tar.TypeDir}, tarFile{name: "eski.txt", body: "yedekteki"})},
		}}
		return e, e.putArchive("fotolar", b.pack(t, nil), false), media
	}
	t.Run("restored", func(t *testing.T) {
		e, rec, media := setup(t)
		v := e.wait(e.jobFrom(e.restore(rec, nil)).ID)
		if v.Status != statusSuccess {
			t.Fatalf("job: %s %q", v.Status, v.Error)
		}
		if b, _ := os.ReadFile(filepath.Join(media, "eski.txt")); string(b) != "yedekteki" {
			t.Fatal("the folder was not restored")
		}
		if _, err := os.Stat(filepath.Join(media, "bugunku.txt")); err == nil {
			t.Fatal("a stale file survived")
		}
		// The safety backup holds the folder as it was.
		r := restoreResult(t, v)
		data, _ := os.ReadFile(filepath.Join(e.m.dir, "fotolar", r.SafetyBackup))
		ar, _ := openArchive(writeTemp(t, data), nil, "")
		defer ar.close()
		res, err := scanArchive(context.Background(), ar, nil)
		if err != nil || len(res.Manifest.Entries) != 2 {
			t.Fatalf("safety backup: %v %+v", err, res)
		}
	})
	t.Run("left alone", func(t *testing.T) {
		e, rec, media := setup(t)
		v := e.wait(e.jobFrom(e.restore(rec, map[string]any{"restore_binds": false})).ID)
		if v.Status != statusSuccess {
			t.Fatalf("job: %s %q", v.Status, v.Error)
		}
		if b, _ := os.ReadFile(filepath.Join(media, "bugunku.txt")); string(b) != "bugünkü" {
			t.Fatal("the folder was changed although it was excluded")
		}
		if r := restoreResult(t, v); len(r.Skipped) != 1 || r.Skipped[0] != media || len(r.Restored) != 1 {
			t.Fatalf("result %+v", r)
		}
	})
	t.Run("folder became a symlink out of the allowed roots", func(t *testing.T) {
		e, rec, media := setup(t)
		outside := t.TempDir()
		os.WriteFile(filepath.Join(outside, "gizli"), []byte("dışarıda"), 0o600)
		os.RemoveAll(media)
		if err := os.Symlink(outside, media); err != nil {
			t.Fatal(err)
		}
		v := e.wait(e.jobFrom(e.restore(rec, map[string]any{"safety_backup": false})).ID)
		if v.Status != statusFailed || !strings.Contains(v.Error, "dışına") {
			t.Fatalf("job: %s %q", v.Status, v.Error)
		}
		if m := mutating(e.ev.all()); len(m) != 0 {
			t.Fatalf("the server was touched: %q", m)
		}
		if b, _ := os.ReadFile(filepath.Join(outside, "gizli")); string(b) != "dışarıda" {
			t.Fatal("a folder outside the allowed roots was cleared")
		}
	})
}

func TestVerifyJob(t *testing.T) {
	e, rec := restoreEnv(t, nil)
	v := e.wait(e.jobFrom(e.do(e.admin, "POST", "/backups/"+itoa(rec.ID)+"/verify", map[string]any{})).ID)
	if v.Status != statusSuccess || v.Kind != jobVerify {
		t.Fatalf("job: %+v", v)
	}
	if m := mutating(e.ev.all()); len(m) != 0 {
		t.Fatalf("a verification touched the server: %q", m)
	}
	raw, _ := json.Marshal(v.Result)
	var p Preview
	json.Unmarshal(raw, &p)
	if p.Slug != "fotolar" || !p.AppInstalled || !p.AppRunning || p.SecretCount != 1 || len(p.Entries) != 1 || !p.Entries[0].Exists ||
		p.Entries[0].Files != 1 {
		t.Fatalf("preview %+v", p)
	}
	if strings.Contains(string(raw), "cok-gizli") {
		t.Fatal("the preview contains a secret environment value")
	}
	after, _ := e.m.store.get(context.Background(), rec.ID)
	if !after.VerifyOK || after.VerifiedAt == 0 {
		t.Fatalf("record %+v", after)
	}
	if a := e.auditOf("backup.verify"); len(a) != 1 || !a[0].Success {
		t.Fatalf("audit %+v", a)
	}
}
