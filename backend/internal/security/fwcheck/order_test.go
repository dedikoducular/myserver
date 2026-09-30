package fwcheck

import (
	"net/netip"
	"testing"
)

// Fixtures follow ufw's formats from memory; not captured from a live system.

func TestReachableFirstMatchWins(t *testing.T) {
	lan := netip.MustParseAddr("192.168.1.50")
	v6 := netip.MustParseAddr("2001:db8::5")
	none := netip.Addr{}
	head := "Status: active\n\n     To                         Action      From\n     --                         ------      ----\n"
	cases := []struct {
		name         string
		rules        []Rule
		client       netip.Addr
		defaultAllow bool
		want         bool
	}{
		{"empty, default deny", nil, lan, false, false},
		{"empty, default allow", nil, lan, true, true},
		{"allow", ParseAdded("ufw allow 22/tcp\n"), lan, false, true},
		{"deny then allow", ParseAdded("ufw deny 22/tcp\nufw allow 22/tcp\n"), lan, false, false},
		{"reject then allow", ParseAdded("ufw reject 22\nufw allow 22/tcp\n"), lan, false, false},
		{"allow then deny", ParseAdded("ufw allow 22/tcp\nufw deny 22/tcp\n"), lan, false, true},
		{"limit then deny", ParseAdded("ufw limit 22/tcp\nufw deny 22/tcp\n"), lan, false, true},
		{"deny, default allow", ParseAdded("ufw deny 22/tcp\n"), lan, true, false},
		{"unrelated deny, default allow", ParseAdded("ufw deny 23/tcp\nufw deny 22/udp\nufw deny out 22/tcp\n"), lan, true, true},
		{"deny other source then allow", ParseAdded("ufw deny from 10.0.0.0/8 to any port 22\nufw allow 22/tcp\n"), lan, false, true},
		{"deny client source then allow", ParseAdded("ufw deny from 192.168.0.0/16 to any port 22\nufw allow 22/tcp\n"), lan, false, false},
		{"deny client address then allow", ParseAdded("ufw deny from 192.168.1.50\nufw allow 22/tcp\n"), lan, false, false},
		{"source deny, unknown client", ParseAdded("ufw deny from 192.168.0.0/16 to any port 22\nufw allow 22/tcp\n"), none, false, true},
		{"general deny, unknown client", ParseAdded("ufw deny 22\nufw allow 22/tcp\n"), none, false, false},
		{"deny bound to a local address then allow", ParseAdded("ufw deny from any to 192.168.1.10 port 22\nufw allow 22/tcp\n"), lan, false, false},
		{"deny with source port is ignored", ParseAdded("ufw deny from any port 4000 to any port 22 proto tcp\nufw allow 22/tcp\n"), lan, false, true},
		{"deny of OpenSSH profile", ParseAdded("ufw deny OpenSSH\nufw allow 22/tcp\n"), lan, false, false},
		{"deny of another profile", ParseAdded("ufw deny Samba\nufw allow 22/tcp\n"), lan, false, true},
		{"v4 rows then v6 rows, v4 client",
			ParseNumbered(head + "[ 1] 22/tcp DENY IN Anywhere\n[ 2] 22/tcp (v6) ALLOW IN Anywhere (v6)\n"), lan, false, false},
		{"v4 rows then v6 rows, v6 client",
			ParseNumbered(head + "[ 1] 22/tcp DENY IN Anywhere\n[ 2] 22/tcp (v6) ALLOW IN Anywhere (v6)\n"), v6, false, true},
		{"v6 deny, v4 allow, v4 client",
			ParseNumbered(head + "[ 1] 22/tcp (v6) DENY IN Anywhere (v6)\n[ 2] 22/tcp ALLOW IN Anywhere\n"), lan, false, true},
		{"both families denied, unknown client",
			ParseNumbered(head + "[ 1] 22/tcp DENY IN Anywhere\n[ 2] 22/tcp ALLOW IN Anywhere\n[ 3] 22/tcp (v6) DENY IN Anywhere (v6)\n[ 4] 22/tcp (v6) ALLOW IN Anywhere (v6)\n"),
			none, false, false},
		{"one family still open, unknown client",
			ParseNumbered(head + "[ 1] 22/tcp DENY IN Anywhere\n[ 2] 22/tcp ALLOW IN Anywhere\n[ 3] 22/tcp (v6) ALLOW IN Anywhere (v6)\n"),
			none, false, true},
		{"v4 deny, default allow, v6 client",
			ParseNumbered(head + "[ 1] 22/tcp DENY IN Anywhere\n"), v6, true, true},
		{"v4 deny, default allow, mapped v4 client",
			ParseNumbered(head + "[ 1] 22/tcp DENY IN Anywhere\n"), netip.MustParseAddr("::ffff:192.168.1.50"), true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if len(c.rules) == 0 && c.name != "empty, default deny" && c.name != "empty, default allow" {
				t.Fatal("fixture did not parse")
			}
			if got := Reachable(c.rules, 22, "tcp", c.client, c.defaultAllow); got != c.want {
				t.Errorf("got %v want %v (rules %+v)", got, c.want, c.rules)
			}
		})
	}
}

func TestDeniesIgnoresAllowRules(t *testing.T) {
	for _, r := range ParseAdded("ufw allow 22/tcp\nufw limit 22/tcp\nufw deny out 22/tcp\nufw route deny 22/tcp\n") {
		if Denies(r, 22, "tcp", netip.Addr{}) {
			t.Errorf("%q counted as denying incoming SSH", r.Raw)
		}
	}
}
