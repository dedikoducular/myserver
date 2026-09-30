package backup

import (
	"archive/tar"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type route struct{ method, path string }

// Every route the module registers. TestRouteListIsComplete keeps it in
// step with Register.
func allRoutes(id int64, job string) []route {
	b := "/backups/" + itoa(id)
	return []route{
		{"GET", "/overview"}, {"GET", "/backups"}, {"POST", "/backups"}, {"DELETE", b}, {"GET", b + "/download"},
		{"POST", b + "/verify"}, {"POST", b + "/restore"}, {"POST", "/upload"}, {"POST", "/imports"}, {"GET", "/jobs"},
		{"GET", "/jobs/" + job}, {"POST", "/jobs/" + job + "/cancel"}, {"GET", "/stream"}, {"GET", "/schedules/fotolar"},
		{"PUT", "/schedules/fotolar"}, {"GET", "/encryption"}, {"PUT", "/encryption"}, {"DELETE", "/encryption"},
	}
}

//go:embed module.go
var moduleSource string

func TestRouteListIsComplete(t *testing.T) {
	src := moduleSource
	n := strings.Count(string(src), "\ta.Get(") + strings.Count(string(src), "\ta.Post(") + strings.Count(string(src), "\ta.Put(") +
		strings.Count(string(src), "\ta.Delete(")
	if n != len(allRoutes(1, "x")) {
		t.Fatalf("Register has %d routes, the test list %d", n, len(allRoutes(1, "x")))
	}
}

func TestEveryEndpointRefusesNonAdmins(t *testing.T) {
	e, rec := restoreEnv(t, nil)
	bodies := map[string]any{
		"/backups":           map[string]any{"slug": "fotolar"},
		"/imports":           map[string]any{"upload_id": strings.Repeat("a", 24), "passphrase": ""},
		"/encryption":        map[string]any{"passphrase": "yeni-parola-1234567"},
		"/schedules/fotolar": map[string]any{"enabled": true, "frequency": "daily", "hour": 3, "minute": 0, "weekday": 1, "monthday": 1, "keep_last": 1},
	}
	before := e.files()
	for _, r := range allRoutes(rec.ID, "yok") {
		var body any
		if r.method != "GET" && r.method != "DELETE" {
			body = bodies[r.path]
			if strings.HasSuffix(r.path, "/restore") {
				body = map[string]any{"confirm": "fotolar"}
			} else if body == nil {
				body = map[string]any{}
			}
		}
		if res := e.do(e.user, r.method, r.path, body); res.Status != 403 || res.code() != "forbidden" {
			t.Errorf("%s %s as an ordinary user: %d %s", r.method, r.path, res.Status, res.Body)
		}
		if res := e.do(nil, r.method, r.path, body); res.Status != 401 {
			t.Errorf("%s %s without a session: %d %s", r.method, r.path, res.Status, res.Body)
		}
		if r.method != "GET" {
			req := httptest.NewRequest(r.method, "http://"+testHost+"/api/v1/backup"+r.path, strings.NewReader("{}"))
			req.Header.Set("X-CSRF-Token", "yanlis")
			if res := e.serve(req, e.admin); res.Status != 403 || res.code() != "csrf_invalid" {
				t.Errorf("%s %s with a wrong CSRF token: %d %s", r.method, r.path, res.Status, res.Body)
			}
		}
	}
	if len(e.ev.all()) != 0 || len(e.m.jobs.list()) != 0 || fmt.Sprint(e.files()) != fmt.Sprint(before) ||
		len(e.records("")) != 1 {
		t.Fatalf("a refused request had an effect: events %q files %q", e.ev.all(), e.files())
	}
	if km, _, _ := e.m.keys.current(); km != nil {
		t.Fatal("a refused request set the encryption key")
	}
	if sc, _ := e.m.store.schedule(context.Background(), "fotolar"); sc != nil {
		t.Fatal("a refused request stored a schedule")
	}
	for _, a := range e.audit() {
		if strings.HasPrefix(a.Action, "backup.") {
			t.Fatalf("audit record of a refused request: %+v", a)
		}
	}
}

func TestBackupIDsAreNumbersOnly(t *testing.T) {
	e, rec := restoreEnv(t, nil)
	content, _ := os.ReadFile(filepath.Join(e.m.appDir("fotolar"), rec.FileName))
	ids := []string{"0", "-1", "abc", "1abc", "1.0", "1e0", "0x1", "99999999999999999999", "9223372036854775808", "%2e%2e", "%2e%2e%2f1",
		"1%2f..%2f1", "..%2f..%2fetc%2fpasswd", "%00", "1%00", "%20", "1%20", "%31%2f", rec.FileName, "fotolar", "fotolar%2f" + rec.FileName,
		"1;2", "1,2", "١"}
	for _, id := range ids {
		for _, r := range []route{{"GET", "/download"}, {"POST", "/verify"}, {"POST", "/restore"}, {"DELETE", ""}} {
			var body io.Reader
			if r.method == "POST" {
				body = strings.NewReader(`{"confirm":"fotolar"}`)
			}
			req := httptest.NewRequest(r.method, "http://"+testHost+"/api/v1/backup/backups/x"+r.path, body)
			req.URL.RawPath = "/api/v1/backup/backups/" + id + r.path
			if p, err := urlUnescape(req.URL.RawPath); err == nil {
				req.URL.Path = p
			} else {
				req.URL.Path = "/api/v1/backup/backups/" + id + r.path
			}
			res := e.serve(req, e.admin)
			if res.Status == 200 || res.Status == 202 || res.Status == 206 {
				t.Errorf("%s with id %q: %d", r.method+r.path, id, res.Status)
			}
			if res.Status >= 500 {
				t.Errorf("%s with id %q: status %d %s", r.method+r.path, id, res.Status, res.Body)
			}
			if bytes.Contains(res.Body, content[:20]) {
				t.Errorf("id %q delivered the archive", id)
			}
		}
	}
	if len(e.records("")) != 1 || len(e.m.jobs.list()) != 0 || len(e.ev.all()) != 0 {
		t.Fatalf("a request with a bad id had an effect: %q", e.ev.all())
	}
	if res := e.do(e.admin, "GET", "/backups/424242/download", nil); res.Status != 404 {
		t.Fatalf("unknown id: %d", res.Status)
	}
	// "+1" is the number one for strconv; it names the same record.
	if res := e.do(e.admin, "GET", "/backups/"+itoa(rec.ID)+"/download", nil); res.Status != 200 || !bytes.Equal(res.Body, content) {
		t.Fatalf("download: %d", res.Status)
	}
}

func urlUnescape(p string) (string, error) {
	req, err := http.NewRequest("GET", "http://x"+p, nil)
	if err != nil {
		return "", err
	}
	return req.URL.Path, nil
}

func TestTamperedDatabaseRecordsAreNotFollowed(t *testing.T) {
	const secret = "BASKA-YERDEKI-GIZLI-DOSYA"
	names := []string{
		"../parolalar/parolalar-20250101T000000Z.tar.gz",
		"../../gizli-20250101T000000Z.tar.gz",
		"alt/fotolar-20250101T000000Z.tar.gz",
		"./fotolar-20250101T000001Z.tar.gz",
		"/etc/passwd",
		"/etc/fotolar-20250101T000000Z.tar.gz",
		"..",
		".",
		"fotolar-20250101T000000Z.tar.gz/../../x",
		`..\parolalar\x-20250101T000000Z.tar.gz`,
		".fotolar-20250101T000000Z.tar.gz.tmp",
		"fotolar-20250101T000000Z.tar.gz\x00.txt",
		"fotolar-20250101T000000Z.tar.gz\n",
		"FOTOLAR-20250101T000000Z.tar.gz",
		"gizli.txt",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			e, rec := restoreEnv(t, nil)
			victim := filepath.Join(e.m.appDir("fotolar"), name)
			if strings.HasPrefix(name, "/") {
				victim = name
			}
			created := false
			if !strings.ContainsAny(name, "\x00") && name != ".." && name != "." && !strings.HasPrefix(name, "/etc/passwd") {
				if err := os.MkdirAll(filepath.Dir(victim), 0o755); err == nil {
					if err := os.WriteFile(victim, []byte(secret), 0o644); err == nil {
						created = true
						t.Cleanup(func() { os.Remove(victim) })
					}
				}
			}
			if _, err := e.db.Exec(`UPDATE backup_backups SET file_name = ? WHERE id = ?`, name, rec.ID); err != nil {
				t.Fatal(err)
			}
			id := itoa(rec.ID)
			for _, r := range []struct {
				method, path string
				body         any
			}{{"GET", "/download", nil}, {"POST", "/verify", map[string]any{}}, {"POST", "/restore", map[string]any{"confirm": "fotolar"}}} {
				res := e.do(e.admin, r.method, "/backups/"+id+r.path, r.body)
				if res.Status != 404 {
					t.Errorf("%s: %d %s", r.path, res.Status, res.Body)
				}
				if bytes.Contains(res.Body, []byte(secret)) || bytes.Contains(res.Body, []byte("root:")) {
					t.Fatalf("%s delivered a file outside the backup directory", r.path)
				}
				if cd := res.Header.Get("Content-Disposition"); cd != "" {
					t.Errorf("%s: Content-Disposition %q", r.path, cd)
				}
			}
			list := e.do(e.admin, "GET", "/backups", nil)
			var recs []Record
			json.Unmarshal(list.Data, &recs)
			if len(recs) != 1 || !recs[0].FileMissing {
				t.Errorf("the list does not mark the record as without file: %s", list.Data)
			}
			// Deleting removes the record and leaves the file alone.
			if res := e.do(e.admin, "DELETE", "/backups/"+id, nil); res.Status != 200 {
				t.Fatalf("delete: %d %s", res.Status, res.Body)
			}
			if len(e.records("")) != 0 {
				t.Fatal("the record is still there")
			}
			if created {
				if b, err := os.ReadFile(victim); err != nil || string(b) != secret {
					t.Fatalf("the file %q outside the application's backup folder was deleted or changed", victim)
				}
			}
			if _, err := os.Stat("/etc/passwd"); err != nil {
				t.Fatal("/etc/passwd is gone")
			}
			if len(mutating(e.ev.all())) != 0 {
				t.Fatalf("events %q", e.ev.all())
			}
		})
	}
	for _, slug := range []string{"../fotolar", "fotolar/..", "/etc", "..", "", "Fotolar", "fotolar\x00"} {
		t.Run("slug "+slug, func(t *testing.T) {
			e, rec := restoreEnv(t, nil)
			if _, err := e.db.Exec(`UPDATE backup_backups SET slug = ? WHERE id = ?`, slug, rec.ID); err != nil {
				t.Fatal(err)
			}
			for _, r := range []route{{"GET", "/download"}, {"POST", "/verify"}, {"POST", "/restore"}} {
				var body any
				if r.path == "/restore" {
					body = map[string]any{"confirm": slug}
				} else if r.method == "POST" {
					body = map[string]any{}
				}
				if res := e.do(e.admin, r.method, "/backups/"+itoa(rec.ID)+r.path, body); res.Status != 404 {
					t.Errorf("%s: %d %s", r.path, res.Status, res.Body)
				}
			}
			e.do(e.admin, "DELETE", "/backups/"+itoa(rec.ID), nil)
			if got := e.files(); len(got) != 1 {
				t.Fatalf("files after deleting the tampered record: %q", got)
			}
		})
	}
}

