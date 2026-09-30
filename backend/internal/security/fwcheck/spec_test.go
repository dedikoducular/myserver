package fwcheck

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizePort(t *testing.T) {
	ok := []struct {
		in, want string
		isRange  bool
	}{
		{"1", "1", false},
		{"22", "22", false},
		{"65535", "65535", false},
		{" 445 ", "445", false},
		{"0022", "22", false},
		{"5000:5010", "5000:5010", true},
		{"5000-5010", "5000:5010", true},
		{"1:65535", "1:65535", true},
	}
	for _, c := range ok {
		got, r, err := NormalizePort(c.in)
		if err != nil || got != c.want || r != c.isRange {
			t.Errorf("%q: got %q,%v,%v want %q,%v", c.in, got, r, err, c.want, c.isRange)
		}
	}
	bad := []string{"", " ", "0", "65536", "99999", "100000", "-1", "+22", "abc", "22a", "2 2", "22/tcp", "ssh",
		"5010:5000", "5000:5000", "0:10", "10:65536", "10:", ":10", "1:2:3", "1-2-3", "10:abc", "--", "-o",
		"--dry-run", "22,80", "22;reboot", "$(id)", "22\n23", "22\x00", "２２", "1e3", "0x16", "22.0"}
	for _, in := range bad {
		if got, _, err := NormalizePort(in); err == nil {
			t.Errorf("%q accepted as %q", in, got)
		} else if _, isUser := err.(Error); !isUser {
			t.Errorf("%q: error is not a fwcheck.Error: %T", in, err)
		}
	}
}

