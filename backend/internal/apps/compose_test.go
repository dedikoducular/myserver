package apps

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The compose files below are realistic: they are what an administrator
// finds in the README of a self-hosted project.

var composeOpts = ConvertOptions{Roots: testRoots, Protected: []string{"/data/panel"}}

func convertOK(t *testing.T, name, src string) *Conversion {
	t.Helper()
	opt := composeOpts
	opt.Name = name
	conv, err := ConvertCompose([]byte(src), opt)
	if err != nil {
		t.Fatalf("conversion refused:\n%v", err)
	}
	return conv
}

func convertRefused(t *testing.T, name, src string) []ComposeIssue {
	t.Helper()
	opt := composeOpts
	opt.Name = name
	conv, err := ConvertCompose([]byte(src), opt)
	var ce *ComposeError
	if !errors.As(err, &ce) {
		t.Fatalf("conversion accepted (%v): %+v", err, conv)
	}
	if len(ce.Problems) == 0 {
		t.Fatal("refused without a problem")
	}
	return ce.Problems
}

// wantProblem asserts that one problem of the service mentions every part.
func wantProblem(t *testing.T, problems []ComposeIssue, service string, parts ...string) {
	t.Helper()
	for _, p := range problems {
		if p.Service != service {
			continue
		}
		text := p.Text()
		all := true
		for _, part := range parts {
			if !strings.Contains(text, part) {
				all = false
			}
		}
		if all {
			return
		}
	}
	var lines []string
	for _, p := range problems {
		lines = append(lines, p.Text())
	}
	t.Errorf("no problem of service %q mentions %q; problems:\n  %s", service, parts, strings.Join(lines, "\n  "))
}

func noteText(conv *Conversion) string {
	var lines []string
	for _, n := range conv.Notes {
		lines = append(lines, n.Text())
	}
	return strings.Join(lines, "\n")
}

func serviceOf(t *testing.T, m *Manifest, name string) *ServiceSpec {
	t.Helper()
	for i := range m.Services {
		if m.Services[i].Name == name {
			return &m.Services[i]
		}
	}
	t.Fatalf("service %s missing", name)
	return nil
}

