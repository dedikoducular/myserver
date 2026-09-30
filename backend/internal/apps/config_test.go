package apps

import (
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

//go:embed config.go
var configSource string

var testRoots = []string{"/data", "/media"}

func mustParse(t *testing.T, y string) *Manifest {
	t.Helper()
	m, err := Parse([]byte(y))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	return m
}

func mustResolve(t *testing.T, m *Manifest, in Inputs, prev *Inputs) (*Config, *Inputs) {
	t.Helper()
	cfg, out, err := Resolve(m, in, prev, testRoots)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return cfg, out
}

func wantInputError(t *testing.T, err error, parts ...string) {
	t.Helper()
	var ie *InputError
	if !errors.As(err, &ie) {
		t.Fatalf("want an input error, got %v", err)
	}
	for _, p := range parts {
		if !strings.Contains(ie.Message, p) {
			t.Errorf("message %q does not contain %q", ie.Message, p)
		}
	}
}

func envOf(s ServiceConfig, name string) (string, bool) {
	for _, e := range s.Env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

/* ---------- bind paths ---------- */

func TestCheckBindPath(t *testing.T) {
	roots := []string{"/data", "/media", "/home"}
	bad := []string{
		"", "data", "./data", "../data", "/", "/data/../etc", "/data/..", "/data/./x", "/data//x", "/data/",
		"/etc", "/etc/passwd", "/proc", "/proc/1/root", "/sys", "/dev", "/dev/sda", "/boot", "/run",
		"/var/run", "/var/run/docker.sock", "/run/docker.sock", "/var/lib/docker", "/var/lib/docker/volumes",
		"/var/lib/myserver", "/var/lib/myserver/myserver.db", "/usr", "/usr/bin", "/bin", "/sbin", "/lib", "/lib64",
		"/root", "/root/.ssh",
		"/srv", "/opt/x", "/database", "/data2", "/mediax/film",
		"/data/x:ro", "/data/x:/etc", "/data/a\nb", "/data/a\x00b", "/data/a\\b", "/data/\x7f",
		"/data/docker.sock",
		"/data/" + strings.Repeat("a", 1100),
	}
	for _, p := range bad {
		if err := CheckBindPath(p, roots); err == nil {
			t.Errorf("CheckBindPath(%q) accepted", p)
		} else {
			wantInputError(t, err)
		}
	}
	for _, p := range []string{"/data", "/data/medya", "/media/film/2024", "/home/ali/indirilenler", "/data/..x", "/data/x..y"} {
		if err := CheckBindPath(p, roots); err != nil {
			t.Errorf("CheckBindPath(%q): %v", p, err)
		}
	}
}

// System locations stay refused even when an administrator has widened the
// allowed roots to contain them.
func TestCheckBindPathSystemPathsWinOverRoots(t *testing.T) {
	for _, roots := range [][]string{{"/"}, {"/etc"}, {"/var"}, {"/var/lib/docker"}, {"/run", "/var/run"}} {
		for _, p := range []string{"/etc/ssh", "/var/lib/docker/volumes", "/var/run/docker.sock", "/run/docker.sock", "/var/lib/myserver"} {
			if err := CheckBindPath(p, roots); err == nil {
				t.Errorf("CheckBindPath(%q, %v) accepted", p, roots)
			}
		}
	}
	// "/" as a root does not allow everything.
	if err := CheckBindPath("/srv/x", []string{"/"}); err == nil {
		t.Error("the root directory must not count as an allowed root")
	}
	if err := CheckBindPath("/data/x", nil); err == nil {
		t.Error("no allowed roots means nothing is allowed")
	}
	if err := CheckBindPath("/data/x", []string{"data", "", "../data"}); err == nil {
		t.Error("relative roots must be ignored")
	}
}

func TestResolveBindPaths(t *testing.T) {
	m := mustParse(t, manifestTek)
	base := Inputs{Env: map[string]string{"ADMIN_PASSWORD": "x"}}
	for _, p := range []string{"/etc", "/data/../etc", "../etc", "/srv/x", "/var/lib/myserver", "/var/run/docker.sock", "/data/a:b"} {
		in := base
		in.Paths = map[string]string{"media": p}
		_, _, err := Resolve(m, in, nil, testRoots)
		if err == nil {
			t.Errorf("path %q accepted", p)
			continue
		}
		wantInputError(t, err, "Medya klasörü")
	}
	in := base
	in.Paths = map[string]string{"media": "/data/film/"}
	cfg, out := mustResolve(t, m, in, nil)
	if out.Paths["media"] != "/data/film" {
		t.Errorf("trailing slash must be removed: %q", out.Paths["media"])
	}
	var found bool
	for _, v := range cfg.Services[0].Volumes {
		if v.Type == VolumeBind {
			found = v.Source == "/data/film" && v.Target == "/media"
		}
	}
	if !found {
		t.Errorf("bind mount missing: %+v", cfg.Services[0].Volumes)
	}
	// An optional bind volume left empty is not mounted.
	cfg, _ = mustResolve(t, m, base, nil)
	for _, v := range cfg.Services[0].Volumes {
		if v.Type == VolumeBind {
			t.Errorf("empty optional bind volume mounted: %+v", v)
		}
	}
	// A required one must be chosen.
	req := mustParse(t, strings.Replace(manifestTek, "label: Medya klasörü", "label: Medya klasörü\n    required: true", 1))
	if _, _, err := Resolve(req, base, nil, testRoots); err == nil {
		t.Error("a required folder left empty must be refused")
	}
}

/* ---------- user input cannot widen access ---------- */

func TestResolveRejectsUnknownKeys(t *testing.T) {
	m := mustParse(t, manifestTek)
	ok := map[string]string{"ADMIN_PASSWORD": "x"}
	cases := map[string]Inputs{
		"port":   {Env: ok, Ports: map[string]int{"ssh": 22}},
		"env":    {Env: map[string]string{"ADMIN_PASSWORD": "x", "LD_PRELOAD": "/x.so"}},
		"path":   {Env: ok, Paths: map[string]string{"sock": "/data/x"}},
		"option": {Env: ok, Options: map[string]bool{"privileged": true}},
	}
	for name, in := range cases {
		if _, _, err := Resolve(m, in, nil, testRoots); err == nil {
			t.Errorf("unknown %s key accepted", name)
		}
	}
	// A value fixed by the manifest cannot be set by the user.
	fixed := mustParse(t, "name: A\nslug: a\ndescription: A\ndocker:\n  image: x/y\nenv:\n  - name: MODE\n    fixed: true\n    default: safe\n")
	if _, _, err := Resolve(fixed, Inputs{Env: map[string]string{"MODE": "unsafe"}}, nil, testRoots); err == nil {
		t.Error("a fixed value was overridden")
	}
	cfg, _ := mustResolve(t, fixed, Inputs{}, &Inputs{Env: map[string]string{"MODE": "unsafe"}})
	if v, _ := envOf(cfg.Services[0], "MODE"); v != "safe" {
		t.Errorf("a stored value overrode a fixed one: %q", v)
	}
}

// Whatever the user sends, the resolved configuration has exactly the
// privileges the manifest declares.
func TestResolveNeverAddsPrivileges(t *testing.T) {
	m := mustParse(t, manifestTek)
	cfg, _ := mustResolve(t, m, Inputs{
		Env: map[string]string{"ADMIN_PASSWORD": "x"}, Paths: map[string]string{"media": "/data/m"},
	}, nil)
	s := cfg.Services[0]
	if s.Privileged || len(s.CapAdd) != 0 || len(s.Devices) != 0 || s.NetworkMode != "bridge" {
		t.Errorf("privileges appeared: %+v", s)
	}
	for _, v := range s.Volumes {
		if v.Type == VolumeSystem {
			t.Errorf("system mount appeared: %+v", v)
		}
	}
	// Inputs is the complete set of things a request can carry.
	var in Inputs
	if err := json.Unmarshal([]byte(`{"privileged":true,"cap_add":["SYS_ADMIN"],"network_mode":"host","volumes":[{"type":"system","source":"/"}]}`), &in); err != nil {
		t.Fatal(err)
	}
	if len(in.Ports)+len(in.Env)+len(in.Paths)+len(in.Options) != 0 || in.BindAddress != "" {
		t.Errorf("inputs took over unknown fields: %+v", in)
	}
}

func TestOptionalCapabilities(t *testing.T) {
	m := mustParse(t, `name: A
slug: a
description: A
docker:
  image: x/y
  cap_add: [NET_BIND_SERVICE]
options:
  - key: dhcp
    label: DHCP sunucusu
    cap_add: [NET_ADMIN, NET_RAW]
  - key: time
    label: Saat
    default: true
    cap_add: [SYS_TIME, NET_RAW]
ports:
  - container: 53
    protocol: udp
    key: dns
  - container: 67
    protocol: udp
    key: dhcp
    option: dhcp
`)
	caps := func(in Inputs, prev *Inputs) (string, *Config) {
		cfg, _ := mustResolve(t, m, in, prev)
		return strings.Join(cfg.Services[0].CapAdd, ","), cfg
	}
	got, cfg := caps(Inputs{}, nil)
	if got != "NET_BIND_SERVICE,SYS_TIME,NET_RAW" {
		t.Errorf("defaults: %s", got)
	}
	if len(cfg.Services[0].Ports) != 1 || cfg.Services[0].Ports[0].Key != "dns" {
		t.Errorf("the port of a disabled option is published: %+v", cfg.Services[0].Ports)
	}
	if got, _ := caps(Inputs{Options: map[string]bool{"time": false}}, nil); got != "NET_BIND_SERVICE" {
		t.Errorf("all off: %s", got)
	}
	got, cfg = caps(Inputs{Options: map[string]bool{"dhcp": true}}, nil)
	if got != "NET_BIND_SERVICE,NET_ADMIN,NET_RAW,SYS_TIME" {
		t.Errorf("all on (no capability twice): %s", got)
	}
	if len(cfg.Services[0].Ports) != 2 {
		t.Errorf("the port of an enabled option is missing: %+v", cfg.Services[0].Ports)
	}
	// A stored choice is kept when the request does not mention it.
	if got, _ := caps(Inputs{}, &Inputs{Options: map[string]bool{"time": false, "dhcp": true}}); got != "NET_BIND_SERVICE,NET_ADMIN,NET_RAW" {
		t.Errorf("stored choice: %s", got)
	}
}

/* ---------- ports of one request ---------- */

func TestResolvePortsOfOneRequest(t *testing.T) {
	m := mustParse(t, `name: A
slug: a
description: A
docker:
  image: x/y
ports:
  - container: 80
    host: 8080
    key: web
    label: Web arayüzü
  - container: 443
    host: 8443
    key: tls
    label: Güvenli arayüz
  - container: 53
    host: 8080
    protocol: udp
    key: dns
`)
	_, _, err := Resolve(m, Inputs{Ports: map[string]int{"tls": 8080}}, nil, testRoots)
	wantInputError(t, err, "8080/tcp", "Web arayüzü", "Güvenli arayüz")
	for _, p := range []int{0, -1, 65536, 1 << 20} {
		if _, _, err := Resolve(m, Inputs{Ports: map[string]int{"web": p}}, nil, testRoots); err == nil {
			t.Errorf("port %d accepted", p)
		}
	}
	cfg, out := mustResolve(t, m, Inputs{Ports: map[string]int{"web": 9000}}, nil)
	if out.Ports["web"] != 9000 || out.Ports["tls"] != 8443 || out.Ports["dns"] != 8080 {
		t.Errorf("ports: %+v", out.Ports)
	}
	if cfg.Services[0].Ports[0].Host != 9000 || cfg.Services[0].Ports[0].Container != 80 {
		t.Errorf("binding: %+v", cfg.Services[0].Ports[0])
	}
}

func TestResolveHostNetworkPortsCannotChange(t *testing.T) {
	m := mustParse(t, manifestAgci)
	if _, _, err := Resolve(m, Inputs{Ports: map[string]int{"dns": 5353}}, nil, testRoots); err == nil {
		t.Error("the port of a host-network service was changed")
	}
}

/* ---------- secrets ---------- */

func TestGeneratePassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		p, err := GeneratePassword()
		if err != nil {
			t.Fatal(err)
		}
		if len(p) != 32 {
			t.Fatalf("length %d, want 32", len(p))
		}
		for _, r := range p {
			if !strings.ContainsRune(passwordAlphabet, r) {
				t.Fatalf("character %q outside the alphabet", r)
			}
		}
		if seen[p] {
			t.Fatalf("password repeated after %d draws", i)
		}
		seen[p] = true
	}
	// 57 symbols, 32 characters: about 186 bits.
	if len(passwordAlphabet) < 50 {
		t.Errorf("alphabet of %d symbols is too small", len(passwordAlphabet))
	}
}

