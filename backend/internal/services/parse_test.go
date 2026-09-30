package services

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// The systemctl fixtures below follow the output format of systemd 255 on
// Ubuntu 24.04. They were written by hand from that format, not captured
// from a running system (the test container has no systemd).

// `systemctl list-units --type=service --all --no-legend --plain --no-pager`
const fixtureListUnits = `accounts-daemon.service                                 loaded    active     running Accounts Service
apparmor.service                                        loaded    active     exited  Load AppArmor profiles
auditd.service                                          not-found inactive   dead    auditd.service
containerd.service                                      loaded    active     running containerd container runtime
cron.service                                            loaded    active     running Regular background program processing daemon
dbus-broker.service                                     loaded    active     running D-Bus System Message Bus
dbus.service                                            loaded    active     running D-Bus System Message Bus
docker.service                                          loaded    active     running Docker Application Container Engine
emergency.service                                       loaded    inactive   dead    Emergency Shell
getty@tty1.service                                      loaded    active     running Getty on tty1
kilitli.service                                         masked    inactive   dead    kilitli.service
myserver.service                                        loaded    active     running MyServer Panel - sunucu yönetim paneli
nginx.service                                           loaded    failed     failed  A high performance web server and a reverse proxy server
ssh.service                                             loaded    active     running OpenBSD Secure Shell server
systemd-fsck@dev-disk-by\x2duuid-1A2B\x2d3C4D.service   loaded    active     exited  File System Check on /dev/disk/by-uuid/1A2B-3C4D
systemd-journald.service                                loaded    active     running Journal Service
systemd-logind.service                                  loaded    active     running User Login Management
systemd-networkd.service                                loaded    active     running Network Configuration
systemd-resolved.service                                loaded    active     running Network Name Resolution
systemd-udevd.service                                   loaded    active     running Rule-based Manager for Device Events and Files
ufw.service                                             loaded    active     exited  Uncomplicated firewall
user@1000.service                                       loaded    active     running User Manager for UID 1000
yedekleme.service                                       loaded    activating start   Günlük yedekleme görevi (çalışıyor)
`

// The same command without --plain: a status glyph precedes units that
// need attention, two spaces precede the others.
const fixtureListUnitsGlyph = `  cron.service      loaded    active   running Regular background program processing daemon
● nginx.service     loaded    failed   failed  A high performance web server and a reverse proxy server
● auditd.service    not-found inactive dead    auditd.service
  user@1000.service loaded    active   running User Manager for UID 1000
● user@1001.service loaded    failed   failed  User Manager for UID 1001
`

// `systemctl list-unit-files --type=service --no-legend --plain --no-pager`
const fixtureUnitFiles = `accounts-daemon.service                enabled         enabled
apparmor.service                       enabled         enabled
containerd.service                     enabled         enabled
cron.service                           enabled         enabled
dbus-broker.service                    enabled         enabled
dbus-org.freedesktop.login1.service    alias           -
dbus.service                           static          -
docker.service                         enabled         enabled
emergency.service                      static          -
getty@.service                         enabled         enabled
kilitli.service                        masked          enabled
myserver.service                       enabled         enabled
nginx.service                          enabled         enabled
rsync.service                          disabled        enabled
ssh.service                            enabled         enabled
sshd.service                           alias           -
systemd-fsck@.service                  static          -
systemd-journald.service               static          -
systemd-logind.service                 static          -
systemd-networkd.service               enabled         enabled
systemd-resolved.service               enabled         enabled
systemd-udevd.service                  static          -
ufw.service                            enabled         enabled
user@.service                          static          -
yedekleme.service                      enabled         enabled
`

