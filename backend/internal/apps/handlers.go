package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"

	"myserver/internal/auth"
	"myserver/internal/httpx"
)

/* ---------- views ---------- */

type webUIView struct {
	Port   int    `json:"port"`
	Scheme string `json:"scheme"`
	Path   string `json:"path"`
	// LocalOnly: the port is published on 127.0.0.1, so the interface
	// cannot be opened from another device.
	LocalOnly bool `json:"local_only"`
}

type portView struct {
	Service string `json:"service"`
	// HostIP is the address the port is reachable on: "0.0.0.0" (all
	// interfaces) or "127.0.0.1" (this server only).
	HostIP string `json:"host_ip"`
	// HostNetwork: the service uses the server's network directly.
	HostNetwork bool   `json:"host_network"`
	Host        int    `json:"host"`
	Container   int    `json:"container"`
	Protocol    string `json:"protocol"`
	Label       string `json:"label"`
}

type installedView struct {
	Slug              string         `json:"slug"`
	Name              string         `json:"name"`
	Description       string         `json:"description"`
	Category          string         `json:"category"`
	CategoryLabel     string         `json:"category_label"`
	Icon              string         `json:"icon"`
	Version           string         `json:"version"`
	AvailableVersion  string         `json:"available_version"`
	ManifestAvailable bool           `json:"manifest_available"`
	InstalledAt       int64          `json:"installed_at"`
	UpdatedAt         int64          `json:"updated_at"`
	State             string         `json:"state"`
	Operation         string         `json:"operation"`
	JobID             string         `json:"job_id"`
	Services          []ServiceState `json:"services"`
	WebUI             *webUIView     `json:"web_ui"`
	Ports             []portView     `json:"ports"`
	// BindAddress is "all" or "loopback".
	BindAddress string `json:"bind_address"`
	// Custom: added by an administrator from a compose file.
	Custom bool `json:"custom"`
}

type catalogView struct {
	Slug          string   `json:"slug"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Category      string   `json:"category"`
	CategoryLabel string   `json:"category_label"`
	Icon          string   `json:"icon"`
	Version       string   `json:"version"`
	Website       string   `json:"website"`
	Images        []string `json:"images"`
	// WarningLevel is "", "warning" or "danger".
	WarningLevel string `json:"warning_level"`
	// Architectures declared by the manifest; empty when not declared.
	Architectures []string `json:"architectures"`
	// Installable is false when the server's CPU architecture is not
	// supported; UnsupportedReason then explains why.
	Installable       bool   `json:"installable"`
	UnsupportedReason string `json:"unsupported_reason"`
	Installed         bool   `json:"installed"`
	Operation         string `json:"operation"`
	JobID             string `json:"job_id"`
	// Custom: added by an administrator from a compose file.
	Custom bool `json:"custom"`
}

type portField struct {
	Key       string `json:"key"`
	Service   string `json:"service"`
	Label     string `json:"label"`
	Container int    `json:"container"`
	Protocol  string `json:"protocol"`
	Default   int    `json:"default"`
	Value     int    `json:"value"`
	// Fixed ports cannot be changed (host networking).
	Fixed bool `json:"fixed"`
	WebUI bool `json:"web_ui"`
	// Option: the port is published only while this option is enabled.
	Option string `json:"option"`
}

type envField struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Default     string `json:"default"`
	// Value is always empty for secret fields.
	Value    string `json:"value"`
	Required bool   `json:"required"`
	Secret   bool   `json:"secret"`
	// Generated: a random value is created when the field is left empty.
	Generated bool `json:"generated"`
	// HasValue: a value is stored for this (secret) field.
	HasValue bool `json:"has_value"`
}

type pathField struct {
	Key         string `json:"key"`
	Service     string `json:"service"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Target      string `json:"target"`
	Default     string `json:"default"`
	Value       string `json:"value"`
	Required    bool   `json:"required"`
	ReadOnly    bool   `json:"read_only"`
}

type optionField struct {
	Key         string   `json:"key"`
	Service     string   `json:"service"`
	Label       string   `json:"label"`
	Description string   `json:"description"`
	Default     bool     `json:"default"`
	Value       bool     `json:"value"`
	CapAdd      []string `json:"cap_add"`
}