// The generator must use crypto/rand and nothing else: this is checked on
// the source, since the output of a good and of a seeded generator cannot be
// told apart by looking at a few values.
func TestGeneratePasswordUsesCryptoRand(t *testing.T) {
	s := configSource
	if !strings.Contains(s, `"crypto/rand"`) || strings.Contains(s, `"math/rand"`) || strings.Contains(s, `"math/rand/v2"`) {
		t.Error("config.go must import crypto/rand and not math/rand")
	}
}

func TestResolveSecrets(t *testing.T) {
	m := mustParse(t, manifestTek)
	_, _, err := Resolve(m, Inputs{}, nil, testRoots)
	wantInputError(t, err, "Yönetici parolası", "zorunlu")

	cfg, out := mustResolve(t, m, Inputs{Env: map[string]string{"ADMIN_PASSWORD": "ilk-parola"}}, nil)
	gen := out.Env["DB_PASSWORD"]
	if len(gen) != 32 {
		t.Fatalf("generated password %q", gen)
	}
	if v, _ := envOf(cfg.Services[0], "DB_PASSWORD"); v != gen {
		t.Errorf("container gets %q, inputs hold %q", v, gen)
	}
	for _, e := range cfg.Services[0].Env {
		if want := e.Name != "TZ"; e.Secret != want {
			t.Errorf("%s: secret=%v", e.Name, e.Secret)
		}
	}

	// Settings dialog: an empty secret keeps the stored one.
	for name, in := range map[string]Inputs{
		"empty":   {Env: map[string]string{"ADMIN_PASSWORD": "", "DB_PASSWORD": ""}},
		"missing": {},
	} {
		cfg2, out2 := mustResolve(t, m, in, out)
		if out2.Env["ADMIN_PASSWORD"] != "ilk-parola" || out2.Env["DB_PASSWORD"] != gen {
			t.Errorf("%s: secrets changed: %+v", name, out2.Env)
		}
		if v, _ := envOf(cfg2.Services[0], "ADMIN_PASSWORD"); v != "ilk-parola" {
			t.Errorf("%s: container gets %q", name, v)
		}
	}
	// A new value replaces it.
	_, out3 := mustResolve(t, m, Inputs{Env: map[string]string{"ADMIN_PASSWORD": "yeni"}}, out)
	if out3.Env["ADMIN_PASSWORD"] != "yeni" || out3.Env["DB_PASSWORD"] != gen {
		t.Errorf("replace: %+v", out3.Env)
	}
	// A non-secret value can be emptied.
	_, out4 := mustResolve(t, m, Inputs{Env: map[string]string{"TZ": ""}}, out)
	if out4.Env["TZ"] != "" {
		t.Errorf("TZ = %q, want it cleared", out4.Env["TZ"])
	}
	// Values with line breaks cannot be smuggled into the environment.
	for _, v := range []string{"a\nEVIL=1", "a\rb", "a\x00b", strings.Repeat("x", 5000)} {
		if _, _, err := Resolve(m, Inputs{Env: map[string]string{"ADMIN_PASSWORD": v}}, nil, testRoots); err == nil {
			t.Errorf("value %q accepted", v)
		} else if strings.Contains(err.Error(), "EVIL") {
			t.Errorf("the error repeats the secret value: %v", err)
		}
	}
}

