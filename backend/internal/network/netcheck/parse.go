// Package netcheck holds the portable, side-effect-free parts of the network
// module: parsers for /proc and configuration files, subnet maths and the
// netplan secret redaction. It imports nothing from the panel so the root
// helper can use it too.
package netcheck

import (
	"encoding/hex"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

// Gateway is one default route.
type Gateway struct {
	Family string `json:"family"` // "ipv4" | "ipv6"
	// Address is empty for a default route without a next hop (point-to-point links).
	Address   string `json:"address"`
	Interface string `json:"interface"`
	Metric    int    `json:"metric"`
}

const (
	rtfUp      = 0x0001
	rtfGateway = 0x0002
	rtfReject  = 0x0200
)

// hexIPv4Host decodes an IPv4 address printed by the kernel as a host-order
// 32-bit hex number (little-endian machines: amd64, arm64).
func hexIPv4Host(s string) (netip.Addr, bool) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 4 {
		return netip.Addr{}, false
	}
	return netip.AddrFrom4([4]byte{b[3], b[2], b[1], b[0]}), true
}

// ParseIPv4Routes extracts default routes from /proc/net/route.
func ParseIPv4Routes(text string) []Gateway {
	out := []Gateway{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 8 || f[0] == "Iface" {
			continue
		}
		if f[1] != "00000000" || f[7] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(f[3], 16, 32)
		if err != nil || flags&rtfUp == 0 || flags&rtfReject != 0 {
			continue
		}
		g := Gateway{Family: "ipv4", Interface: f[0]}
		if m, err := strconv.Atoi(f[6]); err == nil {
			g.Metric = m
		}
		if flags&rtfGateway != 0 {
			if a, ok := hexIPv4Host(f[2]); ok {
				g.Address = a.String()
			}
		}
		out = append(out, g)
	}
	sortGateways(out)
	return out
}

// ParseIPv6Routes extracts default routes from /proc/net/ipv6_route.
func ParseIPv6Routes(text string) []Gateway {
	out := []Gateway{}
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		if strings.Trim(f[0], "0") != "" || f[1] != "00" {
			continue
		}
		flags, err := strconv.ParseUint(f[8], 16, 32)
		if err != nil || flags&rtfUp == 0 || flags&rtfReject != 0 {
			continue
		}
		iface := f[9]
		if iface == "lo" {
			continue
		}
		g := Gateway{Family: "ipv6", Interface: iface}
		if m, err := strconv.ParseUint(f[5], 16, 32); err == nil {
			g.Metric = int(m)
		}
		if flags&rtfGateway != 0 {
			if b, err := hex.DecodeString(f[4]); err == nil && len(b) == 16 {
				var a [16]byte
				copy(a[:], b)
				g.Address = netip.AddrFrom16(a).String()
			}
		}
		key := g.Interface + "|" + g.Address
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, g)
	}
	sortGateways(out)
	return out
}

func sortGateways(g []Gateway) {
	sort.SliceStable(g, func(i, j int) bool { return g[i].Metric < g[j].Metric })
}

// Resolv is the content of a resolv.conf file.
type Resolv struct {
	Servers []string
	Search  []string
}

// ParseResolvConf reads nameserver, search and domain lines.
func ParseResolvConf(text string) Resolv {
	r := Resolv{Servers: []string{}, Search: []string{}}
	seenS := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		if i := strings.IndexAny(line, "#;"); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "nameserver":
			addr := f[1]
			if i := strings.IndexByte(addr, '%'); i >= 0 {
				// Keep the zone for display but validate the address part.
				if _, err := netip.ParseAddr(addr[:i]); err != nil {
					continue
				}
			} else if _, err := netip.ParseAddr(addr); err != nil {
				continue
			}
			if !seenS[addr] {
				seenS[addr] = true
				r.Servers = append(r.Servers, addr)
			}
		case "search", "domain":
			// A later search/domain line replaces an earlier one.
			r.Search = r.Search[:0]
			for _, d := range f[1:] {
				if d != "." && len(d) <= 253 {
					r.Search = append(r.Search, d)
				}
			}
		}
	}
	return r
}