// `systemctl show --no-pager --property=... --timestamp=unix -- <units>`
const fixtureShow = `Id=cron.service
Description=Regular background program processing daemon
UnitFileState=enabled
MainPID=812
MemoryCurrent=1466368
ActiveEnterTimestamp=@1759128306
CanReload=no

Id=ssh.service
Description=OpenBSD Secure Shell server
UnitFileState=enabled
MainPID=1033
MemoryCurrent=6107136
ActiveEnterTimestamp=@1759128310
CanReload=yes
TriggeredBy=ssh.socket

Id=nginx.service
Description=A high performance web server and a reverse proxy server
UnitFileState=enabled
MainPID=0
MemoryCurrent=[not set]
ActiveEnterTimestamp=
CanReload=yes

Id=ufw.service
Description=Uncomplicated firewall
UnitFileState=enabled
MainPID=0
MemoryCurrent=[not set]
ActiveEnterTimestamp=@1759128300
CanReload=no

Id=docker.service
Description=Docker Application Container Engine
UnitFileState=enabled
MainPID=1201
MemoryCurrent=18446744073709551615
ActiveEnterTimestamp=@1759128320
CanReload=yes
TriggeredBy=docker.socket

Id=myserver.service
Description=MyServer Panel - sunucu yönetim paneli
UnitFileState=enabled
MainPID=1500
MemoryCurrent=41234432
ActiveEnterTimestamp=@1759128330
CanReload=no

Id=systemd-fsck@dev-disk-by\x2duuid-1A2B\x2d3C4D.service
Description=File System Check on /dev/disk/by-uuid/1A2B-3C4D
UnitFileState=static
MainPID=0
MemoryCurrent=[not set]
ActiveEnterTimestamp=@1759128290
CanReload=no

Id=user@1000.service
Description=User Manager for UID 1000
UnitFileState=static
MainPID=2210
MemoryCurrent=9424896
ActiveEnterTimestamp=@1759129000
CanReload=yes

Id=systemd-journald.service
Description=Journal Service
UnitFileState=static
MainPID=301
MemoryCurrent=25165824
ActiveEnterTimestamp=@1759128280
CanReload=no
TriggeredBy=systemd-journald.socket systemd-journald-dev-log.socket

Id=dbus.service
Description=D-Bus System Message Bus
UnitFileState=static
MainPID=700
MemoryCurrent=3145728
ActiveEnterTimestamp=@1759128285
CanReload=yes
TriggeredBy=dbus.socket
`

func findListed(list []listedUnit, unit string) (listedUnit, bool) {
	for _, u := range list {
		if u.Unit == unit {
			return u, true
		}
	}
	return listedUnit{}, false
}

func TestParseListUnitsPlain(t *testing.T) {
	got := parseListUnits(fixtureListUnits)
	wantCount := strings.Count(fixtureListUnits, "\n")
	if len(got) != wantCount {
		t.Fatalf("parsed %d units, want %d (every fixture line is a service)", len(got), wantCount)
	}
	cases := []listedUnit{
		{"cron.service", "loaded", "active", "running", "Regular background program processing daemon"},
		{"auditd.service", "not-found", "inactive", "dead", "auditd.service"},
		{"apparmor.service", "loaded", "active", "exited", "Load AppArmor profiles"},
		{"user@1000.service", "loaded", "active", "running", "User Manager for UID 1000"},
		{"getty@tty1.service", "loaded", "active", "running", "Getty on tty1"},
		{`systemd-fsck@dev-disk-by\x2duuid-1A2B\x2d3C4D.service`, "loaded", "active", "exited",
			"File System Check on /dev/disk/by-uuid/1A2B-3C4D"},
		{"myserver.service", "loaded", "active", "running", "MyServer Panel - sunucu yönetim paneli"},
		{"yedekleme.service", "loaded", "activating", "start", "Günlük yedekleme görevi (çalışıyor)"},
		{"nginx.service", "loaded", "failed", "failed", "A high performance web server and a reverse proxy server"},
		{"kilitli.service", "masked", "inactive", "dead", "kilitli.service"},
	}
	for _, want := range cases {
		u, ok := findListed(got, want.Unit)
		if !ok {
			t.Errorf("unit %q missing from the parsed list", want.Unit)
			continue
		}
		if u != want {
			t.Errorf("unit %q parsed as %+v, want %+v", want.Unit, u, want)
		}
	}
}

