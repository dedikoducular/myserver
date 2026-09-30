package fwcheck

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

// NOTE: every ufw fixture in this file was written from knowledge of ufw's
// output formats (ufw 0.36.x as shipped with Ubuntu 24.04). None of them was
// captured from a live system.

const numberedActive = `Status: active

     To                         Action      From
     --                         ------      ----
[ 1] 22/tcp                     ALLOW IN    Anywhere
[ 2] 80,443/tcp                 ALLOW IN    192.168.1.0/24             # web (lan)
[ 3] 5000:5010/tcp              DENY IN     Anywhere
[ 4] Anywhere on eth0           ALLOW IN    10.0.0.0/8
[ 5] OpenSSH                    LIMIT IN    Anywhere
[ 6] 8080/tcp                   REJECT IN   203.0.113.5
[ 7] 53                         ALLOW OUT   Anywhere                   (out)
[ 8] Nginx Full                 ALLOW IN    Anywhere
[ 9] 192.168.1.10 445/tcp       ALLOW IN    192.168.1.0/24
[10] 9090/tcp on eth1           ALLOW IN    Anywhere                   (log)
[11] 137:138/udp                ALLOW IN    192.168.1.0/24 5000/udp
[12] 22/tcp (v6)                ALLOW IN    Anywhere (v6)
[13] 443/tcp                    ALLOW IN    2001:db8::/32
[14] OpenSSH (v6)               LIMIT IN    Anywhere (v6)              # ssh v6
[15] 10.0.0.0/8 80/tcp          ALLOW FWD   Anywhere on eth0
[16] Anywhere                   DENY IN     198.51.100.7
[17] 192.168.1.10/esp           ALLOW IN    Anywhere/esp

`

func ruleByNumber(t *testing.T, rules []Rule, n int) Rule {
	t.Helper()
	for _, r := range rules {
		if r.Number == n {
			return r
		}
	}
	t.Fatalf("rule %d missing from %+v", n, rules)
	return Rule{}
}

func TestParseNumbered(t *testing.T) {
	rules := ParseNumbered(numberedActive)
	if len(rules) != 17 {
		t.Fatalf("got %d rules, want 17: %+v", len(rules), rules)
	}
	for i, r := range rules {
		if r.Number != i+1 {
			t.Errorf("rule %d has number %d", i+1, r.Number)
		}
		if r.Origin != OriginNumbered || r.ID == "" || r.ID != r.Raw {
			t.Errorf("rule %d: origin/id wrong: %+v", r.Number, r)
		}
		if strings.Contains(r.ID, "  ") || strings.TrimSpace(r.ID) != r.ID {
			t.Errorf("rule %d: id not collapsed: %q", r.Number, r.ID)
		}
	}
	type want struct {
		action, dir, port, proto, src, srcPort, dst, iface, app, comment string
		v6                                                               bool
	}
	wants := map[int]want{
		1:  {"allow", "in", "22", "tcp", "any", "", "any", "", "", "", false},
		2:  {"allow", "in", "80,443", "tcp", "192.168.1.0/24", "", "any", "", "", "web (lan)", false},
		3:  {"deny", "in", "5000:5010", "tcp", "any", "", "any", "", "", "", false},
		4:  {"allow", "in", "", "any", "10.0.0.0/8", "", "any", "eth0", "", "", false},
		5:  {"limit", "in", "", "any", "any", "", "any", "", "OpenSSH", "", false},
		6:  {"reject", "in", "8080", "tcp", "203.0.113.5", "", "any", "", "", "", false},
		7:  {"allow", "out", "53", "any", "any", "", "any", "", "", "", false},
		8:  {"allow", "in", "", "any", "any", "", "any", "", "Nginx Full", "", false},
		9:  {"allow", "in", "445", "tcp", "192.168.1.0/24", "", "192.168.1.10", "", "", "", false},
		10: {"allow", "in", "9090", "tcp", "any", "", "any", "eth1", "", "", false},
		11: {"allow", "in", "137:138", "udp", "192.168.1.0/24", "5000", "any", "", "", "", false},
		12: {"allow", "in", "22", "tcp", "any", "", "any", "", "", "", true},
		13: {"allow", "in", "443", "tcp", "2001:db8::/32", "", "any", "", "", "", true},
		14: {"limit", "in", "", "any", "any", "", "any", "", "OpenSSH", "ssh v6", true},
		15: {"allow", "routed", "80", "tcp", "any", "", "10.0.0.0/8", "eth0", "", "", false},
		16: {"deny", "in", "", "any", "198.51.100.7", "", "any", "", "", "", false},
		17: {"allow", "in", "", "esp", "any", "", "192.168.1.10", "", "", "", false},
	}
	for n, w := range wants {
		r := ruleByNumber(t, rules, n)
		got := want{r.Action, r.Direction, r.Port, r.Protocol, r.Source, r.SourcePort, r.Destination, r.Interface, r.App, r.Comment, r.IPv6}
		if got != w {
			t.Errorf("rule %d (%q):\n got %+v\nwant %+v", n, r.Raw, got, w)
		}
	}
}

