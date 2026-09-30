package security

import (
	"net/netip"
	"reflect"
	"testing"

	"myserver/internal/security/fwcheck"
)

// NOTE: the ufw texts in this file were written from knowledge of ufw's
// output formats. They were not captured from a live system.

const tblHead = "Status: active\n\n     To                         Action      From\n     --                         ------      ----\n"

func sp(s string) *string { return &s }

// numberedState is the state of an active firewall with the given table rows.
func numberedState(rows string) *State {
	return &State{
		Installed: true, Active: true, RulesSource: fwcheck.OriginNumbered,
		Rules:    fwcheck.ParseNumbered(tblHead + rows),
		Defaults: fwcheck.Policies{Incoming: sp("deny")},
		SSHPorts: []int{22}, PanelPort: 8080, PanelPortRequired: true,
	}
}

// addedState is the state of an inactive firewall with the given rules.
func addedState(cmds string) *State {
	return &State{
		Installed: true, Active: false, RulesSource: fwcheck.OriginAdded,
		Rules:    fwcheck.ParseAdded(cmds),
		Defaults: fwcheck.Policies{Incoming: sp("deny")},
		SSHPorts: []int{22}, PanelPort: 8080, PanelPortRequired: true,
	}
}

func kinds(ms []Missing) []string {
	out := []string{}
	for _, m := range ms {
		out = append(out, m.Kind)
	}
	return out
}

var (
	lanClient = netip.MustParseAddr("192.168.1.50")
	wanClient = netip.MustParseAddr("203.0.113.9")
)

func TestMissingAccess(t *testing.T) {
	cases := []struct {
		name   string
		st     *State
		client netip.Addr
		want   []string
	}{
		{"no rules at all", addedState(""), lanClient, []string{"ssh", "panel"}},
		{"only ssh", addedState("ufw allow 22/tcp\n"), lanClient, []string{"panel"}},
		{"only panel", addedState("ufw allow 8080/tcp\n"), lanClient, []string{"ssh"}},
		{"both", addedState("ufw allow 22/tcp\nufw allow 8080/tcp\n"), lanClient, []string{}},
		{"openssh profile and limit", addedState("ufw limit OpenSSH\nufw allow 8080\n"), lanClient, []string{}},
		{"udp rules do not count", addedState("ufw allow 22/udp\nufw allow 8080/udp\n"), lanClient, []string{"ssh", "panel"}},
		{"deny rules do not count", addedState("ufw deny 22/tcp\nufw reject 8080/tcp\n"), lanClient, []string{"ssh", "panel"}},
		{"outgoing rules do not count", addedState("ufw allow out 22/tcp\nufw allow out 8080/tcp\n"), lanClient, []string{"ssh", "panel"}},
		{"range covers", addedState("ufw allow 20:25/tcp\nufw allow 8000:8100/tcp\n"), lanClient, []string{}},
		{"panel rule for the client's network",
			addedState("ufw allow 22/tcp\nufw allow from 192.168.1.0/24 to any port 8080 proto tcp\n"), lanClient, []string{}},
		{"panel rule for another network",
			addedState("ufw allow 22/tcp\nufw allow from 10.0.0.0/8 to any port 8080 proto tcp\n"), lanClient, []string{"panel"}},
		{"panel rule for a lan, client outside",
			addedState("ufw allow 22/tcp\nufw allow from 192.168.1.0/24 to any port 8080 proto tcp\n"), wanClient, []string{"panel"}},
		{"panel rule for one other host",
			addedState("ufw allow 22/tcp\nufw allow from 192.168.1.51 to any port 8080 proto tcp\n"), lanClient, []string{"panel"}},
		{"ipv4-mapped client", addedState("ufw allow 22/tcp\nufw allow from 192.168.1.0/24 to any port 8080 proto tcp\n"),
			netip.MustParseAddr("::ffff:192.168.1.50"), []string{}},
		{"active: v6-only panel rule, v4 client",
			numberedState("[ 1] 22/tcp                     ALLOW IN    Anywhere\n[ 2] 8080/tcp (v6)              ALLOW IN    Anywhere (v6)\n"),
			lanClient, []string{"panel"}},
		{"active: both present",
			numberedState("[ 1] 22/tcp                     ALLOW IN    Anywhere\n[ 2] 8080/tcp                   ALLOW IN    Anywhere\n"),
			lanClient, []string{}},
		{"loopback client means unknown client",
			addedState("ufw allow 22/tcp\nufw allow from 192.168.1.0/24 to any port 8080 proto tcp\n"),
			netip.MustParseAddr("127.0.0.1"), []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := missingAccess(c.st, c.client, "192.168.1.0/24")
			if !reflect.DeepEqual(kinds(got), c.want) {
				t.Errorf("got %+v want %v", got, c.want)
			}
			for _, m := range got {
				if m.Protocol != "tcp" || m.SuggestedSource != "192.168.1.0/24" {
					t.Errorf("bad entry %+v", m)
				}
				if (m.Kind == "ssh" && m.Port != 22) || (m.Kind == "panel" && m.Port != 8080) {
					t.Errorf("bad port %+v", m)
				}
			}
		})
	}
}