func TestSharedSecretIsGeneratedOnce(t *testing.T) {
	m := mustParse(t, manifestCift)
	cfg, out := mustResolve(t, m, Inputs{}, nil)
	a, _ := envOf(cfg.Services[0], "DB_PASS")
	b, _ := envOf(cfg.Services[1], "POSTGRES_PASSWORD")
	if a == "" || a != b || a != out.Env["dbpass"] {
		t.Errorf("services got different passwords: %q / %q", a, b)
	}
}

func TestBuildFieldsMasksSecrets(t *testing.T) {
	m := mustParse(t, manifestTek)
	_, out := mustResolve(t, m, Inputs{Env: map[string]string{"ADMIN_PASSWORD": "cok-gizli-parola"}}, nil)
	raw, err := json.Marshal(buildFields(m, out))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"cok-gizli-parola", out.Env["DB_PASSWORD"]} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("fields contain the secret %q: %s", secret, raw)
		}
	}
	f := buildFields(m, out)
	for _, e := range f.Env {
		if e.Secret && (!e.HasValue || e.Value != "" || e.Default != "") {
			t.Errorf("%s: %+v", e.Key, e)
		}
		if !e.Secret && e.Value != "Europe/Istanbul" {
			t.Errorf("%s: plain value missing: %+v", e.Key, e)
		}
	}
	for _, e := range buildFields(m, nil).Env {
		if e.HasValue {
			t.Errorf("%s: has_value without an installation", e.Key)
		}
	}
}

