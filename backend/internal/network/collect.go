package network

import (
	"net"
	"net/netip"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"myserver/internal/network/netcheck"
)

// sysNet and procVlan are variables only so that tests can point them at a
// fixture tree.
var (
	sysNet   = "/sys/class/net"
	procVlan = "/proc/net/vlan"
)

// IPv4Addr is an interface address with its prefix length.
type IPv4Addr struct {
	Address string `json:"address"`
	Prefix  int    `json:"prefix"`
}

// IPv6Addr is an interface address with its prefix length and scope
// ("global", "unique_local", "link" or "host").
type IPv6Addr struct {
	Address string `json:"address"`
	Prefix  int    `json:"prefix"`
	Scope   string `json:"scope"`
}

// Stats are the kernel's counters since the interface was created.
type Stats struct {
	RxBytes   uint64 `json:"rx_bytes"`
	TxBytes   uint64 `json:"tx_bytes"`
	RxPackets uint64 `json:"rx_packets"`
	TxPackets uint64 `json:"tx_packets"`
	RxErrors  uint64 `json:"rx_errors"`
	TxErrors  uint64 `json:"tx_errors"`
	RxDropped uint64 `json:"rx_dropped"`
	TxDropped uint64 `json:"tx_dropped"`
}

// Interface describes one network interface. Pointer fields are null when
// the value cannot be determined on this host.
type Interface struct {
	Name string `json:"name"`
	// Kind: ethernet, wifi, bridge, bond, vlan, virtual, loopback.
	Kind string `json:"kind"`
	// Group: "primary" interfaces are shown expanded; "virtual" ones
	// (loopback, Docker bridges, veth, tunnels) are collapsed by default.
	Group string `json:"group"`
	// State is the kernel operstate: up, down, dormant, lowerlayerdown, unknown...
	State        string             `json:"state"`
	AdminUp      bool               `json:"admin_up"`
	MAC          *string            `json:"mac"`
	MTU          int                `json:"mtu"`
	IPv4         []IPv4Addr         `json:"ipv4"`
	IPv6         []IPv6Addr         `json:"ipv6"`
	SpeedMbps    *int               `json:"speed_mbps"`
	Duplex       *string            `json:"duplex"`
	Carrier      *bool              `json:"carrier"`
	LinkUpSecs   *int64             `json:"link_up_seconds"`
	Driver       *string            `json:"driver"`
	DeviceType   *string            `json:"device_type"`
	Master       *string            `json:"master"`
	DefaultRoute bool               `json:"default_route"`
	Gateways     []netcheck.Gateway `json:"gateways"`
	Stats        *Stats             `json:"stats"`

	prefixes []netip.Prefix
}

func readTrim(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

func readUint(path string) (uint64, bool) {
	s, ok := readTrim(path)
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 10, 64)
	return v, err == nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readStats(name string) *Stats {
	dir := filepath.Join(sysNet, name, "statistics")
	var s Stats
	fields := []struct {
		file string
		dst  *uint64
	}{
		{"rx_bytes", &s.RxBytes}, {"tx_bytes", &s.TxBytes},
		{"rx_packets", &s.RxPackets}, {"tx_packets", &s.TxPackets},
		{"rx_errors", &s.RxErrors}, {"tx_errors", &s.TxErrors},
		{"rx_dropped", &s.RxDropped}, {"tx_dropped", &s.TxDropped},
	}
	for _, f := range fields {
		v, ok := readUint(filepath.Join(dir, f.file))
		if !ok {
			return nil
		}
		*f.dst = v
	}
	return &s
}

func deviceType(name string) string {
	text, ok := readTrim(filepath.Join(sysNet, name, "uevent"))
	if !ok {
		return ""
	}
	for _, line := range strings.Split(text, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "DEVTYPE="); ok {
			return v
		}
	}
	return ""
}

// isVirtualDevice reports whether the interface has no hardware behind it.
func isVirtualDevice(name string) bool {
	p, err := filepath.EvalSymlinks(filepath.Join(sysNet, name))
	if err != nil {
		return !exists(filepath.Join(sysNet, name, "device"))
	}
	return strings.Contains(filepath.ToSlash(p), "/devices/virtual/")
}

