package apps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const baseManifest = `name: Deneme
slug: deneme
description: Deneme uygulaması
category: tools
icon: deneme.svg
architectures: [amd64, arm64]
docker:
  image: example/app:1.0
  restart: unless-stopped
  network_mode: bridge
ports:
  - host: 8080
    container: 80
    protocol: tcp
volumes:
  - source: data
    target: /data
`

// with returns the base manifest with old replaced by new.
func with(t *testing.T, old, new string) string {
	t.Helper()
	if !strings.Contains(baseManifest, old) {
		t.Fatalf("base manifest does not contain %q", old)
	}
	return strings.Replace(baseManifest, old, new, 1)
}

func TestBaseManifestIsValid(t *testing.T) {
	m, err := Parse([]byte(baseManifest))
	if err != nil {
		t.Fatalf("base manifest: %v", err)
	}
	if len(m.Services) != 1 || m.Services[0].Name != DefaultServiceName {
		t.Fatalf("single-service form must become one service named app: %+v", m.Services)
	}
	if m.Docker != nil || m.Ports != nil {
		t.Error("single-service fields must be moved into the service")
	}
	p := m.Services[0].Ports[0]
	if p.Key != "app-80-tcp" || p.Protocol != "tcp" || p.Host != 8080 {
		t.Errorf("port defaults: %+v", p)
	}
}

