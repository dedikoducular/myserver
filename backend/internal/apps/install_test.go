package apps

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"myserver/internal/audit"
)

func wantChanges(t *testing.T, f *fakeDocker, want ...string) {
	t.Helper()
	got := f.changes()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("operations on Docker:\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
}

func envMap(list []string) map[string]string {
	out := map[string]string{}
	for _, kv := range list {
		k, v, _ := strings.Cut(kv, "=")
		out[k] = v
	}
	return out
}

// caps joins capability names; the Docker client sends them with the CAP_
// prefix, which means the same to the daemon.
func caps(list []string) string {
	out := make([]string, 0, len(list))
	for _, c := range list {
		out = append(out, strings.TrimPrefix(c, "CAP_"))
	}
	return strings.Join(out, ",")
}

func body(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// bystanders puts containers, a network and volumes into the fake that do
// not belong to the application under test. Nothing may ever touch them.
func bystanders(f *fakeDocker) func(t *testing.T) {
	f.addContainer("portainer", "portainer/portainer-ce", "running", map[string]string{"com.example": "1"})
	f.addContainer("myserver-diger", "example/diger", "running", managed("diger", "app"))
	f.addContainer("tek", "example/tek:1.0", "exited", nil)
	f.mu.Lock()
	f.volumes["portainer_data"] = nil
	f.volumes["myserver-diger-data"] = managed("diger", "")
	f.networks["myserver-diger"] = managed("diger", "")
	f.networks["bridge"] = nil
	f.mu.Unlock()
	return func(t *testing.T) {
		t.Helper()
		names := f.names()
		for _, n := range []string{"portainer", "myserver-diger", "tek"} {
			if !containsString(names, n) {
				t.Errorf("container %s of someone else is gone", n)
			}
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, v := range []string{"portainer_data", "myserver-diger-data"} {
			if _, ok := f.volumes[v]; !ok {
				t.Errorf("volume %s of someone else is gone", v)
			}
		}
		for _, n := range []string{"myserver-diger", "bridge"} {
			if _, ok := f.networks[n]; !ok {
				t.Errorf("network %s of someone else is gone", n)
			}
		}
		for _, op := range f.ops {
			verb, rest, _ := strings.Cut(op, " ")
			if strings.HasSuffix(verb, ".inspect") || strings.HasSuffix(verb, ".list") {
				continue
			}
			for _, n := range []string{"portainer", "myserver-diger", "bridge"} {
				if rest == n || strings.HasPrefix(rest, n+" ") || strings.HasPrefix(rest, n+"_") || strings.HasPrefix(rest, n+"-") {
					t.Errorf("operation on something that is not ours: %s", op)
				}
			}
			if rest == "tek" || strings.HasPrefix(rest, "tek ") {
				t.Errorf("operation on something that is not ours: %s", op)
			}
		}
	}
}

/* ---------- install ---------- */

func TestInstallSingleService(t *testing.T) {
	logs := captureLogs(t)
	f := newFakeDocker(t)
	untouched := bystanders(f)
	e := newTestEnv(t, f.host(), map[string]string{"tek.yaml": manifestTek})
	media := filepath.Join(e.root, "medya", "film")
	const secret = "kurulum-icin-gizli-parola"

	job := e.startJob("POST", "/apps/catalog/tek/install", body(map[string]any{
		"env": map[string]string{"ADMIN_PASSWORD": secret}, "paths": map[string]string{"media": media},
		"options": map[string]bool{"rawnet": true}, "bind_address": "loopback",
	}))
	if job.Status != JobSuccess || job.Error != "" || job.Kind != JobInstall || job.Slug != "tek" {
		t.Fatalf("job: %+v", job)
	}
	wantChanges(t, f,
		"image.pull example/tek:1.0",
		"network.create myserver-tek",
		"volume.create myserver-tek-config",
		"container.create myserver-tek",
		"container.start myserver-tek",
	)
	untouched(t)

	c := f.created("myserver-tek")
	if c == nil {
		t.Fatal("container not created")
	}
	b := c.Body
	if b.Image != "example/tek:1.0" {
		t.Errorf("image %q", b.Image)
	}
	if len(b.Labels) != 3 || b.Labels[LabelApp] != "tek" || b.Labels[LabelService] != "app" || b.Labels[LabelManaged] != "true" {
		t.Errorf("labels: %+v", b.Labels)
	}
	env := envMap(b.Env)
	generated := env["DB_PASSWORD"]
	if env["ADMIN_PASSWORD"] != secret || len(generated) != 32 || env["TZ"] != "Europe/Istanbul" || len(env) != 3 {
		t.Errorf("environment: %+v", b.Env)
	}
	hc := b.HostConfig
	if hc.Privileged || hc.NetworkMode != "myserver-tek" || caps(hc.CapAdd) != "NET_RAW" || len(hc.Devices) != 0 {
		t.Errorf("host config: %+v", hc)
	}
	if pb := hc.PortBindings["80/tcp"]; len(hc.PortBindings) != 1 || len(pb) != 1 || pb[0].HostIP != "127.0.0.1" || pb[0].HostPort != "18080" {
		t.Errorf("port bindings: %+v", hc.PortBindings)
	}
	var mounts []string
	for _, m := range hc.Mounts {
		mounts = append(mounts, m.Type+":"+m.Source+":"+m.Target)
	}
	sort.Strings(mounts)
	if strings.Join(mounts, ",") != "bind:"+media+":/media,volume:myserver-tek-config:/config" {
		t.Errorf("mounts: %v", mounts)
	}
	f.mu.Lock()
	for kind, labels := range map[string]map[string]string{"network": f.networks["myserver-tek"], "volume": f.volumes["myserver-tek-config"]} {
		if labels[LabelApp] != "tek" || labels[LabelManaged] != "true" {
			t.Errorf("%s labels: %+v", kind, labels)
		}
	}
	f.mu.Unlock()
	if st, err := os.Stat(media); err != nil || !st.IsDir() {
		t.Errorf("the data folder was not created: %v", err)
	}

	it, err := e.mod.store.get(context.Background(), "tek")
	if err != nil || it == nil {
		t.Fatalf("not recorded: %v", err)
	}
	if it.Inputs.Env["DB_PASSWORD"] != generated || it.Config.BindAddress != BindLoopback || it.Images["app"].ID == "" {
		t.Errorf("stored: %+v", it)
	}
	rows := e.findAudit("apps.install")
	if len(rows) != 1 || !rows[0].Success || rows[0].Target != "tek" || rows[0].Username != e.admin.name {
		t.Errorf("audit: %+v", rows)
	}

	// The installed view, derived from the real containers.
	var v installedView
	decodeData(t, e.do("GET", "/apps/installed/tek", e.user, ""), &v)
	if v.State != StateRunning || v.BindAddress != BindLoopback || v.WebUI == nil || !v.WebUI.LocalOnly || v.WebUI.Port != 18080 ||
		len(v.Ports) != 1 || v.Ports[0].HostIP != "127.0.0.1" || v.Operation != "" || v.JobID != "" {
		t.Errorf("installed view: %+v", v)
	}

	// A second installation is refused.
	w := e.do("POST", "/apps/catalog/tek/install", e.admin, `{"env":{"ADMIN_PASSWORD":"x"}}`)
	wantError(t, w, http.StatusConflict, "conflict", "zaten kurulu")

	// Neither secret appears anywhere but in the request to Docker.
	var where []string
	check := func(name, text string) {
		for _, s := range []string{secret, generated} {
			if strings.Contains(text, s) {
				where = append(where, name)
			}
		}
	}
	for _, p := range []string{"/apps/catalog", "/apps/catalog/tek", "/apps/installed", "/apps/installed/tek",
		"/apps/jobs", "/apps/jobs/" + job.ID, "/apps/jobs/" + job.ID + "/stream"} {
		w := e.do("GET", p, e.admin, "")
		if w.Code != 200 {
			t.Errorf("GET %s: %d", p, w.Code)
		}
		check("GET "+p, w.Body.String())
	}
	if !strings.Contains(e.do("GET", "/apps/jobs/"+job.ID+"/stream", e.admin, "").Body.String(), "event: finished") {
		t.Error("the job stream did not end with the finished event")
	}
	for _, a := range e.auditRows() {
		check("audit "+a.Action, a.Detail+a.Target)
	}
	list, _ := e.notify.List(context.Background(), 50, false)
	if len(list) == 0 {
		t.Error("no notification about the installation")
	}
	for _, n := range list {
		check("notification", n.Title+n.Message)
	}
	check("log output", logs.String())
	if len(where) > 0 {
		t.Errorf("secrets leaked into: %v", where)
	}
}

func TestInstallMultiService(t *testing.T) {
	captureLogs(t)
	f := newFakeDocker(t)
	untouched := bystanders(f)
	e := newTestEnv(t, f.host(), map[string]string{"cift.yaml": manifestCift})
	job := e.startJob("POST", "/apps/catalog/cift/install", `{}`)
	if job.Status != JobSuccess {
		t.Fatalf("job: %+v", job)
	}
	wantChanges(t, f,
		"image.pull example/web:2",
		"image.pull example/db:16",
		"network.create myserver-cift",
		"volume.create myserver-cift-db",
		"container.create myserver-cift-db",
		"container.start myserver-cift-db",
		"container.create myserver-cift-web",
		"container.start myserver-cift-web",
	)
	untouched(t)
	db, web := f.created("myserver-cift-db"), f.created("myserver-cift-web")
	if db.Body.Labels[LabelService] != "db" || web.Body.Labels[LabelService] != "web" ||
		db.Body.Labels[LabelApp] != "cift" || web.Body.Labels[LabelManaged] != "true" {
		t.Errorf("labels: %+v %+v", db.Body.Labels, web.Body.Labels)
	}
	if db.Body.HostConfig.NetworkMode != "myserver-cift" || web.Body.HostConfig.NetworkMode != "myserver-cift" {
		t.Error("both services must join the private network")
	}
	p1, p2 := envMap(db.Body.Env)["POSTGRES_PASSWORD"], envMap(web.Body.Env)["DB_PASS"]
	if len(p1) != 32 || p1 != p2 {
		t.Errorf("shared password: %q / %q", p1, p2)
	}
	if len(db.Body.HostConfig.PortBindings) != 0 {
		t.Errorf("the database must not be published: %+v", db.Body.HostConfig.PortBindings)
	}
	if pb := web.Body.HostConfig.PortBindings["8080/tcp"]; len(pb) != 1 || pb[0].HostIP != "0.0.0.0" || pb[0].HostPort != "18081" {
		t.Errorf("web binding: %+v", web.Body.HostConfig.PortBindings)
	}
}

func TestInstallHostNetworkApp(t *testing.T) {
	captureLogs(t)
	f := newFakeDocker(t)
	e := newTestEnv(t, f.host(), map[string]string{"agci.yaml": manifestAgci})
	job := e.startJob("POST", "/apps/catalog/agci/install", `{"bind_address":"loopback"}`)
	if job.Status != JobSuccess {
		t.Fatalf("job: %+v", job)
	}
	wantChanges(t, f, "image.pull example/agci:1", "container.create myserver-agci", "container.start myserver-agci")
	c := f.created("myserver-agci")
	if c.Body.HostConfig.NetworkMode != "host" || len(c.Body.HostConfig.PortBindings) != 0 ||
		caps(c.Body.HostConfig.CapAdd) != "NET_ADMIN" {
		t.Errorf("host config: %+v", c.Body.HostConfig)
	}
}

/* ---------- failure and cleanup ---------- */

func TestInstallFailureCleansUpOnlyItsOwn(t *testing.T) {
	captureLogs(t)
	f := newFakeDocker(t)
	untouched := bystanders(f)
	// Data kept from an earlier installation.
	f.volumes["myserver-tek-config"] = managed("tek", "")
	f.fail["container.start myserver-tek"] = "driver failed programming external connectivity"
	e := newTestEnv(t, f.host(), map[string]string{"tek.yaml": manifestTek})

	job := e.startJob("POST", "/apps/catalog/tek/install", `{"env":{"ADMIN_PASSWORD":"gizli-parola-123"}}`)
	if job.Status != JobFailed || !strings.Contains(job.Error, "başlatılamadı") {
		t.Fatalf("job: %+v", job)
	}
	if strings.Contains(job.Error, "driver failed") {
		t.Errorf("the raw Docker error is shown to the user: %s", job.Error)
	}
	wantChanges(t, f,
		"image.pull example/tek:1.0",
		"network.create myserver-tek",
		"container.create myserver-tek",
		"container.start myserver-tek",
		"container.remove myserver-tek",
		"network.remove myserver-tek",
	)
	untouched(t)
	f.mu.Lock()
	if _, ok := f.volumes["myserver-tek-config"]; !ok {
		t.Error("a volume that existed before the installation was removed")
	}
	if _, ok := f.networks["myserver-tek"]; ok {
		t.Error("the network of the failed installation is left behind")
	}
	f.mu.Unlock()
	if containsString(f.names(), "myserver-tek") {
		t.Error("the container of the failed installation is left behind")
	}
	if it, _ := e.mod.store.get(context.Background(), "tek"); it != nil {
		t.Error("a failed installation was recorded as installed")
	}
	rows := e.findAudit("apps.install")
	if len(rows) != 1 || rows[0].Success || rows[0].Detail == "" || strings.Contains(rows[0].Detail, "gizli-parola-123") {
		t.Errorf("audit: %+v", rows)
	}
	var cat struct {
		Apps []catalogView `json:"apps"`
	}
	decodeData(t, e.do("GET", "/apps/catalog", e.admin, ""), &cat)
	if cat.Apps[0].Installed || cat.Apps[0].Operation != "" {
		t.Errorf("catalog after the failure: %+v", cat.Apps[0])
	}

	// The application can be installed once the cause is gone.
	f.mu.Lock()
	delete(f.fail, "container.start myserver-tek")
	f.mu.Unlock()
	f.reset()
	job = e.startJob("POST", "/apps/catalog/tek/install", `{"env":{"ADMIN_PASSWORD":"gizli-parola-123"}}`)
	if job.Status != JobSuccess {
		t.Fatalf("second attempt: %+v", job)
	}
	wantChanges(t, f, "image.pull example/tek:1.0", "network.create myserver-tek",
		"container.create myserver-tek", "container.start myserver-tek")
}

func TestInstallFailureRemovesWhatItCreated(t *testing.T) {
	captureLogs(t)
	f := newFakeDocker(t)
	untouched := bystanders(f)
	f.fail["container.create myserver-cift-web"] = "no space left on device"
	e := newTestEnv(t, f.host(), map[string]string{"cift.yaml": manifestCift})
	job := e.startJob("POST", "/apps/catalog/cift/install", `{}`)
	if job.Status != JobFailed || !strings.Contains(job.Error, "yeterli boş alan yok") {
		t.Fatalf("job: %+v", job)
	}
	wantChanges(t, f,
		"image.pull example/web:2",
		"image.pull example/db:16",
		"network.create myserver-cift",
		"volume.create myserver-cift-db",
		"container.create myserver-cift-db",
		"container.start myserver-cift-db",
		"container.create myserver-cift-web",
		"container.remove myserver-cift-db",
		"network.remove myserver-cift",
		"volume.remove myserver-cift-db",
	)
	untouched(t)
}

func TestInstallFailsWhenContainerExits(t *testing.T) {
	captureLogs(t)
	f := newFakeDocker(t)
	const secret = "gizli-parola-loglarda"
	f.exitOnStart["myserver-tek"] = true
	f.logs = "starting\nfatal: cannot log in with password " + secret + "\n"
	e := newTestEnv(t, f.host(), map[string]string{"tek.yaml": manifestTek})
	job := e.startJob("POST", "/apps/catalog/tek/install", body(map[string]any{"env": map[string]string{"ADMIN_PASSWORD": secret}}))
	if job.Status != JobFailed || !strings.Contains(job.Error, "hemen sonra durdu") || !strings.Contains(job.Error, "çıkış kodu 1") {
		t.Fatalf("job: %+v", job)
	}
	wantChanges(t, f,
		"image.pull example/tek:1.0", "network.create myserver-tek", "volume.create myserver-tek-config",
		"container.create myserver-tek", "container.start myserver-tek",
		"container.remove myserver-tek", "network.remove myserver-tek", "volume.remove myserver-tek-config",
	)
	w := e.do("GET", "/apps/jobs/"+job.ID, e.admin, "")
	if !strings.Contains(w.Body.String(), "fatal: cannot log in") {
		t.Errorf("the last output of the container is not in the job: %s", w.Body.String())
	}
	// The application printed its own password; the job must not repeat it.
	for _, p := range []string{"/apps/jobs/" + job.ID, "/apps/jobs/" + job.ID + "/stream", "/apps/jobs"} {
		if strings.Contains(e.do("GET", p, e.admin, "").Body.String(), secret) {
			t.Errorf("GET %s contains a secret value printed by the container", p)
		}
	}
}

func TestInstallRefusesForeignContainerName(t *testing.T) {
	captureLogs(t)
	f := newFakeDocker(t)
	f.addContainer("myserver-tek", "someone/else", "running", map[string]string{"owner": "user"})
	e := newTestEnv(t, f.host(), map[string]string{"tek.yaml": manifestTek})
	job := e.startJob("POST", "/apps/catalog/tek/install", `{"env":{"ADMIN_PASSWORD":"x"},"ports":{"web":18085}}`)
	if job.Status != JobFailed || !strings.Contains(job.Error, "bu panelin oluşturmadığı bir konteyner") {
		t.Fatalf("job: %+v", job)
	}
	wantChanges(t, f, "image.pull example/tek:1.0")
	if !containsString(f.names(), "myserver-tek") {
		t.Error("the container of someone else was removed")
	}
}

/* ---------- one operation at a time ---------- */

func TestSecondOperationIsRefused(t *testing.T) {
	captureLogs(t)
	f := newFakeDocker(t)
	gate := make(chan struct{})
	f.block["image.pull example/tek:1.0"] = gate
	e := newTestEnv(t, f.host(), map[string]string{"tek.yaml": manifestTek, "cift.yaml": manifestCift})
	released := false
	release := func() {
		if !released {
			released = true
			close(gate)
		}
	}
	defer release()

	w := e.do("POST", "/apps/catalog/tek/install", e.admin, `{"env":{"ADMIN_PASSWORD":"x"}}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("first request: %d %s", w.Code, w.Body.String())
	}
	var first JobView
	decodeData(t, w, &first)
	deadline := time.Now().Add(10 * time.Second)
	for !containsString(f.seen(), "image.pull example/tek:1.0") {
		if time.Now().After(deadline) {
			t.Fatal("the installation did not reach the download")
		}
		time.Sleep(5 * time.Millisecond)
	}

	w = e.do("POST", "/apps/catalog/tek/install", e.admin, `{"env":{"ADMIN_PASSWORD":"y"}}`)
	wantError(t, w, http.StatusConflict, "conflict", "devam eden bir işlem")

	var d struct {
		Operation string `json:"operation"`
		JobID     string `json:"job_id"`
		Installed bool   `json:"installed"`
	}
	decodeData(t, e.do("GET", "/apps/catalog/tek", e.admin, ""), &d)
	if d.Operation != JobInstall || d.JobID != first.ID || d.Installed {
		t.Errorf("catalog while installing: %+v", d)
	}
	// Another application is not blocked.
	if other := e.startJob("POST", "/apps/catalog/cift/install", `{}`); other.Status != JobSuccess {
		t.Errorf("another application was blocked: %+v", other)
	}
	if n := len(e.mod.jobs.list()); n != 2 {
		t.Errorf("%d jobs, want 2 (the refused request must not create one)", n)
	}

	release()
	if done := e.waitJob(first.ID); done.Status != JobSuccess {
		t.Fatalf("first job: %+v", done)
	}
	if c := f.created("myserver-tek"); c == nil || envMap(c.Body.Env)["ADMIN_PASSWORD"] != "x" {
		t.Error("the container was not created from the first request")
	}
	creates := 0
	for _, op := range f.seen() {
		if op == "container.create myserver-tek" {
			creates++
		}
	}
	if creates != 1 {
		t.Errorf("container created %d times", creates)
	}
}

func TestLockIsPerApplication(t *testing.T) {
	e := newTestEnv(t, "tcp://127.0.0.1:1", nil)
	release, err := e.mod.lock("tek", "install")
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"install", "stop", "uninstall", JobRestore} {
		if _, err := e.mod.lock("tek", op); err != errBusy {
			t.Errorf("%s while installing: %v", op, err)
		}
	}
	other, err := e.mod.lock("cift", "stop")
	if err != nil {
		t.Errorf("another application: %v", err)
	} else {
		other()
	}
	if e.mod.operation("tek") != "install" {
		t.Errorf("operation %q", e.mod.operation("tek"))
	}
	release()
	again, err := e.mod.lock("tek", "stop")
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	again()
}

/* ---------- Docker unreachable ---------- */

func TestDockerUnavailable(t *testing.T) {
	captureLogs(t)
	e := newTestEnv(t, refusedTCPHost(t), map[string]string{"tek.yaml": manifestTek})
	m := e.mod.catalog.Get("tek")
	cfg, in, err := Resolve(m, Inputs{Env: map[string]string{"ADMIN_PASSWORD": "x"}}, nil, e.mod.roots())
	if err != nil {
		t.Fatal(err)
	}

	w := e.do("POST", "/apps/catalog/tek/install", e.admin, `{"env":{"ADMIN_PASSWORD":"x"}}`)
	wantError(t, w, http.StatusServiceUnavailable, "docker_unavailable", "Docker servisine ulaşılamıyor.")
	if len(e.mod.jobs.list()) != 0 {
		t.Error("a job was created without Docker")
	}

	if err := e.mod.store.save(context.Background(), cfg, in, nil); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct{ method, path, body string }{
		{"POST", "/apps/installed/tek/start", ""},
		{"POST", "/apps/installed/tek/stop", ""},
		{"POST", "/apps/installed/tek/restart", ""},
		{"POST", "/apps/installed/tek/update", ""},
		{"PUT", "/apps/installed/tek/settings", `{}`},
		{"POST", "/apps/installed/tek/uninstall", `{"delete_data":false}`},
		{"POST", "/apps/installed/tek/uninstall", `{"delete_data":true,"confirm":"tek"}`},
		{"GET", "/apps/installed/tek/logs", ""},
	} {
		w := e.do(r.method, r.path, e.admin, r.body)
		wantError(t, w, http.StatusServiceUnavailable, "docker_unavailable", "Docker servisine ulaşılamıyor.")
	}
	if it, _ := e.mod.store.get(context.Background(), "tek"); it == nil {
		t.Error("the application was forgotten although Docker could not be reached")
	}
	if e.mod.operation("tek") != "" {
		t.Error("the application stays locked")
	}

	// Reading still works, with an honest state.
	var list struct {
		Apps            []installedView `json:"apps"`
		DockerAvailable bool            `json:"docker_available"`
	}
	decodeData(t, e.do("GET", "/apps/installed", e.user, ""), &list)
	if list.DockerAvailable || len(list.Apps) != 1 || list.Apps[0].State != StateUnknown {
		t.Errorf("installed: %+v", list)
	}
	var v installedView
	decodeData(t, e.do("GET", "/apps/installed/tek", e.user, ""), &v)
	if v.State != StateUnknown || len(v.Services) != 1 || v.Services[0].State != StateUnknown {
		t.Errorf("installed app: %+v", v)
	}
	if w := e.do("GET", "/apps/catalog/tek", e.user, ""); w.Code != 200 {
		t.Errorf("catalog detail: %d", w.Code)
	}
	if err := e.mod.RecreateApp(context.Background(), audit.Actor{Username: "yedek"}, cfg); err == nil {
		t.Error("RecreateApp without Docker must fail")
	} else if userMessage(err) != "Docker servisine ulaşılamıyor." {
		t.Errorf("RecreateApp: %v", err)
	}
}

/* ---------- uninstall ---------- */

func uninstallEnv(t *testing.T) (*testEnv, *fakeDocker, func(t *testing.T), string, *Installed) {
	t.Helper()
	captureLogs(t)
	f := newFakeDocker(t)
	untouched := bystanders(f)
	e := newTestEnv(t, f.host(), map[string]string{"tek.yaml": manifestTek})
	media := filepath.Join(e.root, "medya")
	if err := os.MkdirAll(filepath.Join(media, "album"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(media, "album", "foto.jpg"), []byte("kullanıcının verisi"), 0o644); err != nil {
		t.Fatal(err)
	}
	it := seedInstalled(t, e, f, "tek", Inputs{
		Env: map[string]string{"ADMIN_PASSWORD": "ilk-parola-gizli"}, Paths: map[string]string{"media": media},
	})
	f.reset()
	return e, f, untouched, media, it
}

func wantUserData(t *testing.T, media string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(media, "album", "foto.jpg"))
	if err != nil || string(b) != "kullanıcının verisi" {
		t.Errorf("the user's folder was touched: %v %q", err, b)
	}
}

func TestUninstallKeepingData(t *testing.T) {
	e, f, untouched, media, it := uninstallEnv(t)
	w := e.do("POST", "/apps/installed/tek/uninstall", e.admin, `{"delete_data":false}`)
	var res UninstallResult
	decodeData(t, w, &res)
	wantChanges(t, f, "container.stop myserver-tek", "container.remove myserver-tek", "network.remove myserver-tek")
	untouched(t)
	if strings.Join(res.KeptVolumes, ",") != "myserver-tek-config" || strings.Join(res.KeptPaths, ",") != media ||
		len(res.RemovedVolumes) != 0 || len(res.FailedVolumes) != 0 {
		t.Errorf("result: %+v", res)
	}
	f.mu.Lock()
	if _, ok := f.volumes["myserver-tek-config"]; !ok {
		t.Error("the volume was removed although the data was to be kept")
	}
	f.mu.Unlock()
	wantUserData(t, media)
	ctx := context.Background()
	if got, _ := e.mod.store.get(ctx, "tek"); got != nil {
		t.Error("still recorded as installed")
	}
	kept, _ := e.mod.store.retained(ctx, "tek")
	if kept == nil || kept.Env["DB_PASSWORD"] != it.Inputs.Env["DB_PASSWORD"] || kept.Env["ADMIN_PASSWORD"] != "ilk-parola-gizli" {
		t.Errorf("retained values: %+v", kept)
	}
	rows := e.findAudit("apps.uninstall")
	if len(rows) != 1 || !rows[0].Success || rows[0].Detail != "veriler korundu" {
		t.Errorf("audit: %+v", rows)
	}
	if strings.Contains(w.Body.String(), "ilk-parola-gizli") {
		t.Error("the response contains a secret")
	}

	// Reinstalling reuses the kept secrets and the kept volume; everything
	// else comes from the new form.
	f.reset()
	job := e.startJob("POST", "/apps/catalog/tek/install", `{"env":{"ADMIN_PASSWORD":""},"ports":{"web":18099}}`)
	if job.Status != JobSuccess {
		t.Fatalf("reinstall: %+v", job)
	}
	wantChanges(t, f, "image.pull example/tek:1.0", "network.create myserver-tek",
		"container.create myserver-tek", "container.start myserver-tek")
	c := f.created("myserver-tek")
	env := envMap(c.Body.Env)
	if env["ADMIN_PASSWORD"] != "ilk-parola-gizli" || env["DB_PASSWORD"] != it.Inputs.Env["DB_PASSWORD"] {
		t.Error("the kept secrets were not reused; the kept database would refuse the new password")
	}
	for _, m := range c.Body.HostConfig.Mounts {
		if m.Type == "bind" {
			t.Errorf("the folder of the removed installation was mounted without being chosen again: %+v", m)
		}
	}
	if kept, _ := e.mod.store.retained(ctx, "tek"); kept != nil {
		t.Error("retained values must be dropped once they are in use again")
	}
}

func TestUninstallDeletingDataNeedsConfirmation(t *testing.T) {
	e, f, untouched, media, _ := uninstallEnv(t)
	for _, b := range []string{
		`{"delete_data":true}`, `{"delete_data":true,"confirm":""}`, `{"delete_data":true,"confirm":"Tek"}`,
		`{"delete_data":true,"confirm":"TEK"}`, `{"delete_data":true,"confirm":"tek "}`, `{"delete_data":true,"confirm":" tek"}`,
		`{"delete_data":true,"confirm":"te"}`, `{"delete_data":true,"confirm":"tekk"}`, `{"delete_data":true,"confirm":"tek\n"}`,
		`{"delete_data":true,"confirm":"myserver-tek"}`, `{"delete_data":true,"confirm":"evet"}`,
	} {
		w := e.do("POST", "/apps/installed/tek/uninstall", e.admin, b)
		wantError(t, w, http.StatusBadRequest, "confirmation_required", "uygulama adını yazarak")
	}
	for _, b := range []string{`{"delete_data":true,"confirm":true}`, `{"delete_data":"true","confirm":"tek"}`, ``, `{"delete_data":true,"confirm":"tek","force":true}`} {
		if w := e.do("POST", "/apps/installed/tek/uninstall", e.admin, b); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", b, w.Code)
		}
	}
	if ops := f.seen(); len(ops) != 0 {
		t.Errorf("an unconfirmed request reached Docker: %v", ops)
	}
	if it, _ := e.mod.store.get(context.Background(), "tek"); it == nil {
		t.Fatal("an unconfirmed request removed the application")
	}

	w := e.do("POST", "/apps/installed/tek/uninstall", e.admin, `{"delete_data":true,"confirm":"tek"}`)
	var res UninstallResult
	decodeData(t, w, &res)
	wantChanges(t, f, "container.stop myserver-tek", "container.remove myserver-tek", "network.remove myserver-tek",
		"volume.remove myserver-tek-config")
	untouched(t)
	if strings.Join(res.RemovedVolumes, ",") != "myserver-tek-config" || strings.Join(res.KeptPaths, ",") != media ||
		len(res.KeptVolumes) != 0 || len(res.FailedVolumes) != 0 {
		t.Errorf("result: %+v", res)
	}
	wantUserData(t, media)
	ctx := context.Background()
	if it, _ := e.mod.store.get(ctx, "tek"); it != nil {
		t.Error("still recorded as installed")
	}
	if kept, _ := e.mod.store.retained(ctx, "tek"); kept != nil {
		t.Error("values of deleted data were kept")
	}
	rows := e.findAudit("apps.uninstall")
	if len(rows) != 1 || !rows[0].Success || rows[0].Detail != "veri birimleri silindi" {
		t.Errorf("audit: %+v", rows)
	}
	wantError(t, e.do("POST", "/apps/installed/tek/uninstall", e.admin, `{"delete_data":false}`), http.StatusNotFound, "not_found")
}

func TestUninstallDropsValuesRetainedEarlier(t *testing.T) {
	e, f, _, _, _ := uninstallEnv(t)
	ctx := context.Background()
	decodeData(t, e.do("POST", "/apps/installed/tek/uninstall", e.admin, `{"delete_data":false}`), &UninstallResult{})
	if kept, _ := e.mod.store.retained(ctx, "tek"); kept == nil {
		t.Fatal("nothing retained")
	}
	if job := e.startJob("POST", "/apps/catalog/tek/install", `{"env":{"ADMIN_PASSWORD":""}}`); job.Status != JobSuccess {
		t.Fatalf("reinstall: %+v", job)
	}
	decodeData(t, e.do("POST", "/apps/installed/tek/uninstall", e.admin, `{"delete_data":true,"confirm":"tek"}`), &UninstallResult{})
	if kept, _ := e.mod.store.retained(ctx, "tek"); kept != nil {
		t.Error("retained values survive the deletion of the data")
	}
	f.mu.Lock()
	_, ok := f.volumes["myserver-tek-config"]
	f.mu.Unlock()
	if ok {
		t.Error("the volume is still there")
	}
	// Without retained values the required secret must be entered again.
	w := e.do("POST", "/apps/catalog/tek/install", e.admin, `{"env":{"ADMIN_PASSWORD":""}}`)
	wantError(t, w, http.StatusBadRequest, "invalid_input", "zorunlu")
}

// A volume with the application's name that the panel did not create is not
// deleted.
func TestUninstallLeavesForeignVolume(t *testing.T) {
	e, f, _, _, _ := uninstallEnv(t)
	f.mu.Lock()
	f.volumes["myserver-tek-config"] = map[string]string{"owner": "someone"}
	f.mu.Unlock()
	var res UninstallResult
	decodeData(t, e.do("POST", "/apps/installed/tek/uninstall", e.admin, `{"delete_data":true,"confirm":"tek"}`), &res)
	wantChanges(t, f, "container.stop myserver-tek", "container.remove myserver-tek", "network.remove myserver-tek")
	if strings.Join(res.FailedVolumes, ",") != "myserver-tek-config" || len(res.RemovedVolumes) != 0 {
		t.Errorf("result: %+v", res)
	}
}

/* ---------- settings, update, lifecycle ---------- */

func TestSettingsKeepSecretsAndBindAddress(t *testing.T) {
	captureLogs(t)
	f := newFakeDocker(t)
	untouched := bystanders(f)
	e := newTestEnv(t, f.host(), map[string]string{"tek.yaml": manifestTek})
	it := seedInstalled(t, e, f, "tek", Inputs{
		Env: map[string]string{"ADMIN_PASSWORD": "saklanan-gizli-parola"}, BindAddress: BindLoopback,
	})
	old := f.created("myserver-tek")
	f.reset()

	job := e.startJob("PUT", "/apps/installed/tek/settings", `{"env":{"ADMIN_PASSWORD":"","TZ":"UTC"},"ports":{"web":18088}}`)
	if job.Status != JobSuccess || job.Kind != JobSettings {
		t.Fatalf("job: %+v", job)
	}
	backup := "myserver-tek-onceki-" + job.ID[:8]
	wantChanges(t, f,
		"container.stop myserver-tek",
		"container.rename myserver-tek "+backup,
		"container.create myserver-tek",
		"container.start myserver-tek",
		"container.remove "+backup,
	)
	untouched(t)
	c := f.created("myserver-tek")
	if c == nil || c.ID == old.ID {
		t.Fatal("the container was not replaced")
	}
	env := envMap(c.Body.Env)
	if env["ADMIN_PASSWORD"] != "saklanan-gizli-parola" || env["DB_PASSWORD"] != it.Inputs.Env["DB_PASSWORD"] || env["TZ"] != "UTC" {
		t.Error("an empty secret field must keep the stored secret")
	}
	if pb := c.Body.HostConfig.PortBindings["80/tcp"]; len(pb) != 1 || pb[0].HostIP != "127.0.0.1" || pb[0].HostPort != "18088" {
		t.Errorf("the bind address was lost: %+v", pb)
	}
	now, _ := e.mod.store.get(context.Background(), "tek")
	if now.Config.BindAddress != BindLoopback || now.Inputs.Env["ADMIN_PASSWORD"] != "saklanan-gizli-parola" || now.Inputs.Ports["web"] != 18088 {
		t.Errorf("stored: %+v", now.Inputs)
	}
	for _, p := range []string{"/apps/jobs/" + job.ID, "/apps/catalog/tek", "/apps/installed/tek"} {
		if strings.Contains(e.do("GET", p, e.admin, "").Body.String(), "saklanan-gizli-parola") {
			t.Errorf("GET %s contains the secret", p)
		}
	}

	// Switching the address is possible, anything else is refused.
	f.reset()
	job = e.startJob("PUT", "/apps/installed/tek/settings", `{"bind_address":"all"}`)
	if job.Status != JobSuccess {
		t.Fatalf("job: %+v", job)
	}
	c = f.created("myserver-tek")
	if pb := c.Body.HostConfig.PortBindings["80/tcp"]; len(pb) != 1 || pb[0].HostIP != "0.0.0.0" || pb[0].HostPort != "18088" {
		t.Errorf("binding: %+v", pb)
	}
	if envMap(c.Body.Env)["TZ"] != "UTC" {
		t.Error("a value that was not sent must stay as it is")
	}
	for _, v := range []string{"0.0.0.0", "127.0.0.1", "192.168.1.4", "LOOPBACK", "everywhere"} {
		w := e.do("PUT", "/apps/installed/tek/settings", e.admin, `{"bind_address":"`+v+`"}`)
		wantError(t, w, http.StatusBadRequest, "invalid_input", "Erişim adresi")
		w = e.do("POST", "/apps/catalog/tek/install", e.admin, `{"bind_address":"`+v+`"}`)
		wantError(t, w, http.StatusBadRequest, "invalid_input", "Erişim adresi")
	}
}

func TestSettingsFailurePutsThePreviousContainerBack(t *testing.T) {
	captureLogs(t)
	f := newFakeDocker(t)
	untouched := bystanders(f)
	e := newTestEnv(t, f.host(), map[string]string{"tek.yaml": manifestTek})
	seedInstalled(t, e, f, "tek", Inputs{Env: map[string]string{"ADMIN_PASSWORD": "x"}})
	old := f.created("myserver-tek")
	f.reset()
	f.fail["container.create myserver-tek"] = "invalid mount config"

	job := e.startJob("PUT", "/apps/installed/tek/settings", `{"ports":{"web":18089}}`)
	if job.Status != JobFailed {
		t.Fatalf("job: %+v", job)
	}
	backup := "myserver-tek-onceki-" + job.ID[:8]
	wantChanges(t, f,
		"container.stop myserver-tek",
		"container.rename myserver-tek "+backup,
		"container.create myserver-tek",
		"container.rename "+backup+" myserver-tek",
		"container.start myserver-tek",
	)
	untouched(t)
	c := f.created("myserver-tek")
	if c == nil || c.ID != old.ID || c.State != "running" {
		t.Errorf("the previous container is not back: %+v", c)
	}
	f.mu.Lock()
	_, vol := f.volumes["myserver-tek-config"]
	_, net := f.networks["myserver-tek"]
	f.mu.Unlock()
	if !vol || !net {
		t.Errorf("volume kept=%v network kept=%v", vol, net)
	}
	it, _ := e.mod.store.get(context.Background(), "tek")
	if it == nil || it.Inputs.Ports["web"] != 18080 {
		t.Errorf("the failed settings were stored: %+v", it)
	}
}

func TestUpdateOfLegacyInstallationDoesNotRecreate(t *testing.T) {
	captureLogs(t)
	f := newFakeDocker(t)
	e := newTestEnv(t, f.host(), map[string]string{"tek.yaml": manifestTek})
	it := seedInstalled(t, e, f, "tek", Inputs{Env: map[string]string{"ADMIN_PASSWORD": "x"}})
	// Rewrite the row the way it was stored before the bind address existed.
	rawCfg, _ := json.Marshal(legacy(t, it.Config))
	in := map[string]any{"ports": it.Inputs.Ports, "env": it.Inputs.Env, "paths": it.Inputs.Paths}
	rawIn, _ := json.Marshal(in)
	if _, err := e.db.Exec(`UPDATE apps_installed SET config = ?, inputs = ? WHERE slug = 'tek'`, string(rawCfg), string(rawIn)); err != nil {
		t.Fatal(err)
	}
	f.reset()

	job := e.startJob("POST", "/apps/installed/tek/update", "")
	if job.Status != JobSuccess || job.Kind != JobUpdate {
		t.Fatalf("job: %+v", job)
	}
	wantChanges(t, f, "image.pull example/tek:1.0")
	if !strings.Contains(e.do("GET", "/apps/jobs/"+job.ID, e.admin, "").Body.String(), "zaten güncel") {
		t.Error("the job does not say that the application is up to date")
	}
	var v installedView
	decodeData(t, e.do("GET", "/apps/installed/tek", e.admin, ""), &v)
	if v.BindAddress != BindAll || v.Ports[0].HostIP != "0.0.0.0" {
		t.Errorf("view of a legacy installation: %+v", v)
	}

	// A new image is an update; the bind address chosen later survives it.
	if job := e.startJob("PUT", "/apps/installed/tek/settings", `{"bind_address":"loopback"}`); job.Status != JobSuccess {
		t.Fatalf("settings: %+v", job)
	}
	f.mu.Lock()
	f.images["example/tek:1.0"] = "sha256:" + strings.Repeat("b", 64)
	f.mu.Unlock()
	f.reset()
	job = e.startJob("POST", "/apps/installed/tek/update", "")
	if job.Status != JobSuccess {
		t.Fatalf("update: %+v", job)
	}
	backup := "myserver-tek-onceki-" + job.ID[:8]
	wantChanges(t, f, "image.pull example/tek:1.0", "container.stop myserver-tek", "container.rename myserver-tek "+backup,
		"container.create myserver-tek", "container.start myserver-tek", "container.remove "+backup)
	c := f.created("myserver-tek")
	if pb := c.Body.HostConfig.PortBindings["80/tcp"]; len(pb) != 1 || pb[0].HostIP != "127.0.0.1" {
		t.Errorf("the update lost the bind address: %+v", pb)
	}
	if envMap(c.Body.Env)["ADMIN_PASSWORD"] != "x" {
		t.Error("the update lost a secret")
	}
	now, _ := e.mod.store.get(context.Background(), "tek")
	if now.Images["app"].ID != "sha256:"+strings.Repeat("b", 64) || now.Config.BindAddress != BindLoopback {
		t.Errorf("stored after the update: %+v", now)
	}
}

func TestLifecycleOrder(t *testing.T) {
	captureLogs(t)
	f := newFakeDocker(t)
	untouched := bystanders(f)
	e := newTestEnv(t, f.host(), map[string]string{"cift.yaml": manifestCift})
	seedInstalled(t, e, f, "cift", Inputs{})
	f.reset()

	var v installedView
	decodeData(t, e.do("POST", "/apps/installed/cift/stop", e.admin, ""), &v)
	wantChanges(t, f, "container.stop myserver-cift-web", "container.stop myserver-cift-db")
	if v.State != StateStopped {
		t.Errorf("state after stop: %s", v.State)
	}
	f.reset()
	decodeData(t, e.do("POST", "/apps/installed/cift/start", e.admin, ""), &v)
	wantChanges(t, f, "container.start myserver-cift-db", "container.start myserver-cift-web")
	if v.State != StateRunning {
		t.Errorf("state after start: %s", v.State)
	}
	f.reset()
	decodeData(t, e.do("POST", "/apps/installed/cift/restart", e.admin, ""), &v)
	wantChanges(t, f, "container.stop myserver-cift-web", "container.stop myserver-cift-db",
		"container.start myserver-cift-db", "container.start myserver-cift-web")
	untouched(t)
	for _, a := range []string{"apps.stop", "apps.start", "apps.restart"} {
		if rows := e.findAudit(a); len(rows) != 1 || !rows[0].Success || rows[0].Target != "cift" {
			t.Errorf("audit %s: %+v", a, rows)
		}
	}
	wantError(t, e.do("POST", "/apps/installed/yok/start", e.admin, ""), http.StatusNotFound, "not_found", "kurulu değil")
	wantError(t, e.do("POST", "/apps/installed/Bad_Slug/start", e.admin, ""), http.StatusBadRequest, "bad_request")
}

/* ---------- restore (RecreateApp) failure ---------- */

// A restore of an application that is not installed and fails midway must
// leave nothing of its own behind, and keep the volume restored before it.
func TestRecreateFailureCleansUp(t *testing.T) {
	captureLogs(t)
	f := newFakeDocker(t)
	untouched := bystanders(f)
	f.volumes["myserver-cift-db"] = managed("cift", "")
	f.fail["container.start myserver-cift-web"] = "boom"
	e := newTestEnv(t, f.host(), map[string]string{"cift.yaml": manifestCift})
	cfg, _, err := Resolve(e.mod.catalog.Get("cift"), Inputs{}, nil, e.mod.roots())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.mod.RecreateApp(context.Background(), audit.Actor{Username: "yedek"}, cfg); err == nil {
		t.Fatal("the failure was not reported")
	}
	untouched(t)
	if names := f.names(); containsString(names, "myserver-cift-db") || containsString(names, "myserver-cift-web") {
		t.Errorf("containers left behind: %v", names)
	}
	f.mu.Lock()
	_, vol := f.volumes["myserver-cift-db"]
	_, net := f.networks["myserver-cift"]
	f.mu.Unlock()
	if !vol {
		t.Error("the restored volume was removed")
	}
	if net {
		t.Error("the network created by the failed restore is left behind")
	}
	if containsString(f.changes(), "volume.remove myserver-cift-db") {
		t.Error("the restored volume was asked to be removed")
	}
	if it, _ := e.mod.store.get(context.Background(), "cift"); it != nil {
		t.Error("recorded as installed")
	}
	if e.mod.operation("cift") != "" {
		t.Error("the application stays locked")
	}
}

func TestSecretMasker(t *testing.T) {
	cfg := &Config{Services: []ServiceConfig{
		{Env: []EnvValue{
			{Name: "A", Value: "parola-uzun-ve-gizli", Secret: true},
			{Name: "B", Value: "parola", Secret: true},
			{Name: "C", Value: "abc", Secret: true},
			{Name: "TZ", Value: "Europe/Istanbul"},
		}},
		{Env: []EnvValue{{Name: "D", Value: "parola", Secret: true}, {Name: "E", Value: "", Secret: true}}},
	}}
	mask := secretMasker(cfg)
	got := mask("login parola-uzun-ve-gizli / parola at Europe/Istanbul abc")
	if strings.Contains(got, "parola") || !strings.Contains(got, "Europe/Istanbul") || !strings.Contains(got, "abc") ||
		!strings.HasPrefix(got, "login ") {
		t.Errorf("masked: %q", got)
	}
	if strings.Contains(got, "-uzun-ve-gizli") {
		t.Errorf("the longer secret was masked only in part: %q", got)
	}
	for _, c := range []*Config{nil, {}, {Services: []ServiceConfig{{Env: []EnvValue{{Name: "TZ", Value: "UTC"}}}}}} {
		if got := secretMasker(c)("plain UTC text"); got != "plain UTC text" {
			t.Errorf("text changed without secrets: %q", got)
		}
	}
}