// hasPhysicalPort reports whether a bridge has a hardware interface attached
// (a Docker bridge only has veth ports).
func hasPhysicalPort(bridge string) bool {
	entries, err := os.ReadDir(filepath.Join(sysNet, bridge, "brif"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !isVirtualDevice(e.Name()) {
			return true
		}
	}
	return false
}

func classify(name string, loopback bool, devType string, virtual bool) string {
	dir := filepath.Join(sysNet, name)
	switch {
	case loopback:
		return "loopback"
	case devType == "wlan" || exists(filepath.Join(dir, "wireless")) || exists(filepath.Join(dir, "phy80211")):
		return "wifi"
	case devType == "bridge" || exists(filepath.Join(dir, "bridge")):
		return "bridge"
	case devType == "bond" || exists(filepath.Join(dir, "bonding")):
		return "bond"
	case devType == "vlan" || exists(filepath.Join(procVlan, name)):
		return "vlan"
	case virtual:
		return "virtual"
	default:
		return "ethernet"
	}
}

// groupOf decides whether an interface is shown as a real ("primary") or a
// "virtual" one.
func groupOf(name, kind string, loopback, defaultRoute bool) string {
	switch {
	case defaultRoute && !loopback:
		return "primary"
	case loopback || kind == "virtual":
		return "virtual"
	case kind == "bridge":
		if hasPhysicalPort(name) {
			return "primary"
		}
		return "virtual"
	default:
		return "primary"
	}
}

func ipv6Scope(a netip.Addr) string {
	switch {
	case a.IsLoopback():
		return "host"
	case a.IsLinkLocalUnicast():
		return "link"
	case a.IsPrivate():
		return "unique_local"
	default:
		return "global"
	}
}

func strPtr(s string) *string { return &s }

// collectInterfaces reads every interface from the kernel and /sys.
func collectInterfaces(gateways []netcheck.Gateway) ([]Interface, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]Interface, 0, len(ifs))
	for _, in := range ifs {
		dir := filepath.Join(sysNet, in.Name)
		it := Interface{
			Name:     in.Name,
			MTU:      in.MTU,
			AdminUp:  in.Flags&net.FlagUp != 0,
			State:    "unknown",
			IPv4:     []IPv4Addr{},
			IPv6:     []IPv6Addr{},
			Gateways: []netcheck.Gateway{},
		}
		loopback := in.Flags&net.FlagLoopback != 0
		if len(in.HardwareAddr) > 0 && !loopback {
			it.MAC = strPtr(in.HardwareAddr.String())
		}
		if s, ok := readTrim(filepath.Join(dir, "operstate")); ok && s != "" {
			it.State = s
		}
		// Loopback and many tunnels report "unknown" although they work.
		if it.State == "unknown" && in.Flags&net.FlagRunning != 0 {
			it.State = "up"
		}
		devType := deviceType(in.Name)
		if devType != "" {
			it.DeviceType = strPtr(devType)
		}
		virtual := isVirtualDevice(in.Name)
		it.Kind = classify(in.Name, loopback, devType, virtual)

		// speed is -1 or unreadable for wifi, virtual and down interfaces.
		if s, ok := readTrim(filepath.Join(dir, "speed")); ok {
			if v, err := strconv.Atoi(s); err == nil && v > 0 {
				it.SpeedMbps = &v
			}
		}
		if s, ok := readTrim(filepath.Join(dir, "duplex")); ok && (s == "full" || s == "half") {
			it.Duplex = strPtr(s)
		}
		if s, ok := readTrim(filepath.Join(dir, "carrier")); ok && (s == "0" || s == "1") {
			c := s == "1"
			it.Carrier = &c
		}
		if p, err := os.Readlink(filepath.Join(dir, "device", "driver")); err == nil {
			it.Driver = strPtr(filepath.Base(filepath.ToSlash(p)))
		}
		if p, err := os.Readlink(filepath.Join(dir, "master")); err == nil {
			it.Master = strPtr(filepath.Base(filepath.ToSlash(p)))
		}
		// The kernel exposes how often the carrier changed but not when, so
		// the link uptime cannot be determined: LinkUpSecs stays null.
		it.Stats = readStats(in.Name)

		addrs, _ := in.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			addr, ok := netip.AddrFromSlice(ipn.IP)
			if !ok {
				continue
			}
			addr = addr.Unmap()
			ones, _ := ipn.Mask.Size()
			it.prefixes = append(it.prefixes, netip.PrefixFrom(addr, ones))
			if addr.Is4() {
				it.IPv4 = append(it.IPv4, IPv4Addr{Address: addr.String(), Prefix: ones})
			} else {
				it.IPv6 = append(it.IPv6, IPv6Addr{Address: addr.String(), Prefix: ones, Scope: ipv6Scope(addr)})
			}
		}
		for _, g := range gateways {
			if g.Interface == in.Name {
				it.DefaultRoute = true
				it.Gateways = append(it.Gateways, g)
			}
		}

		it.Group = groupOf(in.Name, it.Kind, loopback, it.DefaultRoute)
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Group == "primary") != (b.Group == "primary") {
			return a.Group == "primary"
		}
		if a.DefaultRoute != b.DefaultRoute {
			return a.DefaultRoute
		}
		return a.Name < b.Name
	})
	return out, nil
}

