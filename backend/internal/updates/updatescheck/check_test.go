package updatescheck

import (
	"embed"
	"reflect"
	"strings"
	"testing"
)

// testdata/captured holds output recorded from apt 2.8 inside an
// ubuntu:24.04 container; testdata/memory holds output written from memory
// of what apt prints (it could not be produced by a read-only simulation).
//
//go:embed testdata
var fixtures embed.FS

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := fixtures.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	// The repository may be checked out with CRLF line ends on Windows.
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

func instLines(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "Inst ") {
			out = append(out, l)
		}
	}
	return out
}

func find(list []Package, operand string) *Package {
	for i := range list {
		if list[i].Operand() == operand {
			return &list[i]
		}
	}
	return nil
}

func TestParseSimulationCapturedDistUpgrade(t *testing.T) {
	got := ParseSimulation(fixture(t, "captured/sim-dist-upgrade.txt"))
	want := []Package{
		{Name: "perl-base", Current: "5.38.2-3.2ubuntu0.4", Candidate: "5.38.2-3.2ubuntu0.6",
			Origin: "Ubuntu:24.04/noble-updates, Ubuntu:24.04/noble-security", Security: true},
		{Name: "libaudit-common", Current: "1:3.1.2-2.1build1.1", Candidate: "1:3.1.2-2.1ubuntu0.1",
			Origin: "Ubuntu:24.04/noble-updates"},
		{Name: "libaudit1", Current: "1:3.1.2-2.1build1.1", Candidate: "1:3.1.2-2.1ubuntu0.1",
			Origin: "Ubuntu:24.04/noble-updates"},
		{Name: "libssl3t64", Current: "3.0.13-0ubuntu3.15", Candidate: "3.0.13-0ubuntu3.16",
			Origin: "Ubuntu:24.04/noble-updates, Ubuntu:24.04/noble-security", Security: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestParseSimulationEmptyAndNoise(t *testing.T) {
	for _, text := range []string{
		"",
		"\n\n",
		"Reading package lists...\nBuilding dependency tree...\n0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n",
		"Conf libc6 (2.39-0ubuntu8.4 Ubuntu:24.04/noble-updates [amd64])\n",
		"Remv libfoo1 [1.0-1]\n",
		"Purg libfoo1 [1.0-1]\n",
		"Instlibc6 [1] (2 Ubuntu:24.04/noble [amd64])\n",
		"Inst\n",
		"Inst \n",
		"Inst libc6\n",
		"Inst libc6 [2.39\n",
		"Inst libc6 [2.39] (2.40 Ubuntu:24.04/noble [amd64]\n",
		"Inst libc6 [2.39] 2.40\n",
		"  libc6 linux-image-generic\n",
	} {
		got := ParseSimulation(text)
		if got == nil {
			t.Errorf("%q: result is nil; the API must return [] for an empty list", text)
		}
		if len(got) != 0 {
			t.Errorf("%q: parsed %+v", text, got)
		}
	}
}

func TestParseSimulationMixed(t *testing.T) {
	got := ParseSimulation(fixture(t, "memory/sim-mixed.txt"))
	const both = "Ubuntu:24.04/noble-updates, Ubuntu:24.04/noble-security"
	want := []Package{
		{Name: "base-files", Current: "13ubuntu10.1", Candidate: "13ubuntu10.2", Origin: "Ubuntu:24.04/noble-updates"},
		{Name: "libc6", Current: "2.39-0ubuntu8.3", Candidate: "2.39-0ubuntu8.4", Origin: both, Security: true},
		{Name: "libc6", Arch: "i386", Current: "2.39-0ubuntu8.3", Candidate: "2.39-0ubuntu8.4", Origin: both, Security: true},
		{Name: "libgnutls30t64", Current: "3.8.3-1.1ubuntu3.2", Candidate: "3.8.3-1.1ubuntu3.3", Origin: "Ubuntu:24.04/noble-security", Security: true},
		{Name: "openssh-server", Current: "1:9.6p1-3ubuntu13.5", Candidate: "1:9.6p1-3ubuntu13.8", Origin: "Ubuntu:24.04/noble-updates"},
		{Name: "vim", Current: "2:9.1.0016-1ubuntu7.5", Candidate: "2:9.1.0016-1ubuntu7.6~really1", Origin: "UbuntuESMApps:24.04/noble-apps-security", Security: true},
		{Name: "linux-firmware", Current: "20240318.git3b128b60-0ubuntu2.6", Candidate: "20240318.git3b128b60-0ubuntu2.7", Origin: "Ubuntu:24.04/noble-updates"},
		{Name: "linux-libc-dev", Current: "6.8.0-44.44", Candidate: "6.8.0-45.45", Origin: both, Security: true},
		{Name: "linux-modules-6.8.0-45-generic", Candidate: "6.8.0-45.45", Origin: both, Security: true, Kernel: true, New: true},
		{Name: "linux-image-6.8.0-45-generic", Candidate: "6.8.0-45.45", Origin: both, Security: true, Kernel: true, New: true},
		{Name: "linux-generic", Current: "6.8.0-44.44", Candidate: "6.8.0-45.45", Origin: both, Security: true, Kernel: true},
	}
	if !reflect.DeepEqual(got, want) {
		for i := range got {
			if i >= len(want) || !reflect.DeepEqual(got[i], want[i]) {
				t.Errorf("entry %d: got %+v", i, got[i])
			}
		}
		t.Fatalf("got %d packages, want %d", len(got), len(want))
	}
	// Removed, deferred (phased) and kept-back packages are not upgrades.
	for _, name := range []string{"libfoo1", "linux-image-6.8.0-31-generic", "fwupd", "libfwupd2",
		"python3-distupgrade", "ubuntu-release-upgrader-core", "docker-ce", "linux-headers-6.8.0-31"} {
		if find(got, name) != nil {
			t.Errorf("%s must not be listed as an upgrade", name)
		}
	}
	if p := find(got, "libc6:i386"); p == nil || p.Operand() != "libc6:i386" {
		t.Errorf("foreign architecture operand: %+v", p)
	}
	if p := find(got, "libc6"); p == nil || p.Arch != "" {
		t.Errorf("native package: %+v", p)
	}
}

func TestParseSimulationKeptBackIsNotListed(t *testing.T) {
	got := ParseSimulation(fixture(t, "captured/sim-upgrade-held.txt"))
	if len(got) != 3 || find(got, "libssl3t64") != nil {
		t.Errorf("a held package was listed: %+v", got)
	}
}

// Every Inst line apt printed must be parsed: a line that is silently
// dropped would hide a package (possibly a kernel) from the kernel check.
func TestParseSimulationCapturedNothingDropped(t *testing.T) {
	for _, name := range []string{
		"captured/sim-dist-upgrade.txt", "captured/sim-upgrade-held.txt", "captured/sim-install-curl.txt",
		"captured/sim-install-linux-image-generic.txt", "captured/sim-install-linux-generic-hwe.txt",
		"captured/sim-install-linux-virtual.txt", "captured/sim-install-nonkernel.txt", "memory/sim-mixed.txt",
	} {
		text := fixture(t, name)
		lines := instLines(text)
		got := ParseSimulation(text)
		if len(lines) == 0 || len(got) != len(lines) {
			t.Errorf("%s: %d Inst lines, %d packages parsed", name, len(lines), len(got))
			continue
		}
		for i, p := range got {
			fields := strings.Fields(lines[i])
			if p.Operand() != fields[1] {
				t.Errorf("%s: line %q parsed as %q", name, lines[i], p.Operand())
			}
			if p.Candidate == "" || !strings.Contains(lines[i], "("+p.Candidate+" ") {
				t.Errorf("%s: candidate of %q = %q", name, lines[i], p.Candidate)
			}
			if p.New != !strings.Contains(lines[i], " [") || p.New != (p.Current == "") {
				// "[" before "(" is the installed version.
				if before, _, _ := strings.Cut(lines[i], "("); strings.Contains(before, "[") == p.New {
					t.Errorf("%s: new=%v current=%q for %q", name, p.New, p.Current, lines[i])
				}
			}
			if strings.Contains(p.Origin, "[") || strings.Contains(p.Origin, "]") || p.Origin == "" {
				t.Errorf("%s: origin of %q = %q", name, lines[i], p.Origin)
			}
			if p.Security != strings.Contains(lines[i], "-security") {
				t.Errorf("%s: security=%v for %q", name, p.Security, lines[i])
			}
		}
	}
}

func TestParseSimulationCapturedVersions(t *testing.T) {
	got := ParseSimulation(fixture(t, "captured/sim-install-linux-generic-hwe.txt"))
	cases := map[string]string{
		"ca-certificates":               "20260601~24.04.1",    // tilde
		"busybox-initramfs":             "1:1.36.1-6ubuntu3.1", // epoch
		"dmsetup":                       "2:1.02.185-3ubuntu3.2",
		"linux-image-7.0.0-34-generic":  "7.0.0-34.34~24.04.1",
		"linux-firmware":                "20240318.git3b128b60.0ubuntu3.1",
		"bpfcc-tools":                   "0.29.1+ds-1ubuntu7", // plus
		"linux-image-generic-hwe-24.04": "7.0.0-34.34~24.04.1",
	}
	for name, version := range cases {
		p := find(got, name)
		if p == nil {
			t.Errorf("%s not parsed", name)
			continue
		}
		if p.Candidate != version || !p.New || p.Current != "" {
			t.Errorf("%s = %+v, want new package with candidate %s", name, *p, version)
		}
	}
}

func TestParseSimulationRejectsHostileLines(t *testing.T) {
	text := strings.Join([]string{
		"Inst --allow-unauthenticated [1] (2 Ubuntu:24.04/noble [amd64])",
		"Inst -y [1] (2 Ubuntu:24.04/noble [amd64])",
		"Inst Libc6 [1] (2 Ubuntu:24.04/noble [amd64])",
		"Inst libc6=2.39 [1] (2 Ubuntu:24.04/noble [amd64])",
		"Inst libc6/noble [1] (2 Ubuntu:24.04/noble [amd64])",
		"Inst libc6;reboot [1] (2 Ubuntu:24.04/noble [amd64])",
		"Inst $(reboot) [1] (2 Ubuntu:24.04/noble [amd64])",
		"Inst libc6:--x [1] (2 Ubuntu:24.04/noble [amd64])",
		"Inst libc6:AMD64 [1] (2 Ubuntu:24.04/noble [amd64])",
		"Inst libc6: [1] (2 Ubuntu:24.04/noble [amd64])",
		"Inst libc6:i386:x [1] (2 Ubuntu:24.04/noble [amd64])",
		"Inst " + strings.Repeat("a", 129) + " [1] (2 Ubuntu:24.04/noble [amd64])",
		"Inst libc6 [1;x] (2 Ubuntu:24.04/noble [amd64])",
		"Inst libc6 [1] (2$(x) Ubuntu:24.04/noble [amd64])",
		"Inst libc6 [1] (" + strings.Repeat("9", 129) + " Ubuntu:24.04/noble [amd64])",
	}, "\n")
	if got := ParseSimulation(text); len(got) != 0 {
		t.Errorf("hostile lines were accepted: %+v", got)
	}
}

func TestParseSimulationTolerantOfFraming(t *testing.T) {
	text := "\r\n  Inst libc6 [2.39-0ubuntu8.3] (2.39-0ubuntu8.4 Ubuntu:24.04/noble-updates [amd64]) \r\n" +
		"Inst libc6 [2.39-0ubuntu8.3] (2.39-0ubuntu8.4 Ubuntu:24.04/noble-updates [amd64])\r\n" +
		"Inst zlib1g (1:1.3.dfsg-3.1ubuntu2.1  [amd64])\n" +
		"Inst docker-ce [5:27.0.1-1~ubuntu.24.04~noble] (5:27.3.1-1~ubuntu.24.04~noble Docker CE:ubuntu/noble [amd64])\n"
	got := ParseSimulation(text)
	want := []Package{
		{Name: "libc6", Current: "2.39-0ubuntu8.3", Candidate: "2.39-0ubuntu8.4", Origin: "Ubuntu:24.04/noble-updates"},
		{Name: "zlib1g", Candidate: "1:1.3.dfsg-3.1ubuntu2.1", New: true},
		{Name: "docker-ce", Current: "5:27.0.1-1~ubuntu.24.04~noble", Candidate: "5:27.3.1-1~ubuntu.24.04~noble", Origin: "Docker CE:ubuntu/noble"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	long := "Inst libc6 [1] (2 " + strings.Repeat("O", 5000) + " [amd64])"
	if got := ParseSimulation(long); len(got) != 1 || len(got[0].Origin) > 300 {
		t.Errorf("origin length is not bounded: %d packages", len(got))
	}
}

func TestSecurityClassification(t *testing.T) {
	cases := map[string]bool{
		"Ubuntu:24.04/noble-security":                             true,
		"Ubuntu:24.04/noble-updates, Ubuntu:24.04/noble-security": true,
		"UbuntuESM:24.04/noble-infra-security":                    true,
		"UbuntuESMApps:24.04/noble-apps-security":                 true,
		"Debian-Security:12/stable-security":                      true,
		"Ubuntu:24.04/noble-updates":                              false,
		"Ubuntu:24.04/noble":                                      false,
		"Ubuntu:24.04/noble-backports":                            false,
		"Docker CE:ubuntu/noble":                                  false,
		"UbuntuESMApps:24.04/noble-apps-updates":                  false,
	}
	for origin, want := range cases {
		got := ParseSimulation("Inst foo-bar [1.0] (1.1 " + origin + " [amd64])")
		if len(got) != 1 {
			t.Errorf("%q: not parsed", origin)
			continue
		}
		if got[0].Security != want || got[0].Origin != origin {
			t.Errorf("%q: security=%v origin=%q, want %v", origin, got[0].Security, got[0].Origin, want)
		}
	}
}

// Decision on the two doubtful names (see also IsKernelPackage):
//
// linux-firmware (and its linux-firmware-* splits) is NOT a kernel package.
// It ships firmware blobs that the running kernel loads; installing it does
// not add or select a kernel image, so the owner's rule "never boot into a
// kernel I did not approve" is not touched. Holding firmware back together
// with the kernel would also withhold security fixes (CPU/Wi-Fi firmware)
// from everyone who leaves the kernel option off.
//
// linux-libc-dev is NOT a kernel package either. It contains the kernel's
// userspace headers under /usr/include for compiling programs; nothing in
// it is loaded or booted. It is versioned with the kernel, which is why the
// name looks alike.
func TestKernelClassification(t *testing.T) {
	kernel := []string{
		"linux-image-6.8.0-45-generic", "linux-image-unsigned-6.8.0-45-generic", "linux-image-generic",
		"linux-image-generic-hwe-24.04", "linux-image-virtual", "linux-image-lowlatency",
		"linux-image-7.0.0-34-generic", "linux-image-amd64", "linux-image-6.1.0-25-amd64",
		"linux-headers-6.8.0-45", "linux-headers-6.8.0-45-generic", "linux-headers-generic",
		"linux-headers-generic-hwe-24.04", "linux-headers-virtual",
		"linux-modules-6.8.0-45-generic", "linux-modules-extra-6.8.0-45-generic",
		"linux-modules-nvidia-535-generic", "linux-modules-extra-7.0.0-34-generic",
		"linux-generic", "linux-generic-hwe-24.04", "linux-generic-hwe-24.04-edge",
		"linux-virtual", "linux-virtual-hwe-24.04", "linux-lowlatency", "linux-lowlatency-hwe-24.04",
		"linux-oem-24.04", "linux-oem-24.04b", "linux-aws", "linux-azure", "linux-gcp", "linux-kvm",
		"linux-oracle", "linux-raspi", "linux-hwe-7.0-headers-7.0.0-34", "linux-signed-generic",
	}
	for _, name := range kernel {
		if !IsKernelPackage(name) {
			t.Errorf("%s is not classified as a kernel package", name)
		}
		if !ValidPackageName(name) {
			t.Errorf("%s is not a valid package name", name)
		}
	}
	other := []string{
		"linux-firmware", "linux-firmware-intel-wireless", "linux-firmware-amd-graphics",
		"linux-base", "linux-libc-dev", "util-linux", "util-linux-extra", "syslinux", "syslinux-common",
		"linux", "linux-", "linuxdoc-tools", "linux-imagex", "linux-generics", "linux-sound-base",
		"alsa-linux-image", "libc6", "firmware-sof-signed", "amd64-microcode", "intel-microcode",
		"initramfs-tools", "grub-pc", "kmod", "libselinux1", "selinux-utils", "",
	}
	for _, name := range other {
		if IsKernelPackage(name) {
			t.Errorf("%s is classified as a kernel package", name)
		}
	}
}

// The classification applied to what apt really offers when a kernel is
// installed: nothing that carries a kernel image, its modules or headers
// may slip through as "not kernel".
func TestKernelClassificationOnCapturedOutput(t *testing.T) {
	for _, name := range []string{
		"captured/sim-install-linux-image-generic.txt", "captured/sim-install-linux-generic-hwe.txt",
		"captured/sim-install-linux-virtual.txt", "captured/sim-install-nonkernel.txt",
	} {
		n := 0
		for _, p := range ParseSimulation(fixture(t, name)) {
			looksKernel := false
			for _, part := range []string{"linux-image", "linux-modules", "linux-headers", "-headers-"} {
				if strings.Contains(p.Name, part) {
					looksKernel = true
				}
			}
			switch p.Name {
			case "linux-generic-hwe-24.04", "linux-virtual":
				looksKernel = true
			}
			if looksKernel {
				n++
			}
			if looksKernel && !p.Kernel {
				t.Errorf("%s: %s is not classified as kernel", name, p.Name)
			}
			if !strings.HasPrefix(p.Name, "linux-") && p.Kernel {
				t.Errorf("%s: %s is classified as kernel", name, p.Name)
			}
			if (strings.HasPrefix(p.Name, "linux-firmware") || p.Name == "linux-base" || p.Name == "linux-libc-dev") && p.Kernel {
				t.Errorf("%s: %s is classified as kernel", name, p.Name)
			}
		}
		if strings.Contains(name, "nonkernel") != (n == 0) {
			t.Errorf("%s: %d kernel packages found", name, n)
		}
	}
}

func TestValidPackageName(t *testing.T) {
	good := []string{"libc6", "g++", "libstdc++6", "python3.12", "0ad", "linux-image-6.8.0-45-generic",
		"libssl3t64", "gcc-13-base", "xz-utils", "ab", strings.Repeat("a", 128)}
	for _, n := range good {
		if !ValidPackageName(n) {
			t.Errorf("%q refused", n)
		}
	}
	bad := []string{
		"", "a", "-y", "--yes", "--allow-unauthenticated", "-o", "-oDpkg::Options::=--force-all", "+x", ".x",
		"libc6 vim", " libc6", "libc6 ", "libc6\t", "libc6\n", "\nlibc6", "libc6\r", "libc6\x00", "lib\x00c6",
		"libc6;reboot", "libc6|sh", "libc6&", "$(reboot)", "`reboot`", "libc6>x", "libc6<x", "lib'c6", `lib"c6`,
		"lib\\c6", "libc6*", "libc6?", "lib[c]6", "{libc6}", "~libc6", "libc6!", "libc6#", "lib(c6)",
		"libc6=2.39-0ubuntu8.4", "libc6=", "libc6/noble-updates", "libc6/", "libc6:amd64", "libc6:",
		"Libc6", "LIBC6", "libC6", "libc6_dev", "libc6,vim", "libç6", "lıbc6", "../libc6", "/usr/bin/apt",
		"libc6%20", "libc6@x", strings.Repeat("a", 129), strings.Repeat("a", 5000),
	}
	for _, n := range bad {
		if ValidPackageName(n) {
			t.Errorf("%q accepted", n)
		}
	}
}

func TestValidArch(t *testing.T) {
	for _, a := range []string{"amd64", "i386", "arm64", "armhf", "all", "riscv64", "ppc64el", "s390x"} {
		if !ValidArch(a) {
			t.Errorf("%q refused", a)
		}
	}
	for _, a := range []string{"", "a", "-x", "--force", "AMD64", "amd64 ", " amd64", "amd64\n", "amd64;x", "amd64:x",
		"amd64=1", "amd64/x", "amd_64", strings.Repeat("a", 17)} {
		if ValidArch(a) {
			t.Errorf("%q accepted", a)
		}
	}
}

func TestOperand(t *testing.T) {
	if got := (Package{Name: "libc6"}).Operand(); got != "libc6" {
		t.Errorf("operand = %q", got)
	}
	if got := (Package{Name: "libc6", Arch: "i386"}).Operand(); got != "libc6:i386" {
		t.Errorf("operand = %q", got)
	}
}

func TestValidReleaseVersion(t *testing.T) {
	for _, v := range []string{"1.2.3", "v1.2.3", "0.0.1", "10.20.30", "1.0.0-rc.1", "1.0.0-alpha", "1.0.0-0.3.7",
		"v2.0.0-beta.11", "1.0.0-x-y-z.1"} {
		if !ValidReleaseVersion(v) {
			t.Errorf("%q refused", v)
		}
	}
	for _, v := range []string{"", "v", "1", "1.2", "1.2.3.4", "01.2.3", "1.02.3", "1.2.03", "V1.2.3", "vv1.2.3",
		"1.2.3+build.5", "1.2.3-", "1.2.3-.", "1.2.3--x", "-1.2.3", "--help", "-h", "--from=/tmp/x", "1.2.3 ", " 1.2.3",
		"1.2.3\n", "1.2.3\x00", "1.2.3;reboot", "1.2.3 --skip-verify", "1.2.3/../x", "1.2.3-rc_1", "1.2.3-rc/1",
		"1.2.3-$(id)", "latest", "١.٢.٣", "1.2.3-" + strings.Repeat("a", 60), "1." + strings.Repeat("9", 70) + ".1"} {
		if ValidReleaseVersion(v) {
			t.Errorf("%q accepted", v)
		}
	}
}

func TestValidSHA256(t *testing.T) {
	sum := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if !ValidSHA256(sum) {
		t.Error("valid digest refused")
	}
	for _, s := range []string{"", sum[:63], sum + "0", strings.ToUpper(sum), "sha256:" + sum, sum[:63] + "g",
		sum[:63] + " ", " " + sum[:63], sum + "\n", sum[:32], strings.Repeat("-", 64), sum[:62] + "\n\n"} {
		if ValidSHA256(s) {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestValidJobID(t *testing.T) {
	if !ValidJobID("0123456789abcdef") {
		t.Error("valid id refused")
	}
	for _, s := range []string{"", "0123456789abcde", "0123456789abcdef0", "0123456789ABCDEF", "0123456789abcdeg",
		"--unit=x", "../../../../etc", "0123456789abcde\n", "0123456789abcdef\n", " 123456789abcdef", "0123456789abcde/"} {
		if ValidJobID(s) {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestValidUpdateURL(t *testing.T) {
	good := []string{
		"https://example.com/myserver-linux-amd64.tar.gz",
		"https://github.com/owner/repo/releases/download/v1.2.3/myserver-linux-amd64.tar.gz",
		"https://updates.example.com:8443/releases/1.2.3/myserver-linux-arm64.tar.gz",
		"https://127.0.0.1:40123/dl/myserver-linux-amd64.tar.gz",
		"https://example.com/a%20b/x.tar.gz",
	}
	for _, u := range good {
		if !ValidUpdateURL(u) {
			t.Errorf("%q refused", u)
		}
	}
	bad := []string{
		"", "https://", "https://example.com", "https://example.com/", "https:///x.tar.gz",
		"http://example.com/x.tar.gz", "HTTP://example.com/x.tar.gz", "ftp://example.com/x.tar.gz",
		"file:///etc/passwd", "//example.com/x.tar.gz", "example.com/x.tar.gz", "/etc/passwd",
		"https:example.com/x.tar.gz", "javascript:alert(1)",
		"https://user@example.com/x.tar.gz", "https://user:parola@example.com/x.tar.gz",
		"https://example.com/x.tar.gz?token=1", "https://example.com/x.tar.gz?", "https://example.com/x.tar.gz#frag",
		"https://example.com/x.tar.gz#", "https://example.com/../x.tar.gz", "https://example.com/a/../../x.tar.gz",
		"https://example.com/a/..", "https://example.com/%2e%2e/x.tar.gz", "https://example.com/a/%2E%2E/x.tar.gz",
		"https://example.com//x.tar.gz", "https://example.com/a//x.tar.gz",
		"https://example.com/x y.tar.gz", " https://example.com/x.tar.gz", "https://example.com/x.tar.gz ",
		"https://example.com/x.tar.gz\n", "https://example.com/x.tar.gz\r\n", "https://example.com/x\n.tar.gz",
		"https://example.com/x\t.tar.gz", "https://example.com/x\x00.tar.gz", "https://example.com/x\x7f.tar.gz",
		"https://example.com/x\x1b[0m.tar.gz", "https://exa mple.com/x.tar.gz",
		"-o", "--config=/etc/x", "-K/etc/x", "--output=/etc/cron.d/x", "-https://example.com/x.tar.gz",
		"https://example.com/x.tar.gz;reboot", "https://example.com/$(id).tar.gz", "https://example.com/`id`.tar.gz",
		"https://example.com/x.tar.gz|sh", "https://example.com/x.tar.gz&", "https://example.com/'x'.tar.gz",
		`https://example.com/"x".tar.gz`, `https://example.com/x\y.tar.gz`, "https://example.com/{a,b}.tar.gz",
		"https://example.com/[1-9].tar.gz", "https://example.com/x.tar.gz,https://evil.example/x",
		"https://[::1]/x.tar.gz", "https://example.com/ş.tar.gz",
		"https://example.com/" + strings.Repeat("a", 500) + ".tar.gz",
	}
	for _, u := range bad {
		if ValidUpdateURL(u) {
			t.Errorf("%q accepted", u)
		}
	}
	// The length bound itself.
	base := "https://example.com/"
	if u := base + strings.Repeat("a", 500-len(base)); !ValidUpdateURL(u) {
		t.Errorf("a %d character address was refused", len(u))
	}
	if u := base + strings.Repeat("a", 501-len(base)); ValidUpdateURL(u) {
		t.Errorf("a %d character address was accepted", len(u))
	}
}

func TestReleaseTarballAndArch(t *testing.T) {
	if got := ReleaseTarball("amd64"); got != "myserver-linux-amd64.tar.gz" {
		t.Errorf("tarball = %q", got)
	}
	for arch, want := range map[string]bool{"amd64": true, "arm64": true, "386": false, "arm": false,
		"riscv64": false, "": false, "AMD64": false, "amd64 ": false} {
		if SupportedArch(arch) != want {
			t.Errorf("SupportedArch(%q) = %v", arch, !want)
		}
	}
}

/* ---------- failure classification ---------- */

func TestClassify(t *testing.T) {
	cases := []struct {
		fixture string
		code    string
		inMsg   string
	}{
		{"captured/err-lock-lists.txt", "apt_locked", "başka bir işlem"},
		{"memory/err-lock-frontend.txt", "apt_locked", "başka bir işlem"},
		{"memory/err-dpkg-interrupted.txt", "dpkg_interrupted", "dpkg --configure -a"},
		{"memory/err-broken.txt", "apt_broken", "apt --fix-broken install"},
		{"memory/err-held-broken.txt", "apt_broken", "bağımlılık"},
		{"captured/err-no-network.txt", "apt_network", "İnternet bağlantısını"},
		{"memory/err-no-space.txt", "disk_full", "boş alan"},
		{"captured/err-held.txt", "apt_held", "sabitlenmiş"},
		{"captured/err-unmet-dependencies.txt", "apt_unresolvable", "bağımlılık"},
		// apt waited for the lock, got it, and then failed for another reason.
		{"memory/err-lock-wait-then-no-space.txt", "disk_full", "boş alan"},
	}
	seen := map[string]string{}
	for _, c := range cases {
		p, ok := Classify(fixture(t, c.fixture))
		if !ok {
			t.Errorf("%s: not recognised", c.fixture)
			continue
		}
		if p.Code != c.code {
			t.Errorf("%s: code %q, want %q", c.fixture, p.Code, c.code)
		}
		if !strings.Contains(p.Message, c.inMsg) {
			t.Errorf("%s: message %q does not contain %q", c.fixture, p.Message, c.inMsg)
		}
		if other, dup := seen[p.Message]; dup && other != p.Code {
			t.Errorf("codes %s and %s share one message", other, p.Code)
		}
		seen[p.Message] = p.Code
		if got := CodeForMessage(p.Message, "fallback"); got != p.Code {
			t.Errorf("CodeForMessage(%s) = %q", p.Code, got)
		}
		// Messages are sentences for the user: no apt text, no paths.
		for _, leak := range []string{"E:", "/var/", "lock-frontend", "pkgProblemResolver"} {
			if strings.Contains(p.Message, leak) {
				t.Errorf("%s: message leaks %q: %s", c.code, leak, p.Message)
			}
		}
	}
	if len(seen) != 7 {
		t.Errorf("%d distinct messages for 7 failure classes", len(seen))
	}
}

func TestClassifyLeavesOrdinaryOutputAlone(t *testing.T) {
	for _, name := range []string{
		"captured/sim-dist-upgrade.txt", "captured/sim-upgrade-held.txt", "captured/sim-install-curl.txt",
		"captured/sim-install-linux-image-generic.txt", "captured/sim-install-linux-generic-hwe.txt",
		"captured/sim-install-linux-virtual.txt", "captured/sim-install-nonkernel.txt",
		"captured/err-unable-to-locate.txt", "memory/sim-mixed.txt",
	} {
		if p, ok := Classify(fixture(t, name)); ok {
			t.Errorf("%s: classified as %s", name, p.Code)
		}
	}
	if _, ok := Classify(""); ok {
		t.Error("empty output classified")
	}
	if got := CodeForMessage("başka bir ileti", "apt_list_failed"); got != "apt_list_failed" {
		t.Errorf("CodeForMessage fallback = %q", got)
	}
}

// A lock that apt only had to wait for is not a failure.
func TestClassifyWaitingForLockOnly(t *testing.T) {
	text := "Waiting for cache lock: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 2841 (unattended-upgr)... 0s\r" +
		"Waiting for cache lock: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 2841 (unattended-upgr)... 1s\r" +
		"Reading package lists...\nE: Some other failure\n"
	if p, ok := Classify(text); ok {
		t.Errorf("classified as %s", p.Code)
	}
	// ... but when the wait ran out, it is.
	text += "E: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 2841 (unattended-upgr)\n" +
		"E: Unable to acquire the dpkg frontend lock (/var/lib/dpkg/lock-frontend), is another process using it?\n"
	if p, ok := Classify(text); !ok || p.Code != "apt_locked" {
		t.Errorf("timed-out lock: %+v %v", p, ok)
	}
}

/* ---------- key=value files ---------- */

func TestParseKV(t *testing.T) {
	got := ParseKV("id=0123456789abcdef\nstate=failed\r\nstarted=1790000000\nfinished=1790000100\n" +
		"message=Diskte yeterli boş alan yok. a=b\n")
	want := map[string]string{
		"id": "0123456789abcdef", "state": "failed", "started": "1790000000", "finished": "1790000100",
		"message": "Diskte yeterli boş alan yok. a=b",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
}

func TestParseKVMalformed(t *testing.T) {
	if got := ParseKV(""); got == nil || len(got) != 0 {
		t.Errorf("empty text: %v", got)
	}
	got := ParseKV("no separator\n=value without key\n\n   \nstate\n" +
		strings.Repeat("k", 41) + "=too long key\n" +
		"big=" + strings.Repeat("x", 5000) + "\n" +
		"state=success\nstate=failed\n\x00\x01\x02\n")
	if len(got) != 2 {
		t.Errorf("got keys %v", keys(got))
	}
	if got["state"] != "failed" {
		t.Errorf("state = %q; the last value must win", got["state"])
	}
	if len(got["big"]) != 1000 {
		t.Errorf("value length %d is not capped", len(got["big"]))
	}
	var many strings.Builder
	for i := 0; i < 500; i++ {
		many.WriteString("k" + strings.Repeat("a", i%30) + string(rune('a'+i%26)) + "=v\n")
	}
	if got := ParseKV(many.String()); len(got) > 32 {
		t.Errorf("%d keys accepted", len(got))
	}
}

func keys(m map[string]string) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestOneLine(t *testing.T) {
	cases := map[string]string{
		"Tamam":                   "Tamam",
		"  boşluklu  ":            "boşluklu",
		"bir\niki":                "bir iki",
		"bir\r\niki":              "bir  iki",
		"x\nstate=success":        "x state=success",
		"a\x00b\x1b[31mc\x7fd\te": "a b [31mc d e",
		"\n\n":                    "",
		"çok güzel ğüşiöç İĞÜŞÖÇ ✓":   "çok güzel ğüşiöç İĞÜŞÖÇ ✓",
		"id=1\nid=2\nmessage=x\n\n\n": "id=1 id=2 message=x",
	}
	for in, want := range cases {
		got := OneLine(in)
		if got != want {
			t.Errorf("OneLine(%q) = %q, want %q", in, got, want)
		}
		if strings.ContainsAny(got, "\n\r") {
			t.Errorf("OneLine(%q) spans lines", in)
		}
	}
	// A message must not be able to add keys to the file it is written to.
	kv := ParseKV("id=0123456789abcdef\nstate=failed\nmessage=" + OneLine("x\nstate=success\nid=ffffffffffffffff") + "\n")
	if kv["state"] != "failed" || kv["id"] != "0123456789abcdef" {
		t.Errorf("message overrode other keys: %v", kv)
	}
}
