package termcheck

import (
	"strings"
	"testing"
)

// ubuntuPasswd is /etc/passwd captured from an ubuntu:24.04 container.
const ubuntuPasswd = `root:x:0:0:root:/root:/bin/bash
daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin
bin:x:2:2:bin:/bin:/usr/sbin/nologin
sys:x:3:3:sys:/dev:/usr/sbin/nologin
sync:x:4:65534:sync:/bin:/bin/sync
games:x:5:60:games:/usr/games:/usr/sbin/nologin
man:x:6:12:man:/var/cache/man:/usr/sbin/nologin
lp:x:7:7:lp:/var/spool/lpd:/usr/sbin/nologin
mail:x:8:8:mail:/var/mail:/usr/sbin/nologin
news:x:9:9:news:/var/spool/news:/usr/sbin/nologin
uucp:x:10:10:uucp:/var/spool/uucp:/usr/sbin/nologin
proxy:x:13:13:proxy:/bin:/usr/sbin/nologin
www-data:x:33:33:www-data:/var/www:/usr/sbin/nologin
backup:x:34:34:backup:/var/backups:/usr/sbin/nologin
list:x:38:38:Mailing List Manager:/var/list:/usr/sbin/nologin
irc:x:39:39:ircd:/run/ircd:/usr/sbin/nologin
_apt:x:42:65534::/nonexistent:/usr/sbin/nologin
nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin
ubuntu:x:1000:1000:Ubuntu:/home/ubuntu:/bin/bash
`

// hostilePasswd is written by hand: accounts a real system could contain and
// entries that are malformed on purpose.
const hostilePasswd = `# a comment
root:x:0:0:root:/root:/bin/bash
toor:x:0:0:second root:/root:/bin/bash
lowuid:x:999:999::/home/lowuid:/bin/bash
ayse:x:1001:1001:Ayşe Yılmaz,Oda 1,555,556:/home/ayse:/bin/bash
zshuser:x:1002:1002::/home/zshuser:/usr/bin/zsh
nologin1:x:1003:1003::/home/n:/usr/sbin/nologin
nologin2:x:1004:1004::/home/n:/sbin/nologin
false1:x:1005:1005::/home/n:/bin/false
true1:x:1006:1006::/home/n:/usr/bin/true
sync1:x:1007:1007::/home/n:/bin/sync
halt1:x:1008:1008::/home/n:/sbin/halt
shutdown1:x:1009:1009::/home/n:/sbin/shutdown
gituser:x:1010:1010::/home/git:/usr/bin/git-shell
emptyshell:x:1011:1011::/home/n:
relshell:x:1012:1012::/home/n:bin/bash
dotshell:x:1013:1013::/home/n:/bin/../bin/bash
spaceshell:x:1014:1014::/home/n:/bin/bash -c id
relhome:x:1015:1015::home/n:/bin/bash
emptyhome:x:1016:1016:::/bin/bash
overflow:x:65534:1017::/home/n:/bin/bash
sixfields:x:1018:1018::/home/n
eightfields:x:1019:1019::/home/n:/bin/bash:extra
baduid:x:abc:1020::/home/n:/bin/bash
neguid:x:-1:1021::/home/n:/bin/bash
plusuid:x:+1022:1022::/home/n:/bin/bash
emptyuid:x::1023::/home/n:/bin/bash
spaceuid:x: 1024:1024::/home/n:/bin/bash
biguid:x:4294967296:1025::/home/n:/bin/bash
maxuid:x:4294967295:1026::/home/n:/bin/bash
maxgid:x:1027:4294967295::/home/n:/bin/bash
badgid:x:1028:users::/home/n:/bin/bash
+nisuser:x:1029:1029::/home/n:/bin/bash
+::::::
-denied:x:1030:1030::/home/n:/bin/bash
Upper:x:1031:1031::/home/n:/bin/bash
root:x:1032:1032:fake root:/home/n:/bin/bash
dup:x:1033:1033:first:/home/dup:/usr/sbin/nologin
dup:x:1034:1034:second:/home/dup:/bin/bash
crlf:x:1035:1035::/home/crlf:/bin/bash` + "\r\n" + `last:x:1036:1036::/home/last:/bin/sh`

