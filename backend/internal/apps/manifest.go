// Package apps is the application system: a manifest-driven catalog of
// self-hosted applications that are installed as Docker containers.
//
// Applications are not known to the code. Each one is described by a YAML
// manifest in the manifest directory; see apps/manifests/README.md for the
// schema. This file holds the manifest types, the parser and the validator.
// It is pure logic and does not touch Docker or the file system.
package apps

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the manifest schema understood by this build.
const SchemaVersion = 1

// DefaultServiceName names the only service of a single-service manifest.
const DefaultServiceName = "app"

// Manifest describes one application.
type Manifest struct {
	Schema          int    `yaml:"schema"`
	Name            string `yaml:"name"`
	Slug            string `yaml:"slug"`
	Description     string `yaml:"description"`
	LongDescription string `yaml:"long_description"`
	Category        string `yaml:"category"`
	Icon            string `yaml:"icon"`
	Version         string `yaml:"version"`
	Website         string `yaml:"website"`
	Notes           string `yaml:"notes"`
	// Architectures lists the CPU architectures (Go GOARCH names) the
	// images support. Empty means "not declared": no restriction.
	Architectures []string `yaml:"architectures"`

	// Single-service form.
	Docker  *DockerSpec  `yaml:"docker"`
	Env     []EnvSpec    `yaml:"env"`
	Ports   []PortSpec   `yaml:"ports"`
	Volumes []VolumeSpec `yaml:"volumes"`

	// Options of the single-service form.
	Options []OptionSpec `yaml:"options"`

	// Multi-service form. After Parse this is always filled: a
	// single-service manifest becomes one service named "app".
	Services []ServiceSpec `yaml:"services"`
}

// DockerSpec is the container definition of a service.
type DockerSpec struct {
	Image       string       `yaml:"image"`
	Restart     string       `yaml:"restart"`
	NetworkMode string       `yaml:"network_mode"`
	Privileged  bool         `yaml:"privileged"`
	CapAdd      []string     `yaml:"cap_add"`
	Devices     []DeviceSpec `yaml:"devices"`
	Command     []string     `yaml:"command"`
	User        string       `yaml:"user"`
	ShmSize     string       `yaml:"shm_size"`
	Tmpfs       []TmpfsSpec  `yaml:"tmpfs"`
	Healthcheck *HealthSpec  `yaml:"healthcheck"`
}

// ServiceSpec is one container of a multi-service application.
type ServiceSpec struct {
	Name       string `yaml:"name"`
	DockerSpec `yaml:",inline"`
	DependsOn  []string     `yaml:"depends_on"`
	Options    []OptionSpec `yaml:"options"`
	Env        []EnvSpec    `yaml:"env"`
	Ports      []PortSpec   `yaml:"ports"`
	Volumes    []VolumeSpec `yaml:"volumes"`
}

// OptionSpec is an opt-in choice offered in the install dialog. When the
// user enables it, the listed capabilities are added to the service.
type OptionSpec struct {
	Key         string   `yaml:"key"`
	Label       string   `yaml:"label"`
	Description string   `yaml:"description"`
	Default     bool     `yaml:"default"`
	CapAdd      []string `yaml:"cap_add"`
}

// knownArch lists the accepted architecture names (Go GOARCH values).
var knownArch = map[string]bool{
	"amd64": true, "arm64": true, "arm": true, "386": true,
	"riscv64": true, "ppc64le": true, "s390x": true,
}

// SupportsArch reports whether the application can run on arch.
func (m *Manifest) SupportsArch(arch string) bool {
	if len(m.Architectures) == 0 {
		return true
	}
	for _, a := range m.Architectures {
		if a == arch {
			return true
		}
	}
	return false
}

type DeviceSpec struct {
	Host        string `yaml:"host" json:"host"`
	Container   string `yaml:"container" json:"container"`
	Permissions string `yaml:"permissions" json:"permissions"`
	// Optional devices are skipped when the host does not have them.
	Optional bool `yaml:"optional" json:"optional"`
}

type TmpfsSpec struct {
	Target string `yaml:"target" json:"target"`
	Size   string `yaml:"size" json:"size"`
}

