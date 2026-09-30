// Package updatescheck holds the portable validators and parsers shared by
// the updates module and the root helper. It imports only the standard
// library so the helper can use it without import cycles.
package updatescheck

import (
	"net/url"
	"regexp"
	"strings"
)

// UpdateScript is the fixed location of the installed self-update script.
const UpdateScript = "/usr/local/share/myserver/scripts/update.sh"

// Upgrade modes accepted by the updates-apt-upgrade helper action.
const (
	ModeUpgrade = "upgrade" // never removes packages
	ModeFull    = "full"    // dist-upgrade: may remove packages

	KernelInclude = "kernel"
	KernelExclude = "no-kernel"
)

var (
	pkgRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)
	archRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,15}$`)
	debVerRe  = regexp.MustCompile(`^[0-9A-Za-z.+:~-]{1,128}$`)
	releaseRe = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z][0-9A-Za-z.-]*)?$`)
	sha256Re  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ValidPackageName reports whether name is a well-formed Debian package name.
func ValidPackageName(name string) bool {
	return len(name) <= 128 && pkgRe.MatchString(name)
}

// ValidArch reports whether arch is a well-formed architecture qualifier.
func ValidArch(arch string) bool { return archRe.MatchString(arch) }

// ValidReleaseVersion reports whether v is an acceptable MyServer release
// version argument (semantic version, optional leading "v").
func ValidReleaseVersion(v string) bool {
	return len(v) <= 64 && releaseRe.MatchString(v)
}

// ValidSHA256 reports whether s is a lowercase hex SHA-256 digest.
func ValidSHA256(s string) bool { return sha256Re.MatchString(s) }

var kernelPrefixes = []string{
	"linux-image", "linux-headers", "linux-modules", "linux-signed",
	"linux-generic", "linux-virtual", "linux-lowlatency", "linux-oem",
	"linux-hwe", "linux-aws", "linux-azure", "linux-gcp", "linux-gke",
	"linux-kvm", "linux-oracle", "linux-raspi", "linux-ibm", "linux-nvidia",
	"linux-tools", "linux-cloud-tools", "linux-crashdump", "linux-realtime",
	"linux-unsigned", "linux-buildinfo", "linux-riscv", "linux-intel",
}

// IsKernelPackage reports whether the package is a kernel image, header,
// module or kernel meta package. linux-firmware, linux-base and
// linux-libc-dev are not kernels and are not matched.
func IsKernelPackage(name string) bool {
	for _, p := range kernelPrefixes {
		if name == p || strings.HasPrefix(name, p+"-") {
			return true
		}
	}
	return false
}

// Package is one package that an upgrade would install.
type Package struct {
	Name      string `json:"name"`
	Arch      string `json:"arch"`
	Current   string `json:"current_version"` // empty when newly installed
	Candidate string `json:"candidate_version"`
	Origin    string `json:"origin"`
	Security  bool   `json:"security"`
	Kernel    bool   `json:"kernel"`
	New       bool   `json:"new"`
}

// Operand is the package as passed to apt-get (name or name:arch).
func (p Package) Operand() string {
	if p.Arch != "" {
		return p.Name + ":" + p.Arch
	}
	return p.Name
}

// ParseSimulation extracts the "Inst" lines of `apt-get --simulate` output.
// Lines that do not match the documented shape, or whose package name fails
// validation, are skipped.
//
//	Inst libc6 [2.39-0ubuntu8.3] (2.39-0ubuntu8.4 Ubuntu:24.04/noble-security [amd64])
//	Inst linux-image-6.8.0-45-generic (6.8.0-45.45 Ubuntu:24.04/noble-updates [amd64])
func ParseSimulation(text string) []Package {
	out := []Package{}
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "Inst ")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		name, rest, _ := strings.Cut(rest, " ")
		var p Package
		var qualified bool
		p.Name, p.Arch, qualified = strings.Cut(name, ":")
		if !ValidPackageName(p.Name) || (qualified && !ValidArch(p.Arch)) {
			continue
		}
		rest = strings.TrimSpace(rest)
		if strings.HasPrefix(rest, "[") {
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				continue
			}
			p.Current = rest[1:end]
			rest = strings.TrimSpace(rest[end+1:])
		}
		if !strings.HasPrefix(rest, "(") {
			continue
		}
		end := strings.IndexByte(rest, ')')
		if end < 0 {
			continue
		}
		inner := strings.TrimSpace(rest[1:end])
		cand, origin, _ := strings.Cut(inner, " ")
		if i := strings.LastIndex(origin, " ["); i >= 0 {
			origin = origin[:i]
		}
		if !debVerRe.MatchString(cand) || (p.Current != "" && !debVerRe.MatchString(p.Current)) {
			continue
		}
		p.Candidate = cand
		p.Origin = strings.TrimSpace(origin)
		if len(p.Origin) > 300 {
			p.Origin = p.Origin[:300]
		}
		p.Security = strings.Contains(p.Origin, "-security")
		p.Kernel = IsKernelPackage(p.Name)
		p.New = p.Current == ""
		if seen[p.Operand()] {
			continue
		}
		seen[p.Operand()] = true
		out = append(out, p)
	}
	return out
}

// Problem is a recognised apt/dpkg failure.
type Problem struct {
	Code    string
	Message string
}

