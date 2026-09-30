package apps

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests use real directories and real symbolic links.

type bindDirs struct {
	root    string // allowed root
	outside string // a directory next to it, not allowed
}

func newBindDirs(t *testing.T) bindDirs {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := bindDirs{root: filepath.Join(base, "veri"), outside: filepath.Join(base, "disarida")}
	for _, p := range []string{d.root, d.outside, filepath.Join(d.root, "gercek"), filepath.Join(d.outside, "ozel")} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
}

func entries(t *testing.T, dir string) string {
	t.Helper()
	list, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range list {
		names = append(names, e.Name())
	}
	return strings.Join(names, ",")
}

func TestPrepareBindDirSymlinks(t *testing.T) {
	d := newBindDirs(t)
	roots := []string{d.root}
	symlink(t, d.outside, filepath.Join(d.root, "kacak"))
	symlink(t, "/etc", filepath.Join(d.root, "sistem"))
	symlink(t, "/", filepath.Join(d.root, "kok"))
	symlink(t, "../disarida/ozel", filepath.Join(d.root, "goreli"))
	symlink(t, filepath.Join(d.outside, "yok"), filepath.Join(d.root, "kirik"))
	symlink(t, "/var/run/docker.sock", filepath.Join(d.root, "soket"))
	symlink(t, filepath.Join(d.root, "gercek"), filepath.Join(d.root, "icerde"))
	symlink(t, filepath.Join(d.root, "kacak"), filepath.Join(d.root, "zincir"))
	if err := os.WriteFile(filepath.Join(d.root, "dosya"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := entries(t, d.outside)

	refused := []string{
		filepath.Join(d.root, "kacak"),
		filepath.Join(d.root, "kacak", "ozel"),
		filepath.Join(d.root, "kacak", "yeni", "klasor"),
		filepath.Join(d.root, "sistem"),
		filepath.Join(d.root, "sistem", "ssh"),
		filepath.Join(d.root, "kok", "etc"),
		filepath.Join(d.root, "kok", "home", "yeni"),
		filepath.Join(d.root, "goreli"),
		filepath.Join(d.root, "kirik"),
		filepath.Join(d.root, "kirik", "alt"),
		filepath.Join(d.root, "soket"),
		filepath.Join(d.root, "zincir"),
		filepath.Join(d.root, "zincir", "yeni"),
		filepath.Join(d.root, "dosya"),
		d.outside,
		filepath.Join(d.outside, "ozel"),
	}
	for _, p := range refused {
		err := prepareBindDir(p, roots, nil)
		if err == nil {
			t.Errorf("%s accepted", strings.TrimPrefix(p, filepath.Dir(d.root)))
			continue
		}
		wantInputError(t, err)
	}
	if after := entries(t, d.outside); after != before {
		t.Errorf("a refused path created something outside the allowed root: %q -> %q", before, after)
	}
	if _, err := os.Lstat("/home/yeni"); err == nil {
		t.Error("/home/yeni was created through a symlink to /")
		os.Remove("/home/yeni")
	}

	accepted := []string{
		d.root,
		filepath.Join(d.root, "gercek"),
		filepath.Join(d.root, "icerde"),
		filepath.Join(d.root, "icerde", "alt"),
		filepath.Join(d.root, "yeni", "derin", "klasor"),
	}
	for _, p := range accepted {
		if err := prepareBindDir(p, roots, nil); err != nil {
			t.Errorf("%s: %v", strings.TrimPrefix(p, filepath.Dir(d.root)), err)
			continue
		}
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			t.Errorf("%s was not created", p)
		}
	}
	if _, err := os.Stat(filepath.Join(d.root, "gercek", "alt")); err != nil {
		t.Error("a link inside the root must lead to the real folder")
	}
}

// An allowed root may itself be a symbolic link (a disk mounted elsewhere).
func TestPrepareBindDirRootIsSymlink(t *testing.T) {
	d := newBindDirs(t)
	link := filepath.Join(filepath.Dir(d.root), "baglanti")
	symlink(t, d.root, link)
	if err := prepareBindDir(filepath.Join(link, "film"), []string{link}, nil); err != nil {
		t.Fatalf("path below a linked root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(d.root, "film")); err != nil {
		t.Error("folder not created in the real root")
	}
	symlink(t, d.outside, filepath.Join(d.root, "kacak"))
	if err := prepareBindDir(filepath.Join(link, "kacak", "x"), []string{link}, nil); err == nil {
		t.Error("escape below a linked root accepted")
	}
	// A root that does not exist allows nothing.
	if err := prepareBindDir(filepath.Join(d.outside, "x"), []string{filepath.Join(d.outside, "yok"), d.root}, nil); err == nil {
		t.Error("path outside accepted")
	}
}

// The same through the API: the installation fails, cleans up, and nothing
// is created outside.
func TestInstallRefusesSymlinkEscape(t *testing.T) {
	captureLogs(t)
	f := newFakeDocker(t)
	e := newTestEnv(t, f.host(), map[string]string{"tek.yaml": manifestTek})
	outside := filepath.Join(filepath.Dir(e.root), "disarida")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, outside, filepath.Join(e.root, "kacak"))
	job := e.startJob("POST", "/apps/catalog/tek/install", body(map[string]any{
		"env": map[string]string{"ADMIN_PASSWORD": "x"}, "paths": map[string]string{"media": filepath.Join(e.root, "kacak", "film")},
	}))
	if job.Status != JobFailed || !strings.Contains(job.Error, "izin verilen klasörlerin dışına işaret ediyor") {
		t.Fatalf("job: %+v", job)
	}
	for _, op := range f.changes() {
		if strings.HasPrefix(op, "container.") {
			t.Errorf("a container was created for a refused folder: %s", op)
		}
	}
	f.mu.Lock()
	if len(f.networks) != 0 || len(f.volumes) != 0 {
		t.Errorf("left behind: networks %v volumes %v", f.networks, f.volumes)
	}
	f.mu.Unlock()
	if got := entries(t, outside); got != "" {
		t.Errorf("created outside the allowed root: %s", got)
	}
	if it, _ := e.mod.store.get(context.Background(), "tek"); it != nil {
		t.Error("recorded as installed")
	}
}

// The panel's own data directory holds the database with every stored
// secret and session. It must not be handed to an application, wherever the
// administrator has placed it.
func TestPanelDataDirectoryCannotBeMounted(t *testing.T) {
	captureLogs(t)
	f := newFakeDocker(t)
	e := newTestEnv(t, f.host(), map[string]string{"tek.yaml": manifestTek})
	// Allow the parent of the data directory, as an administrator who keeps
	// everything on one data disk would.
	base := filepath.Dir(e.cfg.DataDir)
	if err := e.store.SetStrings(context.Background(), "files.allowed_roots", []string{base}); err != nil {
		t.Fatal(err)
	}
	symlink(t, e.cfg.DataDir, filepath.Join(e.root, "panel-linki"))
	direct := []string{e.cfg.DataDir, filepath.Join(e.cfg.DataDir, "backups"), e.cfg.DataDir + "/"}
	for _, p := range direct {
		w := e.do("POST", "/apps/catalog/tek/install", e.admin, body(map[string]any{
			"env": map[string]string{"ADMIN_PASSWORD": "x"}, "paths": map[string]string{"media": p},
		}))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", strings.TrimPrefix(p, base), w.Code, w.Body.String())
			if w.Code == http.StatusAccepted {
				var v JobView
				decodeData(t, w, &v)
				e.waitJob(v.ID)
				e.do("POST", "/apps/installed/tek/uninstall", e.admin, `{"delete_data":false}`)
			}
		}
	}
	// Through a symbolic link it is found when the folder is prepared.
	job := e.startJob("POST", "/apps/catalog/tek/install", body(map[string]any{
		"env": map[string]string{"ADMIN_PASSWORD": "x"}, "paths": map[string]string{"media": filepath.Join(e.root, "panel-linki")},
	}))
	if job.Status != JobFailed {
		t.Errorf("the data directory was mounted through a symlink: %+v", job)
	}
	// A restored configuration is checked the same way.
	m := e.mod.catalog.Get("tek")
	cfg, _, err := Resolve(m, Inputs{Env: map[string]string{"ADMIN_PASSWORD": "x"}, Paths: map[string]string{"media": filepath.Join(e.root, "m")}},
		nil, e.mod.roots())
	if err != nil {
		t.Fatal(err)
	}
	for i := range cfg.Services[0].Volumes {
		if cfg.Services[0].Volumes[i].Type == VolumeBind {
			cfg.Services[0].Volumes[i].Source = e.cfg.DataDir
		}
	}
	if err := e.mod.RecreateApp(context.Background(), auditActor("yedek"), cfg); err == nil {
		t.Error("RecreateApp mounted the data directory")
	}
	// Other folders of the same disk stay usable.
	job = e.startJob("POST", "/apps/catalog/tek/install", body(map[string]any{
		"env": map[string]string{"ADMIN_PASSWORD": "x"}, "paths": map[string]string{"media": filepath.Join(base, "filmler")},
	}))
	if job.Status != JobSuccess {
		t.Errorf("a folder next to the data directory was refused: %+v", job)
	}
}