type fieldsView struct {
	Ports   []portField   `json:"ports"`
	Env     []envField    `json:"env"`
	Paths   []pathField   `json:"paths"`
	Options []optionField `json:"options"`
}

// archError explains why an application cannot be installed on this
// server, or returns "" when it can.
func archError(man *Manifest) string {
	if man.SupportsArch(runtime.GOARCH) {
		return ""
	}
	return "Bu uygulama sunucunuzun işlemci mimarisini (" + runtime.GOARCH +
		") desteklemiyor. Desteklenen mimariler: " + strings.Join(man.Architectures, ", ") + "."
}

type serviceView struct {
	Name      string   `json:"name"`
	Image     string   `json:"image"`
	DependsOn []string `json:"depends_on"`
}

type volumeView struct {
	Service  string `json:"service"`
	Type     string `json:"type"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	Label    string `json:"label"`
	ReadOnly bool   `json:"read_only"`
}

type detailView struct {
	catalogView
	LongDescription        string    `json:"long_description"`
	Notes                  string    `json:"notes"`
	Warnings               []Warning `json:"warnings"`
	RequiresRiskAcceptance bool      `json:"requires_risk_acceptance"`
	AllowedRoots           []string  `json:"allowed_roots"`
	// BindAddress is the current choice ("all" or "loopback").
	BindAddress string `json:"bind_address"`
	// PublishesPorts: at least one bridge service publishes a port.
	PublishesPorts bool `json:"publishes_ports"`
	// HostNetwork: at least one service uses the server's network.
	HostNetwork  bool           `json:"host_network"`
	Fields       fieldsView     `json:"fields"`
	Services     []serviceView  `json:"services"`
	Volumes      []volumeView   `json:"volumes"`
	InstalledApp *installedView `json:"installed_app"`
}

func categoryLabel(id string) string {
	for _, c := range Categories {
		if c.ID == id {
			return c.Label
		}
	}
	return "Diğer"
}

func warningLevel(m *Manifest) string {
	level := ""
	for _, w := range Warnings(m) {
		if w.Level == "danger" {
			return "danger"
		}
		level = "warning"
	}
	return level
}

func (m *Module) jobID(slug string) string {
	if j := m.jobs.running(slug); j != nil {
		return j.ID
	}
	return ""
}

func (m *Module) catalogItem(man *Manifest, installed bool) catalogView {
	images := make([]string, 0, len(man.Services))
	for _, s := range man.Services {
		images = append(images, s.Image)
	}
	archs := man.Architectures
	if archs == nil {
		archs = []string{}
	}
	reason := archError(man)
	return catalogView{
		Architectures: archs, Installable: reason == "", UnsupportedReason: reason,
		Slug: man.Slug, Name: man.Name, Description: man.Description, Category: man.Category,
		CategoryLabel: categoryLabel(man.Category), Icon: man.Icon, Version: man.Version,
		Website: man.Website, Images: images, WarningLevel: warningLevel(man),
		Installed: installed, Operation: m.operation(man.Slug), JobID: m.jobID(man.Slug),
		Custom: IsCustomSlug(man.Slug),
	}
}

func containerInfos(list []container.Summary) []ContainerInfo {
	out := make([]ContainerInfo, 0, len(list))
	for _, c := range list {
		out = append(out, ContainerInfo{
			ID: c.ID, Name: containerDisplayName(c), Service: c.Labels[LabelService],
			Image: c.Image, State: string(c.State), Status: c.Status,
		})
	}
	return out
}

func (m *Module) installedItem(it *Installed, containers []ContainerInfo, dockerOK bool) installedView {
	cfg := it.Config
	v := installedView{
		Slug: it.Slug, Name: it.Name, Description: cfg.Description, Category: cfg.Category,
		CategoryLabel: categoryLabel(cfg.Category), Icon: cfg.Icon, Version: it.Version,
		InstalledAt: it.InstalledAt, UpdatedAt: it.UpdatedAt,
		Operation: m.operation(it.Slug), JobID: m.jobID(it.Slug), Ports: []portView{},
		BindAddress: cfg.BindAddress, Custom: IsCustomSlug(it.Slug),
	}
	if man := m.catalog.Get(it.Slug); man != nil {
		v.ManifestAvailable = true
		v.AvailableVersion = man.Version
	}
	if dockerOK {
		v.State, v.Services = DeriveState(cfg, containers)
	} else {
		v.State, v.Services = StateUnknown, unknownState(cfg)
	}
	for _, s := range cfg.Services {
		for _, p := range s.Ports {
			hostIP := p.HostIP
			if hostIP == "" {
				hostIP = AddrAny
			}
			v.Ports = append(v.Ports, portView{
				Service: s.Name, HostIP: hostIP, HostNetwork: s.NetworkMode == "host",
				Host: p.Host, Container: p.Container, Protocol: p.Protocol, Label: p.Label,
			})
			if p.WebUI != nil && v.WebUI == nil {
				v.WebUI = &webUIView{Port: p.Host, Scheme: p.WebUI.Scheme, Path: p.WebUI.Path, LocalOnly: hostIP == AddrLoopback}
			}
		}
	}
	return v
}

// installedViews returns every installed application with its runtime
// state. dockerOK is false when Docker could not be asked; the list is
// still returned, with state "unknown".
func (m *Module) installedViews(ctx context.Context) ([]installedView, bool, error) {
	list, err := m.store.list(ctx)
	if err != nil {
		return nil, false, err
	}
	bySlug := map[string][]ContainerInfo{}
	dockerOK := false
	if cli, err := m.eng.ready(ctx); err == nil {
		if cs, err := m.eng.appContainers(ctx, cli, ""); err == nil {
			dockerOK = true
			for _, c := range cs {
				slug := c.Labels[LabelApp]
				bySlug[slug] = append(bySlug[slug], containerInfos([]container.Summary{c})...)
			}
		}
	}
	out := make([]installedView, 0, len(list))
	for _, it := range list {
		out = append(out, m.installedItem(it, bySlug[it.Slug], dockerOK))
	}
	return out, dockerOK, nil
}

func buildFields(man *Manifest, in *Inputs) fieldsView {
	f := fieldsView{Ports: []portField{}, Env: []envField{}, Paths: []pathField{}, Options: []optionField{}}
	for _, s := range man.Services {
		for _, o := range s.Options {
			of := optionField{
				Key: o.Key, Service: s.Name, Label: o.Label, Description: o.Description,
				Default: o.Default, Value: o.Default, CapAdd: o.CapAdd,
			}
			if in != nil {
				if v, ok := in.Options[o.Key]; ok {
					of.Value = v
				}
			}
			f.Options = append(f.Options, of)
		}
	}
	seenEnv := map[string]bool{}
	for _, s := range man.Services {
		for _, p := range s.Ports {
			pf := portField{
				Key: p.Key, Service: s.Name, Label: portLabel(p), Container: p.Container, Protocol: p.Protocol,
				Default: p.Host, Value: p.Host, Fixed: s.NetworkMode == "host", WebUI: p.WebUI != nil, Option: p.Option,
			}
			if in != nil {
				if v, ok := in.Ports[p.Key]; ok {
					pf.Value = v
				}
			}
			f.Ports = append(f.Ports, pf)
		}
	}
	for _, s := range man.Services {
		for _, e := range s.Env {
			if e.Fixed || seenEnv[e.Key] {
				continue
			}
			seenEnv[e.Key] = true
			ef := envField{
				Key: e.Key, Name: e.Name, Label: e.Label, Description: e.Description, Default: e.Default,
				Value: e.Default, Required: e.Required, Secret: e.Secret, Generated: e.Generate != "",
			}
			if ef.Label == "" {
				ef.Label = e.Name
			}
			if in != nil {
				if v, ok := in.Env[e.Key]; ok {
					ef.Value = v
					ef.HasValue = v != ""
				}
			}
			if e.Secret {
				ef.Value, ef.Default = "", ""
			}
			f.Env = append(f.Env, ef)
		}
		for _, v := range s.Volumes {
			if v.Type != VolumeBind {
				continue
			}
			pf := pathField{
				Key: v.Key, Service: s.Name, Label: v.Label, Description: v.Description, Target: v.Target,
				Default: v.Source, Value: v.Source, Required: v.Required, ReadOnly: v.ReadOnly,
			}
			if pf.Label == "" {
				pf.Label = v.Target
			}
			if in != nil {
				if val, ok := in.Paths[v.Key]; ok {
					pf.Value = val
				}
			}
			f.Paths = append(f.Paths, pf)
		}
	}
	return f
}

/* ---------- catalog ---------- */

func (m *Module) installedSet(ctx context.Context) (map[string]bool, error) {
	list, err := m.store.list(ctx)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, it := range list {
		set[it.Slug] = true
	}
	return set, nil
}

func (m *Module) handleCatalog(w http.ResponseWriter, r *http.Request) error {
	set, err := m.installedSet(r.Context())
	if err != nil {
		return httpx.Internal(err)
	}
	apps := []catalogView{}
	used := map[string]bool{}
	for _, man := range m.catalog.List() {
		apps = append(apps, m.catalogItem(man, set[man.Slug]))
		used[man.Slug] = true
	}
	cats := []Category{}
	for _, c := range Categories {
		for _, a := range apps {
			if a.Category == c.ID {
				cats = append(cats, c)
				break
			}
		}
	}
	loadedAt, loadErr := m.catalog.Status()
	invalid := []InvalidManifest{}
	if s := auth.From(r.Context()); s != nil && s.User.Role == auth.RoleAdmin {
		invalid = m.catalog.Invalid()
	}
	httpx.OK(w, map[string]any{
		"apps": apps, "categories": cats, "invalid": invalid,
		"loaded_at": loadedAt, "load_error": loadErr,
	})
	return nil
}

func (m *Module) manifest(r *http.Request) (*Manifest, error) {
	slug := r.PathValue("slug")
	if !ValidSlug(slug) {
		return nil, httpx.BadRequest("Uygulama adı geçersiz.")
	}
	man := m.catalog.Get(slug)
	if man == nil {
		return nil, httpx.NotFound("Uygulama katalogda bulunamadı.")
	}
	return man, nil
}

func (m *Module) handleCatalogApp(w http.ResponseWriter, r *http.Request) error {
	man, err := m.manifest(r)
	if err != nil {
		return err
	}
	it, err := m.store.get(r.Context(), man.Slug)
	if err != nil {
		return httpx.Internal(err)
	}
	isAdmin := false
	if s := auth.From(r.Context()); s != nil {
		isAdmin = s.User.Role == auth.RoleAdmin
	}
	d, err := m.detailOf(r.Context(), man, it, isAdmin)
	if err != nil {
		return err
	}
	httpx.OK(w, d)
	return nil
}

// detailOf builds the detail of a manifest: its fields, warnings, services
// and volumes, and the installation when it is installed (it != nil).
func (m *Module) detailOf(ctx context.Context, man *Manifest, it *Installed, isAdmin bool) (detailView, error) {
	d := detailView{
		catalogView:            m.catalogItem(man, it != nil),
		LongDescription:        man.LongDescription,
		Notes:                  man.Notes,
		Warnings:               Warnings(man),
		RequiresRiskAcceptance: NeedsRiskAcceptance(man),
		AllowedRoots:           m.roots(),
		Services:               []serviceView{},
		Volumes:                []volumeView{},
	}
	if d.AllowedRoots == nil {
		d.AllowedRoots = []string{}
	}
	// The values of an installation are shown to administrators only.
	var in *Inputs
	if it != nil && isAdmin {
		in = it.Inputs
	}
	d.Fields = buildFields(man, in)
	d.BindAddress = BindAll
	if ValidBindAddress(man.BindAddress) {
		d.BindAddress = man.BindAddress
	}
	if in != nil && ValidBindAddress(in.BindAddress) {
		d.BindAddress = in.BindAddress
	}
	for _, s := range man.Services {
		if s.NetworkMode == "host" {
			d.HostNetwork = true
		} else if len(s.Ports) > 0 {
			d.PublishesPorts = true
		}
	}
	for _, s := range man.Services {
		deps := s.DependsOn
		if deps == nil {
			deps = []string{}
		}
		d.Services = append(d.Services, serviceView{Name: s.Name, Image: s.Image, DependsOn: deps})
		for _, v := range s.Volumes {
			src := v.Source
			if v.Type == VolumeNamed {
				src = VolumeName(man.Slug, v.Source)
			}
			if v.Type == VolumeBind {
				continue
			}
			d.Volumes = append(d.Volumes, volumeView{Service: s.Name, Type: v.Type, Source: src, Target: v.Target, Label: v.Label, ReadOnly: v.ReadOnly})
		}
	}
	if it != nil {
		views, _, err := m.installedViews(ctx)
		if err != nil {
			return d, httpx.Internal(err)
		}
		for i := range views {
			if views[i].Slug == it.Slug {
				d.InstalledApp = &views[i]
			}
		}
	}
	return d, nil
}

func (m *Module) handleReload(w http.ResponseWriter, r *http.Request) error {
	m.catalog.Reload()
	invalid := m.catalog.Invalid()
	valid := len(m.catalog.List())
	m.deps.Audit.Log(r.Context(), auth.ActorFrom(r), "apps.reload", "",
		fmt.Sprintf("%d geçerli, %d geçersiz tanım", valid, len(invalid)), true)
	m.hub.notify()
	_, loadErr := m.catalog.Status()
	httpx.OK(w, map[string]any{"valid": valid, "invalid": invalid, "load_error": loadErr})
	return nil
}

/* ---------- installed ---------- */

func (m *Module) handleInstalled(w http.ResponseWriter, r *http.Request) error {
	views, dockerOK, err := m.installedViews(r.Context())
	if err != nil {
		return httpx.Internal(err)
	}
	cats := []Category{}
	for _, c := range Categories {
		for _, a := range views {
			if a.Category == c.ID {
				cats = append(cats, c)
				break
			}
		}
	}
	httpx.OK(w, map[string]any{"apps": views, "categories": cats, "docker_available": dockerOK})
	return nil
}

func (m *Module) handleInstalledApp(w http.ResponseWriter, r *http.Request) error {
	it, err := m.installed(r.Context(), r.PathValue("slug"))
	if err != nil {
		return err
	}
	cli, err := m.eng.ready(r.Context())
	if err != nil {
		httpx.OK(w, m.installedItem(it, nil, false))
		return nil
	}
	list, err := m.eng.appContainers(r.Context(), cli, it.Slug)
	if err != nil {
		return err
	}
	httpx.OK(w, m.installedItem(it, containerInfos(list), true))
	return nil
}

type inputsRequest struct {
	Ports       map[string]int    `json:"ports"`
	Env         map[string]string `json:"env"`
	Paths       map[string]string `json:"paths"`
	Options     map[string]bool   `json:"options"`
	BindAddress string            `json:"bind_address"`
	AcceptRisks bool              `json:"accept_risks"`
}

func (m *Module) handleInstall(w http.ResponseWriter, r *http.Request) error {
	man, err := m.manifest(r)
	if err != nil {
		return err
	}
	var req inputsRequest
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	if req.BindAddress != "" && !ValidBindAddress(req.BindAddress) {
		return httpx.NewError(http.StatusBadRequest, "invalid_input", "Erişim adresi seçimi geçersiz.")
	}
	actor := auth.ActorFrom(r)
	it, err := m.store.get(r.Context(), man.Slug)
	if err != nil {
		return httpx.Internal(err)
	}
	if it != nil {
		return httpx.Conflict(man.Name + " zaten kurulu.")
	}
	if reason := archError(man); reason != "" {
		return httpx.NewError(http.StatusConflict, "unsupported_architecture", reason)
	}
	if NeedsRiskAcceptance(man) && !req.AcceptRisks {
		return httpx.NewError(http.StatusBadRequest, "risk_not_accepted",
			"Bu uygulamayı kurmak için güvenlik uyarılarını okuyup kabul etmelisiniz.")
	}
	cli, err := m.eng.ready(r.Context())
	if err != nil {
		return err
	}
	prev, err := m.store.retained(r.Context(), man.Slug)
	if err != nil {
		return httpx.Internal(err)
	}
	if prev != nil {
		// Only secrets are carried over from a removed installation; the
		// form supplies everything else.
		prev.Ports, prev.Paths, prev.Options = map[string]int{}, map[string]string{}, map[string]bool{}
		prev.BindAddress = ""
	}
	cfg, in, err := m.prepare(r.Context(), cli, man, Inputs{Ports: req.Ports, Env: req.Env, Paths: req.Paths, Options: req.Options, BindAddress: req.BindAddress}, prev)
	if err != nil {
		return err
	}
	job, err := m.startInstall(actor, cli, cfg, in)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusAccepted, job.view())
	return nil
}

func (m *Module) handleUpdate(w http.ResponseWriter, r *http.Request) error {
	it, err := m.installed(r.Context(), r.PathValue("slug"))
	if err != nil {
		return err
	}
	cli, err := m.eng.ready(r.Context())
	if err != nil {
		return err
	}
	cfg, in := it.Config, it.Inputs
	if man := m.catalog.Get(it.Slug); man != nil {
		cfg, in, err = m.prepare(r.Context(), cli, man, Inputs{}, it.Inputs)
		if err != nil {
			var he *httpx.Error
			if errors.As(err, &he) && he.Code == "invalid_input" {
				return httpx.NewError(http.StatusConflict, "settings_required",
					"Uygulamanın yeni sürümü ek ayar istiyor: "+he.Message+" Önce Ayarlar penceresinden eksik değeri girin.")
			}
			return err
		}
	} else {
		conflicts, err := m.eng.checkPorts(r.Context(), cli, cfg)
		if err != nil {
			return err
		}
		if len(conflicts) > 0 {
			return portConflictError(conflicts)
		}
	}
	job, err := m.startReplace(auth.ActorFrom(r), cli, JobUpdate, it, cfg, in)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusAccepted, job.view())
	return nil
}

func (m *Module) handleSettings(w http.ResponseWriter, r *http.Request) error {
	it, err := m.installed(r.Context(), r.PathValue("slug"))
	if err != nil {
		return err
	}
	var req inputsRequest
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	if req.BindAddress != "" && !ValidBindAddress(req.BindAddress) {
		return httpx.NewError(http.StatusBadRequest, "invalid_input", "Erişim adresi seçimi geçersiz.")
	}
	man := m.catalog.Get(it.Slug)
	if man == nil {
		return httpx.Conflict("Bu uygulamanın tanım dosyası artık katalogda yok; ayarları değiştirilemez.")
	}
	if NeedsRiskAcceptance(man) && !req.AcceptRisks {
		return httpx.NewError(http.StatusBadRequest, "risk_not_accepted",
			"Ayarları uygulamak için güvenlik uyarılarını okuyup kabul etmelisiniz.")
	}
	cli, err := m.eng.ready(r.Context())
	if err != nil {
		return err
	}
	cfg, in, err := m.prepare(r.Context(), cli, man, Inputs{Ports: req.Ports, Env: req.Env, Paths: req.Paths, Options: req.Options, BindAddress: req.BindAddress}, it.Inputs)
	if err != nil {
		return err
	}
	job, err := m.startReplace(auth.ActorFrom(r), cli, JobSettings, it, cfg, in)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusAccepted, job.view())
	return nil
}

// detached returns a context for a state-changing request that must finish
// even when the browser goes away.
func detached(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), d)
}

func (m *Module) handleLifecycle(action string) httpx.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		ctx, cancel := detached(r, 5*time.Minute)
		defer cancel()
		slug := r.PathValue("slug")
		it, err := m.lifecycle(ctx, slug, action)
		if err != nil {
			if it != nil || ValidSlug(slug) {
				m.deps.Audit.Log(ctx, auth.ActorFrom(r), "apps."+action, slug, userMessage(err), false)
			}
			return err
		}
		m.deps.Audit.Log(ctx, auth.ActorFrom(r), "apps."+action, slug, "", true)
		cli, err := m.eng.ready(ctx)
		if err != nil {
			httpx.OK(w, m.installedItem(it, nil, false))
			return nil
		}
		list, err := m.eng.appContainers(ctx, cli, slug)
		if err != nil {
			return err
		}
		httpx.OK(w, m.installedItem(it, containerInfos(list), true))
		return nil
	}
}

func (m *Module) handleUninstall(w http.ResponseWriter, r *http.Request) error {
	it, err := m.installed(r.Context(), r.PathValue("slug"))
	if err != nil {
		return err
	}
	var req struct {
		DeleteData bool   `json:"delete_data"`
		Confirm    string `json:"confirm"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	if req.DeleteData && req.Confirm != it.Slug {
		return httpx.NewError(http.StatusBadRequest, "confirmation_required",
			"Verileri silmek için uygulama adını yazarak ayrıca onaylamalısınız.")
	}
	ctx, cancel := detached(r, 10*time.Minute)
	defer cancel()
	detail := "veriler korundu"
	if req.DeleteData {
		detail = "veri birimleri silindi"
	}
	res, err := m.uninstall(ctx, it, req.DeleteData)
	if err != nil {
		m.deps.Audit.Log(ctx, auth.ActorFrom(r), "apps.uninstall", it.Slug, userMessage(err), false)
		return err
	}
	m.deps.Audit.Log(ctx, auth.ActorFrom(r), "apps.uninstall", it.Slug, detail, true)
	httpx.OK(w, res)
	return nil
}

