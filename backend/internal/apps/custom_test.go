package apps

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"

	"myserver/internal/httpx"
)

// customCompose is an Uptime Kuma file with a named volume, a bind mount
// inside the allowed root and a password variable.
func customCompose(root string) string {
	return fmt.Sprintf(`
services:
  kuma:
    image: louislam/uptime-kuma:1
    container_name: uptime-kuma
    restart: always
    ports:
      - "13001:3001"
    volumes:
      - kuma-data:/app/data
      - %s/yedek:/backup
    environment:
      TZ: Europe/Istanbul
      ADMIN_PASSWORD: ${ADMIN_PASSWORD}
volumes:
  kuma-data:
`, root)
}

const kumaSlug = "custom-uptime-kuma"

func (e *testEnv) customCount() int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM apps_custom`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func customBody(name, compose, digest string) string {
	m := map[string]string{"name": name, "compose": compose}
	if digest != "" {
		m["digest"] = digest
	}
	return body(m)
}

// The whole life of a custom application: preview, save, install through
// the ordinary install path, restore checks, removal and deletion.
func TestCustomAppLifecycle(t *testing.T) {
	logs := captureLogs(t)
	f := newFakeDocker(t)
	untouched := bystanders(f)
	e := newTestEnv(t, f.host(), map[string]string{"tek.yaml": manifestTek})
	compose := customCompose(e.root)
	ctx := context.Background()

	// Preview: the detail in the catalog shape, nothing stored.
	var pv customPreview
	decodeData(t, e.do("POST", "/apps/custom/preview", e.admin, customBody("Uptime Kuma", compose, "")), &pv)
	if pv.Slug != kumaSlug || !pv.Custom || pv.CategoryLabel != "Özel" || pv.Icon != CustomIcon || pv.Installed {
		t.Errorf("preview: %+v", pv.catalogView)
	}
	if len(pv.Fields.Ports) != 1 || pv.Fields.Ports[0].Default != 13001 || pv.Fields.Ports[0].Container != 3001 {
		t.Errorf("ports: %+v", pv.Fields.Ports)
	}
	if len(pv.Fields.Paths) != 1 || pv.Fields.Paths[0].Default != e.root+"/yedek" || !pv.Fields.Paths[0].Required {
		t.Errorf("paths: %+v", pv.Fields.Paths)
	}
	var secret *envField
	for i := range pv.Fields.Env {
		if pv.Fields.Env[i].Key == "ADMIN_PASSWORD" {
			secret = &pv.Fields.Env[i]
		}
	}
	if secret == nil || !secret.Secret || !secret.Generated {
		t.Errorf("env: %+v", pv.Fields.Env)
	}
	if len(pv.Volumes) != 1 || pv.Volumes[0].Source != "myserver-custom-uptime-kuma-kuma-data" {
		t.Errorf("volumes: %+v", pv.Volumes)
	}
	if len(pv.Warnings) != 0 || pv.RequiresRiskAcceptance {
		t.Errorf("warnings: %+v", pv.Warnings)
	}
	if len(pv.Digest) != 64 || !strings.Contains(pv.ManifestYAML, "slug: "+kumaSlug) || len(pv.ConversionNotes) == 0 || !strings.Contains(pv.Notes, "container_name") {
		t.Errorf("digest %q, notes %+v, yaml:\n%s", pv.Digest, pv.ConversionNotes, pv.ManifestYAML)
	}
	if e.customCount() != 0 || e.mod.catalog.Get(kumaSlug) != nil {
		t.Fatal("the preview stored the definition")
	}
	wantError(t, e.do("GET", "/apps/catalog/"+kumaSlug, e.admin, ""), http.StatusNotFound, "not_found")

	// Save exactly what was previewed.
	w := e.do("POST", "/apps/custom", e.admin, customBody("Uptime Kuma", compose, pv.Digest))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var item catalogView
	decodeData(t, w, &item)
	if item.Slug != kumaSlug || !item.Custom || item.Installed || item.Category != CustomCategory {
		t.Errorf("created: %+v", item)
	}
	if e.customCount() != 1 {
		t.Error("not stored")
	}

	// It is in the catalog, under "Özel", for every signed-in user.
	var cat struct {
		Apps       []catalogView `json:"apps"`
		Categories []Category    `json:"categories"`
	}
	decodeData(t, e.do("GET", "/apps/catalog", e.user, ""), &cat)
	found := false
	for _, a := range cat.Apps {
		if a.Slug == kumaSlug && a.Custom {
			found = true
		}
		if a.Slug == "tek" && a.Custom {
			t.Error("a shipped application is reported as custom")
		}
	}
	if !found || fmt.Sprint(cat.Categories) != fmt.Sprint([]Category{{"tools", "Araçlar"}, {CustomCategory, "Özel"}}) {
		t.Errorf("catalog: %+v", cat)
	}
	var detail detailView
	decodeData(t, e.do("GET", "/apps/catalog/"+kumaSlug, e.user, ""), &detail)
	if detail.Fields.Paths[0].Default != e.root+"/yedek" {
		t.Errorf("detail: %+v", detail.Fields)
	}

	// Installed through the ordinary install endpoint.
	const password = "kuma-yonetici-parolasi"
	job := e.startJob("POST", "/apps/catalog/"+kumaSlug+"/install", `{"env":{"ADMIN_PASSWORD":"`+password+`"}}`)
	if job.Status != JobSuccess {
		t.Fatalf("install: %+v", job)
	}
	wantChanges(t, f,
		"image.pull louislam/uptime-kuma:1",
		"network.create myserver-custom-uptime-kuma",
		"volume.create myserver-custom-uptime-kuma-kuma-data",
		"container.create myserver-custom-uptime-kuma",
		"container.start myserver-custom-uptime-kuma",
	)
	untouched(t)
	c := f.created("myserver-custom-uptime-kuma")
	if c == nil {
		t.Fatal("container not created")
	}
	b := c.Body
	if len(b.Labels) != 3 || b.Labels[LabelApp] != kumaSlug || b.Labels[LabelService] != "kuma" || b.Labels[LabelManaged] != "true" {
		t.Errorf("labels: %+v", b.Labels)
	}
	if pb := b.HostConfig.PortBindings["3001/tcp"]; len(b.HostConfig.PortBindings) != 1 || len(pb) != 1 || pb[0].HostPort != "13001" || pb[0].HostIP != "0.0.0.0" {
		t.Errorf("ports: %+v", b.HostConfig.PortBindings)
	}
	var mounts []string
	for _, m := range b.HostConfig.Mounts {
		mounts = append(mounts, m.Type+":"+m.Source+":"+m.Target)
	}
	sort.Strings(mounts)
	if strings.Join(mounts, ",") != "bind:"+e.root+"/yedek:/backup,volume:myserver-custom-uptime-kuma-kuma-data:/app/data" {
		t.Errorf("mounts: %v", mounts)
	}
	env := envMap(b.Env)
	if env["TZ"] != "Europe/Istanbul" || env["ADMIN_PASSWORD"] != password || len(env) != 2 {
		t.Errorf("env: %v", b.Env)
	}
	if b.HostConfig.Privileged || len(b.HostConfig.CapAdd) != 0 || b.HostConfig.NetworkMode != "myserver-custom-uptime-kuma" {
		t.Errorf("host config: %+v", b.HostConfig)
	}
	var iv installedView
	decodeData(t, e.do("GET", "/apps/installed/"+kumaSlug, e.user, ""), &iv)
	if !iv.Custom || iv.State != StateRunning || iv.WebUI == nil || iv.WebUI.Port != 13001 || !iv.ManifestAvailable {
		t.Errorf("installed view: %+v", iv)
	}

	// A restored backup must conform to the stored manifest.
	cfg, err := e.mod.AppConfig(ctx, kumaSlug)
	if err != nil {
		t.Fatal(err)
	}
	f.reset()
	for name, change := range map[string]func(c *Config){
		"another image": func(c *Config) { c.Services[0].Image = "evil/kuma:1" },
		"extra env": func(c *Config) {
			c.Services[0].Env = append(c.Services[0].Env, EnvValue{Name: "LD_PRELOAD", Key: "x", Value: "/tmp/x.so"})
		},
		"privileged": func(c *Config) { c.Services[0].Privileged = true },
		"docker socket": func(c *Config) {
			c.Services[0].Volumes = append(c.Services[0].Volumes, Mount{Type: VolumeSystem, Source: "/var/run/docker.sock", Target: "/s"})
		},
	} {
		bad := clone(t, cfg)
		change(bad)
		err := e.mod.RecreateApp(ctx, auditActor("yedek"), bad)
		var he *httpx.Error
		if !errors.As(err, &he) || he.Code != "invalid_config" {
			t.Errorf("%s: got %v, want invalid_config", name, err)
		}
	}
	if ops := f.changes(); len(ops) != 0 {
		t.Errorf("refused restores changed Docker: %v", ops)
	}
	good := clone(t, cfg)
	good.Services[0].Command = []string{"--kotu"} // taken from the manifest, not refused
	if err := e.mod.RecreateApp(ctx, auditActor("yedek"), good); err != nil {
		t.Fatalf("restore of the untampered configuration: %v", err)
	}
	if c := f.created("myserver-custom-uptime-kuma"); c == nil || len(c.Body.Cmd) != 0 {
		t.Errorf("restored container: %+v", c)
	}

	// The definition cannot be deleted while the application is installed.
	wantError(t, e.do("DELETE", "/apps/custom/"+kumaSlug, e.admin, ""), http.StatusConflict, "custom_installed", "kaldırın")
	if e.customCount() != 1 || e.mod.catalog.Get(kumaSlug) == nil {
		t.Fatal("deleted while installed")
	}

	// It survives a restart of the panel.
	restarted, err := New(e.mod.deps, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := restarted.(*Module).catalog.Get(kumaSlug); got == nil {
		t.Error("custom application lost after a restart")
	} else if a, b := mustYAML(t, got), mustYAML(t, e.mod.catalog.Get(kumaSlug)); a != b {
		t.Errorf("reloaded manifest differs:\n%s\n---\n%s", a, b)
	}

	// Uninstall, then delete.
	if w := e.do("POST", "/apps/installed/"+kumaSlug+"/uninstall", e.admin, `{}`); w.Code != http.StatusOK {
		t.Fatalf("uninstall: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("DELETE", "/apps/custom/"+kumaSlug, e.admin, ""); w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if e.customCount() != 0 || e.mod.catalog.Get(kumaSlug) != nil {
		t.Error("definition still present")
	}
	wantError(t, e.do("GET", "/apps/catalog/"+kumaSlug, e.admin, ""), http.StatusNotFound, "not_found")
	wantError(t, e.do("DELETE", "/apps/custom/"+kumaSlug, e.admin, ""), http.StatusNotFound, "not_found")

	var got []string
	for _, a := range e.auditRows() {
		if strings.HasPrefix(a.Action, "apps.custom_") {
			if a.Username != e.admin.name || a.Target != kumaSlug {
				t.Errorf("audit row: %+v", a)
			}
			got = append(got, fmt.Sprintf("%s:%v", a.Action, a.Success))
		}
	}
	if strings.Join(got, ",") != "apps.custom_create:true,apps.custom_delete:false,apps.custom_delete:true" {
		t.Errorf("audit: %v", got)
	}
	if strings.Contains(logs.String(), password) {
		t.Error("the password was logged")
	}
}

func mustYAML(t *testing.T, m *Manifest) string {
	t.Helper()
	y, err := manifestYAML(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(y)
}

// A refused file is a 400 listing every problem, and nothing is stored.
func TestCustomAppRefusedWithEveryProblem(t *testing.T) {
	captureLogs(t)
	e := newTestEnv(t, refusedTCPHost(t), nil)
	compose := "services:\n  a:\n    build: .\n  b:\n    image: x/b:1\n    env_file: .env\n    volumes:\n      - /etc:/host-etc\n"
	for _, path := range []string{"/apps/custom/preview", "/apps/custom"} {
		w := e.do("POST", path, e.admin, customBody("Bozuk", compose, ""))
		wantError(t, w, http.StatusBadRequest, "compose_refused", "3 sorun", `servis "a", build`, `servis "b", env_file`, "/etc bir sistem klasörüdür")
		if env := parseEnvelope(t, w); strings.Count(env.Error.Message, "\n• ") != 3 {
			t.Errorf("%s: %q", path, env.Error.Message)
		}
	}
	if e.customCount() != 0 || len(e.mod.catalog.List()) != 0 {
		t.Error("a refused definition was stored")
	}
	rows := e.findAudit("apps.custom_create")
	if len(rows) != 1 || rows[0].Success || rows[0].Target != "custom-bozuk" {
		t.Errorf("audit: %+v", rows)
	}
	// Bodies that are not a compose request at all.
	wantError(t, e.do("POST", "/apps/custom/preview", e.admin, `{"name":"x","compose":"services: {}","extra":1}`), http.StatusBadRequest, "bad_request")
	big := customBody("Büyük", "services:\n  a:\n    image: x/a:1\n#"+strings.Repeat("x", MaxComposeBytes), "")
	wantError(t, e.do("POST", "/apps/custom/preview", e.admin, big), http.StatusBadRequest, "compose_refused", "çok büyük")
}

func TestCustomAppNameCollisions(t *testing.T) {
	captureLogs(t)
	e := newTestEnv(t, refusedTCPHost(t), nil)
	if w := e.do("POST", "/apps/custom", e.admin, customBody("Uptime Kuma", composeUptimeKuma, "")); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	// The same name, spelled differently, gives the same slug.
	for _, name := range []string{"Uptime Kuma", "uptime-kuma", "UPTIME  KUMA!"} {
		for _, path := range []string{"/apps/custom/preview", "/apps/custom"} {
			wantError(t, e.do("POST", path, e.admin, customBody(name, composeUptimeKuma, "")), http.StatusBadRequest, "compose_refused", "zaten var")
		}
	}
	// A slug still used by an installed application (for example restored
	// from a backup) cannot be taken either.
	cfg := &Config{Slug: "custom-eski", Name: "Eski", Services: []ServiceConfig{{Name: "app", ContainerName: "myserver-custom-eski", Image: "x/a:1"}}}
	if err := e.mod.store.save(context.Background(), cfg, &Inputs{}, nil); err != nil {
		t.Fatal(err)
	}
	wantError(t, e.do("POST", "/apps/custom/preview", e.admin, customBody("Eski", composeUptimeKuma, "")), http.StatusBadRequest, "compose_refused", "kurulu bir uygulama")
	if e.customCount() != 1 {
		t.Errorf("%d definitions stored", e.customCount())
	}
}

// Two applications may not produce the same Docker volume name.
func TestCustomAppVolumeNameClash(t *testing.T) {
	captureLogs(t)
	e := newTestEnv(t, refusedTCPHost(t), nil)
	first := "services:\n  a:\n    image: x/a:1\n    volumes:\n      - b-data:/data\nvolumes:\n  b-data:\n"
	if w := e.do("POST", "/apps/custom", e.admin, customBody("A", first, "")); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	second := "services:\n  a:\n    image: x/a:1\n    volumes:\n      - data:/data\nvolumes:\n  data:\n"
	wantError(t, e.do("POST", "/apps/custom", e.admin, customBody("A B", second, "")), http.StatusBadRequest, "compose_refused",
		"myserver-custom-a-b-data", "aynı ada")
	if e.customCount() != 1 {
		t.Error("the clashing definition was stored")
	}
}

// What is saved must be what was reviewed.
func TestCustomAppDigestMismatch(t *testing.T) {
	captureLogs(t)
	e := newTestEnv(t, refusedTCPHost(t), nil)
	var pv customPreview
	decodeData(t, e.do("POST", "/apps/custom/preview", e.admin, customBody("Uptime Kuma", composeUptimeKuma, "")), &pv)
	changed := strings.Replace(composeUptimeKuma, "3001:3001", "3002:3001", 1)
	wantError(t, e.do("POST", "/apps/custom", e.admin, customBody("Uptime Kuma", changed, pv.Digest)), http.StatusConflict, "custom_changed", "yeniden önizleyin")
	if e.customCount() != 0 {
		t.Error("stored despite the changed definition")
	}
	if w := e.do("POST", "/apps/custom", e.admin, customBody("Uptime Kuma", composeUptimeKuma, pv.Digest)); w.Code != http.StatusCreated {
		t.Errorf("matching digest: %d %s", w.Code, w.Body.String())
	}
}

func TestCustomAppDeleteChecks(t *testing.T) {
	captureLogs(t)
	e := newTestEnv(t, refusedTCPHost(t), map[string]string{"tek.yaml": manifestTek})
	wantError(t, e.do("DELETE", "/apps/custom/tek", e.admin, ""), http.StatusBadRequest, "bad_request")
	wantError(t, e.do("DELETE", "/apps/custom/custom-yok", e.admin, ""), http.StatusNotFound, "not_found")
	wantError(t, e.do("DELETE", "/apps/custom/custom-..", e.admin, ""), http.StatusBadRequest, "bad_request")
	if e.mod.catalog.Get("tek") == nil {
		t.Error("a shipped application was removed")
	}
}

// All three endpoints are for administrators only; a refused request
// neither stores nor records anything.
func TestCustomAppAdminOnly(t *testing.T) {
	captureLogs(t)
	e := newTestEnv(t, refusedTCPHost(t), nil)
	if w := e.do("POST", "/apps/custom", e.admin, customBody("Uptime Kuma", composeUptimeKuma, "")); w.Code != http.StatusCreated {
		t.Fatalf("create: %d", w.Code)
	}
	req := customBody("Başka", composeUptimeKuma, "")
	wantError(t, e.do("POST", "/apps/custom/preview", e.user, req), http.StatusForbidden, "forbidden")
	wantError(t, e.do("POST", "/apps/custom", e.user, req), http.StatusForbidden, "forbidden")
	wantError(t, e.do("DELETE", "/apps/custom/"+kumaSlug, e.user, ""), http.StatusForbidden, "forbidden")
	if w := e.do("POST", "/apps/custom/preview", nil, req); w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", w.Code)
	}
	if e.customCount() != 1 || e.mod.catalog.Get(kumaSlug) == nil {
		t.Error("a refused request changed the definitions")
	}
	for _, a := range e.auditRows() {
		if a.Username == e.user.name {
			t.Errorf("recorded: %+v", a)
		}
	}
}

// Dangerous settings of a compose file need the same explicit acceptance
// as in a shipped manifest, through the same install endpoint.
func TestCustomAppRiskAcceptance(t *testing.T) {
	captureLogs(t)
	f := newFakeDocker(t)
	e := newTestEnv(t, f.host(), nil)
	compose := "services:\n  agent:\n    image: portainer/agent:2.21.0\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n"
	if w := e.do("POST", "/apps/custom", e.admin, customBody("Ajan", compose, "")); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var d detailView
	decodeData(t, e.do("GET", "/apps/catalog/custom-ajan", e.admin, ""), &d)
	if !d.RequiresRiskAcceptance || len(d.Warnings) != 1 || d.Warnings[0].Code != "docker_socket" || d.WarningLevel != "danger" {
		t.Errorf("detail: %+v", d.Warnings)
	}
	wantError(t, e.do("POST", "/apps/catalog/custom-ajan/install", e.admin, `{}`), http.StatusBadRequest, "risk_not_accepted")
	if ops := f.changes(); len(ops) != 0 {
		t.Errorf("Docker changed: %v", ops)
	}
}

// A stored definition is validated again when the panel starts; one that
// no longer validates is reported, not loaded, and can still be deleted.
func TestCustomAppStoredDefinitionIsRevalidated(t *testing.T) {
	captureLogs(t)
	e := newTestEnv(t, refusedTCPHost(t), nil)
	conv := convertOK(t, "Uptime Kuma", composeUptimeKuma)
	rows := map[string]string{
		kumaSlug:         string(conv.YAML),
		"custom-bozuk":   "name: [\n",
		"custom-baskasi": string(conv.YAML), // slug inside differs
	}
	for slug, y := range rows {
		if _, err := e.db.Exec(`INSERT INTO apps_custom (slug, name, manifest, created_at) VALUES (?, ?, ?, 1)`, slug, slug, y); err != nil {
			t.Fatal(err)
		}
	}
	mod, err := New(e.mod.deps, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := mod.(*Module)
	if m.catalog.Get(kumaSlug) == nil || m.catalog.Get("custom-bozuk") != nil || m.catalog.Get("custom-baskasi") != nil {
		t.Errorf("loaded: %v", m.catalog.List())
	}
	var names []string
	for _, i := range m.catalog.Invalid() {
		names = append(names, i.File)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "Özel uygulama: custom-baskasi,Özel uygulama: custom-bozuk" {
		t.Errorf("invalid: %v", names)
	}
	// The table refuses a slug outside the custom name space.
	if _, err := e.db.Exec(`INSERT INTO apps_custom (slug, name, manifest, created_at) VALUES ('tek', 'x', 'x', 1)`); err == nil {
		t.Error("a non-custom slug was stored")
	}
	e.mod.loadCustom(context.Background())
	if w := e.do("DELETE", "/apps/custom/custom-bozuk", e.admin, ""); w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	for _, i := range e.mod.catalog.Invalid() {
		if strings.Contains(i.File, "custom-bozuk") {
			t.Error("the deleted definition is still reported")
		}
	}
}