func TestArchiveThatIsASymlinkIsNotServed(t *testing.T) {
	e, rec := restoreEnv(t, nil)
	outside := filepath.Join(t.TempDir(), "gizli")
	os.WriteFile(outside, []byte("DISARIDAKI-DOSYA"), 0o644)
	p := filepath.Join(e.m.appDir("fotolar"), rec.FileName)
	os.Remove(p)
	if err := os.Symlink(outside, p); err != nil {
		t.Skip(err)
	}
	for _, r := range []route{{"GET", "/download"}, {"POST", "/verify"}, {"POST", "/restore"}} {
		var body any
		if r.path == "/restore" {
			body = map[string]any{"confirm": "fotolar"}
		} else if r.method == "POST" {
			body = map[string]any{}
		}
		res := e.do(e.admin, r.method, "/backups/"+itoa(rec.ID)+r.path, body)
		if res.Status != 404 || bytes.Contains(res.Body, []byte("DISARIDAKI")) {
			t.Fatalf("%s: %d %s", r.path, res.Status, res.Body)
		}
	}
	if res := e.do(e.admin, "DELETE", "/backups/"+itoa(rec.ID), nil); res.Status != 200 {
		t.Fatalf("delete: %d", res.Status)
	}
	if b, _ := os.ReadFile(outside); string(b) != "DISARIDAKI-DOSYA" {
		t.Fatal("the target of the symlink was deleted")
	}
	if _, err := os.Lstat(p); err == nil {
		t.Fatal("the symlink was not removed")
	}
}