func specEnv(t *testing.T, s *ServiceSpec, name string) EnvSpec {
	t.Helper()
	for _, e := range s.Env {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("env %s missing in %s", name, s.Name)
	return EnvSpec{}
}

/* ---------- accepted files ---------- */

const composeUptimeKuma = `
services:
  uptime-kuma:
    image: louislam/uptime-kuma:1
    container_name: uptime-kuma
    volumes:
      - uptime-kuma:/app/data
    ports:
      - "3001:3001"
    restart: always
volumes:
  uptime-kuma:
`

func TestComposeSingleService(t *testing.T) {
	conv := convertOK(t, "Uptime Kuma", composeUptimeKuma)
	m := conv.Manifest
	if m.Slug != "custom-uptime-kuma" || m.Name != "Uptime Kuma" || m.Category != CustomCategory || m.Icon != CustomIcon {
		t.Errorf("identity: %s %q %s %s", m.Slug, m.Name, m.Category, m.Icon)
	}
	if len(m.Services) != 1 {
		t.Fatalf("services: %+v", m.Services)
	}
	s := m.Services[0]
	if s.Name != "uptime-kuma" || s.Image != "louislam/uptime-kuma:1" || s.Restart != "always" || s.NetworkMode != "bridge" {
		t.Errorf("service: %+v", s)
	}
	if len(s.Ports) != 1 || s.Ports[0].Host != 3001 || s.Ports[0].Container != 3001 || s.Ports[0].Protocol != "tcp" ||
		s.Ports[0].WebUI == nil || s.Ports[0].WebUI.Scheme != "http" {
		t.Errorf("ports: %+v", s.Ports)
	}
	if len(s.Volumes) != 1 || s.Volumes[0].Type != VolumeNamed || s.Volumes[0].Source != "uptime-kuma" || s.Volumes[0].Target != "/app/data" {
		t.Errorf("volumes: %+v", s.Volumes)
	}
	if !strings.Contains(noteText(conv), "container_name") {
		t.Errorf("the ignored container name is not explained: %s", noteText(conv))
	}
	if len(Warnings(m)) != 0 || NeedsRiskAcceptance(m) {
		t.Errorf("warnings: %+v", Warnings(m))
	}

	// What is stored is exactly what was validated, and reads back the same.
	again, err := Parse(conv.YAML)
	if err != nil {
		t.Fatalf("stored YAML does not parse: %v\n%s", err, conv.YAML)
	}
	y2, _ := manifestYAML(again)
	if string(y2) != string(conv.YAML) {
		t.Errorf("YAML does not round trip:\n%s\n---\n%s", conv.YAML, y2)
	}
	if len(conv.Digest) != 64 || convertOK(t, "Uptime Kuma", composeUptimeKuma).Digest != conv.Digest {
		t.Error("the digest is not stable")
	}

	cfg, _ := mustResolve(t, m, Inputs{}, nil)
	sc := cfg.Services[0]
	if sc.ContainerName != "myserver-custom-uptime-kuma" || sc.Volumes[0].Source != "myserver-custom-uptime-kuma-uptime-kuma" {
		t.Errorf("resolved: %+v", sc)
	}
	if err := ValidateConfig(clone(t, cfg), m, testRoots); err != nil {
		t.Errorf("resolved configuration does not validate: %v", err)
	}
}

const composeBlog = `
name: blog
services:
  web:
    image: wordpress:6
    depends_on:
      db:
        condition: service_healthy
    ports:
      - target: 80
        published: 8088
        protocol: tcp
        name: Site
    environment:
      WORDPRESS_DB_HOST: db
      WORDPRESS_DB_USER: wp
      WORDPRESS_DB_PASSWORD: ${DB_PASSWORD}
      WORDPRESS_TABLE_PREFIX: ${PREFIX:-wp_}
  db:
    image: mariadb:11
    environment:
      - MARIADB_USER=wp
      - MARIADB_PASSWORD=${DB_PASSWORD}
      - MARIADB_RANDOM_ROOT_PASSWORD=1
      - MARIADB_DATABASE
    healthcheck:
      test: ["CMD", "healthcheck.sh", "--connect", "--innodb_initialized"]
      interval: 10s
      timeout: 5s
      start_period: 1m
      retries: 5
    volumes:
      - db:/var/lib/mysql
volumes:
  db: {}
`

func TestComposeTwoServicesWithDatabase(t *testing.T) {
	conv := convertOK(t, "", composeBlog)
	m := conv.Manifest
	if m.Slug != "custom-blog" || m.Name != "blog" {
		t.Errorf("the compose name was not used: %s %q", m.Slug, m.Name)
	}
	web, db := serviceOf(t, m, "web"), serviceOf(t, m, "db")
	if strings.Join(web.DependsOn, ",") != "db" {
		t.Errorf("depends_on: %v", web.DependsOn)
	}
	order, _ := StartOrder(m.Services)
	if strings.Join(order, ",") != "db,web" {
		t.Errorf("start order %v", order)
	}
	if h := db.Healthcheck; h == nil || h.Test[0] != "CMD" || h.Interval != "10s" || h.StartPeriod != "1m" || h.Retries != 5 {
		t.Errorf("healthcheck: %+v", db.Healthcheck)
	}
	if p := web.Ports[0]; p.Host != 8088 || p.Container != 80 || p.Label != "Site" || p.WebUI == nil {
		t.Errorf("long port: %+v", p)
	}
	// A literal becomes an editable field with the value as default.
	if e := specEnv(t, web, "WORDPRESS_DB_HOST"); e.Default != "db" || e.Secret || e.Key != "web.WORDPRESS_DB_HOST" {
		t.Errorf("literal: %+v", e)
	}
	// ${DB_PASSWORD} is one secret field shared by both services, generated
	// when left empty.
	a, b := specEnv(t, web, "WORDPRESS_DB_PASSWORD"), specEnv(t, db, "MARIADB_PASSWORD")
	if a.Key != "DB_PASSWORD" || b.Key != "DB_PASSWORD" || !a.Secret || a.Generate != "password" || a.Default != "" {
		t.Errorf("shared secret: %+v %+v", a, b)
	}
	if e := specEnv(t, web, "WORDPRESS_TABLE_PREFIX"); e.Key != "PREFIX" || e.Default != "wp_" || e.Secret {
		t.Errorf("variable with default: %+v", e)
	}
	// A name without value is asked at install time.
	if e := specEnv(t, db, "MARIADB_DATABASE"); e.Default != "" || e.Key != "db.MARIADB_DATABASE" {
		t.Errorf("value-less variable: %+v", e)
	}
	fields := buildFields(m, nil)
	keys := map[string]int{}
	for _, f := range fields.Env {
		keys[f.Key]++
	}
	if keys["DB_PASSWORD"] != 1 {
		t.Errorf("the shared variable is not one field: %+v", fields.Env)
	}

	cfg, in := mustResolve(t, m, Inputs{}, nil)
	pw := in.Env["DB_PASSWORD"]
	if len(pw) != 32 {
		t.Fatalf("password not generated: %q", pw)
	}
	for _, s := range cfg.Services {
		for _, e := range s.Env {
			if (e.Name == "WORDPRESS_DB_PASSWORD" || e.Name == "MARIADB_PASSWORD") && e.Value != pw {
				t.Errorf("%s: %s differs from the shared value", s.Name, e.Name)
			}
		}
	}
	if cfg.Network != "myserver-custom-blog" {
		t.Errorf("network %q", cfg.Network)
	}
}

func TestComposeHealthcheckStringForm(t *testing.T) {
	conv := convertOK(t, "Sağlık", `
services:
  app:
    image: ghcr.io/example/app:2.0
    healthcheck:
      test: curl -fsS http://localhost:8080/health || exit 1
      interval: 1m30s
      retries: 3
`)
	h := conv.Manifest.Services[0].Healthcheck
	if h == nil || len(h.Test) != 2 || h.Test[0] != "CMD-SHELL" || h.Test[1] != "curl -fsS http://localhost:8080/health || exit 1" || h.Interval != "1m30s" {
		t.Errorf("healthcheck: %+v", h)
	}
	if conv.Manifest.Slug != "custom-saglik" {
		t.Errorf("Turkish letters in the slug: %s", conv.Manifest.Slug)
	}
}

const composeHomeAssistant = `
services:
  homeassistant:
    image: ghcr.io/home-assistant/home-assistant:stable
    network_mode: host
    privileged: true
    restart: unless-stopped
    environment:
      TZ: Europe/Istanbul
    volumes:
      - /data/homeassistant:/config
    devices:
      - /dev/ttyUSB0:/dev/ttyUSB0
    cap_add: [NET_ADMIN, CAP_NET_RAW]
    ports:
      - 8123:8123
`

// The warnings of a converted file are those of a catalog manifest that
// declares the same things: same function, same codes, same texts.
const manifestHomeAssistantLike = `name: Ev
slug: ev
description: Karşılaştırma
docker:
  image: ghcr.io/home-assistant/home-assistant:stable
  network_mode: host
  privileged: true
  cap_add: [NET_ADMIN, NET_RAW]
  devices:
    - host: /dev/ttyUSB0
`

func TestComposeHostNetworkAndPrivileges(t *testing.T) {
	conv := convertOK(t, "Ev Otomasyonu", composeHomeAssistant)
	m := conv.Manifest
	s := m.Services[0]
	if s.NetworkMode != "host" || !s.Privileged || strings.Join(s.CapAdd, ",") != "NET_ADMIN,NET_RAW" ||
		len(s.Devices) != 1 || s.Devices[0].Host != "/dev/ttyUSB0" || s.Devices[0].Permissions != "rwm" {
		t.Errorf("service: %+v", s)
	}
	if len(s.Volumes) != 1 || s.Volumes[0].Type != VolumeBind || s.Volumes[0].Source != "/data/homeassistant" || !s.Volumes[0].Required {
		t.Errorf("bind volume: %+v", s.Volumes)
	}
	got, want := Warnings(m), Warnings(mustParse(t, manifestHomeAssistantLike))
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("warnings differ from a catalog manifest with the same settings:\n got %+v\nwant %+v", got, want)
	}
	if !NeedsRiskAcceptance(m) {
		t.Error("privileged mode must require risk acceptance")
	}
	cfg, _ := mustResolve(t, m, Inputs{}, nil)
	if cfg.Services[0].Ports[0].HostIP != "" || cfg.Network != "" {
		t.Errorf("host networking: %+v", cfg.Services[0].Ports)
	}
}

