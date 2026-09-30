package security

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"myserver/internal/network"
	"myserver/internal/network/netcheck"
	"myserver/internal/privileged"
	"myserver/internal/security/fwcheck"
)

// InstallHint tells the administrator how UFW is installed.
const InstallHint = "sudo apt update && sudo apt install ufw"

// State is the firewall status returned by GET /firewall/status.
type State struct {
	Installed bool `json:"installed"`
	Active    bool `json:"active"`
	// Defaults holds the default policies and the logging level; while
	// the firewall is inactive they are the configured values.
	Defaults fwcheck.Policies `json:"defaults"`
	Rules    []fwcheck.Rule   `json:"rules"`
	// RulesSource: "numbered" (active firewall) or "added" (configured
	// rules of an inactive firewall).
	RulesSource string `json:"rules_source"`
	SSHPorts    []int  `json:"ssh_ports"`
	SSHEvidence string `json:"ssh_evidence"`
	// PanelPort is 0 when the listen address could not be parsed.
	PanelPort int `json:"panel_port"`
	// PanelPortRequired is false when the panel only listens on loopback
	// (behind a reverse proxy), where no firewall rule is needed for it.
	PanelPortRequired bool   `json:"panel_port_required"`
	InstallHint       string `json:"install_hint"`
	CheckedAt         int64  `json:"checked_at"`
}

// detectSSHPorts reads the ports sshd is configured to listen on.
func detectSSHPorts() ([]int, string) {
	files := []string{"/etc/ssh/sshd_config"}
	extra, _ := filepath.Glob("/etc/ssh/sshd_config.d/*.conf")
	sort.Strings(extra)
	files = append(files, extra...)
	seen := map[int]bool{}
	ports := []int{}
	readAny := false
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		readAny = true
		for _, p := range netcheck.ParseSSHDPorts(string(b)) {
			if !seen[p] {
				seen[p] = true
				ports = append(ports, p)
			}
		}
	}
	if len(ports) > 0 {
		sort.Ints(ports)
		return ports, "sshd yapılandırmasındaki Port satırlarından okundu"
	}
	if readAny {
		return []int{22}, "sshd yapılandırmasında Port satırı yok; varsayılan 22 kullanılıyor"
	}
	return []int{22}, "sshd yapılandırması okunamadı; varsayılan 22 kabul edildi"
}

// panelPort parses the panel's listen address.
func panelPort(listen string) (port int, required bool) {
	host, p, err := net.SplitHostPort(listen)
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return 0, false
	}
	if host == "localhost" {
		return n, false
	}
	if a, err := netip.ParseAddr(host); err == nil && a.IsLoopback() {
		return n, false
	}
	return n, true
}

func mergePolicies(primary, fallback fwcheck.Policies) fwcheck.Policies {
	if primary.Incoming == nil {
		primary.Incoming = fallback.Incoming
	}
	if primary.Outgoing == nil {
		primary.Outgoing = fallback.Outgoing
	}
	if primary.Routed == nil {
		primary.Routed = fallback.Routed
	}
	if primary.Logging == nil {
		primary.Logging = fallback.Logging
	}
	return primary
}

// buildState turns the helper's raw status into the API state.
func buildState(raw fwcheck.RawStatus, listen string) *State {
	st := &State{
		Installed:   raw.Installed,
		Rules:       []fwcheck.Rule{},
		RulesSource: fwcheck.OriginAdded,
		InstallHint: InstallHint,
		CheckedAt:   time.Now().Unix(),
	}
	st.SSHPorts, st.SSHEvidence = detectSSHPorts()
	st.PanelPort, st.PanelPortRequired = panelPort(listen)
	if !raw.Installed {
		return st
	}
	st.Rules, st.Active = raw.Rules()
	cfg := fwcheck.ParseConfig(raw.DefaultConf, raw.UfwConf)
	if st.Active {
		st.RulesSource = fwcheck.OriginNumbered
		st.Defaults = mergePolicies(fwcheck.ParseVerbose(raw.Verbose), cfg)
	} else {
		st.Defaults = cfg
	}
	for i := range st.Rules {
		r := &st.Rules[i]
		for _, p := range st.SSHPorts {
			if fwcheck.Covers(*r, p, "tcp", netip.Addr{}) {
				r.Protects = "ssh"
			}
		}
		if r.Protects == "" && st.PanelPortRequired && fwcheck.Covers(*r, st.PanelPort, "tcp", netip.Addr{}) {
			r.Protects = "panel"
		}
	}
	return st
}

