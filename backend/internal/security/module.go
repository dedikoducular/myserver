// Package security is the firewall module: it shows and manages UFW through
// the root helper. Its API lives under /api/v1/firewall.
package security

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"myserver/internal/auth"
	"myserver/internal/httpx"
	"myserver/internal/module"
	"myserver/internal/privileged"
	"myserver/internal/security/fwcheck"
	"myserver/internal/settings"
)

type Module struct {
	deps module.Deps

	// readState and run are the module's only paths to the system (the
	// firewall status and the root helper). They are fields so that tests
	// can replace them; New always sets the real ones.
	readState func(ctx context.Context) (*State, error)
	run       func(ctx context.Context, action string, args ...string) ([]byte, error)

	mu       sync.Mutex
	cached   *State
	cachedAt time.Time
}

const (
	cacheTTL  = 5 * time.Second
	healthTTL = 2 * time.Minute
)

func New(deps module.Deps, _ *settings.API) (module.Module, error) {
	m := &Module{deps: deps}
	m.readState = func(ctx context.Context) (*State, error) {
		return ReadState(ctx, m.deps.Priv, m.listen())
	}
	m.run = func(ctx context.Context, action string, args ...string) ([]byte, error) {
		return m.deps.Priv.Run(ctx, action, args...)
	}
	return m, nil
}

func (m *Module) Name() string { return "firewall" }

func (m *Module) Register(api, _ *httpx.Router) {
	g := api.Group("/firewall")
	g.Get("/status", m.handleStatus)
	g.Get("/suggestions", m.handleSuggestions)

	a := api.Group("/firewall", auth.RequireAdmin)
	a.Get("/enable-check", m.handleEnableCheck)
	a.Post("/enable", m.handleEnable)
	a.Post("/disable", m.handleDisable)
	a.Post("/rules", m.handleRuleAdd)
	a.Post("/rules/delete", m.handleRuleDelete)
}

func (m *Module) listen() string {
	if m.deps.Cfg == nil {
		return ""
	}
	return m.deps.Cfg.ListenAddr
}

// state returns the firewall state, at most maxAge old.
func (m *Module) state(ctx context.Context, maxAge time.Duration) (*State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cached != nil && maxAge > 0 && time.Since(m.cachedAt) < maxAge {
		return m.cached, nil
	}
	st, err := m.readState(ctx)
	if err != nil {
		return nil, err
	}
	m.cached, m.cachedAt = st, time.Now()
	return st, nil
}

func (m *Module) invalidate() {
	m.mu.Lock()
	m.cached = nil
	m.mu.Unlock()
}

func helperError(err error, code, fallback string) *httpx.Error {
	return httpx.NewError(http.StatusBadGateway, code, privileged.UserMessage(err, fallback)).Wrap(err)
}

func notInstalled() *httpx.Error {
	return httpx.NewError(http.StatusConflict, "firewall_not_installed",
		"UFW kurulu değil. Sunucuda şu komutla kurulabilir: "+InstallHint)
}

func clientAddr(r *http.Request) netip.Addr {
	a, err := netip.ParseAddr(httpx.ClientIP(r))
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap().WithZone("")
}

func (m *Module) handleStatus(w http.ResponseWriter, r *http.Request) error {
	st, err := m.state(r.Context(), cacheTTL)
	if err != nil {
		return helperError(err, "firewall_status_failed", "Güvenlik duvarı durumu okunamadı.")
	}
	httpx.OK(w, st)
	return nil
}

// Service is a well-known service offered as a preset when adding a rule.
type Service struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Port     string `json:"port"`
	Protocol string `json:"protocol"`
	// LANOnly services should never be opened to the internet.
	LANOnly bool `json:"lan_only"`
}

var services = []Service{
	{"smb", "SMB / Windows dosya paylaşımı", "445", "tcp", true},
	{"netbios", "NetBIOS oturum servisi (eski SMB)", "139", "tcp", true},
	{"netbios-ns", "NetBIOS ad servisi", "137:138", "udp", true},
	{"nfs", "NFS", "2049", "tcp", true},
	{"rpcbind", "rpcbind (NFS için)", "111", "any", true},
	{"mdns", "mDNS / Avahi", "5353", "udp", true},
	{"ssh", "SSH", "22", "tcp", false},
	{"http", "HTTP", "80", "tcp", false},
	{"https", "HTTPS", "443", "tcp", false},
}

// Suggestions is the response of GET /firewall/suggestions.
type Suggestions struct {
	LANSources []LANSource `json:"lan_sources"`
	Services   []Service   `json:"services"`
	// ClientIP is the address this request came from.
	ClientIP string `json:"client_ip"`
	// DefaultSource is the preselected source for LAN services.
	DefaultSource string `json:"default_source"`
}