func TestComposeDockerSocket(t *testing.T) {
	for _, sock := range []string{"/var/run/docker.sock", "/run/docker.sock"} {
		conv := convertOK(t, "Yönetici", `
services:
  agent:
    image: portainer/agent:2.21.0
    volumes:
      - `+sock+`:/var/run/docker.sock:ro
`)
		m := conv.Manifest
		v := m.Services[0].Volumes[0]
		if v.Type != VolumeSystem || v.Source != sock || !v.ReadOnly {
			t.Errorf("%s: %+v", sock, v)
		}
		ws := Warnings(m)
		if len(ws) != 1 || ws[0].Code != "docker_socket" || ws[0].Level != "danger" || !NeedsRiskAcceptance(m) {
			t.Errorf("%s: warnings %+v", sock, ws)
		}
		// The restored configuration may only mount it because the stored
		// manifest declares it.
		cfg, _ := mustResolve(t, m, Inputs{}, nil)
		if err := ValidateConfig(clone(t, cfg), m, testRoots); err != nil {
			t.Errorf("%s: %v", sock, err)
		}
	}
}

func TestComposePortSyntax(t *testing.T) {
	conv := convertOK(t, "Portlar", `
services:
  app:
    image: example/app:1
    ports:
      - "8080:80"
      - "127.0.0.1:9000:9000"
      - "5353:53/udp"
      - 7000
      - "0.0.0.0:8443:443"
      - target: 25
        published: "2525"
        host_ip: 127.0.0.1
      - target: 69
        protocol: udp
`)
	m := conv.Manifest
	type p struct {
		host, cont int
		proto      string
	}
	var got []p
	for _, x := range m.Services[0].Ports {
		got = append(got, p{x.Host, x.Container, x.Protocol})
	}
	want := []p{{8080, 80, "tcp"}, {9000, 9000, "tcp"}, {5353, 53, "udp"}, {7000, 7000, "tcp"}, {8443, 443, "tcp"}, {2525, 25, "tcp"}, {69, 69, "udp"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("ports:\n got %v\nwant %v", got, want)
	}
	if m.BindAddress != BindLoopback {
		t.Errorf("a port on 127.0.0.1 must make loopback the default, got %q", m.BindAddress)
	}
	if !strings.Contains(noteText(conv), "127.0.0.1") {
		t.Error("the loopback default is not explained")
	}
	cfg, _ := mustResolve(t, m, Inputs{}, nil)
	for _, pb := range cfg.Services[0].Ports {
		if pb.HostIP != "127.0.0.1" {
			t.Errorf("port %d published on %q", pb.Host, pb.HostIP)
		}
	}
	// The user can still choose all interfaces explicitly.
	cfg, _ = mustResolve(t, m, Inputs{BindAddress: BindAll}, nil)
	if cfg.Services[0].Ports[0].HostIP != "0.0.0.0" {
		t.Error("explicit choice ignored")
	}
}

func TestComposeEnvironmentForms(t *testing.T) {
	conv := convertOK(t, "Ortam", `
services:
  list:
    image: example/a:1
    environment:
      - PUID=1000
      - EMPTY=
      - PRICE=$$5
      - TZ=${TZ:-Europe/Istanbul}
      - API_TOKEN=${API_TOKEN}
      - ADMIN_PASSWORD=${ADMIN_PASSWORD:?parola gerekli}
  map:
    image: example/b:1
    environment:
      ENABLED: true
      PORT: 8080
      QUOTED: "a b c"
      PASSTHROUGH:
      TZ: ${TZ:-Europe/Istanbul}
`)
	m := conv.Manifest
	l, mp := serviceOf(t, m, "list"), serviceOf(t, m, "map")
	check := func(e EnvSpec, key, def string, secret, gen, req bool) {
		t.Helper()
		if e.Key != key || e.Default != def || e.Secret != secret || (e.Generate != "") != gen || e.Required != req {
			t.Errorf("%s: %+v", e.Name, e)
		}
	}
	check(specEnv(t, l, "PUID"), "list.PUID", "1000", false, false, false)
	check(specEnv(t, l, "EMPTY"), "list.EMPTY", "", false, false, false)
	check(specEnv(t, l, "PRICE"), "list.PRICE", "$5", false, false, false)
	check(specEnv(t, l, "TZ"), "TZ", "Europe/Istanbul", false, false, false)
	check(specEnv(t, l, "API_TOKEN"), "API_TOKEN", "", true, false, false)
	check(specEnv(t, l, "ADMIN_PASSWORD"), "ADMIN_PASSWORD", "", true, true, true)
	check(specEnv(t, mp, "ENABLED"), "map.ENABLED", "true", false, false, false)
	check(specEnv(t, mp, "PORT"), "map.PORT", "8080", false, false, false)
	check(specEnv(t, mp, "QUOTED"), "map.QUOTED", "a b c", false, false, false)
	check(specEnv(t, mp, "PASSTHROUGH"), "map.PASSTHROUGH", "", false, false, false)
	check(specEnv(t, mp, "TZ"), "TZ", "Europe/Istanbul", false, false, false)
}

func TestComposeVolumes(t *testing.T) {
	conv := convertOK(t, "Birimler", `
services:
  app:
    image: example/app:1
    volumes:
      - data:/var/lib/app
      - /data/fotolar:/photos:ro
      - ./config:/config
      - ../paylasim:/shared
      - /cache
      - type: volume
        source: data
        target: /backup
        read_only: true
      - type: bind
        source: /media/muzik
        target: /music
      - type: tmpfs
        target: /run/app
        tmpfs:
          size: 67108864
    tmpfs:
      - /tmp:size=64m
    shm_size: 256mb
  yan:
    image: example/yan:1
    volumes:
      - ./config:/etc/yan
      - data:/data
volumes:
  data:
    driver: local
`)
	m := conv.Manifest
	app, yan := serviceOf(t, m, "app"), serviceOf(t, m, "yan")
	type v struct {
		typ, src, target string
		ro, req          bool
	}
	var got []v
	for _, x := range app.Volumes {
		got = append(got, v{x.Type, x.Source, x.Target, x.ReadOnly, x.Required})
	}
	want := []v{
		{"volume", "data", "/var/lib/app", false, false},
		{"bind", "/data/fotolar", "/photos", true, true},
		{"volume", "yerel-config", "/config", false, false},
		{"volume", "yerel-paylasim", "/shared", false, false},
		{"volume", "anon-app-cache", "/cache", false, false},
		{"volume", "data", "/backup", true, false},
		{"bind", "/media/muzik", "/music", false, true},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("volumes:\n got %v\nwant %v", got, want)
	}
	if fmt.Sprint(app.Tmpfs) != fmt.Sprint([]TmpfsSpec{{"/run/app", "64m"}, {"/tmp", "64m"}}) {
		t.Errorf("tmpfs: %+v", app.Tmpfs)
	}
	if app.ShmSize != "256mb" {
		t.Errorf("shm_size %q", app.ShmSize)
	}
	// The same relative folder is the same volume in both services.
	if yan.Volumes[0].Source != "yerel-config" {
		t.Errorf("yan: %+v", yan.Volumes)
	}
	notes := noteText(conv)
	for _, want := range []string{"./config", "yerel-config", "boş başlar", "/cache"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes do not mention %q:\n%s", want, notes)
		}
	}
	if !strings.Contains(m.Notes, "./config") {
		t.Error("the notes are not shown in the install dialog")
	}
	fields := buildFields(m, nil)
	if len(fields.Paths) != 2 || fields.Paths[0].Default != "/data/fotolar" || !fields.Paths[0].Required {
		t.Errorf("path fields: %+v", fields.Paths)
	}
	// The user may pick another folder, inside the roots only.
	cfg, _ := mustResolve(t, m, Inputs{Paths: map[string]string{fields.Paths[0].Key: "/data/baska"}}, nil)
	if cfg.Services[0].Volumes[1].Source != "/data/baska" {
		t.Errorf("chosen folder ignored: %+v", cfg.Services[0].Volumes)
	}
	_, _, err := Resolve(m, Inputs{Paths: map[string]string{fields.Paths[0].Key: "/etc"}}, nil, testRoots)
	wantInputError(t, err, "sistem klasörü")
}