// ReadState asks the helper for the firewall status. It never changes
// anything.
func ReadState(ctx context.Context, priv *privileged.Runner, listen string) (*State, error) {
	if _, err := os.Stat(fwcheck.UfwPath); err != nil {
		return buildState(fwcheck.RawStatus{}, listen), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	out, err := priv.Run(ctx, "firewall-status")
	if err != nil {
		return nil, err
	}
	var raw fwcheck.RawStatus
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	return buildState(raw, listen), nil
}

// Missing is an access rule that must exist before the firewall may be
// enabled.
type Missing struct {
	// Kind: "ssh" or "panel".
	Kind     string `json:"kind"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	// SuggestedSource is a LAN network when one is known, otherwise "any".
	SuggestedSource string `json:"suggested_source"`
}

// missingAccess lists the access rules the current rule set lacks. client
// is the address of the administrator's browser; a panel rule that does not
// include it does not count.
func missingAccess(st *State, client netip.Addr, suggested string) []Missing {
	out := []Missing{}
	defaultAllow := st.Defaults.Incoming != nil && *st.Defaults.Incoming == "allow"
	if client.IsValid() && client.Unmap().IsLoopback() {
		// Reached through a local proxy: the real client is unknown.
		client = netip.Addr{}
	}
	// The rule order matters: an allow rule behind a deny rule for the same
	// traffic does not keep the port open.
	covered := func(port int, c netip.Addr) bool {
		return fwcheck.Reachable(st.Rules, port, "tcp", c, defaultAllow)
	}
	for _, p := range st.SSHPorts {
		if !covered(p, netip.Addr{}) {
			out = append(out, Missing{Kind: "ssh", Port: p, Protocol: "tcp", SuggestedSource: suggested})
		}
	}
	if st.PanelPortRequired && !covered(st.PanelPort, client) {
		out = append(out, Missing{Kind: "panel", Port: st.PanelPort, Protocol: "tcp", SuggestedSource: suggested})
	}
	return out
}

// LANSource is a source network offered for LAN-only rules.
type LANSource struct {
	CIDR string `json:"cidr"`
	// Kind: "subnet" is the network the server is on; "block" is the
	// whole private range containing it (e.g. 192.168.0.0/16).
	Kind      string `json:"kind"`
	Interface string `json:"interface"`
}

// LANSources returns the networks to offer as the source of a LAN-only
// rule: first the detected subnets, then their enclosing private blocks.
// Only IPv4 networks are offered.
func LANSources() ([]LANSource, error) {
	subnets, err := network.PrivateSubnets()
	if err != nil {
		return nil, err
	}
	out := []LANSource{}
	seen := map[string]bool{}
	for _, s := range subnets {
		if s.Family != "ipv4" || seen[s.CIDR] {
			continue
		}
		seen[s.CIDR] = true
		out = append(out, LANSource{CIDR: s.CIDR, Kind: "subnet", Interface: s.Interface})
	}
	for _, s := range subnets {
		if s.Family != "ipv4" || s.Wide == "" || seen[s.Wide] {
			continue
		}
		seen[s.Wide] = true
		out = append(out, LANSource{CIDR: s.Wide, Kind: "block", Interface: s.Interface})
	}
	return out, nil
}

// suggestSource picks the LAN network containing the client, else the
// first detected one, else "any".
func suggestSource(sources []LANSource, client netip.Addr) string {
	if client.IsValid() {
		c := client.Unmap()
		for _, s := range sources {
			if p, err := netip.ParsePrefix(s.CIDR); err == nil && s.Kind == "subnet" && p.Contains(c) {
				return s.CIDR
			}
		}
	}
	for _, s := range sources {
		if s.Kind == "subnet" {
			return s.CIDR
		}
	}
	return "any"
}

// LANRule builds an allow rule for a port that is restricted to the
// server's own private network, for services that must not be reachable
// from the internet (SMB, NFS...). It fails when the server is on no
// private network; it never falls back to "anywhere".
func LANRule(port, protocol, comment string) (fwcheck.RuleSpec, error) {
	sources, err := LANSources()
	if err != nil || len(sources) == 0 {
		return fwcheck.RuleSpec{}, fwcheck.Error("Sunucunun bağlı olduğu bir yerel ağ bulunamadı.")
	}
	return fwcheck.LANRule(sources[0].CIDR, port, protocol, comment)
}

// AddRule applies a rule through the root helper. The caller is
// responsible for having the user confirm the change and for the audit
// record.
func AddRule(ctx context.Context, priv *privileged.Runner, spec fwcheck.RuleSpec) error {
	return addRule(ctx, priv.Run, spec)
}

func addRule(ctx context.Context, run func(context.Context, string, ...string) ([]byte, error), spec fwcheck.RuleSpec) error {
	spec, err := spec.Normalize()
	if err != nil {
		return err
	}
	_, err = run(ctx, "firewall-rule-add", spec.Action, spec.Port, spec.Protocol, spec.Source, spec.Comment)
	return err
}