var problems = []struct {
	Problem
	needles []string
}{
	{Problem{"apt_locked", "Paket yöneticisi şu anda başka bir işlem tarafından kullanılıyor (örneğin otomatik güvenlik güncellemeleri). Birkaç dakika sonra yeniden deneyin."},
		[]string{"Could not get lock", "Unable to acquire the dpkg frontend lock", "Unable to lock", "is held by process", "is another process using it"}},
	{Problem{"dpkg_interrupted", "Önceki bir paket işlemi yarıda kalmış. Sunucuda 'sudo dpkg --configure -a' komutu çalıştırılarak düzeltilmesi gerekiyor."},
		[]string{"dpkg was interrupted"}},
	{Problem{"apt_held", "Güncelleme, sabitlenmiş (hold) bir paketin değiştirilmesini gerektiriyor. Paket sabitlendiği için işlem yapılmadı."},
		[]string{"Held packages were changed"}},
	{Problem{"apt_broken", "Paket bağımlılıkları bozuk durumda. Sunucuda 'sudo apt --fix-broken install' komutu çalıştırılarak düzeltilmesi gerekiyor."},
		[]string{"Unmet dependencies", "--fix-broken", "held broken packages"}},
	// The request cannot be satisfied although the system itself is intact.
	{Problem{"apt_unresolvable", "Bazı paketlerin bağımlılıkları karşılanamadığı için güncelleme uygulanamadı. Ayrıntılar için işlem çıktısına bakın."},
		[]string{"have unmet dependencies", "pkgProblemResolver::Resolve generated breaks"}},
	{Problem{"disk_full", "Diskte yeterli boş alan yok. Paket işlemi tamamlanamadı."},
		[]string{"No space left on device", "You don't have enough free space"}},
	{Problem{"apt_network", "Paket depolarına ulaşılamadı. İnternet bağlantısını ve DNS ayarlarını kontrol edin."},
		[]string{"Temporary failure resolving", "Could not resolve", "Failed to fetch", "Could not connect to", "Connection timed out"}},
}

// Classify recognises well-known apt/dpkg failures in command output.
// While apt waits for a lock (DPkg::Lock::Timeout) it prints progress lines
// that quote the lock error; those are not failures and are ignored, so
// that a run which waited and then failed is reported with its real cause.
func Classify(output string) (Problem, bool) {
	if strings.Contains(output, "Waiting for cache lock") {
		var b strings.Builder
		for _, line := range strings.FieldsFunc(output, func(r rune) bool { return r == 10 || r == 13 }) {
			if !strings.HasPrefix(strings.TrimSpace(line), "Waiting for cache lock") {
				b.WriteString(line)
				b.WriteByte(10)
			}
		}
		output = b.String()
	}
	for _, p := range problems {
		for _, n := range p.needles {
			if strings.Contains(output, n) {
				return p.Problem, true
			}
		}
	}
	return Problem{}, false
}

// CodeForMessage maps a helper user message back to its problem code, or
// returns fallback.
func CodeForMessage(message, fallback string) string {
	for _, p := range problems {
		if p.Message == message {
			return p.Code
		}
	}
	return fallback
}

// Transient unit and files of a package upgrade. The directory is owned by
// root and writable only by root, so nobody can plant a symlink in it; its
// files are world-readable (they hold apt's output and the job result).
const (
	AptUnit       = "myserver-apt-upgrade"
	SelfUnit      = "myserver-update"
	AptDir        = "/var/lib/myserver-updates"
	AptLogFile    = AptDir + "/apt-upgrade.log"
	AptResultFile = AptDir + "/apt-upgrade.result"

	StateRunning = "running"
	StateSuccess = "success"
	StateFailed  = "failed"
)

var (
	jobIDRe = regexp.MustCompile(`^[0-9a-f]{16}$`)
	// The same character set scripts/update.sh accepts for MYSERVER_UPDATE_URL.
	updateURLRe = regexp.MustCompile(`^https://[A-Za-z0-9._:/%+~-]+$`)
)

// ValidJobID reports whether id is a panel job identifier.
func ValidJobID(id string) bool { return jobIDRe.MatchString(id) }

// ValidUpdateURL reports whether raw is an acceptable release tarball
// address: HTTPS, no credentials, query or fragment, no whitespace or
// control characters, bounded length, with a host and a file path.
func ValidUpdateURL(raw string) bool {
	if len(raw) > 500 || !updateURLRe.MatchString(raw) {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" ||
		u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	if strings.Contains(u.Path, "..") || strings.Contains(u.Path, "//") {
		return false
	}
	return len(u.Path) > 1 && !strings.HasSuffix(u.Path, "/")
}

// ReleaseTarball is the artifact name update.sh installs for an architecture.
func ReleaseTarball(arch string) string { return "myserver-linux-" + arch + ".tar.gz" }

// SupportedArch reports whether MyServer releases exist for arch.
func SupportedArch(arch string) bool { return arch == "amd64" || arch == "arm64" }

// ParseKV parses "key=value" lines (the format of the result and status
// files). Unknown or malformed lines are ignored; values are single lines.
func ParseKV(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(strings.TrimRight(line, "\r"), "=")
		if !ok || k == "" || len(k) > 40 || len(out) >= 32 {
			continue
		}
		if len(v) > 1000 {
			v = v[:1000]
		}
		out[k] = v
	}
	return out
}

// OneLine makes s safe to store as a key=value value.
func OneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}