func TestDownload(t *testing.T) {
	e, rec := restoreEnv(t, nil)
	content, _ := os.ReadFile(filepath.Join(e.m.appDir("fotolar"), rec.FileName))
	res := e.do(e.admin, "GET", "/backups/"+itoa(rec.ID)+"/download", nil)
	if res.Status != 200 || !bytes.Equal(res.Body, content) {
		t.Fatalf("download: %d, %d bytes", res.Status, len(res.Body))
	}
	if cd := res.Header.Get("Content-Disposition"); cd != `attachment; filename="`+rec.FileName+`"` {
		t.Fatalf("Content-Disposition %q", cd)
	}
	if strings.ContainsAny(rec.FileName, "\"\\/\r\n;") {
		t.Fatalf("file name %q needs quoting", rec.FileName)
	}
	if res.Header.Get("Content-Type") != "application/octet-stream" || res.Header.Get("X-Content-Type-Options") != "nosniff" ||
		res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("headers %v", res.Header)
	}
	if a := e.auditOf("backup.download"); len(a) != 1 || a[0].Detail != rec.FileName || a[0].User != "yonetici" {
		t.Fatalf("audit %+v", a)
	}
	// Records without a file.
	for _, status := range []string{statusFailed, statusCancelled, statusRunning} {
		e.db.Exec(`UPDATE backup_backups SET status = ? WHERE id = ?`, status, rec.ID)
		if res := e.do(e.admin, "GET", "/backups/"+itoa(rec.ID)+"/download", nil); res.Status != 409 {
			t.Fatalf("status %s: %d", status, res.Status)
		}
	}
	e.db.Exec(`UPDATE backup_backups SET status = 'success' WHERE id = ?`, rec.ID)
	os.Remove(filepath.Join(e.m.appDir("fotolar"), rec.FileName))
	if res := e.do(e.admin, "GET", "/backups/"+itoa(rec.ID)+"/download", nil); res.Status != 404 {
		t.Fatalf("missing file: %d", res.Status)
	}
}

func TestDelete(t *testing.T) {
	e, rec := restoreEnv(t, nil)
	other := e.putArchive("fotolar", (&blueprint{}).pack(t, nil), false)
	// Refused while a job of the application runs.
	reached, release := make(chan struct{}), make(chan struct{})
	e.docker.setHook("read", func(*http.Request) { close(reached); <-release })
	job, err := e.m.startBackup(context.Background(), backupOptions{Slug: "fotolar", Trigger: triggerManual, Actor: testActor})
	if err != nil {
		t.Fatal(err)
	}
	<-reached
	if res := e.do(e.admin, "DELETE", "/backups/"+itoa(rec.ID), nil); res.Status != 409 {
		t.Fatalf("delete during a job: %d %s", res.Status, res.Body)
	}
	close(release)
	e.wait(job.ID)

	if res := e.do(e.admin, "DELETE", "/backups/"+itoa(rec.ID), nil); res.Status != 200 {
		t.Fatalf("delete: %d %s", res.Status, res.Body)
	}
	if _, err := os.Stat(filepath.Join(e.m.appDir("fotolar"), rec.FileName)); err == nil {
		t.Fatal("the archive is still on disk")
	}
	if _, err := os.Stat(filepath.Join(e.m.appDir("fotolar"), other.FileName)); err != nil {
		t.Fatal("another archive was deleted")
	}
	if got, _ := e.m.store.get(context.Background(), rec.ID); got != nil {
		t.Fatal("the record is still there")
	}
	if res := e.do(e.admin, "DELETE", "/backups/"+itoa(rec.ID), nil); res.Status != 404 {
		t.Fatalf("second delete: %d", res.Status)
	}
	if a := e.auditOf("backup.delete"); len(a) != 1 || !a[0].Success || a[0].Detail != rec.FileName {
		t.Fatalf("audit %+v", a)
	}
}

/* ---------- upload and import ---------- */

func (e *testEnv) incoming() []string {
	entries, _ := os.ReadDir(e.m.incomingDir())
	var out []string
	for _, en := range entries {
		out = append(out, en.Name())
	}
	return out
}