/* ---------- jobs ---------- */

func (m *Module) handleJobs(w http.ResponseWriter, _ *http.Request) error {
	httpx.OK(w, map[string]any{"jobs": m.jobs.list()})
	return nil
}

func (m *Module) job(r *http.Request) (*Job, error) {
	id := r.PathValue("id")
	if len(id) != 24 || strings.Trim(id, "0123456789abcdef") != "" {
		return nil, httpx.BadRequest("İş numarası geçersiz.")
	}
	j := m.jobs.get(id)
	if j == nil {
		return nil, httpx.NotFound("İş bulunamadı. Panel yeniden başlatılmış olabilir.")
	}
	return j, nil
}

func (m *Module) handleJob(w http.ResponseWriter, r *http.Request) error {
	j, err := m.job(r)
	if err != nil {
		return err
	}
	events, _ := j.since(0)
	if events == nil {
		events = []Event{}
	}
	httpx.OK(w, map[string]any{"job": j.view(), "events": events})
	return nil
}

/* ---------- server-sent events ---------- */

type sse struct {
	w http.ResponseWriter
	f http.Flusher
}

func startSSE(w http.ResponseWriter) (*sse, error) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, httpx.Internal(errors.New("response writer does not support streaming"))
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": bağlandı\n\n")
	f.Flush()
	return &sse{w: w, f: f}, nil
}

