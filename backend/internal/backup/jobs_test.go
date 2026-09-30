package backup

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"myserver/internal/apps"
)

func TestOneJobPerAppAtATime(t *testing.T) {
	e, rec := restoreEnv(t, nil)
	other := testConfig("parolalar")
	e.apps.install(other, false)
	e.docker.addVolume("myserver-parolalar-data", "parolalar", tarFile{name: "kasa", body: "x"})

	reached, release := make(chan struct{}), make(chan struct{})
	first := true
	e.docker.setHook("read", func(*http.Request) {
		if first {
			first = false
			close(reached)
			<-release
		}
	})
	job := e.jobFrom(e.do(e.admin, "POST", "/backups", map[string]any{"slug": "fotolar"}))
	<-reached

	id := "/backups/" + itoa(rec.ID)
	for name, res := range map[string]*apiResponse{
		"backup":  e.do(e.admin, "POST", "/backups", map[string]any{"slug": "fotolar"}),
		"restore": e.do(e.admin, "POST", id+"/restore", map[string]any{"confirm": "fotolar"}),
		"verify":  e.do(e.admin, "POST", id+"/verify", map[string]any{}),
	} {
		if res.Status != 409 || !strings.Contains(res.message(), "başka bir") {
			t.Errorf("second job (%s) for the same application: %d %s", name, res.Status, res.Body)
		}
	}
	if n := len(e.m.jobs.list()); n != 1 {
		t.Fatalf("%d jobs", n)
	}
	// Another application is not held up.
	v := e.wait(e.jobFrom(e.do(e.admin, "POST", "/backups", map[string]any{"slug": "parolalar"})).ID)
	if v.Status != statusSuccess {
		t.Fatalf("job of the other application: %s %q", v.Status, v.Error)
	}
	list := e.do(e.admin, "GET", "/jobs", nil)
	if !strings.Contains(string(list.Data), job.ID) || !strings.Contains(string(list.Data), v.ID) {
		t.Fatalf("job list %s", list.Data)
	}
	close(release)
	if v := e.wait(job.ID); v.Status != statusSuccess {
		t.Fatalf("job: %s %q", v.Status, v.Error)
	}
	// The lock is free again, also after a failure and after a panic.
	e.docker.setFail("read", 500)
	if v := e.wait(e.jobFrom(e.do(e.admin, "POST", "/backups", map[string]any{"slug": "fotolar"})).ID); v.Status != statusFailed {
		t.Fatalf("job: %s", v.Status)
	}
	view, err := e.m.launch("fotolar", &Job{Kind: jobVerify, Slug: "fotolar"}, func(context.Context, *Job) error { panic("beklenmeyen") })
	if err != nil {
		t.Fatal(err)
	}
	if v := e.wait(view.ID); v.Status != statusFailed || strings.Contains(v.Error, "beklenmeyen") && !strings.HasPrefix(v.Error, "Beklenmeyen") {
		t.Fatalf("job after a panic: %s %q", v.Status, v.Error)
	}
	e.docker.setFail("read", 0)
	if v := e.wait(e.jobFrom(e.do(e.admin, "POST", "/backups", map[string]any{"slug": "fotolar"})).ID); v.Status != statusSuccess {
		t.Fatalf("job after the lock was released: %s %q", v.Status, v.Error)
	}
	if res := e.do(e.admin, "GET", "/jobs/yok", nil); res.Status != 404 {
		t.Fatalf("unknown job: %d", res.Status)
	}
}

func TestJobLogIsBounded(t *testing.T) {
	jm := newJobManager()
	j := &Job{Kind: jobBackup}
	if _, _, ok := jm.create(context.Background(), "x", j); !ok {
		t.Fatal("create")
	}
	for i := 0; i < maxJobLogs*3; i++ {
		j.Log("satır")
	}
	if n := len(j.view().Logs); n != maxJobLogs {
		t.Fatalf("%d log lines", n)
	}
}

// startModule runs Start until the recovery is over and stops it again.
func (e *testEnv) recoverNow() {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.m.recoverState(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		e.t.Fatal("recovery did not end")
	}
	e.t.Cleanup(cancel)
}