func TestManifestFieldValidation(t *testing.T) {
	long := strings.Repeat("a", 33)
	cases := []struct {
		name     string
		old, new string
		ok       bool
	}{
		{"slug upper case", "slug: deneme", "slug: Deneme", false},
		{"slug underscore", "slug: deneme", "slug: de_neme", false},
		{"slug leading dash", "slug: deneme", `slug: "-deneme"`, false},
		{"slug trailing dash", "slug: deneme", `slug: "deneme-"`, false},
		{"slug with slash", "slug: deneme", "slug: a/b", false},
		{"slug with dots", "slug: deneme", `slug: ".."`, false},
		{"slug with space", "slug: deneme", `slug: "a b"`, false},
		{"slug with newline", "slug: deneme", `slug: "deneme\n"`, false},
		{"slug too long", "slug: deneme", "slug: " + long, false},
		{"slug empty", "slug: deneme", `slug: ""`, false},
		{"slug 32 characters", "slug: deneme", "slug: " + long[:32], true},
		{"slug one character", "slug: deneme", "slug: a", true},

		{"image with tag and digest", "example/app:1.0", "ghcr.io/org/app:1.0@sha256:" + strings.Repeat("a", 64), true},
		{"image with registry port", "example/app:1.0", "registry.local:5000/app:1", true},
		{"image empty", "image: example/app:1.0", `image: ""`, false},
		{"image looks like an option", "image: example/app:1.0", `image: "--privileged"`, false},
		{"image looks like a short option", "image: example/app:1.0", `image: "-v"`, false},
		{"image with space", "image: example/app:1.0", `image: "nginx latest"`, false},
		{"image with option after space", "image: example/app:1.0", `image: "nginx --privileged"`, false},
		{"image with tab", "image: example/app:1.0", `image: "nginx\tlatest"`, false},
		{"image with trailing newline", "image: example/app:1.0", `image: "nginx\n"`, false},
		{"image with NUL", "image: example/app:1.0", `image: "nginx\0"`, false},
		{"image with escape", "image: example/app:1.0", `image: "nginx\e"`, false},
		{"image upper case", "image: example/app:1.0", "image: Example/App", false},
		{"image with shell", "image: example/app:1.0", `image: "nginx;reboot"`, false},
		{"image with substitution", "image: example/app:1.0", `image: "$(id)"`, false},
		{"image empty tag", "image: example/app:1.0", `image: "nginx:"`, false},
		{"image bad digest", "image: example/app:1.0", `image: "nginx@sha256:abc"`, false},
		{"image too long", "image: example/app:1.0", "image: " + strings.Repeat("a", 256), false},

		{"port zero container", "container: 80", "container: 0", false},
		{"port negative", "container: 80", "container: -1", false},
		{"port too large", "container: 80", "container: 65536", false},
		{"host port too large", "host: 8080", "host: 70000", false},
		{"host port negative", "host: 8080", "host: -5", false},
		{"port maximum", "container: 80", "container: 65535", true},
		{"port not a number", "container: 80", "container: http", false},

		{"protocol udp", "protocol: tcp", "protocol: udp", true},
		{"protocol upper case is normalised", "protocol: tcp", "protocol: TCP", true},
		{"protocol sctp", "protocol: tcp", "protocol: sctp", false},
		{"protocol with slash", "protocol: tcp", `protocol: "tcp/udp"`, false},

		{"restart always", "restart: unless-stopped", "restart: always", true},
		{"restart no", "restart: unless-stopped", `restart: "no"`, true},
		{"restart on-failure", "restart: unless-stopped", "restart: on-failure", true},
		{"restart unknown", "restart: unless-stopped", "restart: sometimes", false},
		{"restart with count", "restart: unless-stopped", `restart: "on-failure:3"`, false},

		{"network host", "network_mode: bridge\nports:\n  - host: 8080\n    container: 80", "network_mode: host\nports:\n  - host: 80\n    container: 80", true},
		{"network container", "network_mode: bridge", `network_mode: "container:other"`, false},
		{"network unknown", "network_mode: bridge", "network_mode: macvlan", false},
		{"network none with ports", "network_mode: bridge", "network_mode: none", false},
		{"network host with different ports", "network_mode: bridge", "network_mode: host", false},

		{"category unknown", "category: tools", "category: games", false},
		{"category upper case", "category: tools", "category: Tools", false},
		{"category media", "category: tools", "category: media", true},

		{"arch unknown", "[amd64, arm64]", "[amd64, sparc]", false},
		{"arch docker style", "[amd64, arm64]", "[x86_64]", false},
		{"arch duplicate", "[amd64, arm64]", "[amd64, amd64]", false},
		{"arch upper case", "[amd64, arm64]", "[AMD64]", false},
		{"arch empty list", "[amd64, arm64]", "[]", true},

		{"icon png", "icon: deneme.svg", "icon: deneme.png", true},
		{"icon traversal", "icon: deneme.svg", `icon: "../deneme.svg"`, false},
		{"icon absolute", "icon: deneme.svg", "icon: /etc/deneme.svg", false},
		{"icon sub folder", "icon: deneme.svg", "icon: a/deneme.svg", false},
		{"icon backslash", "icon: deneme.svg", `icon: 'a\deneme.svg'`, false},
		{"icon other extension", "icon: deneme.svg", "icon: deneme.js", false},
		{"icon double extension", "icon: deneme.svg", "icon: deneme.html.svg", false},
		{"icon upper case", "icon: deneme.svg", "icon: Deneme.svg", false},
		{"icon hidden file", "icon: deneme.svg", "icon: .svg", false},
		{"icon trailing newline", "icon: deneme.svg", `icon: "deneme.svg\n"`, false},
		{"icon NUL", "icon: deneme.svg", `icon: "deneme\0.svg"`, false},

		{"volume target relative", "target: /data", "target: data", false},
		{"volume target with dot dot", "target: /data", "target: /data/../etc", false},
		{"volume target trailing dot dot", "target: /data", "target: /data/..", false},
		{"volume target root", "target: /data", "target: /", false},
		{"volume target not normalised", "target: /data", "target: /data//x", false},
		{"volume target with colon", "target: /data", `target: "/data:ro"`, false},
		{"volume target with newline", "target: /data", `target: "/data\n"`, false},
		{"volume target empty", "target: /data", `target: ""`, false},
		{"volume name with slash", "source: data", "source: /etc", false},
		{"volume name with dot dot", "source: data", `source: "../x"`, false},
		{"volume type unknown", "source: data", "source: data\n    type: tmpfs", false},
		{"system mount of the root", "source: data", "type: system\n    source: /", false},
		{"system mount relative", "source: data", "type: system\n    source: etc", false},
		{"system mount with dot dot", "source: data", "type: system\n    source: /srv/../etc", false},
		{"bind default relative", "source: data", "type: bind\n    source: media", false},
		{"bind default with dot dot", "source: data", "type: bind\n    source: /data/../etc", false},

		{"name empty", "name: Deneme", `name: ""`, false},
		{"name with control character", "name: Deneme", `name: "De\aneme"`, false},
		{"description missing", "description: Deneme uygulaması\n", "", false},
		{"website javascript", "category: tools", "category: tools\nwebsite: \"javascript:alert(1)\"", false},
		{"website https", "category: tools", "category: tools\nwebsite: https://example.org", true},
		{"schema unsupported", "name: Deneme", "schema: 2\nname: Deneme", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(with(t, c.old, c.new)))
			if c.ok && err != nil {
				t.Fatalf("must be accepted: %v", err)
			}
			if !c.ok && err == nil {
				t.Fatalf("must be rejected: %q -> %q", c.old, c.new)
			}
		})
	}
}