func TestParseNumberedIgnoresNoise(t *testing.T) {
	for _, in := range []string{"", "Status: inactive\n", "Status: active\n\n     To   Action   From\n     --   ------   ----\n\n",
		"[ 0] 22/tcp ALLOW IN Anywhere\n", "[ x] 22/tcp ALLOW IN Anywhere\n", "[ 1] 22/tcp MAYBE IN Anywhere\n",
		"22/tcp ALLOW IN Anywhere\n", "ERROR: You need to be root to run this script\n"} {
		if got := ParseNumbered(in); got == nil || len(got) != 0 {
			t.Errorf("%q: got %+v", in, got)
		}
	}
	// Windows line endings and three-digit numbers.
	got := ParseNumbered("[100] 22/tcp                     ALLOW IN    Anywhere\r\n")
	if len(got) != 1 || got[0].Number != 100 || got[0].Source != "any" || got[0].ID != "22/tcp ALLOW IN Anywhere" {
		t.Errorf("got %+v", got)
	}
}

const showAdded = `Added user rules (see 'ufw status' for running firewall):
ufw allow 22/tcp
ufw allow from 192.168.1.0/24 to any port 445 proto tcp comment 'SMB (LAN)'
ufw limit OpenSSH
ufw deny 5000:5010/tcp
ufw allow in on eth0 to any port 80
ufw reject from 203.0.113.5 to any port 8080 proto tcp
ufw allow 'Nginx Full'
ufw route allow in on eth0 out on eth1
ufw allow from 2001:db8::/32 to any port 443 proto tcp
ufw allow 53
ufw deny from 198.51.100.7
ufw allow out 123/udp
ufw allow from 192.168.1.0/24 port 5000 to any port 137:138 proto udp
ufw allow from 10.0.0.0/8 to 192.168.1.10 port 22 proto tcp
ufw allow from any to any app Samba
ufw allow log 8443/tcp
`