/* ---------- bind address ---------- */

func TestValidBindAddress(t *testing.T) {
	for _, v := range []string{"all", "loopback"} {
		if !ValidBindAddress(v) {
			t.Errorf("%q refused", v)
		}
	}
	for _, v := range []string{"", "ALL", "Loopback", "0.0.0.0", "127.0.0.1", "::", "192.168.1.5", "lan", "all ", "loopback\n"} {
		if ValidBindAddress(v) {
			t.Errorf("%q accepted", v)
		}
	}
}

func TestResolveBindAddress(t *testing.T) {
	m := mustParse(t, manifestCift)
	ips := func(cfg *Config) string {
		var out []string
		for _, s := range cfg.Services {
			for _, p := range s.Ports {
				out = append(out, p.HostIP)
			}
		}
		return strings.Join(out, ",")
	}
	cfg, out := mustResolve(t, m, Inputs{}, nil)
	if cfg.BindAddress != BindAll || out.BindAddress != BindAll || ips(cfg) != "0.0.0.0" {
		t.Errorf("default: %q %q %s", cfg.BindAddress, out.BindAddress, ips(cfg))
	}
	cfg, out = mustResolve(t, m, Inputs{BindAddress: BindLoopback}, nil)
	if cfg.BindAddress != BindLoopback || out.BindAddress != BindLoopback || ips(cfg) != "127.0.0.1" {
		t.Errorf("loopback: %q %q %s", cfg.BindAddress, out.BindAddress, ips(cfg))
	}
	// Update (no inputs) and a settings change that does not mention the
	// address keep the choice.
	cfg, out = mustResolve(t, m, Inputs{}, out)
	if cfg.BindAddress != BindLoopback || ips(cfg) != "127.0.0.1" {
		t.Errorf("update lost the choice: %q %s", cfg.BindAddress, ips(cfg))
	}
	cfg, out = mustResolve(t, m, Inputs{Ports: map[string]int{"web": 9999}}, out)
	if cfg.BindAddress != BindLoopback || ips(cfg) != "127.0.0.1" || out.BindAddress != BindLoopback {
		t.Errorf("settings change lost the choice: %q %s", cfg.BindAddress, ips(cfg))
	}
	cfg, _ = mustResolve(t, m, Inputs{BindAddress: BindAll}, out)
	if cfg.BindAddress != BindAll || ips(cfg) != "0.0.0.0" {
		t.Errorf("back to all: %q %s", cfg.BindAddress, ips(cfg))
	}
	for _, v := range []string{"0.0.0.0", "127.0.0.1", "192.168.1.10", "LOOPBACK", "none"} {
		_, _, err := Resolve(m, Inputs{BindAddress: v}, nil, testRoots)
		if err == nil {
			t.Errorf("bind address %q accepted", v)
		}
	}
	if _, _, err := Resolve(m, Inputs{}, &Inputs{BindAddress: "10.0.0.1"}, testRoots); err == nil {
		t.Error("a stored invalid bind address was accepted")
	}
}