func TestComposeAnchorsAndMergeKeys(t *testing.T) {
	conv := convertOK(t, "Çapa", `
x-common: &common
  restart: unless-stopped
  environment:
    TZ: Europe/Istanbul
x-image: &img example/a:1
services:
  a:
    <<: *common
    image: *img
  b:
    <<: [*common]
    image: example/b:1
    restart: always
`)
	a, b := serviceOf(t, conv.Manifest, "a"), serviceOf(t, conv.Manifest, "b")
	if a.Image != "example/a:1" || a.Restart != "unless-stopped" || b.Restart != "always" {
		t.Errorf("merge: a=%+v b=%+v", a.DockerSpec, b.DockerSpec)
	}
	if specEnv(t, a, "TZ").Default != "Europe/Istanbul" || specEnv(t, b, "TZ").Default != "Europe/Istanbul" {
		t.Error("merged environment missing")
	}
}

func TestComposeCommand(t *testing.T) {
	conv := convertOK(t, "Komut", `
services:
  a:
    image: example/a:1
    command: sh -c 'echo "merhaba dünya" && exec app --port=$$PORT'
    user: "1000:1000"
  b:
    image: example/b:1
    command: ["--config", "/config/app.yml"]
    user: 1000
`)
	a, b := serviceOf(t, conv.Manifest, "a"), serviceOf(t, conv.Manifest, "b")
	if fmt.Sprintf("%q", a.Command) != `["sh" "-c" "echo \"merhaba dünya\" && exec app --port=$PORT"]` || a.User != "1000:1000" {
		t.Errorf("a: %q %q", a.Command, a.User)
	}
	if strings.Join(b.Command, " ") != "--config /config/app.yml" || b.User != "1000" {
		t.Errorf("b: %q %q", b.Command, b.User)
	}
}