func TestParseAdded(t *testing.T) {
	rules := ParseAdded(showAdded)
	type want struct {
		action, dir, port, proto, src, srcPort, dst, iface, app, comment string
		v6                                                               bool
	}
	wants := map[int]want{
		1:  {"allow", "in", "22", "tcp", "any", "", "any", "", "", "", false},
		2:  {"allow", "in", "445", "tcp", "192.168.1.0/24", "", "any", "", "", "SMB (LAN)", false},
		3:  {"limit", "in", "", "any", "any", "", "any", "", "OpenSSH", "", false},
		4:  {"deny", "in", "5000:5010", "tcp", "any", "", "any", "", "", "", false},
		5:  {"allow", "in", "80", "any", "any", "", "any", "eth0", "", "", false},
		6:  {"reject", "in", "8080", "tcp", "203.0.113.5", "", "any", "", "", "", false},
		7:  {"allow", "in", "", "any", "any", "", "any", "", "Nginx Full", "", false},
		9:  {"allow", "in", "443", "tcp", "2001:db8::/32", "", "any", "", "", "", true},
		10: {"allow", "in", "53", "any", "any", "", "any", "", "", "", false},
		11: {"deny", "in", "", "any", "198.51.100.7", "", "any", "", "", "", false},
		12: {"allow", "out", "123", "udp", "any", "", "any", "", "", "", false},
		13: {"allow", "in", "137:138", "udp", "192.168.1.0/24", "5000", "any", "", "", "", false},
		14: {"allow", "in", "22", "tcp", "10.0.0.0/8", "", "192.168.1.10", "", "", "", false},
		15: {"allow", "in", "", "any", "any", "", "any", "", "Samba", "", false},
		16: {"allow", "in", "8443", "tcp", "any", "", "any", "", "", "", false},
	}
	seen := map[int]bool{}
	for _, r := range rules {
		seen[r.Number] = true
		w, ok := wants[r.Number]
		if !ok {
			// Rule 8 (a route rule without a port) may be skipped or parsed,
			// but must never be reported as an incoming rule.
			if r.Number == 8 && r.Direction == "routed" {
				continue
			}
			t.Errorf("unexpected rule %+v", r)
			continue
		}
		got := want{r.Action, r.Direction, r.Port, r.Protocol, r.Source, r.SourcePort, r.Destination, r.Interface, r.App, r.Comment, r.IPv6}
		if got != w {
			t.Errorf("rule %d (%q):\n got %+v\nwant %+v", r.Number, r.Raw, got, w)
		}
		if r.Origin != OriginAdded || r.ID != r.Raw || !strings.HasPrefix(r.ID, "ufw ") {
			t.Errorf("rule %d: origin/id wrong: %+v", r.Number, r)
		}
	}
	for n := range wants {
		if !seen[n] {
			t.Errorf("rule %d was not parsed", n)
		}
	}
}

func TestParseAddedEmptyAndNoise(t *testing.T) {
	for _, in := range []string{"", "Added user rules (see 'ufw status' for running firewall):\n(None)\n",
		"ufw\n", "ufw allow\n", "ufw frobnicate 22\n", "ufw allow from example.com to any port 22\n",
		"ufw allow 22 bogus value\n", "sudo ufw allow 22\n"} {
		if got := ParseAdded(in); got == nil || len(got) != 0 {
			t.Errorf("%q: got %+v", in, got)
		}
	}
	// A line that cannot be parsed still takes its position: numbers are
	// the positions in ufw's own list.
	got := ParseAdded("ufw frobnicate 1\nufw allow 22/tcp\n")
	if len(got) != 1 || got[0].Number != 2 {
		t.Errorf("got %+v", got)
	}
}

func TestParseActive(t *testing.T) {
	cases := []struct {
		in         string
		active, ok bool
	}{
		{"Status: active\n", true, true},
		{"Status: inactive\n", false, true},
		{numberedActive, true, true},
		{"Status: active\r\nLogging: on (low)\r\n", true, true},
		{"", false, false},
		{"ERROR: problem running iptables\n", false, false},
		{"Status: enabled\n", false, false},
		{"  Status: active\n", false, false},
	}
	for _, c := range cases {
		a, ok := ParseActive(c.in)
		if a != c.active || ok != c.ok {
			t.Errorf("%q: got %v,%v", c.in, a, ok)
		}
	}
}

func str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func TestParseVerbose(t *testing.T) {
	in := `Status: active
Logging: on (low)
Default: deny (incoming), allow (outgoing), disabled (routed)
New profiles: skip

To                         Action      From
--                         ------      ----
22/tcp                     ALLOW IN    Anywhere
`
	p := ParseVerbose(in)
	if str(p.Incoming) != "deny" || str(p.Outgoing) != "allow" || str(p.Routed) != "disabled" || str(p.Logging) != "low" {
		t.Errorf("got %s %s %s %s", str(p.Incoming), str(p.Outgoing), str(p.Routed), str(p.Logging))
	}
	p = ParseVerbose("Status: active\nLogging: off\nDefault: reject (incoming), deny (outgoing), allow (routed)\n")
	if str(p.Incoming) != "reject" || str(p.Outgoing) != "deny" || str(p.Routed) != "allow" || str(p.Logging) != "off" {
		t.Errorf("got %s %s %s %s", str(p.Incoming), str(p.Outgoing), str(p.Routed), str(p.Logging))
	}
	p = ParseVerbose("Status: inactive\n")
	if p.Incoming != nil || p.Outgoing != nil || p.Routed != nil || p.Logging != nil {
		t.Errorf("inactive: %+v", p)
	}
	p = ParseVerbose("Default: bogus (incoming), allow (outgoing)\n")
	if p.Incoming != nil || str(p.Outgoing) != "allow" {
		t.Errorf("bogus policy: %s %s", str(p.Incoming), str(p.Outgoing))
	}
}