func TestBindAddressDoesNotTouchHostNetwork(t *testing.T) {
	m := mustParse(t, manifestAgci)
	for _, bind := range []string{BindAll, BindLoopback} {
		cfg, _ := mustResolve(t, m, Inputs{BindAddress: bind}, nil)
		p := cfg.Services[0].Ports[0]
		if p.HostIP != "" || cfg.Services[0].NetworkMode != "host" {
			t.Errorf("%s: host-network port got address %q", bind, p.HostIP)
		}
		if cfg.Network != "" {
			t.Errorf("%s: a host-network application needs no private network", bind)
		}
		if err := ValidateConfig(cfg, m, testRoots); err != nil {
			t.Errorf("%s: %v", bind, err)
		}
		_, hc, nc, err := buildContainer(cfg, &cfg.Services[0], nopReporter{})
		if err != nil {
			t.Fatal(err)
		}
		if len(hc.PortBindings) != 0 || nc != nil || string(hc.NetworkMode) != "host" {
			t.Errorf("%s: host-network container publishes ports: %+v", bind, hc.PortBindings)
		}
	}
}

func TestBuildContainerHostIP(t *testing.T) {
	m := mustParse(t, manifestTek)
	for bind, want := range map[string]string{BindAll: "0.0.0.0", BindLoopback: "127.0.0.1"} {
		cfg, _ := mustResolve(t, m, Inputs{BindAddress: bind, Env: map[string]string{"ADMIN_PASSWORD": "x"}}, nil)
		cc, hc, _, err := buildContainer(cfg, &cfg.Services[0], nopReporter{})
		if err != nil {
			t.Fatal(err)
		}
		if len(hc.PortBindings) != 1 {
			t.Fatalf("bindings: %+v", hc.PortBindings)
		}
		for port, list := range hc.PortBindings {
			if string(port) != "80/tcp" || len(list) != 1 || list[0].HostIP != want || list[0].HostPort != "18080" {
				t.Errorf("%s: %s -> %+v", bind, port, list)
			}
		}
		if cc.Labels[LabelApp] != "tek" || cc.Labels[LabelService] != "app" || cc.Labels[LabelManaged] != "true" {
			t.Errorf("labels: %+v", cc.Labels)
		}
	}
}