type HealthSpec struct {
	Test        []string `yaml:"test" json:"test"`
	Interval    string   `yaml:"interval" json:"interval"`
	Timeout     string   `yaml:"timeout" json:"timeout"`
	StartPeriod string   `yaml:"start_period" json:"start_period"`
	Retries     int      `yaml:"retries" json:"retries"`
}

type EnvSpec struct {
	Name        string `yaml:"name"`
	Key         string `yaml:"key"`
	Label       string `yaml:"label"`
	Description string `yaml:"description"`
	Default     string `yaml:"default"`
	Required    bool   `yaml:"required"`
	Secret      bool   `yaml:"secret"`
	Generate    string `yaml:"generate"`
	// Fixed values are set by the manifest and not offered to the user.
	Fixed bool `yaml:"fixed"`
}

type WebUISpec struct {
	Scheme string `yaml:"scheme" json:"scheme"`
	Path   string `yaml:"path" json:"path"`
}

type PortSpec struct {
	Key       string     `yaml:"key"`
	Host      int        `yaml:"host"`
	Container int        `yaml:"container"`
	Protocol  string     `yaml:"protocol"`
	Label     string     `yaml:"label"`
	WebUI     *WebUISpec `yaml:"web_ui"`
	// SameAsHost makes the container port follow the host port chosen by
	// the user, for applications that must know their public port.
	SameAsHost bool `yaml:"same_as_host"`
	// Option, when set, publishes the port only while that option of the
	// same service is enabled.
	Option string `yaml:"option"`
}

// Volume types.
const (
	VolumeNamed  = "volume"
	VolumeBind   = "bind"
	VolumeSystem = "system"
)

type VolumeSpec struct {
	Type        string `yaml:"type"`
	Key         string `yaml:"key"`
	Source      string `yaml:"source"`
	Target      string `yaml:"target"`
	Label       string `yaml:"label"`
	Description string `yaml:"description"`
	ReadOnly    bool   `yaml:"read_only"`
	Required    bool   `yaml:"required"`
}

// Category is an application category with its Turkish label.
type Category struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Categories lists the known categories in display order.
var Categories = []Category{
	{"media", "Medya"},
	{"download", "İndirme"},
	{"management", "Yönetim"},
	{"smart_home", "Akıllı Ev"},
	{"tools", "Araçlar"},
	{"network", "Ağ"},
	{"cloud", "Bulut"},
	{"security", "Güvenlik"},
	{"other", "Diğer"},
}

func knownCategory(id string) bool {
	for _, c := range Categories {
		if c.ID == id {
			return true
		}
	}
	return false
}