func TestComposeNetworksWithoutSettings(t *testing.T) {
	conv := convertOK(t, "Ağlar", `
services:
  a:
    image: example/a:1
    networks: [arka]
  b:
    image: example/b:1
    networks:
      arka:
      default:
networks:
  arka:
`)
	if !strings.Contains(noteText(conv), "tek özel ağına") {
		t.Errorf("notes: %s", noteText(conv))
	}
}

/* ---------- refused files ---------- */

// Every unsupported key of every service is reported, in one answer.
func TestComposeRefusesEveryUnsupportedKeyAtOnce(t *testing.T) {
	problems := convertRefused(t, "Hepsi", `
services:
  derle:
    build: .
  web:
    image: example/web:1
    env_file: .env
    entrypoint: /bin/sh
    extends:
      service: base
    profiles: [dev]
    deploy:
      replicas: 2
    pid: host
    ipc: host
    security_opt: [seccomp:unconfined]
    sysctls:
      net.core.somaxconn: 1024
    volumes_from: [derle]
    links: [derle]
    extra_hosts: ["host.docker.internal:host-gateway"]
    labels:
      traefik.enable: "true"
    cap_drop: [ALL]
    configs: [ayar]
    secrets: [sifre]
    networks:
      arka:
        ipv4_address: 172.20.0.5
    garip_anahtar: 1
networks:
  arka:
    driver: bridge
configs:
  ayar:
    file: ./ayar.conf
secrets:
  sifre:
    file: ./sifre.txt
include:
  - other.yml
`)
	wantProblem(t, problems, "derle", "build", "görüntü derlemez")
	for key, text := range map[string]string{
		"env_file": "Ortam dosyaları", "entrypoint": "entrypoint", "extends": "miras", "profiles": "Profiller",
		"deploy": "deploy", "pid": "süreç ad alanını", "ipc": "IPC", "security_opt": "seccomp", "sysctls": "sysctls",
		"volumes_from": "devralmak", "links": "servis adıyla", "extra_hosts": "hosts", "labels": "etiket",
		"cap_drop": "cap_drop", "configs": "config", "secrets": "secret", "networks.arka": "Ağ ayarları",
		"garip_anahtar": "desteklenmiyor",
	} {
		wantProblem(t, problems, "web", key, text)
	}
	for key, text := range map[string]string{
		"networks.arka": "Özel ağ tanımları", "configs": "config", "secrets": "secret", "include": "include",
	} {
		wantProblem(t, problems, "", key, text)
	}
	if len(problems) < 23 {
		t.Errorf("only %d problems reported", len(problems))
	}
}