func TestValidUsername(t *testing.T) {
	valid := []string{
		"ubuntu", "a", "_apt", "_", "www-data", "user_1", "u-2", "a0",
		strings.Repeat("a", 32),
	}
	for _, n := range valid {
		if !ValidUsername(n) {
			t.Errorf("ValidUsername(%q) = false, want true", n)
		}
	}
	invalid := []string{
		"", " ", "Root", "ROOT", "1abc", "-abc", "-", "a b", " ubuntu", "ubuntu ",
		"ubuntu\n", "\nubuntu", "ubuntu\x00", "a:b", "a/b", "../etc", "a.b", "a$", "a;id",
		"$(id)", "`id`", "ü", "ayşe", "a\tb", "user@host", "--help",
		strings.Repeat("a", 33), strings.Repeat("a", 4096),
	}
	for _, n := range invalid {
		if ValidUsername(n) {
			t.Errorf("ValidUsername(%q) = true, want false", n)
		}
	}
}

func TestParsePasswdUbuntu(t *testing.T) {
	entries := ParsePasswd(ubuntuPasswd)
	if len(entries) != 19 {
		t.Fatalf("parsed %d entries, want 19", len(entries))
	}
	first := entries[0]
	if first != (Entry{Name: "root", UID: 0, GID: 0, Gecos: "root", Home: "/root", Shell: "/bin/bash"}) {
		t.Errorf("first entry = %+v", first)
	}
	last := entries[18]
	if last != (Entry{Name: "ubuntu", UID: 1000, GID: 1000, Gecos: "Ubuntu", Home: "/home/ubuntu", Shell: "/bin/bash"}) {
		t.Errorf("last entry = %+v", last)
	}
	apt, ok := Find(entries, "_apt")
	if !ok || apt.UID != 42 || apt.GID != 65534 || apt.Gecos != "" || apt.Home != "/nonexistent" {
		t.Errorf("_apt = %+v, found %v", apt, ok)
	}
}

func TestParsePasswdEdgeCases(t *testing.T) {
	if got := ParsePasswd(""); len(got) != 0 {
		t.Errorf("empty input gave %d entries", len(got))
	}
	if got := ParsePasswd("\n\n\n"); len(got) != 0 {
		t.Errorf("blank lines gave %d entries", len(got))
	}
	// No trailing newline, CRLF line endings.
	got := ParsePasswd("a:x:1000:1000:A:/home/a:/bin/bash\r\nb:x:1001:1001:B:/home/b:/bin/sh")
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[0].Shell != "/bin/bash" || got[1].Shell != "/bin/sh" {
		t.Errorf("shells = %q, %q", got[0].Shell, got[1].Shell)
	}
}

func TestParsePasswdSkipsMalformed(t *testing.T) {
	entries := ParsePasswd(hostilePasswd)
	skipped := []string{
		"sixfields", "eightfields", "baduid", "neguid", "plusuid", "emptyuid", "spaceuid",
		"biguid", "maxuid", "maxgid", "badgid", "+nisuser", "nisuser", "-denied", "denied", "", "+",
		"# a comment",
	}
	for _, n := range skipped {
		if e, ok := Find(entries, n); ok {
			t.Errorf("malformed entry %q was parsed: %+v", n, e)
		}
	}
	for _, n := range []string{"ayse", "crlf", "last", "dup", "toor"} {
		if _, ok := Find(entries, n); !ok {
			t.Errorf("well-formed entry %q was not parsed", n)
		}
	}
	if e, _ := Find(entries, "crlf"); e.Shell != "/bin/bash" {
		t.Errorf("CRLF entry shell = %q", e.Shell)
	}
}

func TestFindReturnsFirstMatch(t *testing.T) {
	e, ok := Find(ParsePasswd(hostilePasswd), "dup")
	if !ok || e.UID != 1033 || e.Gecos != "first" {
		t.Errorf("Find(dup) = %+v, %v; want the first record", e, ok)
	}
}

func TestFullName(t *testing.T) {
	cases := map[string]string{
		"Ayşe Yılmaz,Oda 1,555,556": "Ayşe Yılmaz",
		"Ubuntu":                    "Ubuntu",
		"":                          "",
		",,,":                       "",
		"  Spaced  ,x":              "Spaced",
	}
	for gecos, want := range cases {
		if got := (Entry{Gecos: gecos}).FullName(); got != want {
			t.Errorf("FullName(%q) = %q, want %q", gecos, got, want)
		}
	}
}

func TestValidShell(t *testing.T) {
	valid := []string{"/bin/bash", "/bin/sh", "/usr/bin/zsh", "/usr/bin/fish", "/bin/dash", "/usr/bin/bash"}
	for _, s := range valid {
		if !ValidShell(s) {
			t.Errorf("ValidShell(%q) = false, want true", s)
		}
	}
	invalid := []string{
		"", "bash", "bin/bash", "./bash", "/bin/../bin/bash", "/bin//bash", "/bin/bash/", "/bin/./bash",
		"/", "/bin/bash -l", "/bin/bash\n", "/bin/bash\r", "/bin/ba\x00sh",
		"/usr/sbin/nologin", "/sbin/nologin", "/bin/false", "/usr/bin/false", "/bin/true",
		"/bin/sync", "/sbin/halt", "/sbin/shutdown", "/usr/bin/git-shell",
	}
	for _, s := range invalid {
		if ValidShell(s) {
			t.Errorf("ValidShell(%q) = true, want false", s)
		}
	}
}