func TestParseListUnitsGlyph(t *testing.T) {
	got := parseListUnits(fixtureListUnitsGlyph)
	want := []listedUnit{
		{"cron.service", "loaded", "active", "running", "Regular background program processing daemon"},
		{"nginx.service", "loaded", "failed", "failed", "A high performance web server and a reverse proxy server"},
		{"auditd.service", "not-found", "inactive", "dead", "auditd.service"},
		{"user@1000.service", "loaded", "active", "running", "User Manager for UID 1000"},
		{"user@1001.service", "loaded", "failed", "failed", "User Manager for UID 1001"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseListUnitsIgnoresGarbage(t *testing.T) {
	text := "\n\n" +
		"UNIT LOAD ACTIVE SUB DESCRIPTION\n" +
		"ssh.socket loaded active listening OpenBSD Secure Shell server socket\n" +
		"cron.service loaded active\n" + // too few fields
		"bad;name.service loaded active running Injected\n" +
		"--now.service loaded active running Option-like\n" +
		"24 loaded units listed.\n" +
		"To show all installed unit files use 'systemctl list-unit-files'.\n" +
		"ok.service loaded active running \x1b[31mRed\x1b[0m text\x07\r\n"
	got := parseListUnits(text)
	if len(got) != 1 {
		t.Fatalf("parsed %d units (%+v), want only ok.service", len(got), got)
	}
	if got[0].Unit != "ok.service" {
		t.Fatalf("unit = %q, want ok.service", got[0].Unit)
	}
	for _, r := range got[0].Description {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("description %q still contains control character %#x", got[0].Description, r)
		}
	}
	if len(parseListUnits("")) != 0 {
		t.Fatal("empty input must yield no units")
	}
}

func TestParseListUnitsDescriptionCapped(t *testing.T) {
	long := strings.Repeat("ş", 1000)
	got := parseListUnits("x.service loaded active running " + long + "\n")
	if len(got) != 1 {
		t.Fatalf("parsed %d units, want 1", len(got))
	}
	if n := len([]rune(got[0].Description)); n != 300 {
		t.Fatalf("description has %d runes, want 300", n)
	}
	if got[0].Description != strings.Repeat("ş", 300) {
		t.Fatal("capping must cut on rune boundaries")
	}
}

func TestParseUnitFiles(t *testing.T) {
	got := parseUnitFiles(fixtureUnitFiles)
	if want := strings.Count(fixtureUnitFiles, "\n"); len(got) != want {
		t.Fatalf("parsed %d unit files, want %d", len(got), want)
	}
	cases := map[string]string{
		"cron.service":                        "enabled",
		"dbus.service":                        "static",
		"rsync.service":                       "disabled",
		"kilitli.service":                     "masked",
		"sshd.service":                        "alias",
		"dbus-org.freedesktop.login1.service": "alias",
		"getty@.service":                      "enabled",
		"user@.service":                       "static",
	}
	for unit, want := range cases {
		if got[unit] != want {
			t.Errorf("state of %q = %q, want %q", unit, got[unit], want)
		}
	}
	junk := parseUnitFiles("UNIT FILE STATE PRESET\n\n25 unit files listed.\nssh.socket enabled enabled\nlonely.service\n")
	if len(junk) != 0 {
		t.Fatalf("junk parsed as unit files: %+v", junk)
	}
}

func TestParseShow(t *testing.T) {
	got := parseShow(fixtureShow)
	if len(got) != 10 {
		t.Fatalf("parsed %d blocks, want 10", len(got))
	}
	ssh := got["ssh.service"]
	if ssh == nil {
		t.Fatal("ssh.service block missing")
	}
	want := map[string]string{
		"Id": "ssh.service", "Description": "OpenBSD Secure Shell server", "UnitFileState": "enabled",
		"MainPID": "1033", "MemoryCurrent": "6107136", "ActiveEnterTimestamp": "@1759128310",
		"CanReload": "yes", "TriggeredBy": "ssh.socket",
	}
	if !reflect.DeepEqual(ssh, want) {
		t.Fatalf("ssh.service = %+v, want %+v", ssh, want)
	}
	if got[`systemd-fsck@dev-disk-by\x2duuid-1A2B\x2d3C4D.service`] == nil {
		t.Error("escaped instance name missing")
	}
	if got["user@1000.service"]["MainPID"] != "2210" {
		t.Error("user@1000.service not parsed")
	}
	if d := got["myserver.service"]["Description"]; d != "MyServer Panel - sunucu yönetim paneli" {
		t.Errorf("non-ASCII description = %q", d)
	}
	if v := got["nginx.service"]["ActiveEnterTimestamp"]; v != "" {
		t.Errorf("empty value parsed as %q", v)
	}
}

func TestParseShowEdgeCases(t *testing.T) {
	text := "Id=a.service\r\nDescription=key=value pairs = kept\r\n\r\n" +
		"Description=block without an id\nMainPID=5\n\n\n\n" +
		"garbage line without separator\nId=b.service\nDescription=İkinci servis"
	got := parseShow(text)
	if len(got) != 2 {
		t.Fatalf("parsed %d blocks (%+v), want 2", len(got), got)
	}
	if d := got["a.service"]["Description"]; d != "key=value pairs = kept" {
		t.Errorf("value containing '=' parsed as %q", d)
	}
	if got["a.service"]["Id"] != "a.service" {
		t.Errorf("carriage return not stripped: %q", got["a.service"]["Id"])
	}
	if _, leaked := got["a.service"]["MainPID"]; leaked {
		t.Error("property of an id-less block leaked into another unit")
	}
	if _, leaked := got["b.service"]["MainPID"]; leaked {
		t.Error("property of an id-less block leaked into the following unit")
	}
	if d := got["b.service"]["Description"]; d != "İkinci servis" {
		t.Errorf("last block without trailing newline parsed as %q", d)
	}
	if len(parseShow("")) != 0 {
		t.Error("empty input must yield no blocks")
	}
}

func TestParseTimestamp(t *testing.T) {
	ist := time.FixedZone("+03", 3*3600)
	classic := time.Date(2026, 9, 29, 1, 45, 6, 0, ist).Unix()
	cases := []struct {
		in   string
		want int64 // 0 means nil
	}{
		{"@1759128306", 1759128306},
		{"@1759128306.123456", 1759128306},
		{" @1759128306 ", 1759128306},
		{"Tue 2026-09-29 01:45:06 +03", classic},
		{"Tue 2026-09-29 01:45:06 UTC", classic}, // zone text is ignored, host zone applies
		{"", 0},
		{"n/a", 0},
		{"0", 0},
		{"@0", 0},
		{"@-5", 0},
		{"@abc", 0},
		{"@", 0},
		{"yesterday", 0},
		{"Tue 2026-13-45 01:45:06 +03", 0},
		{"2026-09-29 01:45:06", 0},
	}
	for _, c := range cases {
		got := parseTimestamp(c.in, ist)
		switch {
		case c.want == 0 && got != nil:
			t.Errorf("parseTimestamp(%q) = %d, want nil", c.in, *got)
		case c.want != 0 && got == nil:
			t.Errorf("parseTimestamp(%q) = nil, want %d", c.in, c.want)
		case c.want != 0 && *got != c.want:
			t.Errorf("parseTimestamp(%q) = %d, want %d", c.in, *got, c.want)
		}
	}
}

func TestParsePositive(t *testing.T) {
	cases := map[string]int64{
		"812": 812, " 42 ": 42, "0": 0, "-1": 0, "": 0, "[not set]": 0, "infinity": 0,
		"18446744073709551615": 0, "12abc": 0, "9223372036854775807": 9223372036854775807,
	}
	for in, want := range cases {
		got := parsePositive(in)
		if want == 0 {
			if got != nil {
				t.Errorf("parsePositive(%q) = %d, want nil", in, *got)
			}
			continue
		}
		if got == nil || *got != want {
			t.Errorf("parsePositive(%q) = %v, want %d", in, got, want)
		}
	}
}

func TestMapState(t *testing.T) {
	cases := []struct {
		load, active, sub, file, want string
	}{
		{"loaded", "active", "running", "enabled", StateRunning},
		{"loaded", "active", "exited", "enabled", StateActive},
		{"loaded", "active", "running", "disabled", StateRunning},
		{"loaded", "reloading", "reload", "enabled", StateRunning},
		{"loaded", "inactive", "dead", "enabled", StateStopped},
		{"loaded", "inactive", "dead", "static", StateStopped},
		{"loaded", "inactive", "dead", "", StateStopped},
		{"loaded", "inactive", "dead", "disabled", StateDisabled},
		{"unloaded", "inactive", "dead", "disabled", StateDisabled},
		{"loaded", "failed", "failed", "enabled", StateFailed},
		{"loaded", "failed", "failed", "disabled", StateFailed},
		{"loaded", "activating", "start", "enabled", StateActivating},
		{"loaded", "activating", "auto-restart", "enabled", StateActivating},
		{"loaded", "deactivating", "stop-sigterm", "enabled", StateDeactivating},
		{"masked", "inactive", "dead", "masked", StateMasked},
		{"masked", "inactive", "dead", "", StateMasked},
		{"loaded", "active", "running", "masked", StateMasked},
		{"loaded", "inactive", "dead", "masked-runtime", StateMasked},
		{"not-found", "inactive", "dead", "", StateNotInstalled},
		{"not-found", "failed", "failed", "", StateNotInstalled},
		{"not-found", "inactive", "dead", "masked", StateNotInstalled},
	}
	for _, c := range cases {
		if got := mapState(c.load, c.active, c.sub, c.file); got != c.want {
			t.Errorf("mapState(%q,%q,%q,%q) = %q, want %q", c.load, c.active, c.sub, c.file, got, c.want)
		}
	}
}

func fixtureServices() []Service {
	return assemble(parseListUnits(fixtureListUnits), parseUnitFiles(fixtureUnitFiles),
		parseShow(fixtureShow), time.UTC)
}

func findService(list []Service, unit string) (Service, bool) {
	for _, s := range list {
		if s.Unit == unit {
			return s, true
		}
	}
	return Service{}, false
}

func mustService(t *testing.T, list []Service, unit string) Service {
	t.Helper()
	s, ok := findService(list, unit)
	if !ok {
		t.Fatalf("service %q missing", unit)
	}
	return s
}

func TestAssemble(t *testing.T) {
	list := fixtureServices()

	for i := 1; i < len(list); i++ {
		if strings.ToLower(list[i-1].Unit) >= strings.ToLower(list[i].Unit) {
			t.Fatalf("list not sorted or has duplicates at %q, %q", list[i-1].Unit, list[i].Unit)
		}
	}
	for _, s := range list {
		if strings.HasSuffix(s.Unit, "@.service") {
			t.Errorf("template %q must not be listed", s.Unit)
		}
		if s.TriggeredBy == nil {
			t.Errorf("%q: triggered_by must be an empty list, not null", s.Unit)
		}
		if s.Name == "" || s.State == "" || s.Risk == "" {
			t.Errorf("%q: incomplete entry %+v", s.Unit, s)
		}
	}
	for _, alias := range []string{"sshd.service", "dbus-org.freedesktop.login1.service"} {
		if _, ok := findService(list, alias); ok {
			t.Errorf("alias %q must not be listed as a service of its own", alias)
		}
	}
	// 23 listed units plus rsync.service, which only has a unit file.
	if len(list) != 24 {
		t.Errorf("assembled %d services, want 24", len(list))
	}

	ssh := mustService(t, list, "ssh.service")
	if ssh.State != StateRunning || !ssh.Installed || ssh.UnitFileState != "enabled" || !ssh.CanReload {
		t.Errorf("ssh.service = %+v", ssh)
	}
	if ssh.MainPID == nil || *ssh.MainPID != 1033 {
		t.Errorf("ssh main pid = %v, want 1033", ssh.MainPID)
	}
	if ssh.MemoryBytes == nil || *ssh.MemoryBytes != 6107136 {
		t.Errorf("ssh memory = %v, want 6107136", ssh.MemoryBytes)
	}
	if ssh.ActiveSince == nil || *ssh.ActiveSince != 1759128310 {
		t.Errorf("ssh active since = %v, want 1759128310", ssh.ActiveSince)
	}
	if !reflect.DeepEqual(ssh.TriggeredBy, []string{"ssh.socket"}) {
		t.Errorf("ssh triggered by = %v", ssh.TriggeredBy)
	}
	if ssh.Name != "SSH" || ssh.Risk != "critical" || ssh.RiskKind != "ssh" {
		t.Errorf("ssh name/risk = %q %q %q", ssh.Name, ssh.Risk, ssh.RiskKind)
	}

	journald := mustService(t, list, "systemd-journald.service")
	if !reflect.DeepEqual(journald.TriggeredBy, []string{"systemd-journald.socket", "systemd-journald-dev-log.socket"}) {
		t.Errorf("journald triggered by = %v", journald.TriggeredBy)
	}
	if journald.Risk != "protected" {
		t.Errorf("journald risk = %q", journald.Risk)
	}

	docker := mustService(t, list, "docker.service")
	if docker.MemoryBytes != nil {
		t.Errorf("docker memory = %d, want null for the uint64 maximum", *docker.MemoryBytes)
	}

	nginx := mustService(t, list, "nginx.service")
	if nginx.State != StateFailed || nginx.MainPID != nil || nginx.MemoryBytes != nil || nginx.ActiveSince != nil {
		t.Errorf("failed nginx.service = %+v", nginx)
	}

	ufw := mustService(t, list, "ufw.service")
	if ufw.State != StateActive || ufw.MainPID != nil {
		t.Errorf("oneshot ufw.service = %+v", ufw)
	}

	rsync := mustService(t, list, "rsync.service")
	if rsync.State != StateDisabled || !rsync.Installed || rsync.ActiveState != "inactive" {
		t.Errorf("unit-file-only rsync.service = %+v", rsync)
	}

	auditd := mustService(t, list, "auditd.service")
	if auditd.State != StateNotInstalled || auditd.Installed {
		t.Errorf("not-found auditd.service = %+v", auditd)
	}

	masked := mustService(t, list, "kilitli.service")
	if masked.State != StateMasked {
		t.Errorf("masked unit state = %q", masked.State)
	}

	fsck := mustService(t, list, `systemd-fsck@dev-disk-by\x2duuid-1A2B\x2d3C4D.service`)
	if fsck.State != StateActive || fsck.UnitFileState != "static" ||
		fsck.Description != "File System Check on /dev/disk/by-uuid/1A2B-3C4D" {
		t.Errorf("escaped instance = %+v", fsck)
	}

	user := mustService(t, list, "user@1000.service")
	if user.State != StateRunning || user.MainPID == nil || *user.MainPID != 2210 {
		t.Errorf("user@1000.service = %+v", user)
	}

	backup := mustService(t, list, "yedekleme.service")
	if backup.State != StateActivating || backup.Description != "Günlük yedekleme görevi (çalışıyor)" {
		t.Errorf("yedekleme.service = %+v", backup)
	}
	if backup.Name != "yedekleme" {
		t.Errorf("display name = %q, want the unit name without suffix", backup.Name)
	}
}

func TestAssembleWithoutDetails(t *testing.T) {
	list := assemble(parseListUnits(fixtureListUnits), nil, nil, time.UTC)
	cron := mustService(t, list, "cron.service")
	if cron.State != StateRunning || cron.MainPID != nil || cron.UnitFileState != "" || cron.TriggeredBy == nil {
		t.Errorf("cron.service without details = %+v", cron)
	}
}

func TestTrimLine(t *testing.T) {
	if got := trimLine("plain line\r"); got != "plain line" {
		t.Errorf("trimLine = %q", got)
	}
	if got := trimLine("bad \xff byte"); got != "bad ? byte" {
		t.Errorf("invalid UTF-8 handled as %q", got)
	}
	long := strings.Repeat("ş", 3000) // 6000 bytes
	got := trimLine(long)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("long line must end with an ellipsis")
	}
	if len(got) > maxLineLength+len("…") {
		t.Errorf("long line is %d bytes, cap is %d", len(got), maxLineLength)
	}
	if strings.ContainsRune(got, '?') || strings.ContainsRune(got, '�') {
		t.Errorf("cut in the middle of a rune left a broken character")
	}
	// An odd byte offset cuts "ş" in half; the half must be dropped.
	got = trimLine("a" + long)
	if strings.ContainsRune(got, '?') || strings.ContainsRune(got, '�') {
		t.Errorf("cut in the middle of a rune left a broken character")
	}
}

func TestValidateFeatured(t *testing.T) {
	got, err := validateFeatured(`[" ssh ", "docker.service", "ssh.service", "", "user@1000"]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := `["ssh.service","docker.service","user@1000.service"]`; got != want {
		t.Errorf("normalized = %s, want %s", got, want)
	}
	if got, err := validateFeatured(`[]`); err != nil || got != `[]` {
		t.Errorf("empty list = %q, %v", got, err)
	}
	many := make([]string, 0, 25)
	for i := 0; i < 25; i++ {
		many = append(many, "svc"+string(rune('a'+i)))
	}
	bad := []string{
		`not json`, `{"a":1}`, `"ssh.service"`, `[1,2]`,
		`["--now"]`, `["a b"]`, `["../etc/passwd"]`, `["getty@"]`, `["getty@.service"]`,
		`["ssh;reboot"]`, `["` + strings.Repeat("a", 200) + `"]`,
		mustJSON(many),
	}
	for _, in := range bad {
		if got, err := validateFeatured(in); err == nil {
			t.Errorf("validateFeatured(%s) = %s, want an error", in, got)
		}
	}
}

func TestResolveFeatured(t *testing.T) {
	byUnit := map[string]Service{}
	for _, s := range fixtureServices() {
		byUnit[s.Unit] = s
	}
	got := resolveFeatured([]string{
		"sshd.service",    // alternative name of the installed ssh.service
		"ssh.service",     // duplicate after resolution
		"chrony.service",  // nothing of the group installed
		"auditd.service",  // known to systemd but not installed
		"bad name",        // invalid, skipped
		"docker.service",  // installed
		"docker.service",  // duplicate
		"nginx.service",   // failed
		"getty@.service",  // not in the list
		"--now.service",   // invalid
		"kilitli.service", // masked
	}, byUnit)
	var units, states []string
	for _, s := range got {
		units = append(units, s.Unit)
		states = append(states, s.State)
		if !s.Featured {
			t.Errorf("%q not marked featured", s.Unit)
		}
		if s.TriggeredBy == nil {
			t.Errorf("%q: triggered_by must not be null", s.Unit)
		}
	}
	wantUnits := []string{"ssh.service", "chrony.service", "auditd.service", "docker.service",
		"nginx.service", "getty@.service", "kilitli.service"}
	wantStates := []string{StateRunning, StateNotInstalled, StateNotInstalled, StateRunning,
		StateFailed, StateNotInstalled, StateMasked}
	if !reflect.DeepEqual(units, wantUnits) {
		t.Errorf("units = %v, want %v", units, wantUnits)
	}
	if !reflect.DeepEqual(states, wantStates) {
		t.Errorf("states = %v, want %v", states, wantStates)
	}
	if byUnit["ssh.service"].Featured {
		t.Error("resolveFeatured must not modify the snapshot")
	}
	if out := resolveFeatured(nil, byUnit); out == nil || len(out) != 0 {
		t.Errorf("no configuration must give an empty list, got %v", out)
	}
}
