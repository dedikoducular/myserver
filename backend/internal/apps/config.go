package apps

import (
	"crypto/rand"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
)

// Label keys set on everything the application system creates.
const (
	LabelApp     = "io.myserver.app"
	LabelService = "io.myserver.service"
	LabelManaged = "io.myserver.managed"
)

// Inputs are the values chosen by the user for one application.
type Inputs struct {
	// Ports maps a port key to the host port.
	Ports map[string]int `json:"ports"`
	// Env maps an environment key to its value. Holds secrets.
	Env map[string]string `json:"env"`
	// Paths maps a bind volume key to the host directory.
	Paths map[string]string `json:"paths"`
	// Options maps an option key to whether the user enabled it.
	Options map[string]bool `json:"options"`
	// BindAddress is BindAll or BindLoopback; "" means BindAll.
	BindAddress string `json:"bind_address"`
}

// Bind address choices for the published ports of an application.
const (
	// BindAll publishes ports on every interface (0.0.0.0).
	BindAll = "all"
	// BindLoopback publishes ports on 127.0.0.1 only.
	BindLoopback = "loopback"
)

// ValidBindAddress reports whether v is a known bind address choice.
func ValidBindAddress(v string) bool { return v == BindAll || v == BindLoopback }

// hostIPFor is the host address of a port binding for a bind choice.
func hostIPFor(bind string) string {
	if bind == BindLoopback {
		return AddrLoopback
	}
	return AddrAny
}

// NormalizeConfig fills the fields added after a configuration was stored:
// a configuration without a bind address publishes on all interfaces.
func NormalizeConfig(cfg *Config) {
	if cfg == nil {
		return
	}
	if cfg.BindAddress == "" {
		cfg.BindAddress = BindAll
	}
	for i := range cfg.Services {
		s := &cfg.Services[i]
		for j := range s.Ports {
			if s.NetworkMode != "bridge" {
				s.Ports[j].HostIP = ""
			} else if s.Ports[j].HostIP == "" {
				s.Ports[j].HostIP = hostIPFor(cfg.BindAddress)
			}
		}
	}
}

func (in *Inputs) fill() {
	if in.Ports == nil {
		in.Ports = map[string]int{}
	}
	if in.Env == nil {
		in.Env = map[string]string{}
	}
	if in.Paths == nil {
		in.Paths = map[string]string{}
	}
	if in.Options == nil {
		in.Options = map[string]bool{}
	}
}

// Config is the resolved configuration of an installed application: exactly
// what its containers are created from. It is stored in apps_installed and
// is sufficient to recreate the application without the manifest.
type Config struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Icon        string `json:"icon"`
	Version     string `json:"version"`
	Network     string `json:"network"` // private network name, "" when unused
	// BindAddress is BindAll ("all") or BindLoopback ("loopback"): where
	// the published ports of bridge services are bound.
	BindAddress string          `json:"bind_address"`
	Services    []ServiceConfig `json:"services"`
}

type ServiceConfig struct {
	Name          string        `json:"name"`
	ContainerName string        `json:"container_name"`
	Image         string        `json:"image"`
	Restart       string        `json:"restart"`
	NetworkMode   string        `json:"network_mode"`
	Privileged    bool          `json:"privileged"`
	CapAdd        []string      `json:"cap_add"`
	Devices       []DeviceSpec  `json:"devices"`
	Command       []string      `json:"command"`
	User          string        `json:"user"`
	ShmSize       int64         `json:"shm_size"`
	Tmpfs         []TmpfsSpec   `json:"tmpfs"`
	Healthcheck   *HealthSpec   `json:"healthcheck"`
	DependsOn     []string      `json:"depends_on"`
	Env           []EnvValue    `json:"env"`
	Ports         []PortBinding `json:"ports"`
	Volumes       []Mount       `json:"volumes"`
}

type EnvValue struct {
	Name   string `json:"name"`
	Key    string `json:"key"`
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
}

type PortBinding struct {
	Key string `json:"key"`
	// HostIP is the host address the port is published on: "0.0.0.0" or
	// "127.0.0.1". It is "" for services using the host's network, which
	// are not published by Docker.
	HostIP    string     `json:"host_ip"`
	Host      int        `json:"host"`
	Container int        `json:"container"`
	Protocol  string     `json:"protocol"`
	Label     string     `json:"label"`
	WebUI     *WebUISpec `json:"web_ui"`
}