func TestComposeRefusesUnsupportedValues(t *testing.T) {
	problems := convertRefused(t, "Değerler", `
services:
  a:
    image: example/a:1
    restart: on-failure:3
    network_mode: service:b
    cap_add: [ALL, SYS_WHATEVER]
    healthcheck:
      test: ["NONE"]
    depends_on:
      b:
        condition: service_completed_successfully
      yok:
    ports:
      - "8000-8010:8000-8010"
      - "192.168.1.5:80:80"
      - "[::1]:81:81"
      - "82:82/sctp"
    user: "root; rm"
    devices:
      - /etc/passwd:/dev/x
    shm_size: çok
  b:
    image: example/b:1
    volumes:
      - dis:/data
      - ~/veri:/veri
      - ./nginx.conf:/etc/nginx/nginx.conf:ro
      - veri:/x:z
  Buyuk_Harf:
    image: example/c:1
volumes:
  dis:
    external: true
  sur:
    driver_opts:
      type: none
      o: bind
      device: /etc
`)
	for _, c := range []struct{ service, key, text string }{
		{"a", "restart", "on-failure:3"},
		{"a", "network_mode", "service:b"},
		{"a", "cap_add", "ALL"},
		{"a", "cap_add", "SYS_WHATEVER"},
		{"a", "healthcheck.test", "NONE"},
		{"a", "depends_on.b.condition", "tek seferlik"},
		{"a", "depends_on", `"yok" bilinmeyen`},
		{"a", "ports", "aralıkları"},
		{"a", "ports", "192.168.1.5"},
		{"a", "ports", "IPv6"},
		{"a", "ports", "sctp"},
		{"a", "user", "geçersiz"},
		{"a", "devices", "/dev altında"},
		{"a", "shm_size", "geçersiz boyut"},
		{"b", "volumes", "ev klasörüne"},
		{"b", "volumes", "tek bir dosyaya benziyor"},
		{"b", "volumes", `"z" seçeneği`},
		{"Buyuk_Harf", "", "küçük harf"},
		{"", "volumes.dis.external", "Dışarıda oluşturulmuş"},
		{"", "volumes.sur.driver_opts", "herhangi bir klasörü"},
	} {
		wantProblem(t, problems, c.service, c.key, c.text)
	}
}

func TestComposeRefusesSystemPaths(t *testing.T) {
	for _, p := range []string{
		"/", "/etc", "/etc/localtime", "/etc/ssh", "/proc", "/proc/1", "/sys", "/sys/fs/cgroup", "/dev", "/dev/sda",
		"/boot", "/root", "/root/.ssh", "/run", "/run/user/1000", "/var/run", "/var/run/docker", "/var/lib/docker",
		"/var/lib/docker/volumes", "/var/lib/myserver", "/var/lib/myserver/myserver.db", "/var/lib/myserver-updates",
		"/var/lib/myserver-updates/apt.log", "/usr", "/bin", "/lib",
	} {
		problems := convertRefused(t, "Sistem", `
services:
  a:
    image: example/a:1
    volumes:
      - `+p+`:/host
`)
		wantProblem(t, problems, "a", "volumes")
		joined := fmt.Sprint(problems)
		if !strings.Contains(joined, "sistem klasörü") && !strings.Contains(joined, "Kök dizin") {
			t.Errorf("%s: %s", p, joined)
		}
		if p == "/etc/localtime" && !strings.Contains(joined, "TZ") {
			t.Errorf("no hint about TZ: %s", joined)
		}
	}
	// The panel's data directory, wherever it is, even inside the roots.
	problems := convertRefused(t, "Panel", `
services:
  a:
    image: example/a:1
    volumes:
      - /data/panel/sub:/x
`)
	wantProblem(t, problems, "a", "volumes", "panelin kendi veri klasörü")
}