func (s *sse) send(event, id string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	if id != "" {
		if _, err := fmt.Fprintf(s.w, "id: %s\n", id); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}

func (s *sse) ping() error {
	if _, err := fmt.Fprint(s.w, ": ping\n\n"); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}

const pingEvery = 25 * time.Second

// handleJobStream replays the events of a job and follows it until it ends.
// A reconnecting browser resumes after the last event it received.
func (m *Module) handleJobStream(w http.ResponseWriter, r *http.Request) error {
	j, err := m.job(r)
	if err != nil {
		return err
	}
	seq := 0
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		seq, _ = strconv.Atoi(v)
	} else if v := r.URL.Query().Get("after"); v != "" {
		seq, _ = strconv.Atoi(v)
	}
	if seq < 0 {
		seq = 0
	}
	ch, cancel := j.subscribe()
	defer cancel()
	s, err := startSSE(w)
	if err != nil {
		return err
	}
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	for {
		events, finished := j.since(seq)
		for _, e := range events {
			if err := s.send("progress", strconv.Itoa(e.Seq), e); err != nil {
				return nil
			}
			seq = e.Seq
		}
		if finished {
			if more, _ := j.since(seq); len(more) == 0 {
				_ = s.send("finished", "", j.view())
				return nil
			}
			continue
		}
		select {
		case <-r.Context().Done():
			return nil
		case <-ping.C:
			if s.ping() != nil {
				return nil
			}
		case <-ch:
		}
	}
}