func TestMissingAccessSeveralSSHPortsAndProxy(t *testing.T) {
	st := addedState("ufw allow 22/tcp\n")
	st.SSHPorts = []int{22, 2222}
	st.PanelPortRequired = false // panel listens on loopback only
	got := missingAccess(st, lanClient, "any")
	if len(got) != 1 || got[0].Kind != "ssh" || got[0].Port != 2222 {
		t.Errorf("got %+v", got)
	}
}

// First match wins in ufw/iptables: an allow rule that comes after a deny
// or reject rule for the same traffic does not keep the port reachable.
func TestMissingAccessRuleOrder(t *testing.T) {
	cases := []struct {
		name   string
		st     *State
		client netip.Addr
		want   []string
	}{
		{"deny before allow (inactive)",
			addedState("ufw deny 22/tcp\nufw allow 22/tcp\nufw reject 8080\nufw allow 8080/tcp\n"), lanClient, []string{"ssh", "panel"}},
		{"deny before allow (active)",
			numberedState("[ 1] 22/tcp                     DENY IN     Anywhere\n" +
				"[ 2] 22/tcp                     ALLOW IN    Anywhere\n" +
				"[ 3] 8080/tcp                   REJECT IN   Anywhere\n" +
				"[ 4] 8080/tcp                   ALLOW IN    Anywhere\n"), lanClient, []string{"ssh", "panel"}},
		{"allow before deny is fine",
			addedState("ufw allow 22/tcp\nufw deny 22/tcp\nufw allow 8080/tcp\nufw deny 8080/tcp\n"), lanClient, []string{}},
		{"deny of a range and of every protocol",
			addedState("ufw deny 20:25/tcp\nufw allow 22/tcp\nufw deny 8080\nufw allow 8080/tcp\n"), lanClient, []string{"ssh", "panel"}},
		{"deny of a port list",
			addedState("ufw allow 22/tcp\nufw deny 80,8080/tcp\nufw allow 8080/tcp\n"), lanClient, []string{"panel"}},
		{"deny of the OpenSSH profile",
			addedState("ufw deny OpenSSH\nufw allow 22/tcp\nufw allow 8080/tcp\n"), lanClient, []string{"ssh"}},
		{"deny from the client's network",
			addedState("ufw allow 22/tcp\nufw deny from 192.168.1.0/24 to any port 8080\nufw allow 8080/tcp\n"), lanClient, []string{"panel"}},
		{"deny of everything from the client's address",
			addedState("ufw allow 22/tcp\nufw deny from 192.168.1.50\nufw allow 8080/tcp\n"), lanClient, []string{"panel"}},
		{"deny from another network does not shadow",
			addedState("ufw allow 22/tcp\nufw deny from 10.0.0.0/8 to any port 8080\nufw allow 8080/tcp\n"), lanClient, []string{}},
		{"deny of another protocol does not shadow",
			addedState("ufw deny 22/udp\nufw allow 22/tcp\nufw deny 8080/udp\nufw allow 8080/tcp\n"), lanClient, []string{}},
		{"deny of another port does not shadow",
			addedState("ufw deny 23/tcp\nufw allow 22/tcp\nufw deny 8081/tcp\nufw allow 8080/tcp\n"), lanClient, []string{}},
		{"outgoing deny does not shadow",
			addedState("ufw deny out 22/tcp\nufw allow 22/tcp\nufw deny out 8080/tcp\nufw allow 8080/tcp\n"), lanClient, []string{}},
		{"an earlier allow for another network does not help",
			addedState("ufw allow 22/tcp\nufw allow from 10.0.0.0/8 to any port 8080\nufw deny 8080/tcp\nufw allow from 192.168.1.0/24 to any port 8080\n"),
			lanClient, []string{"panel"}},
		{"v6 deny does not shadow the v4 allow (active)",
			numberedState("[ 1] 22/tcp                     ALLOW IN    Anywhere\n" +
				"[ 2] 8080/tcp (v6)              DENY IN     Anywhere (v6)\n" +
				"[ 3] 8080/tcp                   ALLOW IN    Anywhere\n"), lanClient, []string{}},
		{"v4 deny shadows for a v4 client although v6 is allowed (active)",
			numberedState("[ 1] 22/tcp                     ALLOW IN    Anywhere\n" +
				"[ 2] 8080/tcp                   DENY IN     Anywhere\n" +
				"[ 3] 8080/tcp                   ALLOW IN    Anywhere\n" +
				"[ 4] 8080/tcp (v6)              ALLOW IN    Anywhere (v6)\n"), lanClient, []string{"panel"}},
		{"unknown client: deny for everyone shadows",
			addedState("ufw allow 22/tcp\nufw deny 8080/tcp\nufw allow 8080/tcp\n"), netip.Addr{}, []string{"panel"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := missingAccess(c.st, c.client, "any")
			if !reflect.DeepEqual(kinds(got), c.want) {
				t.Errorf("got %+v want %v", got, c.want)
			}
		})
	}
}