func TestComposeRefusesPathsOutsideRoots(t *testing.T) {
	problems := convertRefused(t, "Dışarı", `
services:
  a:
    image: example/a:1
    volumes:
      - /opt/uygulama:/data
      - /srv/../etc:/etc2
      - /home/ali:/home
`)
	wantProblem(t, problems, "a", "/opt/uygulama", "izin verilen klasörlerin dışında")
	wantProblem(t, problems, "a", "/etc", "sistem klasörü")
	wantProblem(t, problems, "a", "/home/ali", "izin verilen klasörlerin dışında")
}

func TestComposeRefusesInvalidImages(t *testing.T) {
	problems := convertRefused(t, "Görüntü", `
services:
  a:
    image: Example/App:1
  b:
    image: "--privileged"
  c:
    image: "nginx latest"
  d:
    image: nginx:${TAG}
  e:
    restart: always
`)
	wantProblem(t, problems, "a", "image", "geçerli bir görüntü adı değil")
	wantProblem(t, problems, "b", "image", "geçerli bir görüntü adı değil")
	wantProblem(t, problems, "c", "image", "geçerli bir görüntü adı değil")
	wantProblem(t, problems, "d", "image", "değişkenler")
	wantProblem(t, problems, "e", "image", "zorunludur")
}

func TestComposeRefusesInterpolationItCannotExpress(t *testing.T) {
	problems := convertRefused(t, "Değişken", `
services:
  a:
    image: example/a:1
    environment:
      URL: http://${HOST}:8080
      ALT: ${X:+evet}
      TWICE: ${A}${B}
      SHARED: ${SHARED:-bir}
    ports:
      - "${PORT}:80"
    volumes:
      - ${DATA}:/data
  b:
    image: example/b:1
    environment:
      SHARED: ${SHARED:-iki}
`)
	wantProblem(t, problems, "a", "environment.URL", "başka metinle")
	wantProblem(t, problems, "a", "environment.ALT", "alternatif")
	wantProblem(t, problems, "a", "environment.TWICE", "başka metinle")
	wantProblem(t, problems, "a", "ports", "değişkenler")
	wantProblem(t, problems, "a", "volumes", "değişkenler")
	wantProblem(t, problems, "b", "environment.SHARED", "farklı varsayılan")
}

func TestComposeRefusesDuplicateHostPorts(t *testing.T) {
	problems := convertRefused(t, "Çakışma", `
services:
  a:
    image: example/a:1
    ports: ["8080:80"]
  b:
    image: example/b:1
    ports: ["8080:8080", "8080:81/udp"]
`)
	wantProblem(t, problems, "b", "8080/tcp", "birden fazla")
}

func TestComposeSlugCollision(t *testing.T) {
	opt := composeOpts
	opt.Name = "Uptime Kuma"
	opt.Taken = func(slug string) string {
		if slug == "custom-uptime-kuma" {
			return "Bu adla bir uygulama zaten var."
		}
		return ""
	}
	_, err := ConvertCompose([]byte(composeUptimeKuma), opt)
	var ce *ComposeError
	if !errors.As(err, &ce) {
		t.Fatalf("collision accepted: %v", err)
	}
	wantProblem(t, ce.Problems, "", "zaten var")
	// Names that give no slug, or too long names.
	wantProblem(t, convertRefused(t, "!!!", composeUptimeKuma), "", "kısa ad")
	wantProblem(t, convertRefused(t, strings.Repeat("a", 61), composeUptimeKuma), "", "60 karakter")
	if got := customSlug("Çok Uzun Bir Uygulama Adı Olan Özel Servis"); len(got) > 32 || !ValidSlug(got) || !IsCustomSlug(got) {
		t.Errorf("slug %q", got)
	}
}

func TestComposeLimits(t *testing.T) {
	big := "services:\n  a:\n    image: example/a:1\n# " + strings.Repeat("x", MaxComposeBytes) + "\n"
	wantProblem(t, convertRefused(t, "Büyük", big), "", "çok büyük")

	var b strings.Builder
	b.WriteString("services:\n")
	for i := 0; i < 13; i++ {
		fmt.Fprintf(&b, "  s%d:\n    image: example/s:%d\n", i, i)
	}
	wantProblem(t, convertRefused(t, "Kalabalık", b.String()), "", "en fazla 12 servis")

	b.Reset()
	b.WriteString("services:\n  a:\n    image: example/a:1\n    ports:\n")
	for i := 0; i < maxComposePorts+1; i++ {
		fmt.Fprintf(&b, "      - \"%d:%d\"\n", 20000+i, 20000+i)
	}
	wantProblem(t, convertRefused(t, "Portlu", b.String()), "", "en fazla 64 port")

	b.Reset()
	b.WriteString("services:\n  a:\n    image: example/a:1\n    environment:\n")
	for i := 0; i < maxComposeEnv+1; i++ {
		fmt.Fprintf(&b, "      V%d: x\n", i)
	}
	wantProblem(t, convertRefused(t, "Ortamlı", b.String()), "", "ortam değişkeni")

	deep := "services:\n  a:\n    image: example/a:1\nx-derin: " + strings.Repeat("[", 60) + strings.Repeat("]", 60) + "\n"
	wantProblem(t, convertRefused(t, "Derin", deep), "", "derin")
}