func (m *Module) suggestions(r *http.Request) Suggestions {
	sources, err := LANSources()
	if err != nil || sources == nil {
		sources = []LANSource{}
	}
	c := clientAddr(r)
	s := Suggestions{LANSources: sources, Services: services, DefaultSource: suggestSource(sources, c)}
	if c.IsValid() {
		s.ClientIP = c.String()
	}
	return s
}

func (m *Module) handleSuggestions(w http.ResponseWriter, r *http.Request) error {
	httpx.OK(w, m.suggestions(r))
	return nil
}

// EnableCheck is the response of GET /firewall/enable-check.
type EnableCheck struct {
	Installed bool `json:"installed"`
	Active    bool `json:"active"`
	// CanEnable is true when no access rule is missing.
	CanEnable  bool        `json:"can_enable"`
	Missing    []Missing   `json:"missing"`
	LANSources []LANSource `json:"lan_sources"`
	ClientIP   string      `json:"client_ip"`
	// ClientInLAN is false when the browser's address is outside every
	// detected LAN network, where a LAN-only rule would lock it out.
	ClientInLAN bool `json:"client_in_lan"`
}

func (m *Module) enableCheck(r *http.Request, st *State) EnableCheck {
	sg := m.suggestions(r)
	c := clientAddr(r)
	out := EnableCheck{
		Installed:  st.Installed,
		Active:     st.Active,
		Missing:    missingAccess(st, c, sg.DefaultSource),
		LANSources: sg.LANSources,
		ClientIP:   sg.ClientIP,
	}
	for _, s := range sg.LANSources {
		if p, err := netip.ParsePrefix(s.CIDR); err == nil && c.IsValid() && p.Contains(c) {
			out.ClientInLAN = true
		}
	}
	out.CanEnable = st.Installed && len(out.Missing) == 0
	return out
}

func (m *Module) handleEnableCheck(w http.ResponseWriter, r *http.Request) error {
	st, err := m.state(r.Context(), 0)
	if err != nil {
		return helperError(err, "firewall_status_failed", "Güvenlik duvarı durumu okunamadı.")
	}
	httpx.OK(w, m.enableCheck(r, st))
	return nil
}

func describeMissing(ms []Missing) string {
	parts := make([]string, 0, len(ms))
	for _, x := range ms {
		name := "SSH"
		if x.Kind == "panel" {
			name = "panel"
		}
		parts = append(parts, name+" portu ("+strconv.Itoa(x.Port)+"/"+x.Protocol+")")
	}
	return strings.Join(parts, ", ")
}

func userError(err error, fallback string) *httpx.Error {
	var fe fwcheck.Error
	if errors.As(err, &fe) {
		return httpx.BadRequest(fe.Error())
	}
	return helperError(err, "firewall_failed", fallback)
}

