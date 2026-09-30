package apps

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// Addresses a published port can be bound to.
const (
	AddrAny      = "0.0.0.0"
	AddrLoopback = "127.0.0.1"
	// addrIPv6 stands for any specific (non-wildcard) IPv6 address.
	addrIPv6 = "ipv6"
)

// parseProcAddr converts the hexadecimal local address of /proc/net/tcp and
// friends to a normalized form: AddrAny for the IPv4 or IPv6 wildcard, the
// dotted form for an IPv4 (or IPv4-mapped) address and addrIPv6 for any
// other IPv6 address. The kernel prints each 32-bit word in host byte order
// (little endian on the supported platforms).
func parseProcAddr(h string) (string, bool) {
	v4 := func(w string) (string, bool) {
		parts := make([]string, 0, 4)
		for i := 6; i >= 0; i -= 2 {
			n, err := strconv.ParseUint(w[i:i+2], 16, 8)
			if err != nil {
				return "", false
			}
			parts = append(parts, strconv.FormatUint(n, 10))
		}
		return strings.Join(parts, "."), true
	}
	switch len(h) {
	case 8:
		return v4(h)
	case 32:
		if strings.Trim(h, "0") == "" {
			return AddrAny, true
		}
		if h[:16] == "0000000000000000" && strings.EqualFold(h[16:24], "FFFF0000") {
			return v4(h[24:])
		}
		return addrIPv6, true
	}
	return "", false
}

// ListeningSockets parses the text of /proc/net/tcp, /proc/net/tcp6,
// /proc/net/udp or /proc/net/udp6 and returns, per occupied local port, the
// addresses it is bound on: listening sockets for TCP, bound sockets for
// UDP.
func ListeningSockets(text string, udp bool) map[int][]string {
	out := map[int][]string{}
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	first := true
	for sc.Scan() {
		if first { // header line
			first = false
			continue
		}
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		// f[1] = local address "0100007F:1F90", f[3] = state.
		i := strings.LastIndexByte(f[1], ':')
		if i < 0 {
			continue
		}
		port, err := strconv.ParseUint(f[1][i+1:], 16, 16)
		if err != nil || port == 0 {
			continue
		}
		if !udp && f[3] != "0A" { // TCP_LISTEN
			continue
		}
		addr, ok := parseProcAddr(f[1][:i])
		if !ok {
			continue
		}
		out[int(port)] = append(out[int(port)], addr)
	}
	return out
}

// ListeningPorts is ListeningSockets without the addresses.
func ListeningPorts(text string, udp bool) map[int]bool {
	out := map[int]bool{}
	for p := range ListeningSockets(text, udp) {
		out[p] = true
	}
	return out
}

// BindsOverlap reports whether binding a port on address ours fails because
// the same port is already bound on address theirs. A wildcard on either
// side overlaps with everything; two specific addresses overlap only when
// they are equal. An empty address means the wildcard.
func BindsOverlap(ours, theirs string) bool {
	norm := func(a string) string {
		if a == "" || a == "::" || a == AddrAny {
			return AddrAny
		}
		return a
	}
	ours, theirs = norm(ours), norm(theirs)
	return ours == AddrAny || theirs == AddrAny || ours == theirs
}

// hostListening reads the host's socket tables. determinable is false when
// they cannot be read (then only Docker's view is available).
func hostListening() (tcp, udp map[int][]string, determinable bool) {
	tcp, udp = map[int][]string{}, map[int][]string{}
	for _, src := range []struct {
		file string
		udp  bool
	}{
		{"/proc/net/tcp", false}, {"/proc/net/tcp6", false},
		{"/proc/net/udp", true}, {"/proc/net/udp6", true},
	} {
		data, err := os.ReadFile(src.file)
		if err != nil {
			continue
		}
		determinable = true
		dst := tcp
		if src.udp {
			dst = udp
		}
		for p, addrs := range ListeningSockets(string(data), src.udp) {
			dst[p] = append(dst[p], addrs...)
		}
	}
	return tcp, udp, determinable
}

// PortConflict describes one host port that cannot be used.
type PortConflict struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Label    string `json:"label"`
	// UsedBy is the name of the container holding the port, or "" when a
	// host service listens on it.
	UsedBy string `json:"used_by"`
}

func (c PortConflict) Message() string {
	id := strconv.Itoa(c.Port) + "/" + c.Protocol
	what := ""
	if c.Label != "" {
		what = " (" + c.Label + ")"
	}
	if c.UsedBy != "" {
		return id + " portu" + what + " \"" + c.UsedBy + "\" konteyneri tarafından kullanılıyor."
	}
	return id + " portu" + what + " sunucuda çalışan başka bir servis tarafından kullanılıyor."
}
