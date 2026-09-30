package network

import (
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The directory trees built here imitate /sys, /etc and /run of an Ubuntu
// server. They are written from knowledge of those layouts, not copied from
// a live system.

func mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func write(t *testing.T, content string, parts ...string) {
	t.Helper()
	p := filepath.Join(parts...)
	mkdir(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	mkdir(t, filepath.Dir(link))
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// fakeSys builds a /sys tree and points the package at it.
//
//	enp3s0   PCI ethernet
//	wlp2s0   PCI wifi
//	lo       loopback
//	docker0  Docker bridge with one veth port
//	veth1a2b veth port of docker0
//	br-empty Docker network bridge without ports
//	br0      bridge with the physical port enp4s0
//	enp4s0   PCI ethernet, port of br0
//	wg0      WireGuard tunnel
//	bond0    bond
//	enp3s0.10 VLAN on enp3s0
//	tailscale0 tun device
func fakeSys(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	class := mkdir(t, root, "sys", "class", "net")
	dev := func(name, devPath, uevent string, dirs ...string) {
		real := mkdir(t, root, "sys", "devices", devPath, "net", name)
		if uevent != "" {
			write(t, uevent, real, "uevent")
		}
		for _, d := range dirs {
			mkdir(t, real, d)
		}
		symlink(t, real, filepath.Join(class, name))
	}
	dev("enp3s0", "pci0000:00/0000:00:1c.0/0000:03:00.0", "INTERFACE=enp3s0\nIFINDEX=2\n")
	mkdir(t, root, "sys", "devices", "pci0000:00/0000:00:1c.0/0000:03:00.0", "net", "enp3s0", "device")
	dev("enp4s0", "pci0000:00/0000:00:1c.1/0000:04:00.0", "INTERFACE=enp4s0\nIFINDEX=3\n")
	dev("wlp2s0", "pci0000:00/0000:00:1c.2/0000:02:00.0", "DEVTYPE=wlan\nINTERFACE=wlp2s0\n", "wireless")
	dev("lo", "virtual", "INTERFACE=lo\nIFINDEX=1\n")
	dev("docker0", "virtual", "DEVTYPE=bridge\nINTERFACE=docker0\n", "bridge", "brif")
	dev("veth1a2b", "virtual", "INTERFACE=veth1a2b\n")
	dev("br-empty", "virtual", "DEVTYPE=bridge\nINTERFACE=br-empty\n", "bridge", "brif")
	dev("br0", "virtual", "DEVTYPE=bridge\nINTERFACE=br0\n", "bridge", "brif")
	dev("wg0", "virtual", "DEVTYPE=wireguard\nINTERFACE=wg0\n")
	dev("bond0", "virtual", "DEVTYPE=bond\nINTERFACE=bond0\n", "bonding")
	dev("enp3s0.10", "virtual", "DEVTYPE=vlan\nINTERFACE=enp3s0.10\n")
	dev("tailscale0", "virtual", "INTERFACE=tailscale0\n")
	// Bridge ports are links to the port's entry.
	symlink(t, filepath.Join(class, "veth1a2b"), filepath.Join(root, "sys", "devices", "virtual", "net", "docker0", "brif", "veth1a2b"))
	symlink(t, filepath.Join(class, "enp4s0"), filepath.Join(root, "sys", "devices", "virtual", "net", "br0", "brif", "enp4s0"))

	oldSys, oldVlan := sysNet, procVlan
	sysNet, procVlan = class, filepath.Join(root, "proc", "net", "vlan")
	t.Cleanup(func() { sysNet, procVlan = oldSys, oldVlan })
	return root
}

func TestClassifyAndGroup(t *testing.T) {
	if os.Getenv("OS") == "Windows_NT" {
		t.Skip("needs symbolic links")
	}
	fakeSys(t)
	cases := []struct {
		name         string
		loopback     bool
		defaultRoute bool
		kind, group  string
	}{
		{"enp3s0", false, true, "ethernet", "primary"},
		{"enp4s0", false, false, "ethernet", "primary"},
		{"wlp2s0", false, false, "wifi", "primary"},
		{"lo", true, false, "loopback", "virtual"},
		{"docker0", false, false, "bridge", "virtual"},
		{"veth1a2b", false, false, "virtual", "virtual"},
		{"br-empty", false, false, "bridge", "virtual"},
		{"br0", false, false, "bridge", "primary"},
		{"wg0", false, false, "virtual", "virtual"},
		{"tailscale0", false, false, "virtual", "virtual"},
		{"bond0", false, false, "bond", "primary"},
		{"enp3s0.10", false, false, "vlan", "primary"},
		// A tunnel that carries the default route is what the server is
		// reached through.
		{"wg0", false, true, "virtual", "primary"},
		{"lo", true, true, "loopback", "virtual"},
	}
	for _, c := range cases {
		kind := classify(c.name, c.loopback, deviceType(c.name), isVirtualDevice(c.name))
		group := groupOf(c.name, kind, c.loopback, c.defaultRoute)
		if kind != c.kind || group != c.group {
			t.Errorf("%s (default route %v): got %s/%s want %s/%s", c.name, c.defaultRoute, kind, group, c.kind, c.group)
		}
	}
	if !isVirtualDevice("does-not-exist") {
		t.Error("an interface without a /sys entry must not count as hardware")
	}
}

func pfx(s ...string) []netip.Prefix {
	out := []netip.Prefix{}
	for _, x := range s {
		out = append(out, netip.MustParsePrefix(x))
	}
	return out
}

func TestSubnetsOf(t *testing.T) {
	if os.Getenv("OS") == "Windows_NT" {
		t.Skip("needs symbolic links")
	}
	fakeSys(t)
	type ifc struct {
		name     string
		loopback bool
		route    bool
		addrs    []string
	}
	host := []ifc{
		{"lo", true, false, []string{"127.0.0.1/8", "::1/128"}},
		{"docker0", false, false, []string{"172.17.0.1/16", "fe80::42:acff:fe11:1/64"}},
		{"br-empty", false, false, []string{"172.18.0.1/16"}},
		{"veth1a2b", false, false, []string{"fe80::1c2b:3aff:fe4d:5e6f/64"}},
		{"wg0", false, false, []string{"10.8.0.1/24"}},
		{"tailscale0", false, false, []string{"100.101.102.103/32", "fd7a:115c:a1e0::1/128"}},
		{"wlp2s0", false, false, []string{"192.168.50.7/24"}},
		{"br0", false, false, []string{"10.10.0.2/16"}},
		{"enp3s0", false, true, []string{"192.168.1.10/24", "192.168.1.11/24", "fd12:3456:789a::10/64",
			"2a02:1234::10/64", "fe80::a00:27ff:fe4e:66a1/64", "203.0.113.20/28"}},
	}
	var ifs []Interface
	for _, h := range host {
		kind := classify(h.name, h.loopback, deviceType(h.name), isVirtualDevice(h.name))
		ifs = append(ifs, Interface{Name: h.name, Kind: kind, DefaultRoute: h.route,
			Group: groupOf(h.name, kind, h.loopback, h.route), prefixes: pfx(h.addrs...)})
	}
	got := subnetsOf(ifs)
	want := []Subnet{
		{CIDR: "192.168.50.0/24", Interface: "wlp2s0", Family: "ipv4", Wide: "192.168.0.0/16"},
		{CIDR: "10.10.0.0/16", Interface: "br0", Family: "ipv4", Wide: "10.0.0.0/8"},
		{CIDR: "192.168.1.0/24", Interface: "enp3s0", Family: "ipv4", Wide: "192.168.0.0/16"},
		{CIDR: "fd12:3456:789a::/64", Interface: "enp3s0", Family: "ipv6", Wide: "fc00::/7"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	for _, s := range got {
		for _, bad := range []string{"172.17.", "172.18.", "10.8.", "127.", "100.", "fd7a", "fe80", "203.0.113", "2a02"} {
			if strings.HasPrefix(s.CIDR, bad) {
				t.Errorf("%s must not be offered as a private network", s.CIDR)
			}
		}
	}
	if got := subnetsOf(nil); got == nil || len(got) != 0 {
		t.Errorf("no interfaces: %+v", got)
	}
}

func TestDetectManager(t *testing.T) {
	type tree struct {
		files []string
		dirs  []string
	}
	cases := []struct {
		name    string
		t       tree
		kind    string
		netplan []string
	}{
		{"nothing", tree{}, "unknown", []string{}},
		{"ubuntu server: netplan with networkd",
			tree{files: []string{"etc/netplan/50-cloud-init.yaml", "etc/netplan/99-custom.yml", "etc/netplan/README", "run/systemd/network/10-netplan-enp3s0.network"},
				dirs: []string{"run/systemd/netif/links", "run/systemd/netif/state"}},
			"netplan-networkd", []string{"50-cloud-init.yaml", "99-custom.yml"}},
		{"netplan, nothing generated yet", tree{files: []string{"etc/netplan/01-netcfg.yaml"}}, "netplan-networkd", []string{"01-netcfg.yaml"}},
		{"ubuntu desktop: netplan with NetworkManager",
			tree{files: []string{"etc/netplan/01-network-manager-all.yaml", "run/NetworkManager/system-connections/netplan-enp3s0.nmconnection"},
				dirs: []string{"run/NetworkManager"}},
			"netplan-networkmanager", []string{"01-network-manager-all.yaml"}},
		{"netplan, NetworkManager running, no networkd",
			tree{files: []string{"etc/netplan/01-network-manager-all.yaml"}, dirs: []string{"run/NetworkManager"}},
			"netplan-networkmanager", []string{"01-network-manager-all.yaml"}},
		{"netplan, both daemons running, networkd files generated",
			tree{files: []string{"etc/netplan/01.yaml", "run/systemd/network/10-netplan-eth0.network"},
				dirs: []string{"run/NetworkManager", "run/systemd/netif/links"}},
			"netplan-networkd", []string{"01.yaml"}},
		{"plain NetworkManager",
			tree{files: []string{"etc/NetworkManager/system-connections/home.nmconnection"}, dirs: []string{"run/NetworkManager"}},
			"networkmanager", []string{}},
		{"plain networkd",
			tree{files: []string{"etc/systemd/network/20-wired.network"}, dirs: []string{"run/systemd/netif/links"}},
			"networkd", []string{}},
		{"networkd running without own files", tree{dirs: []string{"run/systemd/netif/state"}}, "networkd", []string{}},
		{"debian ifupdown", tree{files: []string{"etc/network/interfaces=auto lo\niface lo inet loopback\n\nauto eth0\niface eth0 inet dhcp\n", "run/network/ifstate"}},
			"ifupdown", []string{}},
		{"ifupdown with only loopback", tree{files: []string{"etc/network/interfaces=auto lo\niface lo inet loopback\n"}}, "unknown", []string{}},
		{"ifupdown through interfaces.d",
			tree{files: []string{"etc/network/interfaces=source /etc/network/interfaces.d/*\n", "etc/network/interfaces.d/eth0"}},
			"ifupdown", []string{}},
		{"ifupdown file beside running networkd with files wins for networkd",
			tree{files: []string{"etc/network/interfaces=iface eth0 inet dhcp\n", "etc/systemd/network/20-wired.network"}, dirs: []string{"run/systemd/netif/links"}},
			"networkd", []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			for _, f := range c.t.files {
				name, content, _ := strings.Cut(f, "=")
				write(t, content, root, filepath.FromSlash(name))
			}
			for _, d := range c.t.dirs {
				mkdir(t, root, filepath.FromSlash(d))
			}
			m := detectManagerAt(root)
			if m.Kind != c.kind {
				t.Errorf("kind %q want %q (evidence %v)", m.Kind, c.kind, m.Evidence)
			}
			if !reflect.DeepEqual(m.NetplanFiles, c.netplan) {
				t.Errorf("netplan files %v want %v", m.NetplanFiles, c.netplan)
			}
			if m.Label == "" || m.Evidence == nil {
				t.Errorf("label/evidence missing: %+v", m)
			}
			if c.kind != "unknown" && c.name != "networkd running without own files" && len(m.Evidence) == 0 {
				t.Errorf("no evidence given for %s", m.Kind)
			}
			for _, e := range m.Evidence {
				if strings.Contains(e, root) {
					t.Errorf("evidence mentions the fixture directory: %q", e)
				}
			}
		})
	}
}

func TestCollectDNS(t *testing.T) {
	if os.Getenv("OS") == "Windows_NT" {
		t.Skip("needs symbolic links")
	}
	// systemd-resolved: /etc/resolv.conf is a link to the stub file.
	root := t.TempDir()
	write(t, "nameserver 127.0.0.53\noptions edns0 trust-ad\nsearch lan\n", root, "run/systemd/resolve/stub-resolv.conf")
	write(t, "nameserver 192.168.1.1\nnameserver 1.1.1.1\nsearch lan\n", root, "run/systemd/resolve/resolv.conf")
	symlink(t, "../run/systemd/resolve/stub-resolv.conf", filepath.Join(root, "etc", "resolv.conf"))
	d := collectDNSAt(root)
	if !d.SystemdResolved || !reflect.DeepEqual(d.Servers, []string{"192.168.1.1", "1.1.1.1"}) ||
		!reflect.DeepEqual(d.Search, []string{"lan"}) || d.Source == nil || *d.Source != "/run/systemd/resolve/resolv.conf" {
		t.Errorf("resolved: %+v source %v", d, d.Source)
	}

	// A copied stub file (no link) is recognised by its content.
	root = t.TempDir()
	write(t, "nameserver 127.0.0.53\n", root, "etc/resolv.conf")
	write(t, "nameserver 9.9.9.9\n", root, "run/systemd/resolve/resolv.conf")
	d = collectDNSAt(root)
	if !d.SystemdResolved || !reflect.DeepEqual(d.Servers, []string{"9.9.9.9"}) {
		t.Errorf("copied stub: %+v", d)
	}

	// Stub without the upstream file: the stub itself is reported.
	root = t.TempDir()
	write(t, "nameserver 127.0.0.53\n", root, "etc/resolv.conf")
	d = collectDNSAt(root)
	if !d.SystemdResolved || !reflect.DeepEqual(d.Servers, []string{"127.0.0.53"}) || d.Source == nil || *d.Source != "/etc/resolv.conf" {
		t.Errorf("stub only: %+v", d)
	}

	// Plain file.
	root = t.TempDir()
	write(t, "# static\nnameserver 8.8.8.8\nnameserver 2001:4860:4860::8888\nsearch example.com corp.example.com\n", root, "etc/resolv.conf")
	d = collectDNSAt(root)
	if d.SystemdResolved || !reflect.DeepEqual(d.Servers, []string{"8.8.8.8", "2001:4860:4860::8888"}) ||
		!reflect.DeepEqual(d.Search, []string{"example.com", "corp.example.com"}) || *d.Source != "/etc/resolv.conf" {
		t.Errorf("plain: %+v", d)
	}

	// Nothing readable: explicit empty state.
	d = collectDNSAt(t.TempDir())
	if d.Source != nil || d.Servers == nil || d.Search == nil || len(d.Servers) != 0 || d.SystemdResolved {
		t.Errorf("empty: %+v", d)
	}
}

func TestIPv6Scope(t *testing.T) {
	cases := map[string]string{"::1": "host", "fe80::1": "link", "fd00::1": "unique_local", "2001:db8::1": "global"}
	for in, want := range cases {
		if got := ipv6Scope(netip.MustParseAddr(in)); got != want {
			t.Errorf("%s: %s want %s", in, got, want)
		}
	}
}

// Collect reads the real system; in the test container that is a small
// network namespace. Only invariants are checked.
func TestCollectOnThisMachine(t *testing.T) {
	if os.Getenv("OS") == "Windows_NT" {
		t.Skip("Linux only")
	}
	o, err := Collect()
	if err != nil {
		t.Fatal(err)
	}
	if o.Interfaces == nil || o.Gateways == nil || o.PrivateSubnets == nil || o.Listening == nil ||
		o.DNS.Servers == nil || o.DNS.Search == nil || o.Manager.Evidence == nil || o.Manager.NetplanFiles == nil {
		t.Errorf("a list is nil: %+v", o)
	}
	for _, it := range o.Interfaces {
		if it.Kind == "loopback" && it.Group != "virtual" {
			t.Errorf("loopback in group %s", it.Group)
		}
		if it.IPv4 == nil || it.IPv6 == nil || it.Gateways == nil {
			t.Errorf("%s: nil list", it.Name)
		}
	}
	for _, s := range o.PrivateSubnets {
		if strings.HasPrefix(s.CIDR, "127.") {
			t.Errorf("loopback offered: %+v", s)
		}
	}
}