func collectGateways() []netcheck.Gateway {
	out := []netcheck.Gateway{}
	if b, err := os.ReadFile("/proc/net/route"); err == nil {
		out = append(out, netcheck.ParseIPv4Routes(string(b))...)
	}
	if b, err := os.ReadFile("/proc/net/ipv6_route"); err == nil {
		out = append(out, netcheck.ParseIPv6Routes(string(b))...)
	}
	return out
}

// DNS is the resolver configuration.
type DNS struct {
	Servers []string `json:"servers"`
	Search  []string `json:"search"`
	// Source is the file the values were read from; null when none was readable.
	Source          *string `json:"source"`
	SystemdResolved bool    `json:"systemd_resolved"`
}

const (
	resolvConf         = "/etc/resolv.conf"
	resolvedUpstream   = "/run/systemd/resolve/resolv.conf"
	resolvedRuntimeDir = "/run/systemd/resolve"
)

func collectDNS() DNS { return collectDNSAt("/") }

// collectDNSAt reads the resolver configuration below root ("/" on a real
// system).
func collectDNSAt(root string) DNS {
	resolvConf := filepath.Join(root, resolvConf)
	resolvedUpstream := filepath.Join(root, resolvedUpstream)
	d := DNS{Servers: []string{}, Search: []string{}}
	var etc netcheck.Resolv
	etcOK := false
	if b, err := os.ReadFile(resolvConf); err == nil {
		etc = netcheck.ParseResolvConf(string(b))
		etcOK = true
	}
	target, _ := os.Readlink(resolvConf)
	viaResolved := strings.Contains(target, "systemd/resolve") || (etcOK && etc.OnlyStub())
	if viaResolved {
		if b, err := os.ReadFile(resolvedUpstream); err == nil {
			r := netcheck.ParseResolvConf(string(b))
			d.Servers, d.Search = r.Servers, r.Search
			d.Source = strPtr(filepath.Join("/", strings.TrimPrefix(resolvedUpstream, root)))
			d.SystemdResolved = true
			return d
		}
	}
	if etcOK {
		d.Servers, d.Search = etc.Servers, etc.Search
		d.Source = strPtr(filepath.Join("/", strings.TrimPrefix(resolvConf, root)))
		d.SystemdResolved = viaResolved
	}
	return d
}

// Manager is the detected network configuration system.
type Manager struct {
	// Kind: netplan-networkd, netplan-networkmanager, networkmanager,
	// networkd, ifupdown, unknown.
	Kind     string   `json:"kind"`
	Label    string   `json:"label"`
	Evidence []string `json:"evidence"`
	// NetplanFiles are the file names found in /etc/netplan.
	NetplanFiles []string `json:"netplan_files"`
}

func globAny(patterns ...string) []string {
	out := []string{}
	for _, p := range patterns {
		m, _ := filepath.Glob(p)
		out = append(out, m...)
	}
	sort.Strings(out)
	return out
}

func detectManager() Manager { return detectManagerAt("/") }

// detectManagerAt inspects the configuration and runtime directories below
// root ("/" on a real system).
func detectManagerAt(root string) Manager {
	at := func(p string) string { return filepath.Join(root, p) }
	exists := func(p string) bool { return exists(at(p)) }
	globAny := func(patterns ...string) []string {
		for i := range patterns {
			patterns[i] = at(patterns[i])
		}
		return globAny(patterns...)
	}
	m := Manager{Kind: "unknown", Label: "Belirlenemedi", Evidence: []string{}, NetplanFiles: []string{}}
	netplan := globAny("/etc/netplan/*.yaml", "/etc/netplan/*.yml")
	for _, f := range netplan {
		m.NetplanFiles = append(m.NetplanFiles, filepath.Base(f))
	}
	nmRunning := exists("/run/NetworkManager")
	networkdRunning := exists("/run/systemd/netif/state") || exists("/run/systemd/netif/links")
	netplanNetworkd := globAny("/run/systemd/network/*netplan*")
	netplanNM := globAny("/run/NetworkManager/system-connections/netplan-*")
	networkdFiles := globAny("/etc/systemd/network/*.network")
	nmFiles := globAny("/etc/NetworkManager/system-connections/*")

	ifupdown := false
	if b, err := os.ReadFile(at("/etc/network/interfaces")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) >= 2 && f[0] == "iface" && f[1] != "lo" {
				ifupdown = true
			}
			if len(f) >= 1 && (f[0] == "source" || f[0] == "source-directory") {
				if len(globAny("/etc/network/interfaces.d/*")) > 0 {
					ifupdown = true
				}
			}
		}
	}

	if nmRunning {
		m.Evidence = append(m.Evidence, "NetworkManager çalışıyor (/run/NetworkManager mevcut)")
	}
	if networkdRunning {
		m.Evidence = append(m.Evidence, "systemd-networkd çalışıyor (/run/systemd/netif mevcut)")
	}

	switch {
	case len(netplan) > 0:
		m.Evidence = append(m.Evidence, "/etc/netplan içinde "+strconv.Itoa(len(netplan))+" yapılandırma dosyası var")
		switch {
		case len(netplanNM) > 0 || (nmRunning && len(netplanNetworkd) == 0 && !networkdRunning):
			m.Kind, m.Label = "netplan-networkmanager", "Netplan (NetworkManager)"
			if len(netplanNM) > 0 {
				m.Evidence = append(m.Evidence, "Netplan NetworkManager bağlantıları üretmiş (/run/NetworkManager/system-connections/netplan-*)")
			}
		default:
			m.Kind, m.Label = "netplan-networkd", "Netplan (systemd-networkd)"
			if len(netplanNetworkd) > 0 {
				m.Evidence = append(m.Evidence, "Netplan systemd-networkd dosyaları üretmiş (/run/systemd/network/*netplan*)")
			} else {
				m.Evidence = append(m.Evidence, "Netplan'ın varsayılan altyapısı systemd-networkd kabul edildi")
			}
		}
	case nmRunning:
		m.Kind, m.Label = "networkmanager", "NetworkManager"
		if len(nmFiles) > 0 {
			m.Evidence = append(m.Evidence, "/etc/NetworkManager/system-connections içinde bağlantı tanımları var")
		}
	case networkdRunning && len(networkdFiles) > 0:
		m.Kind, m.Label = "networkd", "systemd-networkd"
		m.Evidence = append(m.Evidence, "/etc/systemd/network içinde .network dosyaları var")
	case ifupdown:
		m.Kind, m.Label = "ifupdown", "ifupdown (/etc/network/interfaces)"
		m.Evidence = append(m.Evidence, "/etc/network/interfaces içinde arayüz tanımları var")
		if exists("/run/network/ifstate") {
			m.Evidence = append(m.Evidence, "ifupdown durum dosyası mevcut (/run/network/ifstate)")
		}
	case networkdRunning:
		m.Kind, m.Label = "networkd", "systemd-networkd"
	}
	return m
}