func TestValidImageRejectsControlCharacters(t *testing.T) {
	for _, s := range []string{"nginx\x00", "nginx\n", "\nnginx", "nginx\r", "ng\x1binx", " nginx", "nginx ", "-nginx", "--rm", "nginx ", "nginx "} {
		if ValidImage(s) {
			t.Errorf("ValidImage(%q) = true", s)
		}
	}
	for _, s := range []string{"nginx", "nginx:latest", "library/nginx:1.27", "ghcr.io/immich-app/immich-server:release", "lscr.io/linuxserver/qbittorrent:latest"} {
		if !ValidImage(s) {
			t.Errorf("ValidImage(%q) = false", s)
		}
	}
}

func TestServiceNamesAndDockerFields(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		ok   bool
	}{
		{"service name with space", "services:\n  - name: \"a b\"\n    image: x/y\n", false},
		{"service name looks like an option", "services:\n  - name: \"--rm\"\n    image: x/y\n", false},
		{"service name with newline", "services:\n  - name: \"a\\n\"\n    image: x/y\n", false},
		{"service name upper case", "services:\n  - name: Web\n    image: x/y\n", false},
		{"capability unknown", "services:\n  - name: a\n    image: x/y\n    cap_add: [EVERYTHING]\n", false},
		{"capability with prefix", "services:\n  - name: a\n    image: x/y\n    cap_add: [CAP_NET_ADMIN]\n", false},
		{"capability ALL", "services:\n  - name: a\n    image: x/y\n    cap_add: [ALL]\n", false},
		{"capability known", "services:\n  - name: a\n    image: x/y\n    cap_add: [NET_ADMIN]\n", true},
		{"device outside /dev", "services:\n  - name: a\n    image: x/y\n    devices:\n      - host: /etc/passwd\n", false},
		{"device with dot dot", "services:\n  - name: a\n    image: x/y\n    devices:\n      - host: /dev/../etc/passwd\n", false},
		{"device bad permissions", "services:\n  - name: a\n    image: x/y\n    devices:\n      - host: /dev/dri\n        permissions: rwx\n", false},
		{"device ok", "services:\n  - name: a\n    image: x/y\n    devices:\n      - host: /dev/dri\n", true},
		{"user with option", "services:\n  - name: a\n    image: x/y\n    user: \"-u root\"\n", false},
		{"user numeric", "services:\n  - name: a\n    image: x/y\n    user: \"1000:1000\"\n", true},
		{"tmpfs relative", "services:\n  - name: a\n    image: x/y\n    tmpfs:\n      - target: tmp\n", false},
		{"env name with equals", "services:\n  - name: a\n    image: x/y\n    env:\n      - name: \"A=B\"\n", false},
		{"env default with newline", "services:\n  - name: a\n    image: x/y\n    env:\n      - name: A\n        default: \"x\\ny\"\n", false},
		{"secret with default", "services:\n  - name: a\n    image: x/y\n    env:\n      - name: A\n        secret: true\n        default: hunter2\n", false},
		{"generate on a non secret", "services:\n  - name: a\n    image: x/y\n    env:\n      - name: A\n        generate: password\n", false},
		{"option without capability", "services:\n  - name: a\n    image: x/y\n    options:\n      - key: o\n        label: O\n", false},
		{"option with unknown capability", "services:\n  - name: a\n    image: x/y\n    options:\n      - key: o\n        label: O\n        cap_add: [ROOT]\n", false},
		{"port of an unknown option", "services:\n  - name: a\n    image: x/y\n    ports:\n      - container: 80\n        option: nope\n", false},
		{"no service at all", "", false},
		{"single and multi form together", "docker:\n  image: x/y\nservices:\n  - name: a\n    image: x/y\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte("name: A\nslug: a\ndescription: A\n" + c.yaml))
			if c.ok != (err == nil) {
				t.Fatalf("ok=%v, err=%v", c.ok, err)
			}
		})
	}
}