// With a default policy of "allow" nothing needs an allow rule, but a deny
// rule still cuts the access.
func TestMissingAccessDefaultAllow(t *testing.T) {
	st := addedState("")
	st.Defaults.Incoming = sp("allow")
	if got := missingAccess(st, lanClient, "any"); len(got) != 0 {
		t.Errorf("default allow, no rules: %+v", got)
	}
	st = addedState("ufw deny 22/tcp\nufw reject 8080/tcp\n")
	st.Defaults.Incoming = sp("allow")
	if got := missingAccess(st, lanClient, "any"); !reflect.DeepEqual(kinds(got), []string{"ssh", "panel"}) {
		t.Errorf("default allow with deny rules: %+v", got)
	}
	// Unknown policy is treated like deny.
	st = addedState("")
	st.Defaults.Incoming = nil
	if got := missingAccess(st, lanClient, "any"); len(got) != 2 {
		t.Errorf("unknown policy: %+v", got)
	}
}

func TestPanelPort(t *testing.T) {
	cases := []struct {
		in       string
		port     int
		required bool
	}{
		{":8080", 8080, true},
		{"0.0.0.0:8080", 8080, true},
		{"[::]:8443", 8443, true},
		{"192.168.1.10:9000", 9000, true},
		{"127.0.0.1:8080", 8080, false},
		{"[::1]:8080", 8080, false},
		{"localhost:8080", 8080, false},
		{"", 0, false},
		{"8080", 0, false},
		{":0", 0, false},
		{":65536", 0, false},
		{":http", 0, false},
	}
	for _, c := range cases {
		p, req := panelPort(c.in)
		if p != c.port || req != c.required {
			t.Errorf("%q: got %d,%v want %d,%v", c.in, p, req, c.port, c.required)
		}
	}
}