// handleEvents tells the browser when the state of an application changed,
// so that pages refresh without polling.
func (m *Module) handleEvents(w http.ResponseWriter, r *http.Request) error {
	ch, cancel := m.hub.subscribe()
	defer cancel()
	s, err := startSSE(w)
	if err != nil {
		return err
	}
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return nil
		case <-ping.C:
			if s.ping() != nil {
				return nil
			}
		case <-ch:
			// Coalesce bursts (a restart produces several events).
			select {
			case <-r.Context().Done():
				return nil
			case <-time.After(700 * time.Millisecond):
			}
			select {
			case <-ch:
			default:
			}
			if s.send("changed", "", map[string]int64{"time": time.Now().Unix()}) != nil {
				return nil
			}
		}
	}
}

type logLine struct {
	Service string `json:"service"`
	Stream  string `json:"stream"`
	Time    string `json:"time"`
	Line    string `json:"line"`
}

// handleLogs streams the output of one service of an application.
func (m *Module) handleLogs(w http.ResponseWriter, r *http.Request) error {
	it, err := m.installed(r.Context(), r.PathValue("slug"))
	if err != nil {
		return err
	}
	service := r.URL.Query().Get("service")
	if service == "" {
		service = it.Config.Services[0].Name
	}
	found := false
	for _, s := range it.Config.Services {
		if s.Name == service {
			found = true
		}
	}
	if !found || !serviceRe.MatchString(service) {
		return httpx.BadRequest("Servis adı geçersiz.")
	}
	tail := 200
	if v := r.URL.Query().Get("tail"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 2000 {
			return httpx.BadRequest("Satır sayısı 1 ile 2000 arasında olmalıdır.")
		}
		tail = n
	}
	cli, err := m.eng.ready(r.Context())
	if err != nil {
		return err
	}
	list, err := m.eng.appContainers(r.Context(), cli, it.Slug)
	if err != nil {
		return err
	}
	id := ""
	for _, c := range list {
		if c.Labels[LabelService] == service {
			id = c.ID
		}
	}
	if id == "" {
		return httpx.NewError(http.StatusConflict, "container_missing", "Bu servisin konteyneri bulunamadı.")
	}
	opts := container.LogsOptions{
		ShowStdout: true, ShowStderr: true, Follow: true, Timestamps: true, Tail: strconv.Itoa(tail),
	}
	// A reconnecting browser continues after the last line it received.
	last := r.Header.Get("Last-Event-ID")
	if _, err := time.Parse(time.RFC3339Nano, last); err == nil {
		opts.Since = last
		opts.Tail = "all"
	} else {
		last = ""
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	rc, err := cli.ContainerLogs(ctx, id, opts)
	if err != nil {
		return dockerError(err, "Uygulama logları okunamadı.")
	}
	defer rc.Close()
	if last == "" { // not for automatic reconnects
		m.deps.Audit.Log(r.Context(), auth.ActorFrom(r), "apps.logs", it.Slug, service, true)
	}

	s, err := startSSE(w)
	if err != nil {
		return err
	}
	mask := secretMasker(it.Config)
	lines := make(chan logLine, 256)
	go func() {
		defer close(lines)
		_ = demuxLines(rc, func(stream, line string) bool {
			ts, text := splitTimestamp(line)
			select {
			case lines <- logLine{Service: service, Stream: stream, Time: ts, Line: mask(text)}:
				return true
			case <-ctx.Done():
				return false
			}
		})
	}()
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return nil
		case <-ping.C:
			if s.ping() != nil {
				return nil
			}
		case l, ok := <-lines:
			if !ok {
				// The container stopped. Keep the connection open so
				// that the browser does not reconnect in a loop.
				_ = s.send("end", "", map[string]string{"service": service})
				lines = nil
				continue
			}
			if l.Time != "" && l.Time == last {
				continue // already delivered before the reconnect
			}
			if s.send("log", l.Time, l) != nil {
				return nil
			}
		}
	}
}

