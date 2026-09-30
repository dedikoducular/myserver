package docker

import (
	"strings"
	"testing"
)

const hex64 = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

// hostile values no identifier validator may accept.
var hostileIDs = []string{
	"", " ", "-", "-f", "-rf", "--help", "--force=true",
	"..", "../etc/passwd", "a/../b", "/abs", "a/b", `a\b`,
	"a b", " web", "web ", "web\n", "web\r\n", "\tweb", "web\x00", "web\x00.evil",
	"a;b", "a|b", "a&b", "$(id)", "`id`", "a'b", `a"b`, "a?b=c", "a#b", "a%2Fb",
	"wéb", "ｗｅｂ", ".hidden", "_x",
}

func TestValidContainerID(t *testing.T) {
	valid := []string{
		"a", "web", "my_app-1.2", "Web.Server_01", "0",
		"1a2b3c4d5e6f",             // short ID
		hex64,                      // full ID
		strings.Repeat("a", 128),   // longest allowed
		"compose-project-db-1",     // compose style
		"k8s_POD_name.with.dots_0", // mixed separators
	}
	for _, s := range valid {
		if !validContainerID(s) {
			t.Errorf("validContainerID(%q) = false, want true", s)
		}
	}
	invalid := append([]string{strings.Repeat("a", 129), strings.Repeat("a", 4096)}, hostileIDs...)
	for _, s := range invalid {
		if validContainerID(s) {
			t.Errorf("validContainerID(%q) = true, want false", s)
		}
	}
}

func TestValidImageID(t *testing.T) {
	valid := []string{
		"1a2b3c4d5e6f", hex64, "sha256:" + hex64, "sha256:1a2b3c4d5e6f",
	}
	for _, s := range valid {
		if !validImageID(s) {
			t.Errorf("validImageID(%q) = false, want true", s)
		}
	}
	invalid := append([]string{
		"1a2b3c4d5e6",                // 11 characters
		hex64 + "0",                  // 65 characters
		strings.ToUpper(hex64),       // upper case is not what the Engine prints
		"sha256:", "sha512:" + hex64, // wrong or empty digest
		"sha256:" + hex64 + "\n", // trailing newline
		"nginx", "nginx:latest",  // references are not IDs
		"1a2b3c4d5e6g", "sha256:-abc", // non-hex
	}, hostileIDs...)
	for _, s := range invalid {
		if validImageID(s) {
			t.Errorf("validImageID(%q) = true, want false", s)
		}
	}
}

func TestValidImageRef(t *testing.T) {
	valid := []string{
		"nginx",
		"nginx:latest",
		"nginx:1.25.3-alpine",
		"library/nginx",
		"user/image:tag",
		"my_image", "my__image", "my-image.v2",
		"ghcr.io/kullanici/imaj:1.2",
		"localhost:5000/ns/img:tag",
		"registry.example.com:5000/a/b/c:v1.0-rc1",
		"192.168.1.10:5000/app:dev",
		"nginx@sha256:" + hex64,
		"nginx:1.25@sha256:" + hex64,
		"registry.example.com:443/team/app:1.0@sha256:" + hex64,
		"img:" + strings.Repeat("t", 128), // longest tag
		"img:_underscore", "img:UPPER",
	}
	for _, s := range valid {
		if !validImageRef(s) {
			t.Errorf("validImageRef(%q) = false, want true", s)
		}
	}
	invalid := append([]string{
		"-nginx", "nginx:", "nginx:-tag", "nginx:.tag",
		"Nginx", "user/Image", // repository names are lower case
		"nginx latest", "nginx:la test",
		"a//b", "/nginx", "nginx/", "nginx:tag/", ".nginx", "nginx.",
		"nginx@sha256:abc", "nginx@sha256:" + strings.ToUpper(hex64), "nginx@md5:" + hex64,
		"nginx@", "nginx:tag:tag",
		"http://example.com/nginx", "https://example.com/nginx:1",
		"img:" + strings.Repeat("t", 129),
		strings.Repeat("a", 256),
		"nginx:latest\n", "nginx\x00",
		"nginx --privileged", "nginx;reboot",
	}, hostileIDs...)
	for _, s := range invalid {
		if s == "a/b" {
			continue // hostile as an identifier, but a valid repository path
		}
		if validImageRef(s) {
			t.Errorf("validImageRef(%q) = true, want false", s)
		}
	}
}