func TestLookupUbuntuFixture(t *testing.T) {
	e, r := Lookup(ubuntuPasswd, "ubuntu")
	if r != OK {
		t.Fatalf("Lookup(ubuntu) reason = %q, want OK", r)
	}
	if e.UID != 1000 || e.Shell != "/bin/bash" || e.Home != "/home/ubuntu" {
		t.Errorf("entry = %+v", e)
	}
	// Every other account of a stock system must be refused.
	for _, e := range ParsePasswd(ubuntuPasswd) {
		if e.Name == "ubuntu" {
			continue
		}
		if _, r := Lookup(ubuntuPasswd, e.Name); r == OK {
			t.Errorf("Lookup(%q) = OK, want a refusal", e.Name)
		}
	}
	want := map[string]Reason{
		"root": IsRoot, "daemon": SystemAccount, "sync": SystemAccount, "_apt": SystemAccount,
		"www-data": SystemAccount, "nobody": SystemAccount, "nosuchuser": NotFound,
	}
	for name, reason := range want {
		if _, r := Lookup(ubuntuPasswd, name); r != reason {
			t.Errorf("Lookup(%q) = %q, want %q", name, r, reason)
		}
	}
}

func TestLookupRefusals(t *testing.T) {
	cases := []struct {
		name string
		want Reason
	}{
		{"root", IsRoot},
		{"toor", IsRoot},
		{"lowuid", SystemAccount},
		{"overflow", SystemAccount},
		{"nologin1", NoShell},
		{"nologin2", NoShell},
		{"false1", NoShell},
		{"true1", NoShell},
		{"sync1", NoShell},
		{"halt1", NoShell},
		{"shutdown1", NoShell},
		{"gituser", NoShell},
		{"emptyshell", NoShell},
		{"relshell", NoShell},
		{"dotshell", NoShell},
		{"relhome", BadHome},
		{"emptyhome", BadHome},
		{"dup", NoShell}, // the first record decides, as getpwnam would
		{"nosuchuser", NotFound},
		{"sixfields", NotFound},
		{"eightfields", NotFound},
		{"baduid", NotFound},
		{"neguid", NotFound},
		{"plusuid", NotFound},
		{"emptyuid", NotFound},
		{"spaceuid", NotFound},
		{"biguid", NotFound},
		{"maxuid", NotFound},
		{"maxgid", NotFound},
		{"badgid", NotFound},
		{"nisuser", NotFound},
		{"denied", NotFound},
		{"spaceshell", NoShell},
		{"", BadName},
		{"Upper", BadName},
		{"+nisuser", BadName},
		{"root\n", BadName},
		{"../../etc/passwd", BadName},
	}
	for _, c := range cases {
		e, r := Lookup(hostilePasswd, c.name)
		if r != c.want {
			t.Errorf("Lookup(%q) = %q, want %q (entry %+v)", c.name, r, c.want, e)
		}
	}
	for _, ok := range []string{"ayse", "zshuser", "crlf", "last"} {
		if _, r := Lookup(hostilePasswd, ok); r != OK {
			t.Errorf("Lookup(%q) = %q, want OK", ok, r)
		}
	}
}

// A record named root is refused even when the first matching record carries
// an unprivileged uid, and uid 0 is refused under any name.
func TestCheckRoot(t *testing.T) {
	if r := Check(Entry{Name: "root", UID: 1500, GID: 1500, Home: "/root", Shell: "/bin/bash"}); r != IsRoot {
		t.Errorf("root with uid 1500: %q, want %q", r, IsRoot)
	}
	if r := Check(Entry{Name: "admin", UID: 0, GID: 1500, Home: "/root", Shell: "/bin/bash"}); r != IsRoot {
		t.Errorf("uid 0 named admin: %q, want %q", r, IsRoot)
	}
}

func TestCheckUIDBoundaries(t *testing.T) {
	cases := []struct {
		uid  uint32
		want Reason
	}{
		{1, SystemAccount}, {999, SystemAccount}, {1000, OK}, {1001, OK},
		{65533, OK}, {65534, SystemAccount}, {65535, OK}, {60000, OK},
	}
	for _, c := range cases {
		e := Entry{Name: "user", UID: c.uid, GID: 1000, Home: "/home/user", Shell: "/bin/bash"}
		if r := Check(e); r != c.want {
			t.Errorf("uid %d: %q, want %q", c.uid, r, c.want)
		}
	}
}