func TestParseConfig(t *testing.T) {
	def := `# /etc/default/ufw
IPV6=yes
DEFAULT_INPUT_POLICY="DROP"
DEFAULT_OUTPUT_POLICY="ACCEPT"
#DEFAULT_FORWARD_POLICY="ACCEPT"
DEFAULT_FORWARD_POLICY="REJECT"
DEFAULT_APPLICATION_POLICY="SKIP"
`
	conf := "# /etc/ufw/ufw.conf\nENABLED=no\nLOGLEVEL=low\n"
	p := ParseConfig(def, conf)
	if str(p.Incoming) != "deny" || str(p.Outgoing) != "allow" || str(p.Routed) != "reject" || str(p.Logging) != "low" {
		t.Errorf("got %s %s %s %s", str(p.Incoming), str(p.Outgoing), str(p.Routed), str(p.Logging))
	}
	p = ParseConfig("", "")
	if p.Incoming != nil || p.Outgoing != nil || p.Routed != nil || p.Logging != nil {
		t.Errorf("empty: %+v", p)
	}
	p = ParseConfig("DEFAULT_INPUT_POLICY=\"WHATEVER\"\n", "LOGLEVEL=\"; rm -rf /\"\n")
	if p.Incoming != nil || p.Logging != nil {
		t.Errorf("garbage accepted: %s %s", str(p.Incoming), str(p.Logging))
	}
}

func TestRawStatusRules(t *testing.T) {
	active := RawStatus{Installed: true, Numbered: numberedActive,
		Verbose: "Status: active\nLogging: on (low)\n", Added: showAdded}
	rules, on := active.Rules()
	if !on || len(rules) != 17 || rules[0].Origin != OriginNumbered {
		t.Errorf("active: %v %d", on, len(rules))
	}
	inactive := RawStatus{Installed: true, Numbered: "Status: inactive\n", Verbose: "Status: inactive\n", Added: showAdded}
	rules, on = inactive.Rules()
	if on || len(rules) == 0 || rules[0].Origin != OriginAdded {
		t.Errorf("inactive: %v %+v", on, rules)
	}
	// Nothing readable: not active, and no invented rules.
	rules, on = RawStatus{Installed: true}.Rules()
	if on || len(rules) != 0 {
		t.Errorf("empty: %v %+v", on, rules)
	}
	// The numbered table of a firewall that reports itself inactive is
	// never used.
	odd := RawStatus{Installed: true, Verbose: "Status: inactive\n", Numbered: "Status: inactive\n[ 1] 22/tcp ALLOW IN Anywhere\n"}
	if rules, on = odd.Rules(); on || len(rules) != 0 {
		t.Errorf("odd: %v %+v", on, rules)
	}
}