// Unknown fields are rejected (README: "A file with an unknown field ... is
// skipped"), so that a typo cannot silently drop a setting such as
// read_only.
func TestUnknownFieldsAreRejected(t *testing.T) {
	cases := map[string]string{
		"top level":   baseManifest + "colour: blue\n",
		"docker":      with(t, "  restart: unless-stopped", "  restart: unless-stopped\n  priviliged: true"),
		"port":        with(t, "    protocol: tcp", "    protocol: tcp\n    hostip: 0.0.0.0"),
		"volume typo": with(t, "    target: /data", "    target: /data\n    readonly: true"),
	}
	for name, y := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(y)); err == nil {
				t.Fatal("unknown field must be rejected")
			}
		})
	}
}

func TestDuplicatesAreRejected(t *testing.T) {
	multi := func(body string) string { return "name: A\nslug: a\ndescription: A\nservices:\n" + body }
	cases := map[string]string{
		"duplicate YAML key":         baseManifest + "slug: other\n",
		"duplicate key in a service": with(t, "  image: example/app:1.0", "  image: example/app:1.0\n  image: evil/app:1"),
		"duplicate host port": with(t, "    protocol: tcp",
			"    protocol: tcp\n  - host: 8080\n    container: 81"),
		"duplicate port key": with(t, "    protocol: tcp",
			"    protocol: tcp\n    key: web\n  - host: 8081\n    container: 81\n    key: web"),
		"duplicate volume target": with(t, "    target: /data", "    target: /data\n  - source: other\n    target: /data"),
		"duplicate host port across services": multi(
			"  - name: a\n    image: x/y\n    ports:\n      - container: 80\n" +
				"  - name: b\n    image: x/y\n    ports:\n      - host: 80\n        container: 8080\n"),
		"duplicate service name": multi("  - name: a\n    image: x/y\n  - name: a\n    image: x/z\n"),
		"duplicate env name":     multi("  - name: a\n    image: x/y\n    env:\n      - name: A\n      - name: A\n"),
		"dependency cycle": multi(
			"  - name: a\n    image: x/y\n    depends_on: [b]\n  - name: b\n    image: x/y\n    depends_on: [a]\n"),
		"longer dependency cycle": multi(
			"  - name: a\n    image: x/y\n    depends_on: [b]\n  - name: b\n    image: x/y\n    depends_on: [c]\n" +
				"  - name: c\n    image: x/y\n    depends_on: [a]\n"),
		"dependency on itself":           multi("  - name: a\n    image: x/y\n    depends_on: [a]\n"),
		"dependency on unknown service":  multi("  - name: a\n    image: x/y\n    depends_on: [ghost]\n"),
		"two web interfaces":             multi("  - name: a\n    image: x/y\n    ports:\n      - container: 80\n        web_ui: {}\n      - container: 81\n        web_ui: {}\n"),
		"secret and plain share one key": multi("  - name: a\n    image: x/y\n    env:\n      - name: A\n        key: k\n        secret: true\n  - name: b\n    image: x/y\n    env:\n      - name: B\n        key: k\n"),
	}
	for name, y := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(y)); err == nil {
				t.Fatalf("must be rejected:\n%s", y)
			}
		})
	}
	// The same host port on tcp and udp is not a duplicate.
	ok := with(t, "    protocol: tcp", "    protocol: tcp\n  - host: 8080\n    container: 80\n    protocol: udp")
	if _, err := Parse([]byte(ok)); err != nil {
		t.Fatalf("tcp and udp on one port: %v", err)
	}
}

