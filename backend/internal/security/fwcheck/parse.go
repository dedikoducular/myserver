package fwcheck

import (
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// RawStatus is the output of the firewall-status helper action: ufw's own
// text, parsed by the panel.
type RawStatus struct {
	Installed bool `json:"installed"`
	// Numbered is `ufw status numbered`, Verbose is `ufw status verbose`,
	// Added is `ufw show added`.
	Numbered string `json:"numbered"`
	Verbose  string `json:"verbose"`
	Added    string `json:"added"`
	// DefaultConf is /etc/default/ufw and UfwConf is /etc/ufw/ufw.conf;
	// they give the policies while the firewall is inactive.
	DefaultConf string `json:"default_conf"`
	UfwConf     string `json:"ufw_conf"`
}

// Rule origins.
const (
	OriginNumbered = "numbered" // a row of `ufw status numbered` (firewall active)
	OriginAdded    = "added"    // a line of `ufw show added` (firewall inactive)
)

// Rule is one parsed firewall rule.
type Rule struct {
	// Number is the position in the list the rule came from.
	Number int `json:"number"`
	// ID identifies the exact rule text; deletions quote it so that a rule
	// whose number shifted is never removed by mistake.
	ID     string `json:"id"`
	Origin string `json:"origin"`
	// Action: allow, deny, reject, limit.
	Action string `json:"action"`
	// Direction: in, out, routed.
	Direction string `json:"direction"`
	// Port is empty when the rule applies to every port.
	Port string `json:"port"`
	// Protocol: tcp, udp, any, or another protocol name ufw printed.
	Protocol    string `json:"protocol"`
	Source      string `json:"source"`
	SourcePort  string `json:"source_port"`
	Destination string `json:"destination"`
	Interface   string `json:"interface"`
	// App is a ufw application profile name (e.g. "OpenSSH").
	App     string `json:"app"`
	Comment string `json:"comment"`
	// IPv6 is true for an IPv6 rule. Rules read from `ufw show added`
	// without an address apply to both families and report false.
	IPv6 bool   `json:"ipv6"`
	Raw  string `json:"raw"`
	// Protects is set by the panel: "ssh" or "panel" when the rule keeps
	// that access working, otherwise empty.
	Protects string `json:"protects"`
}

var (
	numberedRe = regexp.MustCompile(`^\[\s*(\d+)\]\s+(.+?)\s+(ALLOW|DENY|REJECT|LIMIT)(?:\s+(IN|OUT|FWD))?(?:\s+(.*))?$`)
	portTokRe  = regexp.MustCompile(`^(\d+(?::\d+)?(?:,\d+(?::\d+)?)*)(?:/([a-z0-9]+))?$`)
	protoRe    = regexp.MustCompile(`^[a-z0-9]{2,10}$`)
	ifaceRe    = regexp.MustCompile(`^[A-Za-z0-9_.:@\-]{1,15}$`)
	spaceRe    = regexp.MustCompile(`\s+`)
)

func collapse(s string) string {
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

func parseAddr(tok string) (string, bool, bool) {
	if p, err := netip.ParsePrefix(tok); err == nil {
		return p.String(), p.Addr().Is6(), true
	}
	if a, err := netip.ParseAddr(tok); err == nil {
		return a.String(), a.Is6(), true
	}
	return "", false, false
}

type endpoint struct {
	addr, port, proto, iface, app string
	v6                            bool
}

// parseEndpoint reads one column ("To" or "From") of ufw's status table.
func parseEndpoint(s string) endpoint {
	e := endpoint{addr: "any"}
	if strings.Contains(s, "(v6)") {
		e.v6 = true
		s = strings.ReplaceAll(s, "(v6)", " ")
	}
	for _, mark := range []string{"(log-all)", "(log)", "(out)"} {
		s = strings.ReplaceAll(s, mark, " ")
	}
	toks := strings.Fields(s)
	app := []string{}
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t == "on" && i+1 < len(toks) && ifaceRe.MatchString(toks[i+1]) {
			e.iface = toks[i+1]
			i++
			continue
		}
		if t == "Anywhere" {
			continue
		}
		if a, v6, ok := parseAddr(t); ok {
			e.addr = a
			e.v6 = e.v6 || v6
			continue
		}
		if m := portTokRe.FindStringSubmatch(t); m != nil {
			e.port = m[1]
			if m[2] != "" {
				e.proto = m[2]
			}
			continue
		}
		// "192.168.1.1/esp" or "Anywhere/udp": an address with a protocol.
		if left, right, ok := strings.Cut(t, "/"); ok && protoRe.MatchString(right) {
			if left == "Anywhere" {
				e.proto = right
				continue
			}
			if a, v6, ok := parseAddr(left); ok {
				e.addr, e.proto = a, right
				e.v6 = e.v6 || v6
				continue
			}
		}
		app = append(app, t)
	}
	e.app = strings.Join(app, " ")
	return e
}

// ParseNumbered parses `ufw status numbered`. Lines it does not understand
// are skipped rather than guessed.
func ParseNumbered(text string) []Rule {
	out := []Rule{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, " \t\r")
		m := numberedRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil || n < 1 {
			continue
		}
		from, comment := m[5], ""
		if i := strings.IndexByte(from, '#'); i >= 0 {
			comment = strings.TrimSpace(from[i+1:])
			from = from[:i]
		}
		to := parseEndpoint(m[2])
		src := parseEndpoint(from)
		r := Rule{
			Number:      n,
			Origin:      OriginNumbered,
			Action:      strings.ToLower(m[3]),
			Direction:   "in",
			Port:        to.port,
			Protocol:    "any",
			Source:      src.addr,
			SourcePort:  src.port,
			Destination: to.addr,
			Interface:   to.iface,
			App:         to.app,
			Comment:     comment,
			IPv6:        to.v6 || src.v6,
		}
		switch m[4] {
		case "OUT":
			r.Direction = "out"
		case "FWD":
			r.Direction = "routed"
		}
		if r.Interface == "" {
			r.Interface = src.iface
		}
		if r.App == "" {
			r.App = src.app
		}
		if to.proto != "" {
			r.Protocol = to.proto
		} else if src.proto != "" {
			r.Protocol = src.proto
		}
		r.Raw = collapse(m[2] + " " + m[3] + " " + m[4] + " " + m[5])
		r.ID = r.Raw
		out = append(out, r)
	}
	return out
}

