package netcheck

import (
	"net/netip"
	"reflect"
	"testing"
)

// The /proc fixtures below are written from the documented kernel formats,
// not captured from a live system.

const routeFixture = "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
	"wlan0\t00000000\tFE01A8C0\t0003\t0\t0\t600\t00000000\t0\t0\t0\n" +
	"eth0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n" +
	"eth0\t0001A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n" +
	"docker0\t000011AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n" +
	"wg0\t00000000\t00000000\t0001\t0\t0\t50\t00000000\t0\t0\t0\n" +
	"eth1\t00000000\t0100000A\t0203\t0\t0\t10\t00000000\t0\t0\t0\n" + // reject route
	"eth2\t00000000\t0100000A\t0002\t0\t0\t10\t00000000\t0\t0\t0\n" + // not up
	"eth3\t00000000\t0100000A\t0003\t0\t0\t10\t000000FF\t0\t0\t0\n" + // mask not zero
	"garbage line\n"

func TestParseIPv4Routes(t *testing.T) {
	got := ParseIPv4Routes(routeFixture)
	want := []Gateway{
		{Family: "ipv4", Address: "", Interface: "wg0", Metric: 50},
		{Family: "ipv4", Address: "192.168.1.1", Interface: "eth0", Metric: 100},
		{Family: "ipv4", Address: "192.168.1.254", Interface: "wlan0", Metric: 600},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestParseIPv4RoutesEmptyAndMalformed(t *testing.T) {
	for _, in := range []string{"", "Iface\tDestination\n", "eth0 00000000 ZZZZZZZZ 0003 0 0 0 00000000 0 0 0\n",
		"eth0 00000000 0101A8C0 XYZ 0 0 0 00000000 0 0 0\n"} {
		got := ParseIPv4Routes(in)
		if got == nil {
			t.Errorf("%q: nil slice, want empty", in)
		}
		for _, g := range got {
			// A malformed gateway must never produce a bogus address.
			if g.Address != "" {
				if _, err := netip.ParseAddr(g.Address); err != nil {
					t.Errorf("%q: invalid address %q", in, g.Address)
				}
			}
		}
	}
	// Bad hex gateway with the gateway flag: route is kept without an address.
	got := ParseIPv4Routes("eth0 00000000 ZZZZZZZZ 0003 0 0 0 00000000 0 0 0\n")
	if len(got) != 1 || got[0].Address != "" {
		t.Errorf("bad gateway hex: %+v", got)
	}
}

const route6Fixture = `00000000000000000000000000000000 00 00000000000000000000000000000000 00 fe800000000000000000000000000001 00000064 00000002 00000000 00000003 eth0
00000000000000000000000000000000 00 00000000000000000000000000000000 00 fe800000000000000000000000000001 00000064 00000002 00000000 00000003 eth0
fe800000000000000000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000001 00000000 00000001 eth0
20010db8000000000000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000064 00000001 00000000 00000001 eth0
00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 ffffffff 00000001 00000000 00200200 lo
00000000000000000000000000000000 00 00000000000000000000000000000000 00 20010db8000000000000000000000001 00000400 00000001 00000000 00000003 wlan0
00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 00000032 00000001 00000000 00000001 wg0
00000000000000000000000000000001 80 00000000000000000000000000000000 00 00000000000000000000000000000000 00000000 00000001 00000000 80200001 lo
`

func TestParseIPv6Routes(t *testing.T) {
	got := ParseIPv6Routes(route6Fixture)
	want := []Gateway{
		{Family: "ipv6", Address: "", Interface: "wg0", Metric: 50},
		{Family: "ipv6", Address: "fe80::1", Interface: "eth0", Metric: 100},
		{Family: "ipv6", Address: "2001:db8::1", Interface: "wlan0", Metric: 1024},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if g := ParseIPv6Routes(""); g == nil || len(g) != 0 {
		t.Errorf("empty input: %+v", g)
	}
}

const tcpFixture = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0
   1: 3500007F:0035 00000000:0000 0A 00000000:00000000 00:00000000 00000000   101        0 2345 1 0000000000000000 100 0 0 10 0
   2: 0A01A8C0:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 3456 1 0000000000000000 100 0 0 10 0
   3: 0A01A8C0:0016 0501A8C0:D431 01 00000000:00000000 02:000A1B2C 00000000     0        0 4567 4 0000000000000000 20 4 30 10 -1
   4: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12346 1 0000000000000000 100 0 0 10 0
   5: 00000000:0000 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 0000000000000000 100 0 0 10 0
   6: nonsense:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 0000000000000000 100 0 0 10 0
`

func TestParseSocketsTCP(t *testing.T) {
	got := ParseSockets(tcpFixture, "tcp", false)
	want := []Listener{
		{Protocol: "tcp", Family: "ipv4", Address: "0.0.0.0", Port: 22, Scope: "all", UID: 0},
		{Protocol: "tcp", Family: "ipv4", Address: "127.0.0.53", Port: 53, Scope: "loopback", UID: 101},
		{Protocol: "tcp", Family: "ipv4", Address: "192.168.1.10", Port: 8080, Scope: "specific", UID: 1000},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

const tcp6Fixture = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:0016 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 22222 1 0000000000000000 100 0 0 10 0
   1: 00000000000000000000000001000000:0277 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 22223 1 0000000000000000 100 0 0 10 0
   2: 0000000000000000FFFF00000100007F:1F91 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000    33        0 22224 1 0000000000000000 100 0 0 10 0
   3: B80D0120000000000000000001000000:01BB 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000    33        0 22225 1 0000000000000000 100 0 0 10 0
`

func TestParseSocketsTCP6(t *testing.T) {
	got := ParseSockets(tcp6Fixture, "tcp", true)
	want := []Listener{
		{Protocol: "tcp", Family: "ipv6", Address: "::", Port: 22, Scope: "all", UID: 0},
		{Protocol: "tcp", Family: "ipv6", Address: "::1", Port: 631, Scope: "loopback", UID: 0},
		{Protocol: "tcp", Family: "ipv6", Address: "::ffff:127.0.0.1", Port: 8081, Scope: "loopback", UID: 33},
		{Protocol: "tcp", Family: "ipv6", Address: "2001:db8::1", Port: 443, Scope: "specific", UID: 33},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

const udpFixture = `   sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
  100: 00000000:0044 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 5001 2 0000000000000000 0
  101: 3500007F:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000   101        0 5002 2 0000000000000000 0
  102: 0A01A8C0:C350 08080808:0035 01 00000000:00000000 00:00000000 00000000  1000        0 5003 2 0000000000000000 0
`

func TestParseSocketsUDP(t *testing.T) {
	got := ParseSockets(udpFixture, "udp", false)
	want := []Listener{
		{Protocol: "udp", Family: "ipv4", Address: "0.0.0.0", Port: 68, Scope: "all", UID: 0},
		{Protocol: "udp", Family: "ipv4", Address: "127.0.0.53", Port: 53, Scope: "loopback", UID: 101},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	got6 := ParseSockets("  5: 00000000000000000000000000000000:14E9 00000000000000000000000000000000:0000 07 00000000:00000000 00:00000000 00000000   108        0 1 2 0000000000000000 0\n", "udp", true)
	if len(got6) != 1 || got6[0].Address != "::" || got6[0].Port != 5353 || got6[0].Family != "ipv6" {
		t.Errorf("udp6: %+v", got6)
	}
}

func TestParseResolvConf(t *testing.T) {
	in := `# This is /run/systemd/resolve/resolv.conf
; another comment style
nameserver 192.168.1.1
nameserver 192.168.1.1
nameserver 2001:4860:4860::8888 # google
nameserver fe80::1%eth0
nameserver not-an-address
nameserver 999.1.1.1
nameserver
domain old.example
search lan example.com .
options edns0 trust-ad
`
	r := ParseResolvConf(in)
	wantS := []string{"192.168.1.1", "2001:4860:4860::8888", "fe80::1%eth0"}
	if !reflect.DeepEqual(r.Servers, wantS) {
		t.Errorf("servers %v want %v", r.Servers, wantS)
	}
	if !reflect.DeepEqual(r.Search, []string{"lan", "example.com"}) {
		t.Errorf("search %v", r.Search)
	}
	if r.OnlyStub() {
		t.Error("OnlyStub true for real servers")
	}
	stub := ParseResolvConf("nameserver 127.0.0.53\noptions edns0 trust-ad\nsearch .\n")
	if !stub.OnlyStub() {
		t.Error("stub resolver not detected")
	}
	if len(stub.Search) != 0 {
		t.Errorf("search %v", stub.Search)
	}
	empty := ParseResolvConf("")
	if empty.OnlyStub() || empty.Servers == nil || empty.Search == nil {
		t.Errorf("empty: %+v", empty)
	}
	if !ParseResolvConf("nameserver 127.0.0.53\r\nnameserver 127.0.0.54\r\n").OnlyStub() {
		t.Error("CRLF file not parsed")
	}
}

func TestParseSSHDPorts(t *testing.T) {
	cases := []struct {
		in   string
		want []int
	}{
		{"", []int{}},
		{"#Port 22\n", []int{}},
		{"Port 2222\n", []int{2222}},
		{"port=2222\nPort 22 # default\n", []int{2222, 22}},
		{"Port \"2200\"\n", []int{2200}},
		{"Port 0\nPort 65536\nPort abc\nPort -1\n", []int{}},
		{"Port 22\nMatch User bob\n  Port 9999\n", []int{22}},
		{"PortForwarding 80\nListenAddress 0.0.0.0:2022\n", []int{}},
	}
	for _, c := range cases {
		if got := ParseSSHDPorts(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %v want %v", c.in, got, c.want)
		}
	}
}

func TestPrivateSubnet(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"192.168.1.10/24", "192.168.1.0/24", true},
		{"10.20.30.40/8", "10.0.0.0/8", true},
		{"172.16.5.4/20", "172.16.0.0/20", true},
		{"172.31.255.1/16", "172.31.0.0/16", true},
		{"172.32.0.1/16", "", false},
		{"8.8.8.8/24", "", false},
		{"100.64.0.1/10", "", false}, // CGNAT is not private
		{"127.0.0.1/8", "", false},
		{"169.254.1.1/16", "", false},
		{"192.168.1.10/32", "", false},
		{"192.168.1.10/0", "", false},
		{"fd12:3456::1/64", "fd12:3456::/64", true},
		{"fe80::1/64", "", false},
		{"2001:db8::1/64", "", false},
		{"::1/128", "", false},
		{"fd00::1/128", "", false},
		{"::ffff:192.168.1.10/120", "", false},
	}
	for _, c := range cases {
		got, ok := PrivateSubnet(netip.MustParsePrefix(c.in))
		if ok != c.ok || (ok && got.String() != c.want) {
			t.Errorf("%s: got %v,%v want %s,%v", c.in, got, ok, c.want, c.ok)
		}
	}
	if _, ok := PrivateSubnet(netip.Prefix{}); ok {
		t.Error("invalid prefix accepted")
	}
}

func TestWideBlock(t *testing.T) {
	cases := map[string]string{
		"192.168.1.0/24": "192.168.0.0/16",
		"10.1.2.0/24":    "10.0.0.0/8",
		"172.20.0.0/16":  "172.16.0.0/12",
		"fd00:1::/64":    "fc00::/7",
		"8.8.8.0/24":     "",
		"2001:db8::/32":  "",
	}
	for in, want := range cases {
		got, ok := WideBlock(netip.MustParsePrefix(in))
		if (want == "") == ok || (ok && got.String() != want) {
			t.Errorf("%s: got %v,%v want %q", in, got, ok, want)
		}
	}
}