/* ---------- icons ---------- */

const maxIconBytes = 512 << 10

// handleIcon serves an application icon from the icon directory. Only plain
// file names ending in .svg or .png are accepted. SVG can carry scripts, so
// the response forbids everything through its Content-Security-Policy and
// is never sniffed into another type.
func (m *Module) handleIcon(w http.ResponseWriter, r *http.Request) error {
	name := r.PathValue("name")
	if !iconRe.MatchString(name) || strings.Contains(name, "..") {
		return httpx.BadRequest("Simge adı geçersiz.")
	}
	dir := m.deps.Cfg.IconDir()
	full := filepath.Join(dir, name)
	if filepath.Dir(full) != filepath.Clean(dir) {
		return httpx.BadRequest("Simge adı geçersiz.")
	}
	st, err := os.Lstat(full)
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxIconBytes {
		return httpx.NotFound("Simge bulunamadı.")
	}
	f, err := os.Open(full)
	if err != nil {
		return httpx.NotFound("Simge bulunamadı.")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxIconBytes+1))
	if err != nil || len(data) > maxIconBytes {
		return httpx.NotFound("Simge bulunamadı.")
	}
	ctype := "image/png"
	if strings.HasSuffix(name, ".svg") {
		ctype = "image/svg+xml"
	} else if len(data) < 8 || string(data[:8]) != "\x89PNG\r\n\x1a\n" {
		return httpx.NotFound("Simge bulunamadı.")
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	h.Set("Content-Disposition", "inline; filename=\""+name+"\"")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Cache-Control", "private, max-age=3600")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
	return nil
}