// tokenize splits a ufw command line, honouring single and double quotes.
func tokenize(line string) []string {
	var toks []string
	var cur strings.Builder
	has := false
	var quote rune
	for _, c := range line {
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteRune(c)
			}
		case c == '\'' || c == '"':
			quote = c
			has = true
		case c == ' ' || c == '\t':
			if has {
				toks = append(toks, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(c)
			has = true
		}
	}
	if has {
		toks = append(toks, cur.String())
	}
	return toks
}

// parseCommand parses one "ufw allow ..." line of `ufw show added`.
func parseCommand(line string) (Rule, bool) {
	toks := tokenize(line)
	if len(toks) < 3 || toks[0] != "ufw" {
		return Rule{}, false
	}
	r := Rule{Origin: OriginAdded, Direction: "in", Protocol: "any", Source: "any", Destination: "any"}
	i := 1
	if toks[i] == "route" {
		r.Direction = "routed"
		i++
	}
	if i >= len(toks) || !ValidAction(toks[i]) {
		return Rule{}, false
	}
	r.Action = toks[i]
	i++
	for i < len(toks) {
		t := toks[i]
		if t == "in" || t == "out" {
			if r.Direction != "routed" {
				r.Direction = t
			}
			i++
			continue
		}
		if t == "on" && i+1 < len(toks) {
			if r.Interface == "" {
				r.Interface = toks[i+1]
			}
			i += 2
			continue
		}
		if t == "log" || t == "log-all" {
			i++
			continue
		}
		break
	}
	if i >= len(toks) {
		return Rule{}, false
	}
	isKey := func(s string) bool {
		switch s {
		case "from", "to", "proto", "port", "app", "comment":
			return true
		}
		return false
	}
	if !isKey(toks[i]) {
		// Simple form: PORT[/proto] or an application profile name.
		if m := portTokRe.FindStringSubmatch(toks[i]); m != nil {
			r.Port = m[1]
			if m[2] != "" {
				r.Protocol = m[2]
			}
		} else {
			r.App = toks[i]
		}
		i++
	}
	side := "to"
	for i+1 < len(toks) {
		k, v := toks[i], toks[i+1]
		i += 2
		switch k {
		case "proto":
			r.Protocol = v
		case "from", "to":
			side = k
			addr := "any"
			if v != "any" {
				a, v6, ok := parseAddr(v)
				if !ok {
					return Rule{}, false
				}
				addr = a
				r.IPv6 = r.IPv6 || v6
			}
			if k == "from" {
				r.Source = addr
			} else {
				r.Destination = addr
			}
		case "port":
			if side == "from" {
				r.SourcePort = v
			} else {
				r.Port = v
			}
		case "app":
			if side == "to" || r.App == "" {
				r.App = v
			}
		case "comment":
			r.Comment = v
		default:
			return Rule{}, false
		}
	}
	r.Raw = collapse(line)
	r.ID = r.Raw
	return r, true
}