func TestBuildStateMarksProtectingRules(t *testing.T) {
	raw := fwcheck.RawStatus{
		Installed: true,
		Verbose:   "Status: active\nLogging: on (low)\nDefault: deny (incoming), allow (outgoing), disabled (routed)\nNew profiles: skip\n",
		Numbered: tblHead +
			"[ 1] 22/tcp                     ALLOW IN    Anywhere\n" +
			"[ 2] 8080/tcp                   ALLOW IN    192.168.1.0/24\n" +
			"[ 3] 445/tcp                    ALLOW IN    192.168.1.0/24\n" +
			"[ 4] 8080/tcp                   DENY IN     203.0.113.5\n" +
			"[ 5] OpenSSH (v6)               LIMIT IN    Anywhere (v6)\n",
		DefaultConf: "DEFAULT_INPUT_POLICY=\"DROP\"\n",
	}
	st := buildState(raw, ":8080")
	if !st.Installed || !st.Active || st.RulesSource != fwcheck.OriginNumbered || len(st.Rules) != 5 {
		t.Fatalf("state %+v", st)
	}
	if st.Defaults.Incoming == nil || *st.Defaults.Incoming != "deny" || st.PanelPort != 8080 || !st.PanelPortRequired {
		t.Errorf("state %+v", st)
	}
	// The SSH port list comes from the machine running the test; the
	// expectations below only hold for the default port.
	if !reflect.DeepEqual(st.SSHPorts, []int{22}) {
		t.Skipf("sshd of this machine listens on %v", st.SSHPorts)
	}
	want := []string{"ssh", "panel", "", "", "ssh"}
	for i, r := range st.Rules {
		if r.Protects != want[i] {
			t.Errorf("rule %d (%q): protects %q want %q", r.Number, r.Raw, r.Protects, want[i])
		}
	}

	off := buildState(fwcheck.RawStatus{}, ":8080")
	if off.Installed || off.Active || off.Rules == nil || len(off.Rules) != 0 || off.InstallHint == "" {
		t.Errorf("not installed: %+v", off)
	}
	inactive := buildState(fwcheck.RawStatus{Installed: true, Verbose: "Status: inactive\n", Numbered: "Status: inactive\n",
		Added: "ufw allow 22/tcp\n", DefaultConf: "DEFAULT_INPUT_POLICY=\"DROP\"\n"}, "127.0.0.1:8080")
	if inactive.Active || inactive.RulesSource != fwcheck.OriginAdded || len(inactive.Rules) != 1 ||
		inactive.PanelPortRequired || inactive.Defaults.Incoming == nil {
		t.Errorf("inactive: %+v", inactive)
	}
}

func TestSuggestSource(t *testing.T) {
	src := []LANSource{
		{CIDR: "10.0.0.0/24", Kind: "subnet", Interface: "eth0"},
		{CIDR: "192.168.1.0/24", Kind: "subnet", Interface: "eth1"},
		{CIDR: "10.0.0.0/8", Kind: "block", Interface: "eth0"},
		{CIDR: "192.168.0.0/16", Kind: "block", Interface: "eth1"},
	}
	if got := suggestSource(src, lanClient); got != "192.168.1.0/24" {
		t.Errorf("lan client: %q", got)
	}
	if got := suggestSource(src, wanClient); got != "10.0.0.0/24" {
		t.Errorf("wan client: %q", got)
	}
	if got := suggestSource(src, netip.Addr{}); got != "10.0.0.0/24" {
		t.Errorf("no client: %q", got)
	}
	if got := suggestSource(nil, lanClient); got != "any" {
		t.Errorf("no sources: %q", got)
	}
	if got := suggestSource(src, netip.MustParseAddr("::ffff:192.168.1.50")); got != "192.168.1.0/24" {
		t.Errorf("mapped client: %q", got)
	}
}