func TestCheckBadFields(t *testing.T) {
	base := Entry{Name: "user", UID: 1000, GID: 1000, Home: "/home/user", Shell: "/bin/bash"}
	if r := Check(base); r != OK {
		t.Fatalf("base entry: %q", r)
	}
	e := base
	e.Name = "User"
	if r := Check(e); r != BadName {
		t.Errorf("bad name: %q", r)
	}
	e = base
	e.Home = "/home/us\x00er"
	if r := Check(e); r != BadHome {
		t.Errorf("NUL in home: %q", r)
	}
	e = base
	e.Home = ""
	if r := Check(e); r != BadHome {
		t.Errorf("empty home: %q", r)
	}
}

func TestEligible(t *testing.T) {
	got := Eligible(ubuntuPasswd)
	if len(got) != 1 || got[0].Name != "ubuntu" {
		t.Errorf("Eligible(ubuntu fixture) = %+v, want only ubuntu", got)
	}
	var names []string
	for _, e := range Eligible(hostilePasswd) {
		names = append(names, e.Name)
	}
	want := "ayse zshuser crlf last"
	if strings.Join(names, " ") != want {
		t.Errorf("Eligible(hostile) = %q, want %q", strings.Join(names, " "), want)
	}
	if got := Eligible(""); got == nil || len(got) != 0 {
		t.Errorf("Eligible(\"\") = %#v, want an empty non-nil slice", got)
	}
}

func TestMessage(t *testing.T) {
	if Message(OK) != "" {
		t.Errorf("Message(OK) = %q", Message(OK))
	}
	seen := map[string]Reason{}
	for _, r := range []Reason{BadName, NotFound, IsRoot, SystemAccount, NoShell, BadHome, Reason("other")} {
		m := Message(r)
		if m == "" {
			t.Errorf("Message(%q) is empty", r)
		}
		if prev, dup := seen[m]; dup {
			t.Errorf("Message(%q) equals Message(%q)", r, prev)
		}
		seen[m] = r
	}
}

func TestValidSize(t *testing.T) {
	valid := [][2]int{{80, 24}, {2, 2}, {500, 500}, {2, 500}, {500, 2}, {132, 43}}
	for _, s := range valid {
		if !ValidSize(s[0], s[1]) {
			t.Errorf("ValidSize(%d, %d) = false", s[0], s[1])
		}
	}
	invalid := [][2]int{
		{0, 0}, {1, 24}, {80, 1}, {501, 24}, {80, 501}, {-1, 24}, {80, -24}, {0, 24}, {80, 0},
		{65536, 24}, {80, 65536}, {65616, 65560}, {1 << 31, 24}, {-1 << 31, -1 << 31},
	}
	for _, s := range invalid {
		if ValidSize(s[0], s[1]) {
			t.Errorf("ValidSize(%d, %d) = true", s[0], s[1])
		}
	}
}

// The stat lines are written by hand in the format of proc(5).
func TestParseStat(t *testing.T) {
	cases := []struct {
		in   string
		want ProcStat
		ok   bool
	}{
		{"1234 (bash) S 1200 1234 1200 34816 1234 4194304 100", ProcStat{PPID: 1200, PGRP: 1234, Session: 1200}, true},
		{"77 (my prog) x) R 1 77 55 0 -1", ProcStat{PPID: 1, PGRP: 77, Session: 55}, true},
		{"77 ((sd-pam)) S 5 6 7 0", ProcStat{PPID: 5, PGRP: 6, Session: 7}, true},
		{"9 (a) S 1 2 3", ProcStat{PPID: 1, PGRP: 2, Session: 3}, true},
		{"9 (a) S 1 2 3\n", ProcStat{PPID: 1, PGRP: 2, Session: 3}, true},
		{"", ProcStat{}, false},
		{"1234 bash S 1 2 3", ProcStat{}, false},
		{"1234 (bash)", ProcStat{}, false},
		{"1234 (bash) S 1 2", ProcStat{}, false},
		{"1234 (bash) S x 2 3", ProcStat{}, false},
		{"1234 (bash) S 1 y 3", ProcStat{}, false},
		{"1234 (bash) S 1 2 z", ProcStat{}, false},
	}
	for _, c := range cases {
		got, ok := ParseStat(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseStat(%q) = %+v, %v; want %+v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