func TestNormalizeSource(t *testing.T) {
	ok := map[string]string{
		"any":                 "any",
		" any ":               "any",
		"192.168.1.10":        "192.168.1.10",
		"192.168.1.0/24":      "192.168.1.0/24",
		"192.168.1.77/24":     "192.168.1.0/24",
		"10.0.0.0/8":          "10.0.0.0/8",
		"203.0.113.5/32":      "203.0.113.5/32",
		"2001:db8::1":         "2001:db8::1",
		"2001:DB8::/32":       "2001:db8::/32",
		"fd00::1234/64":       "fd00::/64",
		"::ffff:192.168.1.10": "192.168.1.10",
	}
	for in, want := range ok {
		got, err := NormalizeSource(in)
		if err != nil || got != want {
			t.Errorf("%q: got %q,%v want %q", in, got, err, want)
		}
	}
	bad := []string{"", "  ", "Any", "ANY", "anywhere", "example.com", "localhost", "my-host", "router.lan",
		"192.168.1", "192.168.1.256", "192.168.1.10/33", "192.168.1.0/-1", "192.168.1.0/", "/24",
		"192.168.1.0/24/24", "0.0.0.0", "0.0.0.0/0", "::", "::/0", "10.0.0.0/0", "fe80::1%eth0",
		"fe80::1%eth0/64", "192.168.1.10 ; reboot", "192.168.1.10 10.0.0.1", "-o", "--foo", "-1.2.3.4",
		"192.168.1.10\n", "192.168.1.\x0010", "192.168.01.10", "0x7f.1", "192.168.1.0/24 ", "1.2.3.4:80",
		"[2001:db8::1]", "$(id)", "`id`", strings.Repeat("1", 65), "::ffff:10.0.0.0/104"}
	for _, in := range bad {
		got, err := NormalizeSource(in)
		// Surrounding white space is trimmed by design; anything else must fail.
		if err == nil && strings.TrimSpace(in) != in {
			if strings.ContainsAny(got, " \t\r\n\x00") {
				t.Errorf("%q: white space survived in %q", in, got)
			}
			continue
		}
		if err == nil {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
}

func TestValidActionAndProtocol(t *testing.T) {
	for _, a := range []string{"allow", "deny", "reject", "limit"} {
		if !ValidAction(a) {
			t.Errorf("action %q refused", a)
		}
	}
	for _, a := range []string{"", "ALLOW", "Allow", "allow ", " allow", "delete", "insert", "enable", "disable",
		"reset", "route", "--force", "-f", "allow\n", "allow;deny", "accept", "drop"} {
		if ValidAction(a) {
			t.Errorf("action %q accepted", a)
		}
	}
	for _, p := range []string{"tcp", "udp", "any"} {
		if !ValidProtocol(p) {
			t.Errorf("protocol %q refused", p)
		}
	}
	for _, p := range []string{"", "TCP", "tcp ", "icmp", "esp", "ah", "gre", "tcp/udp", "both", "-p", "--proto", "6", "all"} {
		if ValidProtocol(p) {
			t.Errorf("protocol %q accepted", p)
		}
	}
}

func TestValidComment(t *testing.T) {
	for _, c := range []string{"", "SSH", "MyServer panel", "web (http/https)", "a_b.c,d:e+f-g", strings.Repeat("a", 64), "a - b", "x-y"} {
		if !ValidComment(c) {
			t.Errorf("comment %q refused", c)
		}
	}
	bad := []string{strings.Repeat("a", 65), " leading", "trailing ", "tab\there", "new\nline", "cr\rhere", "nul\x00",
		"quote'", "dquote\"", "semi;colon", "dollar$", "back`tick", "pipe|", "amp&", "back\\slash", "türkçe",
		"star*", "hash#", "<tag>", "-o", "--dry-run", "--force", "-", "- x", "\x1b[31m", "\u00a0"}
	for _, c := range bad {
		if ValidComment(c) {
			t.Errorf("comment %q accepted", c)
		}
	}
}

func TestNormalizeAndArgs(t *testing.T) {
	cases := []struct {
		in   RuleSpec
		want []string
	}{
		{RuleSpec{"allow", "22", "tcp", "any", ""},
			[]string{"allow", "from", "any", "to", "any", "port", "22", "proto", "tcp"}},
		{RuleSpec{"allow", "445", "tcp", "192.168.1.55/24", "SMB (LAN)"},
			[]string{"allow", "from", "192.168.1.0/24", "to", "any", "port", "445", "proto", "tcp", "comment", "SMB (LAN)"}},
		{RuleSpec{"deny", "53", "any", "203.0.113.5", ""},
			[]string{"deny", "from", "203.0.113.5", "to", "any", "port", "53"}},
		{RuleSpec{"limit", "5000-5010", "udp", "2001:db8::/32", "range"},
			[]string{"limit", "from", "2001:db8::/32", "to", "any", "port", "5000:5010", "proto", "udp", "comment", "range"}},
		{RuleSpec{"reject", " 80 ", "tcp", " any ", ""},
			[]string{"reject", "from", "any", "to", "any", "port", "80", "proto", "tcp"}},
	}
	for _, c := range cases {
		n, err := c.in.Normalize()
		if err != nil {
			t.Errorf("%+v: %v", c.in, err)
			continue
		}
		if got := n.Args(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%+v:\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeRejects(t *testing.T) {
	base := RuleSpec{Action: "allow", Port: "22", Protocol: "tcp", Source: "any"}
	if _, err := base.Normalize(); err != nil {
		t.Fatal(err)
	}
	mut := []func(*RuleSpec){
		func(s *RuleSpec) { s.Action = "" },
		func(s *RuleSpec) { s.Action = "delete" },
		func(s *RuleSpec) { s.Action = "--force" },
		func(s *RuleSpec) { s.Protocol = "" },
		func(s *RuleSpec) { s.Protocol = "icmp" },
		func(s *RuleSpec) { s.Port = "" },
		func(s *RuleSpec) { s.Port = "0" },
		func(s *RuleSpec) { s.Port = "65536" },
		func(s *RuleSpec) { s.Port = "9:8" },
		func(s *RuleSpec) { s.Port = "5000:5010"; s.Protocol = "any" },
		func(s *RuleSpec) { s.Source = "" },
		func(s *RuleSpec) { s.Source = "example.com" },
		func(s *RuleSpec) { s.Source = "-o" },
		func(s *RuleSpec) { s.Comment = "bad;comment" },
		func(s *RuleSpec) { s.Comment = "--dry-run" },
		func(s *RuleSpec) { s.Comment = " x" },
	}
	for i, f := range mut {
		s := base
		f(&s)
		if n, err := s.Normalize(); err == nil {
			t.Errorf("case %d %+v accepted as %+v", i, s, n)
		} else if _, isUser := err.(Error); !isUser || err.Error() == "" {
			t.Errorf("case %d: error %T %q is not a user message", i, err, err)
		}
	}
}

// Whatever the caller sends, the arguments handed to ufw are never
// option-like and never contain control characters, and white space only
// occurs as a plain space inside the comment value.
func TestArgsNeverOptionLike(t *testing.T) {
	hostile := []string{"", "-o", "--foo", "--dry-run", "--force", "-", "a b", "a\tb", "a\nb", "a\x00b", "$(id)",
		"; reboot", "allow", "any", "22", "22 --force", "tcp", "192.168.1.0/24", "192.168.1.0/24 --force",
		"comment", "x' y", "- -", "delete", "1:2", "-1", "\x1b", "any\n--force", "22\n", "tcp\n"}
	actions := append([]string{"allow", "deny", "reject", "limit"}, hostile...)
	ports := append([]string{"22", "1:2"}, hostile...)
	protos := append([]string{"tcp", "udp", "any"}, hostile...)
	sources := append([]string{"any", "10.0.0.0/8"}, hostile...)
	comments := append([]string{"", "ok"}, hostile...)
	accepted := 0
	for _, a := range actions {
		for _, p := range ports {
			for _, pr := range protos {
				for _, s := range sources {
					for _, c := range comments {
						n, err := RuleSpec{Action: a, Port: p, Protocol: pr, Source: s, Comment: c}.Normalize()
						if err != nil {
							continue
						}
						accepted++
						args := n.Args()
						for i, arg := range args {
							isComment := i > 0 && args[i-1] == "comment" && i == len(args)-1
							if arg == "" || strings.HasPrefix(arg, "-") {
								t.Fatalf("option-like or empty argument %q in %q (input %q %q %q %q %q)", arg, args, a, p, pr, s, c)
							}
							for _, ch := range arg {
								if ch < 0x20 || ch == 0x7f || (ch == ' ' && !isComment) {
									t.Fatalf("bad character %q in argument %q of %q", ch, arg, args)
								}
							}
						}
						if !ValidAction(args[0]) {
							t.Fatalf("first argument %q is not an action", args[0])
						}
					}
				}
			}
		}
	}
	if accepted == 0 {
		t.Fatal("no combination was accepted; the test checks nothing")
	}
}

func TestLANRule(t *testing.T) {
	r, err := LANRule("192.168.1.0/24", "445", "tcp", "SMB")
	if err != nil || r.Source != "192.168.1.0/24" || r.Action != "allow" {
		t.Errorf("got %+v %v", r, err)
	}
	for _, s := range []string{"", "any", " any ", "0.0.0.0/0", "example.com"} {
		if r, err := LANRule(s, "445", "tcp", "SMB"); err == nil {
			t.Errorf("source %q accepted: %+v", s, r)
		}
	}
}

func TestParsePortList(t *testing.T) {
	cases := map[string][]PortRange{
		"22":              {{22, 22}},
		"80,443":          {{80, 80}, {443, 443}},
		"5000:5010":       {{5000, 5010}},
		"22,5000:5010,80": {{22, 22}, {5000, 5010}, {80, 80}},
		"":                {},
		"abc":             {},
		"0":               {},
		"22,x,80":         {{22, 22}, {80, 80}},
		"10:":             {},
	}
	for in, want := range cases {
		if got := ParsePortList(in); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %v want %v", in, got, want)
		}
	}
}

func TestBlocks(t *testing.T) {
	cases := []struct {
		s     RuleSpec
		port  int
		proto string
		want  bool
	}{
		{RuleSpec{"deny", "22", "tcp", "any", ""}, 22, "tcp", true},
		{RuleSpec{"reject", "22", "any", "any", ""}, 22, "tcp", true},
		{RuleSpec{"deny", "20:30", "tcp", "any", ""}, 22, "tcp", true},
		{RuleSpec{"deny", "22", "tcp", "203.0.113.5", ""}, 22, "tcp", true},
		{RuleSpec{"deny", "22", "udp", "any", ""}, 22, "tcp", false},
		{RuleSpec{"deny", "23", "tcp", "any", ""}, 22, "tcp", false},
		{RuleSpec{"allow", "22", "tcp", "any", ""}, 22, "tcp", false},
		{RuleSpec{"limit", "22", "tcp", "any", ""}, 22, "tcp", false},
	}
	for _, c := range cases {
		if got := c.s.Blocks(c.port, c.proto); got != c.want {
			t.Errorf("%+v blocks %d/%s: got %v", c.s, c.port, c.proto, got)
		}
	}
}