func TestValidVolumeName(t *testing.T) {
	valid := []string{"data", "my-vol_1.0", hex64, "a", strings.Repeat("v", 255), "project_db-data"}
	for _, s := range valid {
		if !validVolumeName(s) {
			t.Errorf("validVolumeName(%q) = false, want true", s)
		}
	}
	invalid := append([]string{strings.Repeat("v", 256), "/var/lib/data", "./data"}, hostileIDs...)
	for _, s := range invalid {
		if validVolumeName(s) {
			t.Errorf("validVolumeName(%q) = true, want false", s)
		}
	}
}

func TestValidNetworkID(t *testing.T) {
	valid := []string{"bridge", "my-net_1.0", hex64, "1a2b3c4d5e6f", strings.Repeat("n", 128), "project_default"}
	for _, s := range valid {
		if !validNetworkID(s) {
			t.Errorf("validNetworkID(%q) = false, want true", s)
		}
	}
	invalid := append([]string{strings.Repeat("n", 129)}, hostileIDs...)
	for _, s := range invalid {
		if validNetworkID(s) {
			t.Errorf("validNetworkID(%q) = true, want false", s)
		}
	}
}

func TestValidJobID(t *testing.T) {
	if !validJobID("0123456789abcdef") {
		t.Error("16 hex characters must be accepted")
	}
	for _, s := range append([]string{"0123456789abcde", "0123456789abcdef0", "0123456789ABCDEF", "0123456789abcdeg"}, hostileIDs...) {
		if validJobID(s) {
			t.Errorf("validJobID(%q) = true, want false", s)
		}
	}
}

/* ---------- secret masking ---------- */

func TestSensitiveNamesAreMasked(t *testing.T) {
	names := []string{
		"PASSWORD", "DB_PASSWORD", "MYSQL_ROOT_PASSWORD", "password", "db_password", "Password", "dbPassWord",
		"SECRET", "APP_SECRET", "secret_value", "ClientSecret",
		"TOKEN", "API_TOKEN", "github_token", "AccessToken",
		"KEY", "API_KEY", "api-key", "ssh.key", "EncryptionKey",
		"PASS", "DB_PASS", "smtp_pass", "MYSQL_PWD", "pgpwd",
		"CREDENTIAL", "AWS_CREDENTIALS", "credential_file", "Credentials",
		"PRIVATE", "SSH_PRIVATE", "private_data", "PrivateThing",
		"com.example.db.password", "io.myserver.secret",
	}
	for _, n := range names {
		if got := maskValue(n, "hunter2"); got != masked {
			t.Errorf("maskValue(%q) = %q, want masked", n, got)
		}
	}
}

func TestHarmlessNamesAreNotMasked(t *testing.T) {
	for _, n := range []string{"PATH", "HOME", "LANG", "TZ", "PORT", "NODE_ENV", "HOSTNAME", "io.myserver.app", "maintainer"} {
		if got := maskValue(n, "value"); got != "value" {
			t.Errorf("maskValue(%q) = %q, want the value unchanged", n, got)
		}
	}
}

// Judgement call: names that contain a sensitive fragment by accident are
// masked too. The matcher is a substring test, so KEYBOARD_LAYOUT (KEY),
// MONKEY (KEY), PASSENGER_APP_ENV (PASS) and COMPASS_MODE (PASS) lose their
// values in the UI. That is over-masking: it hides harmless data but can
// never reveal a secret, so it is accepted. This test pins the behaviour so
// that a future "smarter" matcher is a deliberate decision.
func TestAccidentalFragmentsAreOverMasked(t *testing.T) {
	for _, n := range []string{"KEYBOARD_LAYOUT", "MONKEY", "PASSENGER_APP_ENV", "COMPASS_MODE", "keyboard_layout"} {
		if got := maskValue(n, "tr"); got != masked {
			t.Errorf("maskValue(%q) = %q; over-masking is the documented behaviour", n, got)
		}
	}
}