func uploadID(t *testing.T, res *apiResponse) uploadResult {
	t.Helper()
	if res.Status != 200 {
		t.Fatalf("upload: %d %s", res.Status, res.Body)
	}
	var u uploadResult
	json.Unmarshal(res.Data, &u)
	if !uploadIDRe.MatchString(u.UploadID) {
		t.Fatalf("upload id %q", u.UploadID)
	}
	return u
}

func TestUploadOfFilesThatAreNotBackups(t *testing.T) {
	e := newEnv(t)
	for name, data := range map[string][]byte{
		"text":               []byte("merhaba, ben bir yedek değilim"),
		"empty":              {},
		"zip":                []byte("PK\x03\x04................................"),
		"elf":                []byte("\x7fELF\x02\x01\x01................................"),
		"shell script":       []byte("#!/bin/sh\nrm -rf /\n"),
		"one byte":           {0x1f},
		"magic, bad header":  append([]byte(encMagic), bytes.Repeat([]byte{0xff}, 200)...),
		"magic, short":       []byte(encMagic + "\x01\x01"),
		"hostile kdf memory": append(craftHeader(3, 1<<32-1, 4, encChunkSize), make([]byte, 100)...),
		"hostile kdf time":   append(craftHeader(1<<31, 64*1024, 4, encChunkSize), make([]byte, 100)...),
		"hostile chunk size": append(craftHeader(3, 64*1024, 4, 1<<31), make([]byte, 100)...),
	} {
		start := time.Now()
		res := e.upload(e.admin, data)
		if res.Status != 400 || res.code() != "invalid_backup" {
			t.Errorf("%s: %d %s", name, res.Status, res.Body)
		}
		if time.Since(start) > 2*time.Second {
			t.Errorf("%s: refusal took %v", name, time.Since(start))
		}
		if left := e.incoming(); len(left) != 0 {
			t.Fatalf("%s: left behind %q", name, left)
		}
	}
	// No file field at all.
	req := httptest.NewRequest("POST", "http://"+testHost+"/api/v1/backup/upload", strings.NewReader("--x\r\nContent-Disposition: form-data; name=\"baska\"\r\n\r\nveri\r\n--x--\r\n"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	if res := e.serve(req, e.admin); res.Status != 400 {
		t.Fatalf("no file field: %d", res.Status)
	}
	req = httptest.NewRequest("POST", "http://"+testHost+"/api/v1/backup/upload", strings.NewReader(`{"file":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	if res := e.serve(req, e.admin); res.Status != 400 {
		t.Fatalf("json body: %d", res.Status)
	}
	if left := e.incoming(); len(left) != 0 {
		t.Fatalf("left behind %q", left)
	}
	if len(e.records("")) != 0 || len(e.files()) != 0 {
		t.Fatalf("files %q", e.files())
	}
}

func TestImportRejectsBadArchivesAndKeepsNothing(t *testing.T) {
	valid := (&blueprint{}).pack(t, nil)
	gz := func(b []byte) []byte {
		return packEntries(t, nil, []rawEntry{{name: "notlar.txt", data: b}})
	}
	cases := map[string][]byte{
		"gzip of something else": gz([]byte("x")),
		"truncated":              valid[:len(valid)-30],
		"truncated to half":      valid[:len(valid)/2],
		"trailing data":          append(bytes.Clone(valid), valid...),
		"slug differs from the content": func() []byte {
			man, entries := (&blueprint{}).build(t)
			man.App.Slug = "parolalar"
			return packEntries(t, nil, append(entries, manifestEntry(t, man)))
		}(),
		"hostile entry": (&blueprint{streams: []streamSpec{{kind: kindVolume, source: "myserver-fotolar-data",
			tar: endOfTar(rawFile("../../etc/cron.d/x", "x"))}}}).pack(t, nil),
		"two step symlink": (&blueprint{streams: []streamSpec{{kind: kindVolume, source: "myserver-fotolar-data",
			tar: endOfTar(rawHeader("s", tar.TypeSymlink, 0, "/etc"), rawFile("s/x", "x"))}}}).pack(t, nil),
		"bind outside the allowed roots": func() []byte {
			cfg := testConfig("fotolar")
			cfg.Services[0].Volumes[0].Type, cfg.Services[0].Volumes[0].Source = "bind", "/etc/cron.d"
			return (&blueprint{cfg: cfg, streams: []streamSpec{{kind: kindBind, source: "/etc/cron.d", tar: simpleVolume(t)}}}).pack(t, nil)
		}(),
		"unknown format version": func() []byte {
			man, entries := (&blueprint{}).build(t)
			man.FormatVersion = 99
			return packEntries(t, nil, append(entries, manifestEntry(t, man)))
		}(),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			u := uploadID(t, e.upload(e.admin, data))
			if len(e.incoming()) != 1 {
				t.Fatalf("staged files %q", e.incoming())
			}
			v := e.wait(e.jobFrom(e.do(e.admin, "POST", "/imports", map[string]any{"upload_id": u.UploadID})).ID)
			if v.Status != statusFailed || v.Error == "" || strings.HasPrefix(v.Error, "Beklenmeyen") {
				t.Fatalf("job: %s %q", v.Status, v.Error)
			}
			if left := e.incoming(); len(left) != 0 {
				t.Fatalf("the rejected upload was kept: %q", left)
			}
			if len(e.records("")) != 0 || len(e.files()) != 0 {
				t.Fatalf("records %d files %q", len(e.records("")), e.files())
			}
			if len(mutating(e.ev.all())) != 0 {
				t.Fatalf("events %q", e.ev.all())
			}
			if a := e.auditOf("backup.import"); len(a) != 1 || a[0].Success {
				t.Fatalf("audit %+v", a)
			}
			if _, err := os.Stat("/etc/cron.d/x"); err == nil {
				t.Fatal("/etc/cron.d/x exists")
			}
		})
	}
}

func TestImportOfAValidArchive(t *testing.T) {
	e := newEnv(t)
	data := (&blueprint{}).pack(t, nil)
	u := uploadID(t, e.upload(e.admin, data))
	if u.Encrypted || u.NeedsPassphrase || u.Size != int64(len(data)) {
		t.Fatalf("upload result %+v", u)
	}
	fi, _ := os.Stat(e.m.stagedPath(u.UploadID))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("staged file mode %v", fi.Mode())
	}
	v := e.wait(e.jobFrom(e.do(e.admin, "POST", "/imports", map[string]any{"upload_id": u.UploadID})).ID)
	if v.Status != statusSuccess || v.Slug != "fotolar" {
		t.Fatalf("job: %+v", v)
	}
	recs := e.records("fotolar")
	if len(recs) != 1 || recs[0].Trigger != triggerImported || recs[0].Status != statusSuccess || !recs[0].VerifyOK ||
		recs[0].CreatedAt != 1_750_000_000 || recs[0].Size != int64(len(data)) || !fileNameRe.MatchString(recs[0].FileName) {
		t.Fatalf("record %+v", recs[0])
	}
	e.wantFiles("fotolar/" + recs[0].FileName)
	fi, _ = os.Stat(filepath.Join(e.m.dir, "fotolar", recs[0].FileName))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("imported file mode %v", fi.Mode())
	}
	// The same upload cannot be imported twice.
	if res := e.do(e.admin, "POST", "/imports", map[string]any{"upload_id": u.UploadID}); res.Status != 404 {
		t.Fatalf("second import: %d", res.Status)
	}
	// An orphan: the application is not installed, the backup is listed.
	ov := e.do(e.admin, "GET", "/overview", nil)
	var o overview
	json.Unmarshal(ov.Data, &o)
	if len(o.Apps) != 0 || len(o.Orphans) != 1 || o.Orphans[0].Slug != "fotolar" {
		t.Fatalf("overview %s", ov.Data)
	}
	if !strings.Contains(string(ov.Data), `"apps":[]`) {
		t.Fatalf("empty lists must be [] in JSON: %s", ov.Data)
	}
}

func TestImportUploadIDs(t *testing.T) {
	e := newEnv(t)
	os.MkdirAll(e.m.incomingDir(), 0o700)
	victim := filepath.Join(e.m.dir, "kurban.upload")
	os.WriteFile(victim, (&blueprint{}).pack(t, nil), 0o600)
	for _, id := range []string{"", "../kurban", "..%2fkurban", "kurban", strings.Repeat("a", 23), strings.Repeat("a", 25),
		strings.Repeat("A", 24), strings.Repeat("g", 24), strings.Repeat("a", 22) + "/.", "../" + strings.Repeat("a", 21),
		strings.Repeat("a", 24) + "\n", strings.Repeat("a", 24) + ".upload"} {
		if res := e.do(e.admin, "POST", "/imports", map[string]any{"upload_id": id}); res.Status != 400 {
			t.Errorf("upload id %q: %d %s", id, res.Status, res.Body)
		}
	}
	if res := e.do(e.admin, "POST", "/imports", map[string]any{"upload_id": strings.Repeat("a", 24)}); res.Status != 404 {
		t.Fatalf("unknown upload: %d", res.Status)
	}
	// A directory or symlink in place of the staged file.
	id := strings.Repeat("b", 24)
	os.Symlink(victim, e.m.stagedPath(id))
	if res := e.do(e.admin, "POST", "/imports", map[string]any{"upload_id": id}); res.Status != 404 {
		t.Fatalf("symlink as upload: %d %s", res.Status, res.Body)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatal("the file outside the staging folder is gone")
	}
	if len(e.m.jobs.list()) != 0 {
		t.Fatal("a job was started")
	}
}

func TestImportOfEncryptedArchives(t *testing.T) {
	foreign := keyFor(t, testPass)
	data := (&blueprint{}).pack(t, foreign)

	t.Run("key not on this server", func(t *testing.T) {
		e := newEnv(t)
		u := uploadID(t, e.upload(e.admin, data))
		if !u.Encrypted || !u.NeedsPassphrase {
			t.Fatalf("upload result %+v", u)
		}
		res := e.do(e.admin, "POST", "/imports", map[string]any{"upload_id": u.UploadID})
		if res.Status != 400 || res.code() != "passphrase_required" {
			t.Fatalf("no passphrase: %d %s", res.Status, res.Body)
		}
		res = e.do(e.admin, "POST", "/imports", map[string]any{"upload_id": u.UploadID, "passphrase": "yanlis-parola-12345"})
		if res.Status != 400 || res.code() != "wrong_passphrase" {
			t.Fatalf("wrong passphrase: %d %s", res.Status, res.Body)
		}
		// The upload is kept for another attempt.
		if len(e.incoming()) != 1 {
			t.Fatalf("staged %q", e.incoming())
		}
		v := e.wait(e.jobFrom(e.do(e.admin, "POST", "/imports", map[string]any{"upload_id": u.UploadID, "passphrase": testPass})).ID)
		if v.Status != statusSuccess {
			t.Fatalf("job: %s %q", v.Status, v.Error)
		}
		rec := e.records("fotolar")[0]
		if !rec.Encrypted || !strings.HasSuffix(rec.FileName, ".tar.gz.enc") {
			t.Fatalf("record %+v", rec)
		}
		// The file is stored as it came: still encrypted with its own key.
		stored, _ := os.ReadFile(filepath.Join(e.m.dir, "fotolar", rec.FileName))
		if !bytes.Equal(stored, data) {
			t.Fatal("the imported file differs from the upload")
		}
	})
	t.Run("key of this server", func(t *testing.T) {
		e := newEnv(t)
		e.m.keys.loaded, e.m.keys.km = true, foreign
		u := uploadID(t, e.upload(e.admin, data))
		if !u.Encrypted || u.NeedsPassphrase {
			t.Fatalf("upload result %+v", u)
		}
		res := e.do(e.admin, "POST", "/imports", map[string]any{"upload_id": u.UploadID, "passphrase": "yanlis-parola-12345"})
		if res.Status != 400 || res.code() != "wrong_passphrase" {
			t.Fatalf("wrong passphrase with the key on the server: %d %s", res.Status, res.Body)
		}
		v := e.wait(e.jobFrom(e.do(e.admin, "POST", "/imports", map[string]any{"upload_id": u.UploadID, "passphrase": ""})).ID)
		if v.Status != statusSuccess {
			t.Fatalf("job: %s %q", v.Status, v.Error)
		}
	})
	t.Run("damaged", func(t *testing.T) {
		e := newEnv(t)
		bad := bytes.Clone(data)
		bad[len(bad)-5] ^= 1
		u := uploadID(t, e.upload(e.admin, bad))
		v := e.wait(e.jobFrom(e.do(e.admin, "POST", "/imports", map[string]any{"upload_id": u.UploadID, "passphrase": testPass})).ID)
		if v.Status != statusFailed || !strings.Contains(v.Error, "bozuk") {
			t.Fatalf("job: %s %q", v.Status, v.Error)
		}
		if len(e.incoming()) != 0 || len(e.files()) != 0 {
			t.Fatalf("left behind: %q %q", e.incoming(), e.files())
		}
	})
}

func TestImportClampsHostileManifestValues(t *testing.T) {
	e := newEnv(t)
	man, entries := (&blueprint{}).build(t)
	man.CreatedAt = 1 << 40
	for i := 0; i < 200; i++ {
		man.Warnings = append(man.Warnings, strings.Repeat("uyarı ", 1000))
	}
	u := uploadID(t, e.upload(e.admin, packEntries(t, nil, append(entries, manifestEntry(t, man)))))
	v := e.wait(e.jobFrom(e.do(e.admin, "POST", "/imports", map[string]any{"upload_id": u.UploadID})).ID)
	if v.Status != statusSuccess {
		t.Fatalf("job: %s %q", v.Status, v.Error)
	}
	rec := e.records("fotolar")[0]
	if rec.CreatedAt > time.Now().Unix()+5 || len(rec.Warnings) > 50 || len(rec.Warnings[0]) > 500 {
		t.Fatalf("record created_at=%d warnings=%d", rec.CreatedAt, len(rec.Warnings))
	}
}

func TestUploadSizeLimit(t *testing.T) {
	t.Run("declared length", func(t *testing.T) {
		e := newEnv(t)
		req := httptest.NewRequest("POST", "http://"+testHost+"/api/v1/backup/upload", strings.NewReader("x"))
		req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
		req.ContentLength = 1 << 50
		res := e.serve(req, e.admin)
		if res.Status != http.StatusInsufficientStorage || res.code() != "no_space" {
			t.Fatalf("%d %s", res.Status, res.Body)
		}
		if len(e.incoming()) != 0 {
			t.Fatalf("staged %q", e.incoming())
		}
	})
	t.Run("streamed beyond the free space", func(t *testing.T) {
		small := os.Getenv("MSTEST_SMALLFS")
		if small == "" {
			t.Skip("MSTEST_SMALLFS is not set (needs a small tmpfs)")
		}
		dir := filepath.Join(small, "upload")
		os.MkdirAll(dir, 0o700)
		defer os.RemoveAll(dir)
		e := newEnvIn(t, dir)
		free, _, err := diskSpace(dir)
		if err != nil || free > 200<<20 {
			t.Skipf("file system too large: %d (%v)", free, err)
		}
		// More than free space minus the reserve, sent without a length.
		size := int64(free) - 64<<20 + 4<<20
		if size <= 0 {
			t.Fatalf("free space %d is below the reserve", free)
		}
		pr, pw := io.Pipe()
		go func() {
			fmt.Fprint(pw, "--x\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.tar.gz\"\r\n\r\n")
			pw.Write([]byte{0x1f, 0x8b})
			io.Copy(pw, newPattern(3, size))
			fmt.Fprint(pw, "\r\n--x--\r\n")
			pw.Close()
		}()
		req := httptest.NewRequest("POST", "http://"+testHost+"/api/v1/backup/upload", pr)
		req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
		req.ContentLength = -1
		res := e.serve(req, e.admin)
		if res.Status != http.StatusInsufficientStorage {
			t.Fatalf("%d %s", res.Status, res.Body)
		}
		if len(e.incoming()) != 0 {
			t.Fatalf("the oversized upload was kept: %q", e.incoming())
		}
		after, _, _ := diskSpace(dir)
		if after+8<<20 < free {
			t.Fatalf("free space went from %d to %d", free, after)
		}
	})
}

/* ---------- secrets ---------- */

func TestPassphraseNeverLeavesTheRequest(t *testing.T) {
	const (
		serverPass = "SUNUCU-parolasi-Xq7"
		oldPass    = "ESKI-yedek-parolasi-Zk3"
		wrongPass  = "YANLIS-girilen-parola-Wm9"
		envSecret  = "cok-gizli-veritabani-parolasi"
	)
	e := newEnv(t)
	e.standardApp(true)
	oldKey := keyFor(t, oldPass)
	old := e.putArchive("fotolar", (&blueprint{}).pack(t, oldKey), true)

	if res := e.do(e.admin, "PUT", "/encryption", map[string]any{"passphrase": serverPass}); res.Status != 200 {
		t.Fatalf("set: %d %s", res.Status, res.Body)
	}
	if res := e.do(e.admin, "PUT", "/encryption", map[string]any{"passphrase": "kisa"}); res.Status != 400 {
		t.Fatalf("short passphrase: %d", res.Status)
	}
	v := e.wait(e.jobFrom(e.do(e.admin, "POST", "/backups", map[string]any{"slug": "fotolar"})).ID)
	if v.Status != statusSuccess {
		t.Fatalf("backup: %s %q", v.Status, v.Error)
	}
	id := "/backups/" + itoa(old.ID)
	e.do(e.admin, "POST", id+"/verify", map[string]any{"passphrase": wrongPass})
	e.do(e.admin, "POST", id+"/restore", map[string]any{"confirm": "fotolar", "passphrase": wrongPass})
	e.do(e.admin, "POST", id+"/verify", map[string]any{"passphrase": serverPass})
	if v := e.wait(e.jobFrom(e.do(e.admin, "POST", id+"/verify", map[string]any{"passphrase": oldPass})).ID); v.Status != statusSuccess {
		t.Fatalf("verify: %s %q", v.Status, v.Error)
	}
	if v := e.wait(e.jobFrom(e.do(e.admin, "POST", id+"/restore", map[string]any{"confirm": "fotolar", "passphrase": oldPass})).ID); v.Status != statusSuccess {
		t.Fatalf("restore: %s %q", v.Status, v.Error)
	}
	u := uploadID(t, e.upload(e.admin, (&blueprint{cfg: testConfig("baska")}).pack(t, oldKey)))
	e.do(e.admin, "POST", "/imports", map[string]any{"upload_id": u.UploadID, "passphrase": wrongPass})
	if v := e.wait(e.jobFrom(e.do(e.admin, "POST", "/imports", map[string]any{"upload_id": u.UploadID, "passphrase": oldPass})).ID); v.Status != statusSuccess {
		t.Fatalf("import: %s %q", v.Status, v.Error)
	}
	// A failing job, a malformed request and every read endpoint.
	e.docker.setFail("read", 500)
	e.wait(e.jobFrom(e.do(e.admin, "POST", "/backups", map[string]any{"slug": "fotolar"})).ID)
	e.do(e.admin, "PUT", "/encryption", `{"passphrase":"`+wrongPass+`","fazladan":1}`)
	e.do(e.admin, "POST", id+"/verify", `{"passphrase":"`+wrongPass+`"`)
	for _, p := range []string{"/overview", "/backups", "/jobs", "/encryption", "/schedules/fotolar"} {
		if res := e.do(e.admin, "GET", p, nil); res.Status != 200 {
			t.Fatalf("GET %s: %d", p, res.Status)
		}
	}
	e.do(e.admin, "DELETE", "/encryption", nil)

	haystacks := map[string]string{"panel log": e.logs.String()}
	for i, a := range e.audit() {
		haystacks[fmt.Sprintf("audit record %d (%s)", i, a.Action)] = fmt.Sprintf("%+v", a)
	}
	haystacks["notifications"] = strings.Join(e.notifications(), "\n")
	for i, r := range e.records("") {
		raw, _ := json.Marshal(r)
		haystacks[fmt.Sprintf("backup record %d", i)] = string(raw) + r.Error
	}
	e.respMu.Lock()
	for i, r := range e.responses {
		haystacks[fmt.Sprintf("response %d", i)] = r
	}
	e.respMu.Unlock()
	if len(e.logs.String()) == 0 || len(e.audit()) < 8 {
		t.Fatalf("the test captured too little: %d log bytes, %d audit records", len(e.logs.String()), len(e.audit()))
	}
	for where, text := range haystacks {
		for _, secret := range []string{serverPass, oldPass, wrongPass, envSecret} {
			if strings.Contains(text, secret) {
				t.Errorf("%s contains %q:\n%.300s", where, secret, text)
			}
		}
	}
}

func TestEncryptionEndpoints(t *testing.T) {
	e := newEnv(t)
	e.standardApp(false)
	var info encryptionInfo
	res := e.do(e.admin, "GET", "/encryption", nil)
	json.Unmarshal(res.Data, &info)
	if info.Enabled || info.Cipher != "AES-256-GCM" || info.KDF != "Argon2id" || info.Error != "" {
		t.Fatalf("info %+v", info)
	}
	for _, bad := range []any{"", "onbir-karak", strings.Repeat("a", 257), "a\x00bcdefghijklmnop", 12345678901234} {
		if res := e.do(e.admin, "PUT", "/encryption", map[string]any{"passphrase": bad}); res.Status != 400 {
			t.Errorf("passphrase %v: %d", bad, res.Status)
		}
	}
	if _, err := os.Stat(e.m.keys.path); err == nil {
		t.Fatal("a key file was written for a refused passphrase")
	}
	// Twelve characters, not twelve bytes.
	if res := e.do(e.admin, "PUT", "/encryption", map[string]any{"passphrase": "şşşşşşğğğğğğ"}); res.Status != 200 {
		t.Fatalf("12 characters: %d %s", res.Status, res.Body)
	}
	json.Unmarshal(res.Data, &info)
	res = e.do(e.admin, "GET", "/encryption", nil)
	json.Unmarshal(res.Data, &info)
	if !info.Enabled || info.SetAt == 0 {
		t.Fatalf("info %+v", info)
	}
	if strings.Contains(string(res.Data), "kek") || strings.Contains(string(res.Data), "salt") {
		t.Fatalf("the API returns key material: %s", res.Data)
	}
	// Not while a job runs.
	reached, release := make(chan struct{}), make(chan struct{})
	e.docker.setHook("read", func(*http.Request) { close(reached); <-release })
	job, _ := e.m.startBackup(context.Background(), backupOptions{Slug: "fotolar", Trigger: triggerManual, Actor: testActor})
	<-reached
	before, _ := os.ReadFile(e.m.keys.path)
	if res := e.do(e.admin, "PUT", "/encryption", map[string]any{"passphrase": "baska-bir-parola-12"}); res.Status != 409 {
		t.Fatalf("set during a job: %d", res.Status)
	}
	if res := e.do(e.admin, "DELETE", "/encryption", nil); res.Status != 409 {
		t.Fatalf("clear during a job: %d", res.Status)
	}
	after, _ := os.ReadFile(e.m.keys.path)
	if !bytes.Equal(before, after) {
		t.Fatal("the key changed during a job")
	}
	close(release)
	if v := e.wait(job.ID); v.Status != statusSuccess || !e.records("fotolar")[0].Encrypted {
		t.Fatalf("job %s", v.Status)
	}
	if res := e.do(e.admin, "DELETE", "/encryption", nil); res.Status != 200 {
		t.Fatalf("clear: %d", res.Status)
	}
	if _, err := os.Stat(e.m.keys.path); err == nil {
		t.Fatal("key file still exists")
	}
	if a := e.auditOf("backup.encryption_set"); len(a) != 1 || a[0].Detail != "" {
		t.Fatalf("audit %+v", a)
	}
}

func TestCreateAndScheduleValidation(t *testing.T) {
	e := newEnv(t)
	e.standardApp(false)
	for _, body := range []string{`{}`, `{"slug":""}`, `{"slug":"../x"}`, `{"slug":"Fotolar"}`, `{"slug":"fotolar","bilinmeyen":1}`, `{"slug":["fotolar"]}`, `x`} {
		if res := e.do(e.admin, "POST", "/backups", body); res.Status != 400 {
			t.Errorf("create %s: %d", body, res.Status)
		}
	}
	if res := e.do(e.admin, "POST", "/backups", `{"slug":"yok"}`); res.Status != 404 {
		t.Errorf("create for an unknown application: %d", res.Status)
	}
	if len(e.records("")) != 0 {
		t.Fatal("records were created")
	}
	good := map[string]any{"enabled": true, "frequency": "weekly", "hour": 2, "minute": 30, "weekday": 0, "monthday": 31, "keep_last": 3,
		"prune_manual": false, "live": false, "include_binds": true}
	for field, values := range map[string][]any{
		"frequency": {"", "hourly", "DAILY", 1},
		"hour":      {-1, 24, "3", 1.5},
		"minute":    {-1, 60},
		"weekday":   {-1, 7},
		"monthday":  {0, 32, -1},
		"keep_last": {0, -1, 366},
	} {
		for _, v := range values {
			body := map[string]any{}
			for k, x := range good {
				body[k] = x
			}
			body[field] = v
			if res := e.do(e.admin, "PUT", "/schedules/fotolar", body); res.Status != 400 {
				t.Errorf("schedule with %s=%v: %d", field, v, res.Status)
			}
		}
	}
	if sc, _ := e.m.store.schedule(context.Background(), "fotolar"); sc != nil {
		t.Fatal("an invalid schedule was stored")
	}
	if res := e.do(e.admin, "PUT", "/schedules/yok", good); res.Status != 404 {
		t.Errorf("schedule for an unknown application: %d", res.Status)
	}
	for _, slug := range []string{"..", "%2e%2e", "Fotolar", "a%2fb"} {
		if res := e.do(e.admin, "GET", "/schedules/"+slug, nil); res.Status == 200 {
			t.Errorf("schedule of %q: %d", slug, res.Status)
		}
	}
	res := e.do(e.admin, "PUT", "/schedules/fotolar", good)
	var sc Schedule
	json.Unmarshal(res.Data, &sc)
	if res.Status != 200 || !sc.Enabled || !sc.Exists || sc.NextRunAt <= time.Now().Unix() || sc.NextRunAt > time.Now().Unix()+8*86400 {
		t.Fatalf("schedule %d %+v", res.Status, sc)
	}
	res = e.do(e.admin, "GET", "/schedules/baska", nil)
	json.Unmarshal(res.Data, &sc)
	if sc.Exists || sc.Enabled || sc.NextRunAt != 0 || sc.KeepLast != 7 || sc.Frequency != "daily" {
		t.Fatalf("defaults %+v", sc)
	}
}

func TestOverviewWithoutApps(t *testing.T) {
	e := newEnv(t)
	res := e.do(e.admin, "GET", "/overview", nil)
	var o overview
	if err := json.Unmarshal(res.Data, &o); err != nil || res.Status != 200 {
		t.Fatal(err)
	}
	if !o.AppsAvailable || len(o.Apps) != 0 || o.Dir != e.m.dir || o.FreeBytes == nil {
		t.Fatalf("overview %s", res.Data)
	}
	for _, field := range []string{`"apps":[]`, `"orphans":[]`, `"jobs":[]`} {
		if !strings.Contains(string(res.Data), field) {
			t.Errorf("overview lacks %s: %s", field, res.Data)
		}
	}
	e.m.SetApps(nil)
	res = e.do(e.admin, "GET", "/overview", nil)
	json.Unmarshal(res.Data, &o)
	if res.Status != 200 || o.AppsAvailable || o.AppsError == "" {
		t.Fatalf("overview without the application module: %d %s", res.Status, res.Data)
	}
	if res := e.do(e.admin, "POST", "/backups", `{"slug":"fotolar"}`); res.Status != 503 || res.code() != "apps_unavailable" {
		t.Fatalf("create without the application module: %d %s", res.Status, res.Body)
	}
}