// ParseAdded parses `ufw show added`. Rule numbers are positions in that
// list (1-based).
func ParseAdded(text string) []Rule {
	out := []Rule{}
	n := 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ufw ") {
			continue
		}
		n++
		r, ok := parseCommand(line)
		if !ok {
			continue
		}
		r.Number = n
		out = append(out, r)
	}
	return out
}

// DeleteArgs returns the ufw arguments deleting a rule listed by
// `ufw show added`, or false when a token is outside the safe character set.
func DeleteArgs(line string) ([]string, bool) {
	toks := tokenize(line)
	if len(toks) < 3 || toks[0] != "ufw" {
		return nil, false
	}
	rest := toks[1:]
	// The comment is not part of a rule's identity.
	for i, t := range rest {
		if t == "comment" {
			rest = rest[:i]
			break
		}
	}
	safe := regexp.MustCompile(`^[A-Za-z0-9_.:,/@ \-]{1,64}$`)
	for _, t := range rest {
		if !safe.MatchString(t) || strings.HasPrefix(t, "-") {
			return nil, false
		}
	}
	if len(rest) < 2 {
		return nil, false
	}
	if rest[0] == "route" {
		if !ValidAction(rest[1]) {
			return nil, false
		}
		return append([]string{"route", "delete"}, rest[1:]...), true
	}
	if !ValidAction(rest[0]) {
		return nil, false
	}
	return append([]string{"delete"}, rest...), true
}

// Policies are the default policies and logging level. A nil field means
// the value could not be determined.
type Policies struct {
	Incoming *string `json:"incoming"`
	Outgoing *string `json:"outgoing"`
	Routed   *string `json:"routed"`
	Logging  *string `json:"logging"`
}

var (
	statusRe  = regexp.MustCompile(`(?m)^Status:\s*(\S+)`)
	loggingRe = regexp.MustCompile(`(?m)^Logging:\s*(\S+)(?:\s*\(([a-z]+)\))?`)
	defaultRe = regexp.MustCompile(`(?m)^Default:\s*(.+)$`)
	policyRe  = regexp.MustCompile(`([a-z]+)\s*\((incoming|outgoing|routed)\)`)
)

// ParseActive reads the "Status:" line. ok is false when there is none.
func ParseActive(text string) (active, ok bool) {
	m := statusRe.FindStringSubmatch(text)
	if m == nil {
		return false, false
	}
	switch m[1] {
	case "active":
		return true, true
	case "inactive":
		return false, true
	}
	return false, false
}

func sp(s string) *string { return &s }