func TestStartOrderRespectsDependsOn(t *testing.T) {
	services := []ServiceSpec{
		{Name: "web", DependsOn: []string{"api", "cache"}},
		{Name: "api", DependsOn: []string{"db"}},
		{Name: "cache"},
		{Name: "db"},
		{Name: "worker", DependsOn: []string{"api"}},
	}
	order, err := StartOrder(services)
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != len(services) {
		t.Fatalf("order %v must contain every service once", order)
	}
	for _, s := range services {
		for _, d := range s.DependsOn {
			if indexOf(order, d) > indexOf(order, s.Name) {
				t.Errorf("%s starts before its dependency %s: %v", s.Name, d, order)
			}
		}
	}
	again, _ := StartOrder(services)
	if strings.Join(order, ",") != strings.Join(again, ",") {
		t.Errorf("start order is not deterministic: %v / %v", order, again)
	}
	if _, err := StartOrder([]ServiceSpec{{Name: "a", DependsOn: []string{"b"}}, {Name: "b", DependsOn: []string{"a"}}}); err == nil {
		t.Error("a cycle must fail")
	}
}

func TestManifestSizeLimit(t *testing.T) {
	big := baseManifest + "notes: " + strings.Repeat("a", 257<<10) + "\n"
	if _, err := Parse([]byte(big)); err == nil {
		t.Fatal("an oversized manifest must be rejected")
	}
}

/* ---------- catalog ---------- */

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for n, b := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCatalogSkipsBrokenManifests(t *testing.T) {
	captureLogs(t)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"a-tek.yaml":      manifestTek,
		"b-broken.yaml":   "name: [unterminated\n",
		"c-invalid.yml":   strings.Replace(manifestKol, "slug: kol", "slug: KOL", 1),
		"d-duplicate.yml": strings.Replace(manifestKol, "slug: kol", "slug: tek", 1),
		"e-unknown.yaml":  manifestKol + "surprise: true\n",
		"f-empty.yaml":    "",
		"g-kol.yml":       manifestKol,
		".hidden.yaml":    manifestCift,
		"README.md":       "# not a manifest",
		"notes.txt":       manifestCift,
	})
	if err := os.Mkdir(filepath.Join(dir, "folder.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := NewCatalog(dir)
	c.Reload()

	var slugs []string
	for _, m := range c.List() {
		slugs = append(slugs, m.Slug)
	}
	if strings.Join(slugs, ",") != "kol,tek" {
		t.Errorf("valid manifests = %v, want kol and tek ordered by name", slugs)
	}
	if m := c.Get("tek"); m == nil || m.Services[0].Image != "example/tek:1.0" {
		t.Errorf("the first file keeps a duplicated slug, got %+v", m)
	}
	var bad []string
	for _, i := range c.Invalid() {
		bad = append(bad, i.File)
		if i.Error == "" {
			t.Errorf("%s has no error text", i.File)
		}
	}
	want := "b-broken.yaml,c-invalid.yml,d-duplicate.yml,e-unknown.yaml,f-empty.yaml"
	if strings.Join(bad, ",") != want {
		t.Errorf("invalid = %v, want %s", bad, want)
	}
	if _, loadErr := c.Status(); loadErr != "" {
		t.Errorf("load error %q", loadErr)
	}
}

func TestCatalogMissingDirectory(t *testing.T) {
	captureLogs(t)
	c := NewCatalog(filepath.Join(t.TempDir(), "absent"))
	c.Reload()
	if len(c.List()) != 0 || len(c.Invalid()) != 0 {
		t.Error("nothing can be loaded from a missing directory")
	}
	if _, loadErr := c.Status(); loadErr == "" {
		t.Error("a missing directory must be reported")
	}
}

func TestCatalogDoesNotFollowManifestSymlinkToSpecialFile(t *testing.T) {
	captureLogs(t)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"tek.yaml": manifestTek})
	if err := os.Symlink("/dev/zero", filepath.Join(dir, "zero.yaml")); err != nil {
		t.Skipf("symlinks are not available: %v", err)
	}
	c := NewCatalog(dir)
	c.Reload() // must return, and not read the device
	if c.Get("tek") == nil || len(c.Invalid()) != 1 {
		t.Errorf("valid=%d invalid=%v", len(c.List()), c.Invalid())
	}
}