func collectListeners() []netcheck.Listener {
	out := []netcheck.Listener{}
	sources := []struct {
		path, proto string
		v6          bool
	}{
		{"/proc/net/tcp", "tcp", false},
		{"/proc/net/tcp6", "tcp", true},
		{"/proc/net/udp", "udp", false},
		{"/proc/net/udp6", "udp", true},
	}
	for _, s := range sources {
		b, err := os.ReadFile(s.path)
		if err != nil {
			continue
		}
		out = append(out, netcheck.ParseSockets(string(b), s.proto, s.v6)...)
	}
	names := map[int]*string{}
	for i := range out {
		uid := out[i].UID
		if uid < 0 {
			continue
		}
		n, done := names[uid]
		if !done {
			if u, err := user.LookupId(strconv.Itoa(uid)); err == nil && u.Username != "" {
				n = strPtr(u.Username)
			}
			names[uid] = n
		}
		out[i].User = n
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Port != b.Port {
			return a.Port < b.Port
		}
		if a.Protocol != b.Protocol {
			return a.Protocol < b.Protocol
		}
		return a.Address < b.Address
	})
	return out
}

// Subnet is a private network the server is attached to.
type Subnet struct {
	CIDR      string `json:"cidr"`
	Interface string `json:"interface"`
	Family    string `json:"family"`
	// Wide is the whole private block containing CIDR (e.g. 192.168.0.0/16).
	Wide string `json:"wide"`
}

func subnetsOf(ifs []Interface) []Subnet {
	out := []Subnet{}
	seen := map[string]bool{}
	for _, it := range ifs {
		if it.Group != "primary" {
			continue
		}
		for _, p := range it.prefixes {
			s, ok := netcheck.PrivateSubnet(p)
			if !ok || seen[s.String()] {
				continue
			}
			seen[s.String()] = true
			sn := Subnet{CIDR: s.String(), Interface: it.Name, Family: "ipv4"}
			if s.Addr().Is6() {
				sn.Family = "ipv6"
			}
			if w, ok := netcheck.WideBlock(s); ok {
				sn.Wide = w.String()
			}
			out = append(out, sn)
		}
	}
	// IPv4 networks of the default-route interface come first.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Family != out[j].Family {
			return out[i].Family == "ipv4"
		}
		return false
	})
	return out
}

// PrivateSubnets returns the private networks (RFC 1918 and IPv6 unique
// local) the server is attached to through its real interfaces. Docker
// bridges, veth pairs, tunnels and loopback are ignored. The network of the
// interface holding the default route comes first.
func PrivateSubnets() ([]Subnet, error) {
	ifs, err := collectInterfaces(collectGateways())
	if err != nil {
		return nil, err
	}
	return subnetsOf(ifs), nil
}

// Listeners returns the sockets currently accepting traffic.
func Listeners() []netcheck.Listener { return collectListeners() }