func TestEmptyValueIsNotMasked(t *testing.T) {
	if got := maskValue("DB_PASSWORD", ""); got != "" {
		t.Errorf("empty value became %q", got)
	}
}

func TestURLPasswordsAreMasked(t *testing.T) {
	cases := []struct{ in, want string }{
		{"postgres://app:s3cret@db:5432/x", "postgres://app:" + masked + "@db:5432/x"},
		{"redis://:s3cret@cache:6379/0", "redis://:" + masked + "@cache:6379/0"},
		{"https://user:s3cret@example.com", "https://user:" + masked + "@example.com"},
		{"amqp://guest:s3cret@mq/vhost?heartbeat=5", "amqp://guest:" + masked + "@mq/vhost?heartbeat=5"},
		{"prefix postgres://app:s3cret@db/x suffix", "prefix postgres://app:" + masked + "@db/x suffix"},
		// A password that itself contains "@": the authority ends at the
		// last "@", so everything up to it is the password.
		{"amqp://user:s3@cret@mq:5672/", "amqp://user:" + masked + "@mq:5672/"},
		{"mysql://root:p@ss:w0rd@db/app", "mysql://root:" + masked + "@db/app"},
	}
	for _, c := range cases {
		got := maskValue("DATABASE_URL", c.in)
		if got != c.want {
			t.Errorf("maskValue(%q) = %q, want %q", c.in, got, c.want)
		}
		if strings.Contains(got, "s3") || strings.Contains(got, "cret") || strings.Contains(got, "w0rd") {
			t.Errorf("maskValue(%q) = %q still contains part of the password", c.in, got)
		}
	}
}

func TestMultipleURLsNeverLeak(t *testing.T) {
	in := "amqp://u:firstpw@mq1:5672,amqp://u:secondpw@mq2:5672"
	got := maskValue("BROKERS", in)
	if strings.Contains(got, "firstpw") || strings.Contains(got, "secondpw") {
		t.Errorf("maskValue(%q) = %q leaks a password", in, got)
	}
}

func TestURLsWithoutPasswordAreUnchanged(t *testing.T) {
	for _, v := range []string{
		"http://example.com:8080/path",
		"http://example.com:8080/user@example.com",
		"https://example.com/?q=a:b@c",
		"ssh://git@github.com/org/repo.git",
		"user@example.com",
		"10:30@night",
	} {
		if got := maskValue("URL", v); got != v {
			t.Errorf("maskValue(%q) = %q, want unchanged", v, got)
		}
	}
}

// Known limitation, documented rather than asserted as correct: a URL whose
// only credential is the user part (a token used as user name) is not
// recognised, and neither is a password containing an unencoded "/" (which
// is not a valid URL). See the report.
func TestURLMaskingLimitations(t *testing.T) {
	for _, v := range []string{
		"https://ghp_tokenvalue@github.com/org/repo.git",
		"mongodb://user:pa/ss@db/app",
	} {
		t.Logf("maskValue(%q) = %q", v, maskValue("REPO", v))
	}
}

