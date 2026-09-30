// Package fwcheck holds the portable firewall logic shared by the panel and
// the root helper: validation of rule fields, construction of the ufw
// argument vector and parsers for ufw's output. It runs nothing itself.
package fwcheck

import (
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// UfwPath is the only firewall binary ever executed.
const UfwPath = "/usr/sbin/ufw"

// Error is a validation failure with a Turkish, user-presentable message.
type Error string

func (e Error) Error() string { return string(e) }

// RuleSpec is a rule the panel can create.
type RuleSpec struct {
	// Action: allow, deny, reject, limit.
	Action string `json:"action"`
	// Port: a single port ("445") or a range ("5000:5010").
	Port string `json:"port"`
	// Protocol: tcp, udp, any.
	Protocol string `json:"protocol"`
	// Source: "any" or an IP address / CIDR network.
	Source string `json:"source"`
	// Comment: optional; ASCII letters, digits, space and _.,:()/+- only.
	Comment string `json:"comment"`
}

var commentRe = regexp.MustCompile(`^[A-Za-z0-9 _.,:()/+\-]{0,64}$`)

// ValidAction reports whether s is a rule action.
func ValidAction(s string) bool {
	switch s {
	case "allow", "deny", "reject", "limit":
		return true
	}
	return false
}

// ValidProtocol reports whether s is a protocol the panel accepts.
func ValidProtocol(s string) bool {
	return s == "tcp" || s == "udp" || s == "any"
}

func parsePortNumber(s string) (int, bool) {
	if s == "" || len(s) > 5 {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, false
	}
	return n, true
}

// NormalizePort validates a single port or a range and returns it in ufw's
// form ("445", "5000:5010"). "5000-5010" is accepted as a range too.
func NormalizePort(s string) (port string, isRange bool, err error) {
	s = strings.TrimSpace(s)
	sep := strings.IndexAny(s, ":-")
	if sep < 0 {
		n, ok := parsePortNumber(s)
		if !ok {
			return "", false, Error("Port 1 ile 65535 arasında bir sayı olmalıdır.")
		}
		return strconv.Itoa(n), false, nil
	}
	lo, ok1 := parsePortNumber(s[:sep])
	hi, ok2 := parsePortNumber(s[sep+1:])
	if !ok1 || !ok2 {
		return "", false, Error("Port aralığı 1 ile 65535 arasındaki iki sayıdan oluşmalıdır (ör. 5000:5010).")
	}
	if lo >= hi {
		return "", false, Error("Port aralığının başlangıcı bitişinden küçük olmalıdır.")
	}
	return strconv.Itoa(lo) + ":" + strconv.Itoa(hi), true, nil
}

// NormalizeSource validates a rule source and returns "any", a single
// address or a network in canonical form.
func NormalizeSource(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", Error("Kaynak adresi boş olamaz.")
	}
	if s == "any" {
		return "any", nil
	}
	if len(s) > 64 {
		return "", Error("Kaynak adresi geçersiz.")
	}
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil || p.Addr().Zone() != "" {
			return "", Error("Kaynak ağı geçersiz. Örnek: 192.168.1.0/24")
		}
		p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()).Masked()
		if !p.IsValid() {
			return "", Error("Kaynak ağı geçersiz. Örnek: 192.168.1.0/24")
		}
		if p.Bits() == 0 {
			return "", Error("Tüm adresleri kapsayan bir ağ yerine \"Her yerden\" seçeneğini kullanın.")
		}
		return p.String(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" {
		return "", Error("Kaynak adresi geçersiz. Örnek: 192.168.1.10 veya 192.168.1.0/24")
	}
	a = a.Unmap()
	if a.IsUnspecified() {
		return "", Error("Tüm adresler için \"Her yerden\" seçeneğini kullanın.")
	}
	return a.String(), nil
}

// ValidComment reports whether s may be stored as a rule comment.
func ValidComment(s string) bool {
	// A comment is passed to ufw as an argument of its own, so it must never
	// look like an option.
	return commentRe.MatchString(s) && strings.TrimSpace(s) == s && !strings.HasPrefix(s, "-")
}

// Normalize validates every field and returns the canonical rule.
func (s RuleSpec) Normalize() (RuleSpec, error) {
	out := RuleSpec{Action: s.Action, Protocol: s.Protocol, Comment: s.Comment}
	if !ValidAction(s.Action) {
		return out, Error("Kural eylemi geçersiz.")
	}
	if !ValidProtocol(s.Protocol) {
		return out, Error("Protokol geçersiz. TCP, UDP veya her ikisi seçilebilir.")
	}
	port, isRange, err := NormalizePort(s.Port)
	if err != nil {
		return out, err
	}
	if isRange && s.Protocol == "any" {
		return out, Error("Port aralığı için protokol (TCP veya UDP) seçilmelidir.")
	}
	out.Port = port
	if out.Source, err = NormalizeSource(s.Source); err != nil {
		return out, err
	}
	if !ValidComment(s.Comment) {
		return out, Error("Açıklama en fazla 64 karakter olabilir ve yalnızca İngilizce harf, rakam, boşluk ve _.,:()/+- içerebilir.")
	}
	return out, nil
}

// Args returns the ufw arguments that add the rule. The rule must have been
// normalized. Every value is a separate argument; no shell is involved.
func (s RuleSpec) Args() []string {
	args := []string{s.Action, "from", s.Source, "to", "any", "port", s.Port}
	if s.Protocol != "any" {
		args = append(args, "proto", s.Protocol)
	}
	if s.Comment != "" {
		args = append(args, "comment", s.Comment)
	}
	return args
}

// String describes the rule for audit records.
func (s RuleSpec) String() string {
	return s.Action + " " + s.Port + "/" + s.Protocol + " from " + s.Source
}

// LANRule builds an allow rule restricted to one network. It refuses a
// source of "any": use it for services that must only be reachable from the
// local network (SMB, NFS...).
func LANRule(subnet, port, protocol, comment string) (RuleSpec, error) {
	if strings.TrimSpace(subnet) == "any" || subnet == "" {
		return RuleSpec{}, Error("Yerel ağ kuralı için bir ağ adresi gereklidir.")
	}
	return RuleSpec{Action: "allow", Port: port, Protocol: protocol, Source: subnet, Comment: comment}.Normalize()
}

// PortRange is an inclusive range of ports.
type PortRange struct{ Lo, Hi int }

// Contains reports whether p lies inside r.
func (r PortRange) Contains(p int) bool { return p >= r.Lo && p <= r.Hi }

// ParsePortList reads ufw's port notation: "22", "80,443", "5000:5010" or a mix.
func ParsePortList(s string) []PortRange {
	out := []PortRange{}
	for _, part := range strings.Split(s, ",") {
		lo, hi, isRange := strings.Cut(part, ":")
		a, ok := parsePortNumber(lo)
		if !ok {
			continue
		}
		b := a
		if isRange {
			if b, ok = parsePortNumber(hi); !ok {
				continue
			}
		}
		out = append(out, PortRange{a, b})
	}
	return out
}
