package netcheck

import "net/netip"

var rfc1918 = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
}

// PrivateSubnet returns the network a private interface address belongs to.
// It reports false for public, loopback and link-local addresses and for
// host routes (/32, /128).
func PrivateSubnet(p netip.Prefix) (netip.Prefix, bool) {
	if !p.IsValid() {
		return netip.Prefix{}, false
	}
	a := p.Addr().Unmap()
	if !a.IsPrivate() || p.Bits() >= a.BitLen() || p.Bits() <= 0 {
		return netip.Prefix{}, false
	}
	q, err := a.Prefix(p.Bits())
	if err != nil {
		return netip.Prefix{}, false
	}
	return q, true
}

// WideBlock returns the whole private block containing p (192.168.0.0/16,
// 172.16.0.0/12, 10.0.0.0/8, or fc00::/7 for IPv6 unique local addresses).
func WideBlock(p netip.Prefix) (netip.Prefix, bool) {
	a := p.Addr().Unmap()
	if a.Is4() {
		for _, b := range rfc1918 {
			if b.Contains(a) {
				return b, true
			}
		}
		return netip.Prefix{}, false
	}
	ula := netip.MustParsePrefix("fc00::/7")
	if ula.Contains(a) {
		return ula, true
	}
	return netip.Prefix{}, false
}