// A "billion laughs" document: nine levels of nine aliases each would
// expand to 9^9 nodes. It must be refused quickly, with little memory.
func TestComposeAliasBomb(t *testing.T) {
	var b strings.Builder
	b.WriteString(`x-a: &a ["lol","lol","lol","lol","lol","lol","lol","lol","lol"]` + "\n")
	prev := "a"
	for _, n := range []string{"b", "c", "d", "e", "f", "g", "h", "i"} {
		fmt.Fprintf(&b, "x-%s: &%s [*%s,*%s,*%s,*%s,*%s,*%s,*%s,*%s,*%s]\n", n, n, prev, prev, prev, prev, prev, prev, prev, prev, prev)
		prev = n
	}
	b.WriteString("services:\n  a:\n    image: example/a:1\n    command: *i\n")
	start := time.Now()
	problems := convertRefused(t, "Bomba", b.String())
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v", d)
	}
	wantProblem(t, problems, "", "takma adlar")
}

func TestComposeRefusesMalformedFiles(t *testing.T) {
	for name, c := range map[string]struct{ src, text string }{
		"empty":        {"", "boş"},
		"not a map":    {"- a\n- b\n", "en üst düzeyi"},
		"syntax":       {"services:\n  a:\n    image: [\n", "sözdizimi"},
		"undeclared":   {"services:\n  a:\n    image: x/a:1\n    volumes:\n      - veri:/data\n", `"veri" birimi`},
		"two docs":     {"services:\n  a:\n    image: x/a:1\n---\nservices: {}\n", "birden fazla YAML belgesi"},
		"no services":  {"volumes:\n  a:\n", "en az bir servis"},
		"custom tag":   {"services:\n  a:\n    image: !secret x\n", "etiketi"},
		"dup key":      {"services:\n  a:\n    image: x/a:1\n    image: x/b:1\n", "birden fazla kez"},
		"control char": {"services:\n  a:\n    image: x/a:1\x1b\n", "denetim"},
		"bad env name": {"services:\n  a:\n    image: x/a:1\n    environment:\n      - 1ABC=x\n", "değişken adı"},
		"newline env":  {"services:\n  a:\n    image: x/a:1\n    environment:\n      A: \"x\\ny\"\n", "satır sonu"},
	} {
		t.Run(name, func(t *testing.T) {
			problems := convertRefused(t, "Bozuk", c.src)
			if !strings.Contains(fmt.Sprint(problems), c.text) {
				t.Errorf("problems do not mention %q: %v", c.text, problems)
			}
		})
	}
}

func TestComposeErrorMessageListsEveryProblem(t *testing.T) {
	err := &ComposeError{Problems: []ComposeIssue{
		{Service: "web", Key: "build", Message: "bir"},
		{Key: "include", Message: "iki"},
		{Message: "üç"},
	}}
	want := "Compose dosyası kabul edilmedi (3 sorun):\n• servis \"web\", build: bir\n• include: iki\n• üç"
	if err.Error() != want {
		t.Errorf("got\n%s\nwant\n%s", err.Error(), want)
	}
}

// Shipped manifests may not take the prefix or the category of custom
// applications, so that a custom slug can never collide with them.
func TestCatalogReservesCustomNames(t *testing.T) {
	captureLogs(t)
	dir := t.TempDir()
	files := map[string]string{
		"a.yaml": "name: A\nslug: custom-a\ndescription: x\ndocker:\n  image: x/a:1\n",
		"b.yaml": "name: B\nslug: b\ndescription: x\ncategory: custom\ndocker:\n  image: x/b:1\n",
		"c.yaml": "name: C\nslug: c\ndescription: x\ndocker:\n  image: x/c:1\n",
	}
	for n, body := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c := NewCatalog(dir)
	c.Reload()
	if c.Get("custom-a") != nil || c.Get("b") != nil || c.Get("c") == nil {
		t.Errorf("loaded: %v", c.List())
	}
	if inv := c.Invalid(); len(inv) != 2 || !strings.Contains(inv[0].Error, "custom-") {
		t.Errorf("invalid: %+v", inv)
	}
	// Custom applications join the list and survive a reload.
	conv := convertOK(t, "Uptime Kuma", composeUptimeKuma)
	c.PutCustom(conv.Manifest)
	c.Reload()
	if c.Get("custom-uptime-kuma") == nil || len(c.List()) != 2 || c.List()[1].Slug != "custom-uptime-kuma" {
		t.Errorf("custom application lost: %v", c.List())
	}
	c.RemoveCustom("custom-uptime-kuma")
	if c.Get("custom-uptime-kuma") != nil {
		t.Error("not removed")
	}
}