func validPolicy(s string) bool {
	switch s {
	case "allow", "deny", "reject", "disabled":
		return true
	}
	return false
}

// ParseVerbose reads policies and logging from `ufw status verbose`.
func ParseVerbose(text string) Policies {
	var p Policies
	if m := loggingRe.FindStringSubmatch(text); m != nil {
		switch {
		case m[1] == "off":
			p.Logging = sp("off")
		case m[1] == "on" && m[2] != "":
			p.Logging = sp(m[2])
		case m[1] == "on":
			p.Logging = sp("on")
		}
	}
	if m := defaultRe.FindStringSubmatch(text); m != nil {
		for _, pm := range policyRe.FindAllStringSubmatch(m[1], -1) {
			if !validPolicy(pm[1]) {
				continue
			}
			switch pm[2] {
			case "incoming":
				p.Incoming = sp(pm[1])
			case "outgoing":
				p.Outgoing = sp(pm[1])
			case "routed":
				p.Routed = sp(pm[1])
			}
		}
	}
	return p
}

func confValue(text, key string) (string, bool) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		return strings.ToLower(strings.Trim(strings.TrimSpace(v), `"'`)), true
	}
	return "", false
}

func iptablesPolicy(s string) *string {
	switch s {
	case "drop":
		return sp("deny")
	case "accept":
		return sp("allow")
	case "reject":
		return sp("reject")
	}
	return nil
}

// ParseConfig reads the configured policies from /etc/default/ufw and the
// logging level from /etc/ufw/ufw.conf, for use while ufw is inactive.
func ParseConfig(defaultConf, ufwConf string) Policies {
	var p Policies
	if v, ok := confValue(defaultConf, "DEFAULT_INPUT_POLICY"); ok {
		p.Incoming = iptablesPolicy(v)
	}
	if v, ok := confValue(defaultConf, "DEFAULT_OUTPUT_POLICY"); ok {
		p.Outgoing = iptablesPolicy(v)
	}
	if v, ok := confValue(defaultConf, "DEFAULT_FORWARD_POLICY"); ok {
		p.Routed = iptablesPolicy(v)
	}
	if v, ok := confValue(ufwConf, "LOGLEVEL"); ok && protoRe.MatchString(v) {
		p.Logging = sp(v)
	}
	return p
}

// Rules returns the rule list for a status: the numbered table while the
// firewall is active, the configured rules otherwise (ufw prints no table
// when it is inactive).
func (s RawStatus) Rules() (rules []Rule, active bool) {
	active, _ = ParseActive(s.Verbose)
	if !active {
		if a, ok := ParseActive(s.Numbered); ok {
			active = a
		}
	}
	if active {
		return ParseNumbered(s.Numbered), true
	}
	return ParseAdded(s.Added), false
}

// appPorts maps the ufw application profiles the panel understands.
var appPorts = map[string]struct {
	port  int
	proto string
}{
	"OpenSSH": {22, "tcp"},
}

// Covers reports whether the rule lets traffic to the given port through.
// client, when valid, must be inside the rule's source: a rule limited to
// another network does not keep the current connection working.
func Covers(r Rule, port int, protocol string, client netip.Addr) bool {
	if r.Direction != "in" || (r.Action != "allow" && r.Action != "limit") {
		return false
	}
	if r.Destination != "any" && r.Destination != "" {
		// Bound to one local address; too specific to rely on.
		return false
	}
	if r.SourcePort != "" {
		return false
	}
	if r.App != "" {
		a, ok := appPorts[r.App]
		if !ok || a.port != port || a.proto != protocol {
			return false
		}
	} else {
		if r.Protocol != "any" && r.Protocol != protocol {
			return false
		}
		if r.Port != "" {
			hit := false
			for _, pr := range ParsePortList(r.Port) {
				if pr.Contains(port) {
					hit = true
				}
			}
			if !hit {
				return false
			}
		}
	}
	if !client.IsValid() {
		return true
	}
	client = client.Unmap()
	if r.Source == "any" || r.Source == "" {
		if r.Origin == OriginNumbered {
			return r.IPv6 == client.Is6()
		}
		return true
	}
	if p, err := netip.ParsePrefix(r.Source); err == nil {
		return p.Contains(client)
	}
	if a, err := netip.ParseAddr(r.Source); err == nil {
		return a == client
	}
	return false
}