func TestMaskEnv(t *testing.T) {
	got := maskEnv([]string{
		"PATH=/usr/bin",
		"DB_PASSWORD=hunter2",
		"EMPTY_SECRET=",
		"NOVALUE",
		"DATABASE_URL=postgres://app:s3cret@db/x",
		"OPTS=a=b=c",
	})
	want := []EnvVar{
		{Name: "PATH", Value: "/usr/bin", Masked: false},
		{Name: "DB_PASSWORD", Value: masked, Masked: true},
		{Name: "EMPTY_SECRET", Value: "", Masked: false},
		{Name: "NOVALUE", Value: "", Masked: false},
		{Name: "DATABASE_URL", Value: "postgres://app:" + masked + "@db/x", Masked: true},
		{Name: "OPTS", Value: "a=b=c", Masked: false},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if out := maskEnv(nil); out == nil || len(out) != 0 {
		t.Errorf("maskEnv(nil) = %#v, want empty non-nil slice", out)
	}
}

func TestMaskMapDoesNotModifyInput(t *testing.T) {
	in := map[string]string{"api_token": "tok", "app": "nginx"}
	out := maskMap(in)
	if out["api_token"] != masked || out["app"] != "nginx" {
		t.Errorf("maskMap = %v", out)
	}
	if in["api_token"] != "tok" {
		t.Error("maskMap modified its input")
	}
	if maskMap(nil) == nil {
		t.Error("maskMap(nil) must return an empty map, not nil")
	}
}

func TestMaskArgs(t *testing.T) {
	in := []string{
		"server", "--port", "8080",
		"--password", "hunter2",
		"--api-key=abc123",
		"-e", "DB_PASSWORD=hunter3",
		"--TOKEN", "tok-1",
		"--url", "postgres://app:s3cret@db/x",
		"--name=web",
	}
	got := maskArgs(in)
	joined := strings.Join(got, " ")
	for _, secret := range []string{"hunter2", "abc123", "hunter3", "tok-1", "s3cret"} {
		if strings.Contains(joined, secret) {
			t.Errorf("maskArgs output %q contains %q", joined, secret)
		}
	}
	for _, keep := range []string{"server", "--port", "8080", "--password", "--api-key=", "--name=web", "DB_PASSWORD="} {
		if !strings.Contains(joined, keep) {
			t.Errorf("maskArgs output %q lost %q", joined, keep)
		}
	}
	if len(got) != len(in) {
		t.Errorf("argument count changed: %d -> %d", len(in), len(got))
	}
}

/* ---------- small parsers ---------- */

func TestParseDockerTime(t *testing.T) {
	if got := parseDockerTime("2024-05-01T10:00:00.5Z"); got == nil || *got != 1714557600 {
		t.Errorf("got %v, want 1714557600", got)
	}
	if got := parseDockerTime("2024-05-01T13:00:00+03:00"); got == nil || *got != 1714557600 {
		t.Errorf("offset time: got %v, want 1714557600", got)
	}
	for _, s := range []string{"", "0001-01-01T00:00:00Z", "yesterday", "2024-05-01", "1970-01-01T00:00:00Z"} {
		if got := parseDockerTime(s); got != nil {
			t.Errorf("parseDockerTime(%q) = %d, want nil", s, *got)
		}
	}
}

func TestExitCodeFromStatus(t *testing.T) {
	cases := []struct {
		in   string
		code int
		ok   bool
	}{
		{"Exited (137) 3 hours ago", 137, true},
		{"Exited (0) 2 minutes ago", 0, true},
		{"Exited (1) About a minute ago", 1, true},
		{"Up 3 hours", 0, false},
		{"Created", 0, false},
		{"Exited ()", 0, false},
		{"Exited (-1) now", 0, false},
		{"Exited (abc) now", 0, false},
		{"Exited (12", 0, false},
		{"Exited (99999999999999999999) now", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		code, ok := exitCodeFromStatus(c.in)
		if code != c.code || ok != c.ok {
			t.Errorf("exitCodeFromStatus(%q) = %d, %v; want %d, %v", c.in, code, ok, c.code, c.ok)
		}
	}
}

func TestShortID(t *testing.T) {
	cases := map[string]string{
		hex64:             hex64[:12],
		"sha256:" + hex64: hex64[:12],
		"abc":             "abc",
		"":                "",
	}
	for in, want := range cases {
		if got := shortID(in); got != want {
			t.Errorf("shortID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestContainerName(t *testing.T) {
	cases := []struct {
		names []string
		want  string
	}{
		{[]string{"/web"}, "web"},
		{[]string{"/other/alias", "/web"}, "web"},
		{[]string{"/a/b"}, "a/b"},
		{nil, hex64[:12]},
	}
	for _, c := range cases {
		if got := containerName(c.names, hex64); got != c.want {
			t.Errorf("containerName(%v) = %q, want %q", c.names, got, c.want)
		}
	}
}