// Mount is one volume of a service. For Type "volume" Source is the full
// Docker volume name (myserver-<slug>-<name>); for "bind" and "system" it
// is a host path.
type Mount struct {
	Type     string `json:"type"`
	Key      string `json:"key"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	Label    string `json:"label"`
	ReadOnly bool   `json:"read_only"`
}

// InputError is a problem with a value supplied by the user.
type InputError struct{ Message string }

func (e *InputError) Error() string { return e.Message }

func inputErr(format string, args ...any) error {
	return &InputError{Message: fmt.Sprintf(format, args...)}
}

// NetworkName is the private network of an application.
func NetworkName(slug string) string { return "myserver-" + slug }

// VolumeName is the Docker volume name for a manifest volume.
func VolumeName(slug, name string) string { return "myserver-" + slug + "-" + name }

// ContainerName is the container name of a service.
func ContainerName(slug, service string, single bool) string {
	if single {
		return "myserver-" + slug
	}
	return "myserver-" + slug + "-" + service
}

// forbiddenBind lists host locations that must never be chosen as a data
// folder, even when an administrator has widened the allowed roots.
var forbiddenBind = []string{
	"/etc", "/proc", "/sys", "/dev", "/boot", "/run", "/var/run", "/var/lib/docker",
	"/var/lib/myserver", "/var/lib/myserver-updates", "/usr", "/bin", "/sbin", "/lib", "/lib64", "/root",
}

func within(p, root string) bool {
	if root == "/" {
		return true
	}
	return p == root || strings.HasPrefix(p, root+"/")
}

// CheckBindPath validates a host directory chosen by the user: it must be an
// absolute, normalized path inside one of the allowed roots and outside the
// system locations. It does not touch the file system.
func CheckBindPath(p string, roots []string) error {
	if !cleanAbs(p) {
		return inputErr("Klasör yolu %q geçersiz. Mutlak bir yol girin (örnek: /data/medya).", p)
	}
	if p == "/" {
		return inputErr("Kök dizin (/) uygulamaya bağlanamaz.")
	}
	for _, f := range forbiddenBind {
		if within(p, f) {
			return inputErr("%s bir sistem klasörüdür ve uygulamaya bağlanamaz.", p)
		}
	}
	if strings.HasSuffix(p, "docker.sock") {
		return inputErr("Docker soketi uygulamaya veri klasörü olarak bağlanamaz.")
	}
	for _, r := range roots {
		r = path.Clean(r)
		if r == "" || r[0] != '/' || r == "/" {
			continue
		}
		if within(p, r) {
			return nil
		}
	}
	return inputErr("%s izin verilen klasörlerin dışında. İzin verilen kökler: %s", p, strings.Join(roots, ", "))
}

// checkProtected refuses a host path that lies inside one of the protected
// directories: the panel's own data directory, which holds the database with
// every stored secret and session, wherever it has been placed.
func checkProtected(p string, protected []string) error {
	for _, d := range protected {
		d = path.Clean(d)
		if d == "" || d[0] != '/' || d == "/" {
			continue
		}
		if within(p, d) {
			return inputErr("%s panelin kendi veri klasörüdür ve uygulamaya bağlanamaz.", p)
		}
	}
	return nil
}

// checkProtectedMounts applies checkProtected to the bind mounts of cfg.
func checkProtectedMounts(cfg *Config, protected []string) error {
	for _, s := range cfg.Services {
		for _, v := range s.Volumes {
			if v.Type != VolumeBind {
				continue
			}
			if err := checkProtected(v.Source, protected); err != nil {
				return err
			}
		}
	}
	return nil
}

// secretMasker returns a function that replaces the secret values of cfg in
// a text. It is applied to the output of the application's containers, which
// may print their own configuration, before that output leaves the panel.
func secretMasker(cfg *Config) func(string) string {
	seen := map[string]bool{}
	var values []string
	if cfg != nil {
		for _, s := range cfg.Services {
			for _, e := range s.Env {
				// Very short values would blank out unrelated text.
				if e.Secret && len(e.Value) >= 4 && !seen[e.Value] {
					seen[e.Value] = true
					values = append(values, e.Value)
				}
			}
		}
	}
	if len(values) == 0 {
		return func(s string) string { return s }
	}
	// Longest first, so that a value containing another one goes whole.
	sort.SliceStable(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	pairs := make([]string, 0, 2*len(values))
	for _, v := range values {
		pairs = append(pairs, v, "••••••")
	}
	return strings.NewReplacer(pairs...).Replace
}

const passwordAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"

// GeneratePassword returns a random 32-character secret made of letters and
// digits only, so that it is safe inside connection strings.
func GeneratePassword() (string, error) {
	const n = 32
	out := make([]byte, 0, n)
	buf := make([]byte, 64)
	limit := byte(256 - 256%len(passwordAlphabet))
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if b < limit && len(out) < n {
				out = append(out, passwordAlphabet[int(b)%len(passwordAlphabet)])
			}
		}
	}
	return string(out), nil
}

// Resolve combines a manifest with the user's inputs into the configuration
// the containers are created from, and returns the normalized inputs.
//
// prev holds the values of the existing installation (or the values retained
// from a removed one); it supplies secrets and any value the caller did not
// send. roots are the allowed roots for bind paths.
func Resolve(m *Manifest, in Inputs, prev *Inputs, roots []string) (*Config, *Inputs, error) {
	in.fill()
	if prev == nil {
		prev = &Inputs{}
	}
	prev.fill()
	out := &Inputs{}
	out.fill()

	cfg := &Config{
		Slug: m.Slug, Name: m.Name, Description: m.Description, Category: m.Category,
		Icon: m.Icon, Version: m.Version,
	}
	bind := in.BindAddress
	if bind == "" {
		bind = prev.BindAddress
	}
	if bind == "" {
		bind = m.BindAddress
	}
	if bind == "" {
		bind = BindAll
	}
	if !ValidBindAddress(bind) {
		return nil, nil, inputErr("Erişim adresi seçimi geçersiz.")
	}
	cfg.BindAddress = bind
	out.BindAddress = bind
	single := len(m.Services) == 1

	// Reject keys the manifest does not define, so a typo is not ignored.
	portSpecs := map[string]PortSpec{}
	envSpecs := map[string]EnvSpec{}
	pathSpecs := map[string]VolumeSpec{}
	for _, s := range m.Services {
		for _, p := range s.Ports {
			portSpecs[p.Key] = p
		}
		for _, e := range s.Env {
			// The first user-editable definition of a key governs it.
			if cur, ok := envSpecs[e.Key]; !ok || (cur.Fixed && !e.Fixed) {
				envSpecs[e.Key] = e
			}
		}
		for _, v := range s.Volumes {
			if v.Type == VolumeBind {
				pathSpecs[v.Key] = v
			}
		}
	}
	optSpecs := map[string]OptionSpec{}
	for _, s := range m.Services {
		for _, o := range s.Options {
			optSpecs[o.Key] = o
		}
	}
	for k := range in.Options {
		if _, ok := optSpecs[k]; !ok {
			return nil, nil, inputErr("Bilinmeyen seçenek: %s", k)
		}
	}
	for k, o := range optSpecs {
		v := o.Default
		if given, ok := in.Options[k]; ok {
			v = given
		} else if old, ok := prev.Options[k]; ok {
			v = old
		}
		out.Options[k] = v
	}
	for k := range in.Ports {
		if _, ok := portSpecs[k]; !ok {
			return nil, nil, inputErr("Bilinmeyen port alanı: %s", k)
		}
	}
	for k := range in.Env {
		if s, ok := envSpecs[k]; !ok || s.Fixed {
			return nil, nil, inputErr("Bilinmeyen ayar alanı: %s", k)
		}
	}
	for k := range in.Paths {
		if _, ok := pathSpecs[k]; !ok {
			return nil, nil, inputErr("Bilinmeyen klasör alanı: %s", k)
		}
	}

	// Ports.
	keys := sortedKeys(portSpecs)
	used := map[string]string{}
	for _, k := range keys {
		spec := portSpecs[k]
		host := spec.Host
		if v, ok := in.Ports[k]; ok {
			host = v
		} else if v, ok := prev.Ports[k]; ok {
			host = v
		}
		label := portLabel(spec)
		if !validPort(host) {
			return nil, nil, inputErr("%s: port 1 ile 65535 arasında olmalıdır.", label)
		}
		id := strconv.Itoa(host) + "/" + spec.Protocol
		if other, ok := used[id]; ok {
			return nil, nil, inputErr("%s portu hem \"%s\" hem \"%s\" için seçilmiş.", id, other, label)
		}
		used[id] = label
		out.Ports[k] = host
	}

	// Environment values, by key.
	for _, k := range sortedKeys(envSpecs) {
		spec := envSpecs[k]
		if spec.Fixed {
			continue
		}
		label := spec.Label
		if label == "" {
			label = spec.Name
		}
		val, given := in.Env[k]
		if spec.Secret && val == "" {
			given = false // an empty secret means "keep the current one"
		}
		if !given {
			if v, ok := prev.Env[k]; ok {
				val, given = v, true
			}
		}
		if !given {
			val = spec.Default
			if spec.Generate == "password" {
				p, err := GeneratePassword()
				if err != nil {
					return nil, nil, fmt.Errorf("parola üretilemedi: %w", err)
				}
				val = p
			}
		}
		if !validEnvValue(val) {
			return nil, nil, inputErr("%s: değer çok uzun veya satır sonu içeriyor.", label)
		}
		if spec.Required && strings.TrimSpace(val) == "" {
			return nil, nil, inputErr("%s alanı zorunludur.", label)
		}
		out.Env[k] = val
	}

	// Bind paths, by key.
	for _, k := range sortedKeys(pathSpecs) {
		spec := pathSpecs[k]
		label := spec.Label
		if label == "" {
			label = spec.Target
		}
		val, given := in.Paths[k]
		if !given {
			if v, ok := prev.Paths[k]; ok {
				val, given = v, true
			}
		}
		if !given {
			val = spec.Source
		}
		val = strings.TrimSpace(val)
		if val == "" {
			if spec.Required {
				return nil, nil, inputErr("%s için bir klasör seçmelisiniz.", label)
			}
			out.Paths[k] = ""
			continue
		}
		if len(val) > 1 {
			val = strings.TrimRight(val, "/")
		}
		if err := CheckBindPath(val, roots); err != nil {
			var ie *InputError
			if errors.As(err, &ie) {
				return nil, nil, inputErr("%s: %s", label, ie.Message)
			}
			return nil, nil, err
		}
		out.Paths[k] = val
	}

	expand := func(s string) string {
		return templateRe.ReplaceAllStringFunc(s, func(ref string) string {
			key := templateRe.FindStringSubmatch(ref)[1]
			return strconv.Itoa(out.Ports[key])
		})
	}

	for _, s := range m.Services {
		sc := ServiceConfig{
			Name:          s.Name,
			ContainerName: ContainerName(m.Slug, s.Name, single),
			Image:         s.Image,
			Restart:       s.Restart,
			NetworkMode:   s.NetworkMode,
			Privileged:    s.Privileged,
			CapAdd:        append([]string{}, s.CapAdd...),
			Devices:       append([]DeviceSpec{}, s.Devices...),
			Command:       append([]string{}, s.Command...),
			User:          s.User,
			Tmpfs:         append([]TmpfsSpec{}, s.Tmpfs...),
			Healthcheck:   s.Healthcheck,
			DependsOn:     append([]string{}, s.DependsOn...),
			Env:           []EnvValue{},
			Ports:         []PortBinding{},
			Volumes:       []Mount{},
		}
		if s.ShmSize != "" {
			sc.ShmSize, _ = ParseSize(s.ShmSize)
		}
		for _, o := range s.Options {
			if !out.Options[o.Key] {
				continue
			}
			for _, c := range o.CapAdd {
				if !contains(sc.CapAdd, c) {
					sc.CapAdd = append(sc.CapAdd, c)
				}
			}
		}
		if s.NetworkMode == "bridge" {
			cfg.Network = NetworkName(m.Slug)
		}
		for _, e := range s.Env {
			val := ""
			if e.Fixed {
				val = expand(e.Default)
			} else {
				val = out.Env[e.Key]
			}
			if val == "" {
				continue
			}
			sc.Env = append(sc.Env, EnvValue{Name: e.Name, Key: e.Key, Value: val, Secret: e.Secret})
		}
		for _, p := range s.Ports {
			if p.Option != "" && !out.Options[p.Option] {
				continue
			}
			host := out.Ports[p.Key]
			container := p.Container
			if p.SameAsHost {
				container = host
			}
			if s.NetworkMode == "host" && host != p.Container {
				return nil, nil, inputErr("%s: bu uygulama sunucunun ağını doğrudan kullandığı için port değiştirilemez.", portLabel(p))
			}
			hostIP := ""
			if s.NetworkMode == "bridge" {
				hostIP = hostIPFor(bind)
			}
			sc.Ports = append(sc.Ports, PortBinding{
				Key: p.Key, HostIP: hostIP, Host: host, Container: container, Protocol: p.Protocol,
				Label: p.Label, WebUI: p.WebUI,
			})
		}
		for _, v := range s.Volumes {
			mt := Mount{Type: v.Type, Key: v.Key, Target: v.Target, Label: v.Label, ReadOnly: v.ReadOnly}
			switch v.Type {
			case VolumeNamed:
				mt.Source = VolumeName(m.Slug, v.Source)
			case VolumeBind:
				mt.Source = out.Paths[v.Key]
				if mt.Source == "" {
					continue
				}
			case VolumeSystem:
				mt.Source = v.Source
			}
			sc.Volumes = append(sc.Volumes, mt)
		}
		cfg.Services = append(cfg.Services, sc)
	}
	return cfg, out, nil
}

func portLabel(p PortSpec) string {
	if p.Label != "" {
		return p.Label
	}
	return fmt.Sprintf("Port %d/%s", p.Container, p.Protocol)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ValidateConfig checks a stored or restored configuration before containers
// are created from it. A configuration may come from a backup, so it is not
// trusted: every value is validated again, and anything that widens the
// container's access to the host (privileged mode, capabilities, devices,
// host networking, system mounts) must be declared by the current manifest
// of the application. m may be nil when the manifest no longer exists, in
// which case such settings are refused.
func ValidateConfig(cfg *Config, m *Manifest, roots []string) error {
	if cfg == nil || !ValidSlug(cfg.Slug) {
		return inputErr("Uygulama yapılandırması geçersiz.")
	}
	if len(cfg.Services) == 0 || len(cfg.Services) > 12 {
		return inputErr("Uygulama yapılandırmasında servis tanımı yok.")
	}
	if cfg.BindAddress != "" && !ValidBindAddress(cfg.BindAddress) {
		return inputErr("Erişim adresi seçimi geçersiz.")
	}
	NormalizeConfig(cfg)
	if m != nil && m.Slug != cfg.Slug {
		m = nil
	}
	if !textOK(cfg.Name, 60, false) || cfg.Name == "" {
		return inputErr("Uygulama adı geçersiz.")
	}
	if cfg.Icon != "" && !iconRe.MatchString(cfg.Icon) {
		return inputErr("Uygulama simgesi geçersiz.")
	}
	if cfg.Network != "" && cfg.Network != NetworkName(cfg.Slug) {
		return inputErr("Uygulama ağı adı geçersiz.")
	}
	single := len(cfg.Services) == 1
	names := map[string]bool{}
	specs := []ServiceSpec{}
	for _, s := range cfg.Services {
		if !serviceRe.MatchString(s.Name) || names[s.Name] {
			return inputErr("Servis adı %q geçersiz.", s.Name)
		}
		names[s.Name] = true
		specs = append(specs, ServiceSpec{Name: s.Name, DependsOn: s.DependsOn})
	}
	for _, s := range cfg.Services {
		for _, d := range s.DependsOn {
			if !names[d] {
				return inputErr("Servis %s bilinmeyen %q servisine bağımlı.", s.Name, d)
			}
		}
	}
	if _, err := StartOrder(specs); err != nil {
		return inputErr("Servis bağımlılıkları geçersiz.")
	}
	hostPorts := map[string]bool{}
	for i := range cfg.Services {
		s := &cfg.Services[i]
		if s.ContainerName != ContainerName(cfg.Slug, s.Name, single) {
			return inputErr("Servis %s için konteyner adı geçersiz.", s.Name)
		}
		var ms *ServiceSpec
		if m != nil {
			for j := range m.Services {
				if m.Services[j].Name == s.Name {
					ms = &m.Services[j]
				}
			}
		}
		spec := DockerSpec{
			Image: s.Image, Restart: s.Restart, NetworkMode: s.NetworkMode, CapAdd: s.CapAdd,
			Devices: s.Devices, Command: s.Command, User: s.User, Tmpfs: s.Tmpfs, Healthcheck: s.Healthcheck,
		}
		if err := spec.validate(); err != nil {
			return inputErr("Servis %s: %s", s.Name, err.Error())
		}
		if s.ShmSize < 0 || s.ShmSize > 64<<30 {
			return inputErr("Servis %s: paylaşımlı bellek boyutu geçersiz.", s.Name)
		}
		if s.Privileged && (ms == nil || !ms.Privileged) {
			return inputErr("Servis %s ayrıcalıklı kip istiyor ancak uygulama tanımı buna izin vermiyor.", s.Name)
		}
		if s.NetworkMode == "host" && (ms == nil || ms.NetworkMode != "host") {
			return inputErr("Servis %s sunucu ağını istiyor ancak uygulama tanımı buna izin vermiyor.", s.Name)
		}
		for _, c := range s.CapAdd {
			allowed := ms != nil && contains(ms.CapAdd, c)
			if ms != nil && !allowed {
				for _, o := range ms.Options {
					if contains(o.CapAdd, c) {
						allowed = true
					}
				}
			}
			if !allowed {
				return inputErr("Servis %s için %s yetkisi uygulama tanımında yok.", s.Name, c)
			}
		}
		for _, d := range s.Devices {
			ok := false
			if ms != nil {
				for _, md := range ms.Devices {
					if md.Host == d.Host && md.Container == d.Container && md.Permissions == d.Permissions {
						ok = true
					}
				}
			}
			if !ok {
				return inputErr("Servis %s için %s aygıtı uygulama tanımında yok.", s.Name, d.Host)
			}
		}
		envNames := map[string]bool{}
		for _, e := range s.Env {
			if !envNameRe.MatchString(e.Name) || envNames[e.Name] || !validEnvValue(e.Value) {
				return inputErr("Servis %s: ortam değişkeni %q geçersiz.", s.Name, e.Name)
			}
			envNames[e.Name] = true
		}
		for _, p := range s.Ports {
			if !validPort(p.Host) || !validPort(p.Container) || (p.Protocol != "tcp" && p.Protocol != "udp") {
				return inputErr("Servis %s: port tanımı geçersiz.", s.Name)
			}
			wantIP := ""
			if s.NetworkMode == "bridge" {
				wantIP = hostIPFor(cfg.BindAddress)
			}
			if p.HostIP != wantIP {
				return inputErr("Servis %s: port yayın adresi seçilen erişim adresiyle uyuşmuyor.", s.Name)
			}
			id := strconv.Itoa(p.Host) + "/" + p.Protocol
			if hostPorts[id] {
				return inputErr("%s portu birden fazla kez kullanılmış.", id)
			}
			hostPorts[id] = true
			if s.NetworkMode != "bridge" && (s.NetworkMode != "host" || p.Host != p.Container) {
				return inputErr("Servis %s: bu ağ kipinde port yayınlanamaz.", s.Name)
			}
			if w := p.WebUI; w != nil {
				if (w.Scheme != "http" && w.Scheme != "https") || !strings.HasPrefix(w.Path, "/") ||
					len(w.Path) > 200 || strings.ContainsAny(w.Path, " \t\r\n\\\"<>") {
					return inputErr("Servis %s: web arayüzü tanımı geçersiz.", s.Name)
				}
			}
		}
		targets := map[string]bool{}
		for _, v := range s.Volumes {
			if !cleanAbs(v.Target) || v.Target == "/" || targets[v.Target] {
				return inputErr("Servis %s: birim hedefi %q geçersiz.", s.Name, v.Target)
			}
			targets[v.Target] = true
			switch v.Type {
			case VolumeNamed:
				prefix := "myserver-" + cfg.Slug + "-"
				if !strings.HasPrefix(v.Source, prefix) || !volNameRe.MatchString(strings.TrimPrefix(v.Source, prefix)) {
					return inputErr("Servis %s: birim adı %q geçersiz.", s.Name, v.Source)
				}
			case VolumeBind:
				if err := CheckBindPath(v.Source, roots); err != nil {
					return err
				}
			case VolumeSystem:
				ok := false
				if ms != nil {
					for _, mv := range ms.Volumes {
						if mv.Type == VolumeSystem && mv.Source == v.Source && mv.Target == v.Target &&
							(mv.ReadOnly == v.ReadOnly || v.ReadOnly) {
							ok = true
						}
					}
				}
				if !ok {
					return inputErr("Servis %s: %s sistem yolu uygulama tanımında yok, bağlanamaz.", s.Name, v.Source)
				}
			default:
				return inputErr("Servis %s: birim türü %q bilinmiyor.", s.Name, v.Type)
			}
		}
	}
	return nil
}

// ImageRepository returns the repository of an image reference (registry
// and path, without tag and digest), normalized the way Docker does:
// "nginx", "library/nginx" and "docker.io/library/nginx" are the same
// repository. ok is false for a reference that is not acceptable.
func ImageRepository(ref string) (repo string, ok bool) {
	if !ValidImage(ref) {
		return "", false
	}
	name := ref
	if i := strings.IndexByte(name, '@'); i >= 0 {
		name = name[:i]
	}
	// A colon after the last slash starts the tag; one before it belongs
	// to the port of the registry.
	if i := strings.LastIndexByte(name, ':'); i > strings.LastIndexByte(name, '/') {
		name = name[:i]
	}
	if name == "" {
		return "", false
	}
	domain, rest := "docker.io", name
	if i := strings.IndexByte(name, '/'); i >= 0 {
		first := name[:i]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			domain, rest = strings.ToLower(first), name[i+1:]
		}
	}
	if domain == "index.docker.io" || domain == "registry-1.docker.io" {
		domain = "docker.io"
	}
	if rest == "" {
		return "", false
	}
	if domain == "docker.io" && !strings.Contains(rest, "/") {
		rest = "library/" + rest
	}
	return domain + "/" + rest, true
}

// ConformToManifest makes a restored configuration follow the current
// manifest of its application. A backup file can be uploaded, so the
// configuration in it is untrusted: it must not make the panel run an
// image, command or user the manifest does not declare.
//
// Two kinds of settings are handled differently, on purpose:
//
//   - What identifies the application is compared and REFUSED when it
//     differs: the set of services, the image repository of every service
//     (tag and digest may differ, so that a backup taken before a manifest
//     update still restores), the names of the environment variables, and
//     the volumes.
//   - What the user can never configure is TAKEN FROM THE MANIFEST and the
//     stored value is ignored without an error: command, user, tmpfs, shm
//     size, health check, restart policy, dependencies, fixed environment
//     values and the read-only flag of volumes. Ignoring instead of refusing
//     keeps old backups restorable after a manifest update that legitimately
//     changes, say, a command.
//
// The schema has no entrypoint and no working directory; the containers use
// those of the image. Privileged mode, capabilities, devices, host
// networking, system mounts and bind paths are checked by ValidateConfig.
func ConformToManifest(cfg *Config, m *Manifest) error {
	if cfg == nil || m == nil || m.Slug != cfg.Slug {
		return inputErr("Bu uygulamanın tanım dosyası katalogda yok; yapılandırması doğrulanamadığı için geri yüklenemez. Tanım dosyasını kataloğa ekleyip Yenile düğmesine bastıktan sonra tekrar deneyin.")
	}
	specs := map[string]*ServiceSpec{}
	for i := range m.Services {
		specs[m.Services[i].Name] = &m.Services[i]
	}
	have := map[string]bool{}
	for _, s := range cfg.Services {
		if specs[s.Name] == nil {
			return inputErr("Yapılandırmadaki %q servisi uygulama tanımında yok.", s.Name)
		}
		have[s.Name] = true
	}
	for _, ms := range m.Services {
		if !have[ms.Name] {
			return inputErr("Uygulama tanımındaki %q servisi yapılandırmada eksik.", ms.Name)
		}
	}
	ports := map[string]int{}
	for _, s := range cfg.Services {
		for _, p := range s.Ports {
			ports[p.Key] = p.Host
		}
	}
	expand := func(v string) string {
		return templateRe.ReplaceAllStringFunc(v, func(ref string) string {
			key := templateRe.FindStringSubmatch(ref)[1]
			if host, ok := ports[key]; ok {
				return strconv.Itoa(host)
			}
			for _, ms := range m.Services {
				for _, p := range ms.Ports {
					if p.Key == key {
						return strconv.Itoa(p.Host)
					}
				}
			}
			return ""
		})
	}
	for i := range cfg.Services {
		s := &cfg.Services[i]
		ms := specs[s.Name]

		want, ok1 := ImageRepository(ms.Image)
		got, ok2 := ImageRepository(s.Image)
		if !ok1 || !ok2 || got != want {
			return inputErr("Servis %s: görüntü %q uygulama tanımındaki görüntüyle (%s) aynı depodan değil.", s.Name, s.Image, ms.Image)
		}

		s.Command = append([]string{}, ms.Command...)
		s.User = ms.User
		s.Tmpfs = append([]TmpfsSpec{}, ms.Tmpfs...)
		s.Healthcheck = ms.Healthcheck
		s.Restart = ms.Restart
		s.DependsOn = append([]string{}, ms.DependsOn...)
		s.ShmSize = 0
		if ms.ShmSize != "" {
			s.ShmSize, _ = ParseSize(ms.ShmSize)
		}

		stored := map[string]string{}
		for _, e := range s.Env {
			known := false
			for _, me := range ms.Env {
				if me.Name == e.Name {
					known = true
				}
			}
			if !known {
				return inputErr("Servis %s: %q ortam değişkeni uygulama tanımında yok.", s.Name, e.Name)
			}
			stored[e.Name] = e.Value
		}
		env := []EnvValue{}
		for _, me := range ms.Env {
			val := stored[me.Name]
			if me.Fixed {
				val = expand(me.Default)
			}
			if val == "" {
				continue
			}
			env = append(env, EnvValue{Name: me.Name, Key: me.Key, Value: val, Secret: me.Secret})
		}
		s.Env = env

		for j := range s.Volumes {
			v := &s.Volumes[j]
			if v.Type == VolumeSystem {
				continue // compared with the manifest by ValidateConfig
			}
			found := false
			for _, mv := range ms.Volumes {
				if mv.Type != v.Type || mv.Target != v.Target {
					continue
				}
				if v.Type == VolumeNamed && v.Source != VolumeName(cfg.Slug, mv.Source) {
					continue
				}
				found = true
				v.Key, v.Label, v.ReadOnly = mv.Key, mv.Label, mv.ReadOnly
			}
			if !found {
				return inputErr("Servis %s: %s birimi (%s) uygulama tanımında yok.", s.Name, v.Source, v.Target)
			}
		}
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// InputsFromConfig rebuilds the user inputs from a resolved configuration.
// m, when given, is used to infer which options were enabled: an option
// counts as enabled when the service has all of its capabilities.
func InputsFromConfig(cfg *Config, m *Manifest) *Inputs {
	in := &Inputs{}
	in.fill()
	in.BindAddress = cfg.BindAddress
	if in.BindAddress == "" {
		in.BindAddress = BindAll
	}
	if m != nil {
		for _, ms := range m.Services {
			for _, s := range cfg.Services {
				if s.Name != ms.Name {
					continue
				}
				for _, o := range ms.Options {
					on := true
					for _, c := range o.CapAdd {
						if !contains(s.CapAdd, c) {
							on = false
						}
					}
					in.Options[o.Key] = on
				}
			}
		}
	}
	for _, s := range cfg.Services {
		for _, p := range s.Ports {
			in.Ports[p.Key] = p.Host
		}
		for _, e := range s.Env {
			in.Env[e.Key] = e.Value
		}
		for _, v := range s.Volumes {
			if v.Type == VolumeBind {
				in.Paths[v.Key] = v.Source
			}
		}
	}
	return in
}

// Warning is a security notice shown to the user before installing.
type Warning struct {
	// Level is "danger" for settings that give the application control
	// over the server, "warning" otherwise.
	Level   string `json:"level"`
	Code    string `json:"code"`
	Title   string `json:"title"`
	Message string `json:"message"`
}

// Warnings lists the security-relevant settings of a manifest.
func Warnings(m *Manifest) []Warning {
	out := []Warning{}
	multi := len(m.Services) > 1
	for _, s := range m.Services {
		who := "Bu uygulama"
		if multi {
			who = "\"" + s.Name + "\" servisi"
		}
		if s.Privileged {
			out = append(out, Warning{"danger", "privileged", "Ayrıcalıklı kip",
				who + " ayrıcalıklı (privileged) kipte çalışır. Konteyner, sunucunun tüm aygıtlarına ve çekirdek özelliklerine erişebilir; uygulamadaki bir açık sunucunun tamamını ele geçirmek için kullanılabilir. Yalnızca uygulamaya güveniyorsanız kurun."})
		}
		for _, v := range s.Volumes {
			if v.Type != VolumeSystem {
				continue
			}
			mode := "okuma ve yazma"
			if v.ReadOnly {
				mode = "yalnızca okuma"
			}
			if strings.HasSuffix(v.Source, "docker.sock") {
				out = append(out, Warning{"danger", "docker_socket", "Docker soketine erişim",
					who + " Docker soketine (" + v.Source + ") erişir. Bu erişim, sunucudaki tüm konteynerleri yönetme ve dolaylı olarak sunucuda yönetici (root) yetkisi elde etme imkânı verir. Yalnızca uygulamaya tamamen güveniyorsanız kurun."})
				continue
			}
			out = append(out, Warning{"danger", "system_mount", "Sistem klasörüne erişim",
				fmt.Sprintf("%s sunucudaki %s yoluna %s izniyle erişir. Bu yol izin verilen veri klasörlerinin dışındadır.", who, v.Source, mode)})
		}
		if s.NetworkMode == "host" {
			out = append(out, Warning{"warning", "host_network", "Sunucu ağını doğrudan kullanır",
				who + " ağ yalıtımı olmadan, sunucunun ağ arayüzlerini doğrudan kullanır. Açtığı tüm portlar sunucunun ağında erişilebilir olur."})
		}
		if len(s.CapAdd) > 0 {
			out = append(out, Warning{"warning", "cap_add", "Ek çekirdek yetkileri",
				who + " şu ek yetkilerle çalışır: " + strings.Join(s.CapAdd, ", ") + "."})
		}
		if len(s.Devices) > 0 {
			names := make([]string, 0, len(s.Devices))
			for _, d := range s.Devices {
				names = append(names, d.Host)
			}
			out = append(out, Warning{"warning", "devices", "Donanım aygıtlarına erişim",
				who + " şu aygıtlara erişir: " + strings.Join(names, ", ") + "."})
		}
	}
	return out
}

// NeedsRiskAcceptance reports whether installing requires the user to accept
// a danger-level warning explicitly.
func NeedsRiskAcceptance(m *Manifest) bool {
	for _, w := range Warnings(m) {
		if w.Level == "danger" {
			return true
		}
	}
	return false
}