func (m *Module) handleEnable(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		// AddRules lists the missing access rules the user agreed to add.
		AddRules []struct {
			Kind   string `json:"kind"`
			Port   int    `json:"port"`
			Source string `json:"source"`
		} `json:"add_rules"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	actor := auth.ActorFrom(r)
	ctx := r.Context()
	st, err := m.state(ctx, 0)
	if err != nil {
		return helperError(err, "firewall_status_failed", "Güvenlik duvarı durumu okunamadı.")
	}
	if !st.Installed {
		return notInstalled()
	}
	if st.Active {
		httpx.OK(w, st)
		return nil
	}
	client := clientAddr(r)
	check := m.enableCheck(r, st)

	// Every missing rule must be covered by the request, or nothing is done.
	var specs []fwcheck.RuleSpec
	var unresolved []Missing
	for _, miss := range check.Missing {
		source := ""
		for _, a := range req.AddRules {
			if a.Kind == miss.Kind && a.Port == miss.Port {
				source = a.Source
			}
		}
		if source == "" {
			unresolved = append(unresolved, miss)
			continue
		}
		comment := "SSH"
		if miss.Kind == "panel" {
			comment = "MyServer panel"
		}
		spec, err := fwcheck.RuleSpec{
			Action: "allow", Port: strconv.Itoa(miss.Port), Protocol: miss.Protocol,
			Source: source, Comment: comment,
		}.Normalize()
		if err != nil {
			return userError(err, "Kural geçersiz.")
		}
		if miss.Kind == "panel" && spec.Source != "any" && client.IsValid() && !client.IsLoopback() {
			p, perr := netip.ParsePrefix(spec.Source)
			if perr != nil {
				if a, aerr := netip.ParseAddr(spec.Source); aerr == nil {
					p = netip.PrefixFrom(a, a.BitLen())
				}
			}
			if !p.IsValid() || !p.Contains(client) {
				return httpx.Conflict("Panel kuralı için seçilen kaynak (" + spec.Source +
					") şu anki bağlantınızın adresini (" + client.String() +
					") kapsamıyor. Güvenlik duvarı etkinleşince panele erişiminiz kesilirdi.")
			}
		}
		specs = append(specs, spec)
	}
	if len(unresolved) > 0 {
		m.deps.Audit.Log(ctx, actor, "firewall.enable", "ufw", "erişim kuralları eksik: "+describeMissing(unresolved), false)
		return httpx.NewError(http.StatusConflict, "firewall_access_rules_missing",
			"Güvenlik duvarı etkinleştirilmedi. Şu erişimler için izin kuralı yok: "+describeMissing(unresolved)+
				". Bu kurallar olmadan sunucuya erişiminiz kesilir.")
	}

	for _, spec := range specs {
		if err := addRule(ctx, m.run, spec); err != nil {
			m.invalidate()
			m.deps.Audit.Log(ctx, actor, "firewall.rule_add", spec.String(), "başarısız", false)
			return userError(err, "Erişim kuralı eklenemedi; güvenlik duvarı etkinleştirilmedi.")
		}
		m.deps.Audit.Log(ctx, actor, "firewall.rule_add", spec.String(), spec.Comment, true)
	}
	if len(specs) > 0 {
		// Trust nothing: read the rules back and check again.
		m.invalidate()
		st, err = m.state(ctx, 0)
		if err != nil {
			return helperError(err, "firewall_status_failed", "Güvenlik duvarı durumu okunamadı; güvenlik duvarı etkinleştirilmedi.")
		}
		if left := missingAccess(st, client, "any"); len(left) > 0 {
			m.deps.Audit.Log(ctx, actor, "firewall.enable", "ufw", "eklenen kurallar doğrulanamadı", false)
			return httpx.NewError(http.StatusConflict, "firewall_access_rules_missing",
				"Eklenen erişim kuralları doğrulanamadı ("+describeMissing(left)+"); güvenlik duvarı etkinleştirilmedi.")
		}
	}

	if _, err := m.run(ctx, "firewall-enable"); err != nil {
		m.invalidate()
		m.deps.Audit.Log(ctx, actor, "firewall.enable", "ufw", "başarısız", false)
		return helperError(err, "firewall_enable_failed", "Güvenlik duvarı etkinleştirilemedi.")
	}
	m.invalidate()
	m.deps.Audit.Log(ctx, actor, "firewall.enable", "ufw", "", true)
	return m.respondState(w, r)
}

// respondState returns the fresh state after a change. The change itself
// succeeded, so a failed re-read is not reported as a failure.
func (m *Module) respondState(w http.ResponseWriter, r *http.Request) error {
	// The connection may be cut by the change; do not depend on the request.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 45*time.Second)
	defer cancel()
	st, err := m.state(ctx, 0)
	if err != nil {
		httpx.OK(w, nil)
		return nil
	}
	httpx.OK(w, st)
	return nil
}

func (m *Module) handleDisable(w http.ResponseWriter, r *http.Request) error {
	actor := auth.ActorFrom(r)
	st, err := m.state(r.Context(), 0)
	if err != nil {
		return helperError(err, "firewall_status_failed", "Güvenlik duvarı durumu okunamadı.")
	}
	if !st.Installed {
		return notInstalled()
	}
	if _, err := m.run(r.Context(), "firewall-disable"); err != nil {
		m.invalidate()
		m.deps.Audit.Log(r.Context(), actor, "firewall.disable", "ufw", "başarısız", false)
		return helperError(err, "firewall_disable_failed", "Güvenlik duvarı devre dışı bırakılamadı.")
	}
	m.invalidate()
	m.deps.Audit.Log(r.Context(), actor, "firewall.disable", "ufw", "", true)
	return m.respondState(w, r)
}

func (m *Module) handleRuleAdd(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Port     string `json:"port"`
		Protocol string `json:"protocol"`
		Source   string `json:"source"`
		Action   string `json:"action"`
		Comment  string `json:"comment"`
		// AcknowledgeAccessRisk is required for a deny/reject rule that
		// covers the SSH or panel port.
		AcknowledgeAccessRisk bool `json:"acknowledge_access_risk"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	spec, err := fwcheck.RuleSpec{
		Action: req.Action, Port: req.Port, Protocol: req.Protocol,
		Source: req.Source, Comment: strings.TrimSpace(req.Comment),
	}.Normalize()
	if err != nil {
		return userError(err, "Kural geçersiz.")
	}
	actor := auth.ActorFrom(r)
	st, err := m.state(r.Context(), 0)
	if err != nil {
		return helperError(err, "firewall_status_failed", "Güvenlik duvarı durumu okunamadı.")
	}
	if !st.Installed {
		return notInstalled()
	}
	if !req.AcknowledgeAccessRisk {
		for _, p := range st.SSHPorts {
			if spec.Blocks(p, "tcp") {
				return httpx.NewError(http.StatusConflict, "firewall_access_risk",
					"Bu kural SSH portunu ("+strconv.Itoa(p)+") engeller ve sunucuya uzaktan erişiminizi kesebilir. Devam etmek için riski onaylamanız gerekir.")
			}
		}
		if st.PanelPortRequired && spec.Blocks(st.PanelPort, "tcp") {
			return httpx.NewError(http.StatusConflict, "firewall_access_risk",
				"Bu kural panelin portunu ("+strconv.Itoa(st.PanelPort)+") engeller ve panele erişiminizi kesebilir. Devam etmek için riski onaylamanız gerekir.")
		}
	}
	if err := addRule(r.Context(), m.run, spec); err != nil {
		m.invalidate()
		m.deps.Audit.Log(r.Context(), actor, "firewall.rule_add", spec.String(), "başarısız", false)
		return userError(err, "Kural eklenemedi.")
	}
	m.invalidate()
	m.deps.Audit.Log(r.Context(), actor, "firewall.rule_add", spec.String(), spec.Comment, true)
	return m.respondState(w, r)
}