// Denies reports whether the rule refuses traffic to the given port. When
// client is valid the rule must apply to that address. Without a client, a
// rule restricted to some source is not counted: it refuses that source,
// not the access in general. A rule bound to one local address is counted,
// because the address the client connects to is unknown.
func Denies(r Rule, port int, protocol string, client netip.Addr) bool {
	if r.Direction != "in" || (r.Action != "deny" && r.Action != "reject") {
		return false
	}
	if r.SourcePort != "" {
		return false
	}
	if r.App != "" {
		a, ok := appPorts[r.App]
		if !ok || a.port != port || a.proto != protocol {
			return false
		}
	} else {
		if r.Protocol != "any" && r.Protocol != protocol {
			return false
		}
		if r.Port != "" {
			hit := false
			for _, pr := range ParsePortList(r.Port) {
				if pr.Contains(port) {
					hit = true
				}
			}
			if !hit {
				return false
			}
		}
	}
	if r.Source == "any" || r.Source == "" {
		if client.IsValid() && r.Origin == OriginNumbered {
			return r.IPv6 == client.Unmap().Is6()
		}
		return true
	}
	if !client.IsValid() {
		return false
	}
	client = client.Unmap()
	if p, err := netip.ParsePrefix(r.Source); err == nil {
		return p.Contains(client)
	}
	if a, err := netip.ParseAddr(r.Source); err == nil {
		return a == client
	}
	// A source that cannot be read might be the client.
	return true
}

// families returns which address families (IPv4, IPv6) a rule applies to.
func families(r Rule, client netip.Addr) (v4, v6 bool) {
	if client.IsValid() {
		is6 := client.Unmap().Is6()
		return !is6, is6
	}
	if r.Source != "any" && r.Source != "" {
		if p, err := netip.ParsePrefix(r.Source); err == nil {
			return !p.Addr().Is6(), p.Addr().Is6()
		}
		if a, err := netip.ParseAddr(r.Source); err == nil {
			return !a.Is6(), a.Is6()
		}
	}
	if r.Origin == OriginNumbered {
		return !r.IPv6, r.IPv6
	}
	return true, true
}

// Reachable reports whether traffic to the port gets through the rule list.
// The rules are evaluated in order and the first match wins, as in
// ufw/iptables: an allow rule only counts when no earlier rule denies or
// rejects the same traffic. defaultAllow is the default incoming policy,
// applied when no rule matches. client is the address the traffic comes
// from; when it is not valid the question is whether the port is reachable
// at all, and each address family is followed separately.
func Reachable(rules []Rule, port int, protocol string, client netip.Addr, defaultAllow bool) bool {
	denied4, denied6 := false, false
	for _, r := range rules {
		if Denies(r, port, protocol, client) {
			v4, v6 := families(r, client)
			denied4 = denied4 || v4
			denied6 = denied6 || v6
			continue
		}
		if Covers(r, port, protocol, client) {
			v4, v6 := families(r, client)
			if (v4 && !denied4) || (v6 && !denied6) {
				return true
			}
		}
	}
	if !defaultAllow {
		return false
	}
	if client.IsValid() {
		if client.Unmap().Is6() {
			return !denied6
		}
		return !denied4
	}
	return !denied4 || !denied6
}

// Blocks reports whether a new deny/reject rule would cut traffic to port.
func (s RuleSpec) Blocks(port int, protocol string) bool {
	if s.Action != "deny" && s.Action != "reject" {
		return false
	}
	if s.Protocol != "any" && s.Protocol != protocol {
		return false
	}
	for _, pr := range ParsePortList(s.Port) {
		if pr.Contains(port) {
			return true
		}
	}
	return false
}