var (
	slugRe    = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$`)
	serviceRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$`)
	keyRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	iconRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}\.(?:svg|png)$`)
	volNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)
	deviceRe  = regexp.MustCompile(`^/dev/[A-Za-z0-9_./-]{1,120}$`)
	userRe    = regexp.MustCompile(`^[a-z0-9_][a-z0-9_-]{0,31}(?::[a-z0-9_][a-z0-9_-]{0,31})?$`)
	sizeRe    = regexp.MustCompile(`^([0-9]{1,6})(k|m|g|kb|mb|gb)$`)
	imageRe   = regexp.MustCompile(`^(?:[a-z0-9]+(?:[.-][a-z0-9]+)*(?::[0-9]{1,5})?/)?` +
		`[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*` +
		`(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?(?:@sha256:[a-f0-9]{64})?$`)
	templateRe = regexp.MustCompile(`\$\{port:([A-Za-z0-9_.-]+)\}`)
)

var knownCaps = map[string]bool{
	"AUDIT_CONTROL": true, "AUDIT_READ": true, "AUDIT_WRITE": true, "BLOCK_SUSPEND": true,
	"BPF": true, "CHECKPOINT_RESTORE": true, "CHOWN": true, "DAC_OVERRIDE": true,
	"DAC_READ_SEARCH": true, "FOWNER": true, "FSETID": true, "IPC_LOCK": true, "IPC_OWNER": true,
	"KILL": true, "LEASE": true, "LINUX_IMMUTABLE": true, "MAC_ADMIN": true, "MAC_OVERRIDE": true,
	"MKNOD": true, "NET_ADMIN": true, "NET_BIND_SERVICE": true, "NET_BROADCAST": true,
	"NET_RAW": true, "PERFMON": true, "SETFCAP": true, "SETGID": true, "SETPCAP": true,
	"SETUID": true, "SYSLOG": true, "SYS_ADMIN": true, "SYS_BOOT": true, "SYS_CHROOT": true,
	"SYS_MODULE": true, "SYS_NICE": true, "SYS_PACCT": true, "SYS_PTRACE": true,
	"SYS_RAWIO": true, "SYS_RESOURCE": true, "SYS_TIME": true, "SYS_TTY_CONFIG": true,
	"WAKE_ALARM": true,
}

// ValidImage reports whether s is an acceptable image reference.
func ValidImage(s string) bool {
	return len(s) > 0 && len(s) <= 255 && imageRe.MatchString(s)
}

// ValidSlug reports whether s is an acceptable application slug.
func ValidSlug(s string) bool { return slugRe.MatchString(s) }

// cleanAbs reports whether p is an absolute, normalized POSIX path without
// control characters or characters that are special in a bind specification.
func cleanAbs(p string) bool {
	if p == "" || len(p) > 1024 || p[0] != '/' || path.Clean(p) != p {
		return false
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f || r == ':' || r == '\\' {
			return false
		}
	}
	return true
}

// ParseSize converts "128m", "1g" ... to bytes.
func ParseSize(s string) (int64, error) {
	m := sizeRe.FindStringSubmatch(strings.ToLower(strings.TrimSpace(s)))
	if m == nil {
		return 0, fmt.Errorf("geçersiz boyut %q (örnek: 128m, 1g)", s)
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	switch m[2][0] {
	case 'k':
		n <<= 10
	case 'm':
		n <<= 20
	case 'g':
		n <<= 30
	}
	if n <= 0 || n > 64<<30 {
		return 0, fmt.Errorf("boyut %q izin verilen aralığın dışında", s)
	}
	return n, nil
}

// Parse decodes and validates one manifest. Unknown fields are rejected so
// that a typo cannot silently drop a setting.
func Parse(data []byte) (*Manifest, error) {
	if len(data) > 256<<10 {
		return nil, errors.New("manifest dosyası çok büyük")
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("YAML okunamadı: %w", err)
	}
	if err := m.normalize(); err != nil {
		return nil, err
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

func (m *Manifest) normalize() error {
	single := m.Docker != nil || len(m.Env) > 0 || len(m.Ports) > 0 || len(m.Volumes) > 0 || len(m.Options) > 0
	if single && len(m.Services) > 0 {
		return errors.New("docker/env/ports/volumes ile services aynı anda kullanılamaz")
	}
	if single {
		if m.Docker == nil {
			return errors.New("docker.image zorunludur")
		}
		m.Services = []ServiceSpec{{
			Name: DefaultServiceName, DockerSpec: *m.Docker,
			Env: m.Env, Ports: m.Ports, Volumes: m.Volumes, Options: m.Options,
		}}
		m.Docker, m.Env, m.Ports, m.Volumes, m.Options = nil, nil, nil, nil, nil
	}
	if m.Schema == 0 {
		m.Schema = SchemaVersion
	}
	m.Name = strings.TrimSpace(m.Name)
	m.Description = strings.TrimSpace(m.Description)
	m.LongDescription = strings.TrimSpace(m.LongDescription)
	m.Notes = strings.TrimSpace(m.Notes)
	if m.Category == "" {
		m.Category = "other"
	}
	for i := range m.Services {
		s := &m.Services[i]
		if s.Restart == "" {
			s.Restart = "unless-stopped"
		}
		if s.NetworkMode == "" {
			s.NetworkMode = "bridge"
		}
		for j := range s.Env {
			if s.Env[j].Key == "" {
				s.Env[j].Key = s.Env[j].Name
			}
		}
		for j := range s.Ports {
			p := &s.Ports[j]
			p.Protocol = strings.ToLower(p.Protocol)
			if p.Protocol == "" {
				p.Protocol = "tcp"
			}
			if p.Host == 0 {
				p.Host = p.Container
			}
			if p.Key == "" {
				p.Key = fmt.Sprintf("%s-%d-%s", s.Name, p.Container, p.Protocol)
			}
			if p.WebUI != nil {
				if p.WebUI.Scheme == "" {
					p.WebUI.Scheme = "http"
				}
				if p.WebUI.Path == "" {
					p.WebUI.Path = "/"
				}
			}
		}
		for j := range s.Volumes {
			v := &s.Volumes[j]
			if v.Type == "" {
				v.Type = VolumeNamed
			}
			if v.Key == "" {
				v.Key = s.Name + "-" + strings.Trim(strings.ReplaceAll(v.Target, "/", "-"), "-")
			}
		}
		for j := range s.Devices {
			d := &s.Devices[j]
			if d.Container == "" {
				d.Container = d.Host
			}
			if d.Permissions == "" {
				d.Permissions = "rwm"
			}
		}
	}
	return nil
}

func textOK(s string, max int, multiline bool) bool {
	if len(s) > max {
		return false
	}
	for _, r := range s {
		if r == '\n' || r == '\t' {
			if !multiline {
				return false
			}
			continue
		}
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validPort(n int) bool { return n >= 1 && n <= 65535 }

func (m *Manifest) validate() error {
	if m.Schema != SchemaVersion {
		return fmt.Errorf("desteklenmeyen şema sürümü %d", m.Schema)
	}
	if m.Name == "" || !textOK(m.Name, 60, false) {
		return errors.New("name boş veya geçersiz")
	}
	if !slugRe.MatchString(m.Slug) {
		return fmt.Errorf("slug %q geçersiz (küçük harf, rakam ve tire; en fazla 32 karakter)", m.Slug)
	}
	if m.Description == "" || !textOK(m.Description, 120, false) {
		return errors.New("description boş veya geçersiz")
	}
	if !textOK(m.LongDescription, 4000, true) || !textOK(m.Notes, 4000, true) {
		return errors.New("long_description veya notes çok uzun ya da geçersiz karakter içeriyor")
	}
	if !knownCategory(m.Category) {
		return fmt.Errorf("category %q bilinmiyor", m.Category)
	}
	if m.Icon != "" && !iconRe.MatchString(m.Icon) {
		return fmt.Errorf("icon %q geçersiz (yalnızca .svg veya .png dosya adı)", m.Icon)
	}
	if !textOK(m.Version, 40, false) {
		return errors.New("version geçersiz")
	}
	if m.Website != "" {
		u, err := url.Parse(m.Website)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(m.Website) > 300 {
			return fmt.Errorf("website %q geçersiz", m.Website)
		}
	}
	seenArch := map[string]bool{}
	for _, a := range m.Architectures {
		if !knownArch[a] || seenArch[a] {
			return fmt.Errorf("architectures: %q bilinmiyor veya yinelenmiş (amd64, arm64, arm, 386, riscv64, ppc64le, s390x)", a)
		}
		seenArch[a] = true
	}
	if len(m.Services) == 0 {
		return errors.New("en az bir servis (docker.image) tanımlanmalıdır")
	}
	if len(m.Services) > 12 {
		return errors.New("en fazla 12 servis tanımlanabilir")
	}

	names := map[string]bool{}
	for _, s := range m.Services {
		if !serviceRe.MatchString(s.Name) {
			return fmt.Errorf("servis adı %q geçersiz", s.Name)
		}
		if names[s.Name] {
			return fmt.Errorf("servis adı %q birden fazla kez kullanılmış", s.Name)
		}
		names[s.Name] = true
	}

	portKeys := map[string]bool{}
	hostPorts := map[string]bool{}
	webUI := 0
	envByKey := map[string]EnvSpec{}
	volKeys := map[string]bool{}
	optKeys := map[string]bool{}
	for i := range m.Services {
		s := &m.Services[i]
		where := "servis " + s.Name + ": "
		if err := s.DockerSpec.validate(); err != nil {
			return fmt.Errorf("%s%w", where, err)
		}
		for _, d := range s.DependsOn {
			if !names[d] || d == s.Name {
				return fmt.Errorf("%sdepends_on %q bilinmeyen bir servis", where, d)
			}
		}
		if s.NetworkMode != "bridge" && len(m.Services) > 1 && len(s.DependsOn) > 0 && s.NetworkMode == "none" {
			return fmt.Errorf("%sağı olmayan bir servis başka servislere bağımlı olamaz", where)
		}

		for _, o := range s.Options {
			if !keyRe.MatchString(o.Key) || optKeys[o.Key] {
				return fmt.Errorf("%sseçenek anahtarı %q geçersiz veya yinelenmiş", where, o.Key)
			}
			optKeys[o.Key] = true
			if o.Label == "" || !textOK(o.Label, 80, false) || !textOK(o.Description, 600, false) {
				return fmt.Errorf("%sseçenek %s: etiket boş ya da etiket/açıklama geçersiz", where, o.Key)
			}
			if len(o.CapAdd) == 0 {
				return fmt.Errorf("%sseçenek %s: cap_add boş olamaz", where, o.Key)
			}
			for _, c := range o.CapAdd {
				if !knownCaps[c] {
					return fmt.Errorf("%sseçenek %s: cap_add %q bilinmiyor", where, o.Key, c)
				}
			}
		}

		envNames := map[string]bool{}
		for _, e := range s.Env {
			if !envNameRe.MatchString(e.Name) {
				return fmt.Errorf("%sortam değişkeni adı %q geçersiz", where, e.Name)
			}
			if envNames[e.Name] {
				return fmt.Errorf("%sortam değişkeni %q birden fazla kez tanımlanmış", where, e.Name)
			}
			envNames[e.Name] = true
			if !keyRe.MatchString(e.Key) {
				return fmt.Errorf("%sortam değişkeni anahtarı %q geçersiz", where, e.Key)
			}
			if e.Generate != "" && e.Generate != "password" {
				return fmt.Errorf("%s%s: generate değeri %q bilinmiyor", where, e.Name, e.Generate)
			}
			if e.Generate != "" && !e.Secret {
				return fmt.Errorf("%s%s: generate yalnızca secret alanlarda kullanılabilir", where, e.Name)
			}
			if e.Secret && e.Default != "" {
				return fmt.Errorf("%s%s: secret alanın varsayılan değeri olamaz", where, e.Name)
			}
			if e.Fixed && (e.Secret || e.Required) {
				return fmt.Errorf("%s%s: fixed alan secret veya required olamaz", where, e.Name)
			}
			if !textOK(e.Label, 80, false) || !textOK(e.Description, 400, false) || !validEnvValue(e.Default) {
				return fmt.Errorf("%s%s: etiket, açıklama veya varsayılan değer geçersiz", where, e.Name)
			}
			if prev, ok := envByKey[e.Key]; ok {
				if prev.Secret != e.Secret {
					return fmt.Errorf("%sanahtar %q hem secret hem secret olmayan alanlarda kullanılmış", where, e.Key)
				}
			} else {
				envByKey[e.Key] = e
			}
		}

		targets := map[string]bool{}
		for _, p := range s.Ports {
			if !validPort(p.Container) || !validPort(p.Host) {
				return fmt.Errorf("%sport %d:%d geçersiz (1-65535)", where, p.Host, p.Container)
			}
			if p.Protocol != "tcp" && p.Protocol != "udp" {
				return fmt.Errorf("%sport %d: protokol %q bilinmiyor (tcp/udp)", where, p.Container, p.Protocol)
			}
			if !keyRe.MatchString(p.Key) || portKeys[p.Key] {
				return fmt.Errorf("%sport anahtarı %q geçersiz veya yinelenmiş", where, p.Key)
			}
			portKeys[p.Key] = true
			hp := fmt.Sprintf("%d/%s", p.Host, p.Protocol)
			if hostPorts[hp] {
				return fmt.Errorf("%ssunucu portu %s birden fazla kez kullanılmış", where, hp)
			}
			hostPorts[hp] = true
			if p.Option != "" {
				found := false
				for _, o := range s.Options {
					if o.Key == p.Option {
						found = true
					}
				}
				if !found {
					return fmt.Errorf("%sport %d: option %q bu serviste tanımlı değil", where, p.Container, p.Option)
				}
				if p.WebUI != nil {
					return fmt.Errorf("%sport %d: seçeneğe bağlı port web_ui olamaz", where, p.Container)
				}
			}
			if !textOK(p.Label, 80, false) {
				return fmt.Errorf("%sport %d: etiket geçersiz", where, p.Container)
			}
			if s.NetworkMode == "none" {
				return fmt.Errorf("%sağı olmayan servis port yayınlayamaz", where)
			}
			if s.NetworkMode == "host" && (p.Host != p.Container || p.SameAsHost) {
				return fmt.Errorf("%shost ağ kipinde sunucu ve konteyner portu aynı olmalıdır (%d)", where, p.Container)
			}
			if p.WebUI != nil {
				webUI++
				if p.Protocol != "tcp" {
					return fmt.Errorf("%sweb_ui yalnızca tcp portlarda kullanılabilir", where)
				}
				if p.WebUI.Scheme != "http" && p.WebUI.Scheme != "https" {
					return fmt.Errorf("%sweb_ui.scheme %q bilinmiyor", where, p.WebUI.Scheme)
				}
				if !strings.HasPrefix(p.WebUI.Path, "/") || len(p.WebUI.Path) > 200 || strings.ContainsAny(p.WebUI.Path, " \t\r\n\\\"<>") {
					return fmt.Errorf("%sweb_ui.path geçersiz", where)
				}
			}
		}

		for _, v := range s.Volumes {
			if !cleanAbs(v.Target) || v.Target == "/" {
				return fmt.Errorf("%sbirim hedefi %q mutlak ve düzgün bir yol olmalıdır", where, v.Target)
			}
			if targets[v.Target] {
				return fmt.Errorf("%sbirim hedefi %q birden fazla kez kullanılmış", where, v.Target)
			}
			targets[v.Target] = true
			if !textOK(v.Label, 80, false) || !textOK(v.Description, 400, false) {
				return fmt.Errorf("%sbirim %s: etiket veya açıklama geçersiz", where, v.Target)
			}
			switch v.Type {
			case VolumeNamed:
				if !volNameRe.MatchString(v.Source) {
					return fmt.Errorf("%sbirim adı %q geçersiz", where, v.Source)
				}
			case VolumeBind:
				if !keyRe.MatchString(v.Key) || volKeys[v.Key] {
					return fmt.Errorf("%sbirim anahtarı %q geçersiz veya yinelenmiş", where, v.Key)
				}
				volKeys[v.Key] = true
				if v.Source != "" && !cleanAbs(v.Source) {
					return fmt.Errorf("%svarsayılan klasör %q mutlak bir yol olmalıdır", where, v.Source)
				}
			case VolumeSystem:
				if !cleanAbs(v.Source) {
					return fmt.Errorf("%ssistem yolu %q mutlak ve düzgün bir yol olmalıdır", where, v.Source)
				}
				if v.Source == "/" {
					return fmt.Errorf("%skök dizin (/) bağlanamaz", where)
				}
			default:
				return fmt.Errorf("%sbirim türü %q bilinmiyor (volume/bind/system)", where, v.Type)
			}
		}
	}
	if webUI > 1 {
		return errors.New("yalnızca bir port web_ui olarak işaretlenebilir")
	}

	// ${port:KEY} references must point at declared ports.
	for _, s := range m.Services {
		for _, e := range s.Env {
			for _, ref := range templateRe.FindAllStringSubmatch(e.Default, -1) {
				if !portKeys[ref[1]] {
					return fmt.Errorf("servis %s: %s değeri bilinmeyen %q portuna başvuruyor", s.Name, e.Name, ref[1])
				}
				if !e.Fixed {
					return fmt.Errorf("servis %s: %s port başvurusu içerdiği için fixed olmalıdır", s.Name, e.Name)
				}
			}
		}
	}
	if _, err := StartOrder(m.Services); err != nil {
		return err
	}
	return nil
}

func validEnvValue(s string) bool {
	if len(s) > 4096 {
		return false
	}
	for _, r := range s {
		if r == 0 || r == '\n' || r == '\r' {
			return false
		}
	}
	return true
}

func (d *DockerSpec) validate() error {
	if !ValidImage(d.Image) {
		return fmt.Errorf("image %q geçerli bir görüntü adı değil", d.Image)
	}
	switch d.Restart {
	case "no", "always", "unless-stopped", "on-failure":
	default:
		return fmt.Errorf("restart %q bilinmiyor", d.Restart)
	}
	switch d.NetworkMode {
	case "bridge", "host", "none":
	default:
		return fmt.Errorf("network_mode %q bilinmiyor (bridge/host/none)", d.NetworkMode)
	}
	for _, c := range d.CapAdd {
		if !knownCaps[c] {
			return fmt.Errorf("cap_add %q bilinmiyor (CAP_ öneki olmadan, büyük harfle yazın)", c)
		}
	}
	for _, dev := range d.Devices {
		if !validDevice(dev.Host) || !validDevice(dev.Container) {
			return fmt.Errorf("aygıt %q geçersiz (/dev altında olmalıdır)", dev.Host)
		}
		if !validDevicePerm(dev.Permissions) {
			return fmt.Errorf("aygıt %s: izinler %q geçersiz (r, w, m)", dev.Host, dev.Permissions)
		}
	}
	if len(d.Command) > 64 {
		return errors.New("command çok uzun")
	}
	for _, c := range d.Command {
		if !validEnvValue(c) {
			return errors.New("command geçersiz karakter içeriyor")
		}
	}
	if d.User != "" && !userRe.MatchString(d.User) {
		return fmt.Errorf("user %q geçersiz", d.User)
	}
	if d.ShmSize != "" {
		if _, err := ParseSize(d.ShmSize); err != nil {
			return fmt.Errorf("shm_size: %w", err)
		}
	}
	for _, t := range d.Tmpfs {
		if !cleanAbs(t.Target) || t.Target == "/" {
			return fmt.Errorf("tmpfs hedefi %q geçersiz", t.Target)
		}
		if t.Size != "" {
			if _, err := ParseSize(t.Size); err != nil {
				return fmt.Errorf("tmpfs %s: %w", t.Target, err)
			}
		}
	}
	if h := d.Healthcheck; h != nil {
		if len(h.Test) < 2 || (h.Test[0] != "CMD" && h.Test[0] != "CMD-SHELL") {
			return errors.New(`healthcheck.test ["CMD", ...] veya ["CMD-SHELL", "..."] biçiminde olmalıdır`)
		}
		for _, t := range h.Test {
			if !validEnvValue(t) {
				return errors.New("healthcheck.test geçersiz karakter içeriyor")
			}
		}
		for _, v := range []string{h.Interval, h.Timeout, h.StartPeriod} {
			if v == "" {
				continue
			}
			dur, err := time.ParseDuration(v)
			if err != nil || dur < time.Second || dur > time.Hour {
				return fmt.Errorf("healthcheck süresi %q geçersiz (örnek: 30s)", v)
			}
		}
		if h.Retries < 0 || h.Retries > 100 {
			return errors.New("healthcheck.retries geçersiz")
		}
	}
	return nil
}

func validDevice(p string) bool {
	return deviceRe.MatchString(p) && path.Clean(p) == p
}

func validDevicePerm(p string) bool {
	if p == "" || len(p) > 3 {
		return false
	}
	seen := map[rune]bool{}
	for _, r := range p {
		if (r != 'r' && r != 'w' && r != 'm') || seen[r] {
			return false
		}
		seen[r] = true
	}
	return true
}

// StartOrder returns service names ordered so that every service comes after
// the services it depends on. It fails on a dependency cycle.
func StartOrder(services []ServiceSpec) ([]string, error) {
	deps := map[string][]string{}
	order := make([]string, 0, len(services))
	for _, s := range services {
		deps[s.Name] = s.DependsOn
	}
	state := map[string]int{} // 1 = visiting, 2 = done
	var visit func(string) error
	visit = func(n string) error {
		switch state[n] {
		case 1:
			return fmt.Errorf("servis bağımlılıklarında döngü var (%s)", n)
		case 2:
			return nil
		}
		state[n] = 1
		for _, d := range deps[n] {
			if err := visit(d); err != nil {
				return err
			}
		}
		state[n] = 2
		order = append(order, n)
		return nil
	}
	for _, s := range services {
		if err := visit(s.Name); err != nil {
			return nil, err
		}
	}
	return order, nil
}