func (m *Module) handleRuleDelete(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Origin string `json:"origin"`
		Number int    `json:"number"`
		ID     string `json:"id"`
		// AcknowledgeAccessRisk is required to delete a rule that keeps
		// SSH or the panel reachable.
		AcknowledgeAccessRisk bool `json:"acknowledge_access_risk"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	if req.Origin != fwcheck.OriginNumbered && req.Origin != fwcheck.OriginAdded {
		return httpx.BadRequest("Kural kaynağı geçersiz.")
	}
	if req.Number < 1 || req.Number > 100000 || req.ID == "" || len(req.ID) > 400 ||
		strings.ContainsAny(req.ID, "\n\r\x00") {
		return httpx.BadRequest("Kural tanımı geçersiz.")
	}
	actor := auth.ActorFrom(r)
	st, err := m.state(r.Context(), 0)
	if err != nil {
		return helperError(err, "firewall_status_failed", "Güvenlik duvarı durumu okunamadı.")
	}
	if !st.Installed {
		return notInstalled()
	}
	var rule *fwcheck.Rule
	for i := range st.Rules {
		x := &st.Rules[i]
		if x.Origin == req.Origin && x.Number == req.Number && x.ID == req.ID {
			rule = x
		}
	}
	if rule == nil {
		return httpx.Conflict("Kural listesi değişmiş; kural silinmedi. Listeyi yenileyip tekrar deneyin.")
	}
	if rule.Protects != "" && !req.AcknowledgeAccessRisk {
		what := "SSH"
		if rule.Protects == "panel" {
			what = "panel"
		}
		return httpx.NewError(http.StatusConflict, "firewall_protected_rule",
			"Bu kural "+what+" erişimini açık tutuyor. Silinirse sunucuya erişiminiz kesilebilir. Silmek için riski onaylamanız gerekir.")
	}
	if _, err := m.run(r.Context(), "firewall-rule-delete", req.Origin, strconv.Itoa(req.Number), req.ID); err != nil {
		m.invalidate()
		m.deps.Audit.Log(r.Context(), actor, "firewall.rule_delete", rule.Raw, "başarısız", false)
		return helperError(err, "firewall_rule_delete_failed", "Kural silinemedi.")
	}
	m.invalidate()
	detail := ""
	if rule.Protects != "" {
		detail = "erişim riski onaylandı (" + rule.Protects + ")"
	}
	m.deps.Audit.Log(r.Context(), actor, "firewall.rule_delete", rule.Raw, detail, true)
	return m.respondState(w, r)
}

// Health reports the firewall to the health monitor from a cached state.
func (m *Module) Health(ctx context.Context) []module.HealthCheck {
	check := module.HealthCheck{ID: "firewall.status", Name: "Güvenlik Duvarı", Status: module.Healthy}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	st, err := m.state(ctx, healthTTL)
	switch {
	case err != nil:
		check.Status = module.WarningLevel
		check.Message = "Güvenlik duvarı durumu okunamadı"
	case !st.Installed:
		check.Message = "UFW kurulu değil"
	case !st.Active:
		check.Status = module.WarningLevel
		check.Message = "Güvenlik duvarı etkin değil"
	default:
		check.Message = "Güvenlik duvarı etkin"
	}
	return []module.HealthCheck{check}
}