// legacy returns cfg as it was stored before bind_address and host_ip
// existed.
func legacy(t *testing.T, cfg *Config) *Config {
	t.Helper()
	raw, _ := json.Marshal(cfg)
	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	delete(tree, "bind_address")
	for _, s := range tree["services"].([]any) {
		for _, p := range s.(map[string]any)["ports"].([]any) {
			delete(p.(map[string]any), "host_ip")
		}
	}
	raw, _ = json.Marshal(tree)
	if strings.Contains(string(raw), "host_ip") || strings.Contains(string(raw), "bind_address") {
		t.Fatalf("legacy form still has the new fields: %s", raw)
	}
	out := &Config{}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestLegacyConfigNormalisesToAll(t *testing.T) {
	for _, y := range []string{manifestTek, manifestCift, manifestAgci} {
		m := mustParse(t, y)
		in := Inputs{}
		if m.Slug == "tek" {
			in.Env = map[string]string{"ADMIN_PASSWORD": "x"}
		}
		fresh, out := mustResolve(t, m, in, nil)
		old := legacy(t, fresh)
		NormalizeConfig(old)
		if old.BindAddress != BindAll {
			t.Errorf("%s: bind address %q", m.Slug, old.BindAddress)
		}
		// An update resolves the manifest again with the stored inputs;
		// the result must equal the stored configuration, otherwise every
		// old installation is recreated for nothing.
		again, _ := mustResolve(t, m, Inputs{}, out)
		if !sameConfig(old, again) {
			a, _ := json.Marshal(old)
			b, _ := json.Marshal(again)
			t.Errorf("%s: normalised legacy configuration differs:\n%s\n%s", m.Slug, a, b)
		}
		if err := ValidateConfig(legacy(t, fresh), m, testRoots); err != nil {
			t.Errorf("%s: legacy configuration does not validate: %v", m.Slug, err)
		}
	}
	NormalizeConfig(nil) // must not panic
}

/* ---------- warnings ---------- */

func TestWarnings(t *testing.T) {
	codes := func(y string) (string, bool) {
		m := mustParse(t, y)
		var out []string
		for _, w := range Warnings(m) {
			out = append(out, w.Level+":"+w.Code)
			if w.Title == "" || w.Message == "" {
				t.Errorf("warning %s without text", w.Code)
			}
		}
		return strings.Join(out, ","), NeedsRiskAcceptance(m)
	}
	head := "name: A\nslug: a\ndescription: A\ndocker:\n  image: x/y\n"
	cases := []struct {
		name, yaml, want string
		risk             bool
	}{
		{"plain", head, "", false},
		{"named and bind volumes", manifestTek, "", false},
		{"docker socket", manifestSoket, "danger:docker_socket", true},
		{"system mount", head + "volumes:\n  - type: system\n    source: /etc/localtime\n    target: /etc/localtime\n    read_only: true\n", "danger:system_mount", true},
		{"privileged", head + "  privileged: true\n", "danger:privileged", true},
		{"host network and capability", manifestAgci, "warning:host_network,warning:cap_add", false},
		{"devices", head + "  devices:\n    - host: /dev/dri\n", "warning:devices", false},
	}
	for _, c := range cases {
		got, risk := codes(c.yaml)
		if got != c.want || risk != c.risk {
			t.Errorf("%s: warnings %q risk %v, want %q %v", c.name, got, risk, c.want, c.risk)
		}
	}
	m := mustParse(t, cases[3].yaml)
	if w := Warnings(m)[0]; !strings.Contains(w.Message, "/etc/localtime") || !strings.Contains(w.Message, "yalnızca okuma") {
		t.Errorf("system mount warning does not name the path and mode: %s", w.Message)
	}
}