func TestInterruptedJobsAreRecoveredAtStart(t *testing.T) {
	e := newEnv(t)
	e.standardApp(false) // stopped: the interrupted backup had stopped it
	other := testConfig("parolalar")
	e.apps.install(other, false)
	ctx := context.Background()

	stopped := &Record{Slug: "fotolar", AppName: "Fotolar", CreatedAt: time.Now().Unix(), Status: statusRunning,
		Consistency: consistencyStopped, Trigger: triggerScheduled, AppWasRunning: true}
	stopped.ID, _ = e.m.store.insert(ctx, stopped)
	e.m.store.setAppWasRunning(ctx, stopped.ID, true)
	untouched := &Record{Slug: "parolalar", AppName: "Parolalar", CreatedAt: time.Now().Unix(), Status: statusRunning,
		Consistency: consistencyLive, Trigger: triggerManual}
	untouched.ID, _ = e.m.store.insert(ctx, untouched)
	good := e.seed(triggerManual, statusSuccess, time.Now().Add(-time.Hour))

	// What the crash left on disk.
	dir := e.m.appDir("fotolar")
	tmp := filepath.Join(dir, ".fotolar-20260101T000000Z.tar.gz.tmp")
	os.WriteFile(tmp, []byte("yarım"), 0o600)
	os.MkdirAll(e.m.incomingDir(), 0o700)
	staged := e.m.stagedPath(strings.Repeat("a", 24))
	os.WriteFile(staged, []byte("yüklenmiş"), 0o600)
	// And in Docker: helpers of the crashed run beside other containers.
	e.docker.mu.Lock()
	for name, labels := range map[string]map[string]string{
		"myserver-backup-helper-aaaa": {apps.LabelManaged: "true", labelRole: roleHelper},
		"myserver-backup-helper-bbbb": {apps.LabelManaged: "true", labelRole: roleHelper},
		"myserver-fotolar":            {apps.LabelManaged: "true", apps.LabelApp: "fotolar"},
		"baska-bir-yardimci":          {labelRole: roleHelper},
		"kullanicinin-konteyneri":     {"com.example": "x"},
		"myserver-updates-helper":     {apps.LabelManaged: "true", labelRole: "updates-helper"},
	} {
		e.docker.containers[name] = &fakeContainer{id: name, name: name, labels: labels, state: "created"}
	}
	e.docker.mu.Unlock()

	e.recoverNow()
	e.waitFor("the application to be started", func() bool { return e.ev.count("apps.start fotolar") == 1 })

	left := strings.Join(e.docker.liveContainers(), " ")
	if strings.Contains(left, "myserver-backup-helper") {
		t.Errorf("helper containers were left: %s", left)
	}
	for _, keep := range []string{"myserver-fotolar", "baska-bir-yardimci", "kullanicinin-konteyneri", "myserver-updates-helper"} {
		if !strings.Contains(" "+left+" ", " "+keep+" ") {
			t.Errorf("the container %q was removed", keep)
		}
	}
	if e.ev.count("docker.remove") != 2 {
		t.Errorf("events %q", e.ev.all())
	}
	for _, id := range []int64{stopped.ID, untouched.ID} {
		r, _ := e.m.store.get(ctx, id)
		if r.Status != statusFailed || !strings.Contains(r.Error, "yeniden başlatıldığı için") || r.AppWasRunning || r.FileName != "" {
			t.Errorf("record %+v", r)
		}
	}
	if r, _ := e.m.store.get(ctx, good.ID); r.Status != statusSuccess || r.FileName != good.FileName {
		t.Errorf("a finished record was changed: %+v", r)
	}
	if e.ev.count("apps.start parolalar") != 0 || e.ev.count("apps.start") != 1 {
		t.Errorf("events %q: only the application the backup had stopped is started", e.ev.all())
	}
	if !e.apps.isRunning("fotolar") {
		t.Error("the application is not running")
	}
	if _, err := os.Stat(tmp); err == nil {
		t.Error("the temporary file of the interrupted backup is still there")
	}
	if _, err := os.Stat(staged); err == nil {
		t.Error("the upload that was never imported is still there")
	}
	if _, err := os.Stat(filepath.Join(dir, good.FileName)); err != nil {
		t.Error("a finished archive was removed")
	}
	if n := strings.Join(e.notifications(), "\n"); strings.Count(n, "Yedekleme başarısız") != 2 || !strings.Contains(n, "yeniden başlatıldı") {
		t.Errorf("notifications %q", n)
	}

	// A second start finds nothing to do.
	e.ev.reset()
	e.recoverNow()
	time.Sleep(100 * time.Millisecond)
	if m := mutating(e.ev.all()); len(m) != 0 {
		t.Fatalf("second recovery: %q", m)
	}
}

func TestRecoveryWithoutDocker(t *testing.T) {
	e := newEnv(t)
	e.standardApp(false)
	e.docker.srv.Close()
	rec := &Record{Slug: "fotolar", AppName: "Fotolar", CreatedAt: time.Now().Unix(), Status: statusRunning,
		Consistency: consistencyStopped, Trigger: triggerManual}
	rec.ID, _ = e.m.store.insert(context.Background(), rec)
	e.m.store.setAppWasRunning(context.Background(), rec.ID, true)
	e.recoverNow()
	e.waitFor("the application to be started", func() bool { return e.ev.count("apps.start fotolar") == 1 })
	if r, _ := e.m.store.get(context.Background(), rec.ID); r.Status != statusFailed {
		t.Fatalf("record %+v", r)
	}
}

func TestStartReturnsWhenCancelled(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.m.Start(ctx)
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return")
	}
}

func TestSweepOfOldUploads(t *testing.T) {
	e := newEnv(t)
	os.MkdirAll(e.m.incomingDir(), 0o700)
	old, fresh, busy := e.m.stagedPath(strings.Repeat("a", 24)), e.m.stagedPath(strings.Repeat("b", 24)), e.m.stagedPath(strings.Repeat("c", 24))
	for _, p := range []string{old, fresh, busy} {
		os.WriteFile(p, []byte("x"), 0o600)
	}
	past := time.Now().Add(-7 * time.Hour)
	os.Chtimes(old, past, past)
	os.Chtimes(busy, past, past)
	_, release, _ := e.m.jobs.create(context.Background(), "import:"+strings.Repeat("c", 24), &Job{Kind: jobImport})
	defer release()
	e.m.sweepIncoming(6 * time.Hour)
	if _, err := os.Stat(old); err == nil {
		t.Error("an old upload was kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a fresh upload was removed")
	}
	if _, err := os.Stat(busy); err != nil {
		t.Error("an upload that is being imported was removed")
	}
}