/* ---------- shipped manifests ---------- */

// repoAppsDir finds the repository's apps/ directory: MYSERVER_TEST_APPS_DIR,
// the mount point used by the test container, or the path relative to this
// package when the tests run from a checkout.
func repoAppsDir(t *testing.T) string {
	t.Helper()
	candidates := []string{os.Getenv("MYSERVER_TEST_APPS_DIR"), "/repo-apps", filepath.Join("..", "..", "..", "apps")}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if st, err := os.Stat(filepath.Join(c, "manifests")); err == nil && st.IsDir() {
			return c
		}
	}
	t.Skip("shipped manifests not found: mount the repository's apps/ directory at /repo-apps or set MYSERVER_TEST_APPS_DIR")
	return ""
}

func TestShippedManifests(t *testing.T) {
	captureLogs(t)
	apps := repoAppsDir(t)
	dir := filepath.Join(apps, "manifests")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, e := range entries {
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".yaml" || ext == ".yml" {
			files++
		}
	}
	if files == 0 {
		t.Fatal("no manifest shipped")
	}
	c := NewCatalog(dir)
	c.Reload()
	for _, i := range c.Invalid() {
		t.Errorf("shipped manifest %s is invalid: %s", i.File, i.Error)
	}
	if len(c.List()) != files {
		t.Errorf("%d of %d shipped manifests loaded", len(c.List()), files)
	}
	roots := []string{"/home", "/data", "/media", "/mnt"}
	for _, m := range c.List() {
		t.Run(m.Slug, func(t *testing.T) {
			if m.Icon == "" {
				t.Error("no icon")
			} else if st, err := os.Lstat(filepath.Join(apps, "icons", m.Icon)); err != nil || !st.Mode().IsRegular() {
				t.Errorf("icon %s is missing", m.Icon)
			}
			// The defaults must produce an installable configuration.
			in := Inputs{Env: map[string]string{}, Paths: map[string]string{}}
			for _, s := range m.Services {
				for _, e := range s.Env {
					if e.Required && !e.Fixed && e.Default == "" && e.Generate == "" {
						in.Env[e.Key] = "deneme-degeri"
					}
				}
				for _, v := range s.Volumes {
					if v.Type == VolumeBind && v.Required && v.Source == "" {
						in.Paths[v.Key] = "/data/" + m.Slug
					}
				}
			}
			cfg, _, err := Resolve(m, in, nil, roots)
			if err != nil {
				t.Fatalf("defaults do not resolve: %v", err)
			}
			if err := ValidateConfig(cfg, m, roots); err != nil {
				t.Errorf("resolved configuration does not validate: %v", err)
			}
			for _, s := range m.Services {
				for _, v := range s.Volumes {
					if v.Type == VolumeSystem && !NeedsRiskAcceptance(m) {
						t.Errorf("system mount %s without risk acceptance", v.Source)
					}
				}
				if s.Privileged && !NeedsRiskAcceptance(m) {
					t.Error("privileged without risk acceptance")
				}
			}
		})
	}
}