// OnlyStub reports whether every nameserver is the systemd-resolved stub.
func (r Resolv) OnlyStub() bool {
	if len(r.Servers) == 0 {
		return false
	}
	for _, s := range r.Servers {
		if s != "127.0.0.53" && s != "127.0.0.54" {
			return false
		}
	}
	return true
}

// Listener is a socket that accepts traffic.
type Listener struct {
	Protocol string `json:"protocol"` // "tcp" | "udp"
	Family   string `json:"family"`   // "ipv4" | "ipv6"
	Address  string `json:"address"`
	Port     int    `json:"port"`
	// Scope: "all" (wildcard bind), "loopback" or "specific".
	Scope string `json:"scope"`
	UID   int    `json:"uid"`
	// User is the owner's account name; Process is never guessed.
	User    *string `json:"user"`
	Process *string `json:"process"`
}

func decodeSocketAddr(s string, v6 bool) (netip.Addr, int, bool) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return netip.Addr{}, 0, false
	}
	port, err := strconv.ParseUint(s[i+1:], 16, 16)
	if err != nil {
		return netip.Addr{}, 0, false
	}
	h := s[:i]
	if !v6 {
		a, ok := hexIPv4Host(h)
		return a, int(port), ok
	}
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != 16 {
		return netip.Addr{}, 0, false
	}
	var a [16]byte
	for w := 0; w < 4; w++ {
		a[w*4+0] = b[w*4+3]
		a[w*4+1] = b[w*4+2]
		a[w*4+2] = b[w*4+1]
		a[w*4+3] = b[w*4+0]
	}
	return netip.AddrFrom16(a), int(port), true
}

// ParseSockets reads /proc/net/{tcp,tcp6,udp,udp6}. For TCP only LISTEN
// sockets are returned; for UDP, bound sockets without a connected peer.
func ParseSockets(text, protocol string, v6 bool) []Listener {
	out := []Listener{}
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 8 || !strings.HasSuffix(f[0], ":") {
			continue
		}
		addr, port, ok := decodeSocketAddr(f[1], v6)
		if !ok || port == 0 {
			continue
		}
		if protocol == "tcp" {
			if f[3] != "0A" {
				continue
			}
		} else {
			raddr, rport, ok := decodeSocketAddr(f[2], v6)
			if !ok || rport != 0 || !raddr.IsUnspecified() {
				continue
			}
		}
		l := Listener{Protocol: protocol, Family: "ipv4", Address: addr.String(), Port: port, UID: -1}
		if v6 {
			l.Family = "ipv6"
		}
		switch {
		case addr.IsUnspecified():
			l.Scope = "all"
		case addr.IsLoopback() || (addr.Is4In6() && addr.Unmap().IsLoopback()):
			l.Scope = "loopback"
		default:
			l.Scope = "specific"
		}
		if uid, err := strconv.Atoi(f[7]); err == nil {
			l.UID = uid
		}
		key := l.Protocol + "|" + l.Address + "|" + strconv.Itoa(l.Port)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, l)
	}
	return out
}

// ParseSSHDPorts returns the ports named by "Port" lines of an sshd_config
// file, outside Match blocks.
func ParseSSHDPorts(text string) []int {
	out := []int{}
	for _, line := range strings.Split(text, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.ReplaceAll(line, "=", " ")
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if strings.EqualFold(f[0], "Match") {
			break
		}
		if len(f) >= 2 && strings.EqualFold(f[0], "Port") {
			if p, err := strconv.Atoi(strings.Trim(f[1], `"`)); err == nil && p >= 1 && p <= 65535 {
				out = append(out, p)
			}
		}
	}
	return out
}