func TestDeleteArgs(t *testing.T) {
	ok := map[string][]string{
		"ufw allow 22/tcp": {"delete", "allow", "22/tcp"},
		"ufw allow from 192.168.1.0/24 to any port 445 proto tcp comment 'SMB (LAN)'": {"delete", "allow", "from", "192.168.1.0/24", "to", "any", "port", "445", "proto", "tcp"},
		"ufw allow 'Nginx Full'":                 {"delete", "allow", "Nginx Full"},
		"ufw route allow in on eth0 out on eth1": {"route", "delete", "allow", "in", "on", "eth0", "out", "on", "eth1"},
		"ufw limit OpenSSH":                      {"delete", "limit", "OpenSSH"},
		"ufw deny from 2001:db8::/32":            {"delete", "deny", "from", "2001:db8::/32"},
	}
	for in, want := range ok {
		got, valid := DeleteArgs(in)
		if !valid || !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %q,%v want %q", in, got, valid, want)
		}
	}
	bad := []string{"", "ufw", "ufw allow", "allow 22", "ufw --force reset", "ufw reset", "ufw disable", "ufw enable",
		"ufw delete 1", "ufw allow --dry-run", "ufw allow -h", "ufw allow 22 --force", "ufw --dry-run allow 22",
		"ufw allow $(id)", "ufw allow 22;reboot", "ufw allow 'a\tb'", "ufw allow `id`", "ufw allow a|b",
		"ufw route", "ufw route reset x", "ufw insert 1 allow 22", "ufw default allow", "ufw logging off",
		"ufw allow " + strings.Repeat("a", 65), "ufw comment x", "ufw app update all",
		"ufw allow from -1 to any"}
	for _, in := range bad {
		if got, valid := DeleteArgs(in); valid {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
	// No accepted line may yield an option-like argument, and the result
	// always starts with a delete of a rule action.
	for in := range ok {
		got, _ := DeleteArgs(in)
		for _, a := range got {
			if strings.HasPrefix(a, "-") || strings.ContainsAny(a, "\n\r\t\x00") {
				t.Errorf("%q: unsafe argument %q", in, a)
			}
		}
	}
}

func TestCovers(t *testing.T) {
	none := netip.Addr{}
	lan := netip.MustParseAddr("192.168.1.50")
	wan := netip.MustParseAddr("203.0.113.9")
	v6 := netip.MustParseAddr("2001:db8::5")
	mapped := netip.MustParseAddr("::ffff:192.168.1.50")
	rules := ParseNumbered(numberedActive)
	cases := []struct {
		rule   int
		port   int
		proto  string
		client netip.Addr
		want   bool
	}{
		{1, 22, "tcp", none, true},
		{1, 22, "tcp", lan, true},
		{1, 22, "tcp", mapped, true},
		{1, 22, "tcp", v6, false}, // an IPv4 row does not admit an IPv6 client
		{1, 22, "udp", none, false},
		{1, 23, "tcp", none, false},
		{2, 443, "tcp", lan, true},
		{2, 80, "tcp", lan, true},
		{2, 443, "tcp", wan, false},
		{2, 8080, "tcp", lan, false},
		{3, 5005, "tcp", lan, false}, // deny
		{4, 8080, "tcp", netip.MustParseAddr("10.1.2.3"), true},
		{4, 8080, "tcp", lan, false},
		{5, 22, "tcp", lan, true}, // limit OpenSSH
		{5, 2222, "tcp", lan, false},
		{6, 8080, "tcp", netip.MustParseAddr("203.0.113.5"), false}, // reject
		{7, 53, "tcp", none, false},                                 // outgoing
		{8, 80, "tcp", none, false},                                 // unknown application profile
		{9, 445, "tcp", lan, false},                                 // bound to one destination
		{11, 137, "udp", lan, false},                                // source port restricted
		{12, 22, "tcp", v6, true},
		{12, 22, "tcp", lan, false},
		{13, 443, "tcp", v6, true},
		{13, 443, "tcp", netip.MustParseAddr("2001:db9::1"), false},
		{15, 80, "tcp", none, false}, // routed
		{16, 22, "tcp", none, false},
	}
	for _, c := range cases {
		r := ruleByNumber(t, rules, c.rule)
		if got := Covers(r, c.port, c.proto, c.client); got != c.want {
			t.Errorf("rule %d (%q) covers %d/%s for %v: got %v want %v", c.rule, r.Raw, c.port, c.proto, c.client, got, c.want)
		}
	}
	// Rules of an inactive firewall apply to both address families.
	added := ParseAdded("ufw allow 8080/tcp\nufw allow from 192.168.1.7 to any port 9000\n")
	if !Covers(added[0], 8080, "tcp", v6) || !Covers(added[0], 8080, "tcp", lan) {
		t.Error("added rule should cover both families")
	}
	if !Covers(added[1], 9000, "tcp", netip.MustParseAddr("192.168.1.7")) || Covers(added[1], 9000, "tcp", lan) {
		t.Error("single-address source handled wrongly")
	}
}
