//go:build linux

package helper

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"myserver/internal/updates/updatescheck"
)

// The update actions are run against recording shell scripts standing in
// for apt-get, systemctl and systemd-run, and against a temporary directory
// standing in for /var/lib/myserver-updates. The real tools are never
// executed, nothing is installed and nothing is restarted.

const updFakeTool = `#!/bin/sh
{
	printf '%s\n' '@NAME@'
	for a in "$@"; do printf '%s\n' "$a"; done
	printf '%s\n' '--end--'
} >> '@LOG@'
case '@NAME@' in
apt-get)
	sim=run
	verb=""
	for a in "$@"; do
		case "$a" in
		--) break ;;
		--simulate) sim=sim ;;
		update|upgrade|dist-upgrade|install) [ -z "$verb" ] && verb="$a" ;;
		esac
	done
	f='@DIR@'/apt-$sim-$verb
	[ -f "$f.out" ] && cat "$f.out"
	[ -f "$f.rc" ] && exit "$(cat "$f.rc")"
	exit 0
	;;
systemctl)
	if [ "$1" = "is-active" ]; then
		for a in "$@"; do unit="$a"; done
		if grep -qx "$unit" '@DIR@/units-active' 2>/dev/null; then echo active; exit 0; fi
		if grep -qx "$unit" '@DIR@/units-activating' 2>/dev/null; then echo activating; exit 3; fi
		if grep -qx "$unit" '@DIR@/units-unknown' 2>/dev/null; then echo inactive; exit 4; fi
		echo inactive
		exit 3
	fi
	exit 0
	;;
systemd-run)
	[ -f '@DIR@/systemd-run.rc' ] && exit "$(cat '@DIR@/systemd-run.rc')"
	exit 0
	;;
esac
exit 0
`

const (
	updJob  = "0123456789abcdef"
	updSum  = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	updURL  = "https://updates.example.com/1.2.3/myserver-linux-amd64.tar.gz"
	updHost = "sunucu-1"

	updSecurity = "Ubuntu:24.04/noble-updates, Ubuntu:24.04/noble-security"
)

type updFakes struct {
	t      *testing.T
	dir    string
	log    string
	state  string
	script string
}

func newUpdFakes(t *testing.T) *updFakes {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root: the directory checks require root-owned files")
	}
	dir := t.TempDir()
	f := &updFakes{t: t, dir: dir, log: filepath.Join(dir, "calls.log"),
		state: filepath.Join(dir, "state"), script: filepath.Join(dir, "update.sh")}
	tool := func(name string) string {
		body := strings.ReplaceAll(updFakeTool, "@NAME@", name)
		body = strings.ReplaceAll(body, "@LOG@", f.log)
		body = strings.ReplaceAll(body, "@DIR@", dir)
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// Never executed: systemd-run is a recording script.
	if err := os.WriteFile(f.script, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.write("cgroup", "0::/system.slice/"+updatescheck.AptUnit+".service\n")

	oa, os1, or, od, ol, ores, oc, osc, oh := aptGetBin, systemctlBin, systemdRunBin, updatesAptDir,
		updatesLogFile, updatesResFile, updatesCgroup, updatesScript, updatesHostname
	aptGetBin, systemctlBin, systemdRunBin = tool("apt-get"), tool("systemctl"), tool("systemd-run")
	updatesAptDir = f.state
	updatesLogFile = filepath.Join(f.state, "apt-upgrade.log")
	updatesResFile = filepath.Join(f.state, "apt-upgrade.result")
	updatesCgroup = filepath.Join(dir, "cgroup")
	updatesScript = f.script
	updatesHostname = func() (string, error) { return updHost, nil }
	t.Cleanup(func() {
		aptGetBin, systemctlBin, systemdRunBin, updatesAptDir, updatesLogFile, updatesResFile,
			updatesCgroup, updatesScript, updatesHostname = oa, os1, or, od, ol, ores, oc, osc, oh
	})
	return f
}

func (f *updFakes) write(name, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *updFakes) read(path string) string {
	f.t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

// calls returns every invocation as its argument list, tool name first.
func (f *updFakes) calls() [][]string {
	f.t.Helper()
	b, err := os.ReadFile(f.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	var out [][]string
	var cur []string
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if line == "--end--" {
			out = append(out, cur)
			cur = nil
			continue
		}
		cur = append(cur, line)
	}
	return out
}

func (f *updFakes) callsOf(tool string) [][]string {
	var out [][]string
	for _, c := range f.calls() {
		if c[0] == tool {
			out = append(out, c[1:])
		}
	}
	return out
}

// changes returns the invocations that would change the system: everything
// except "systemctl is-active", "systemctl reset-failed" and simulations.
func (f *updFakes) changes() [][]string {
	var out [][]string
	for _, c := range f.calls() {
		if c[0] == "systemctl" && len(c) > 1 && (c[1] == "is-active" || c[1] == "reset-failed") {
			continue
		}
		if c[0] == "apt-get" && contains(c, "--simulate") {
			continue
		}
		out = append(out, c)
	}
	return out
}

func (f *updFakes) wantNothingRun(what string) {
	f.t.Helper()
	if c := f.calls(); len(c) != 0 {
		f.t.Errorf("%s: commands were executed: %q", what, c)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func updRun(t *testing.T, name string, args ...string) (string, error) {
	t.Helper()
	a, ok := actions[name]
	if !ok {
		t.Fatalf("action %q is not registered", name)
	}
	var out bytes.Buffer
	err := a(context.Background(), args, strings.NewReader(""), &out)
	return out.String(), err
}

func updMessage(err error) string {
	var ue *UserError
	if errors.As(err, &ue) {
		return ue.Message
	}
	return ""
}

func inst(name, current, candidate, origin string) string {
	line := "Inst " + name
	if current != "" {
		line += " [" + current + "]"
	}
	return line + " (" + candidate + " " + origin + " [amd64])\n"
}

// operands returns the arguments after "--" and whether "--" was present.
func operands(args []string) ([]string, bool) {
	for i, a := range args {
		if a == "--" {
			return args[i+1:], true
		}
	}
	return nil, false
}

func options(args []string) []string {
	for i, a := range args {
		if a == "--" {
			return args[:i]
		}
	}
	return args
}

/* ---------- registration and argument validation ---------- */

func TestUpdatesActionsRegistered(t *testing.T) {
	for _, name := range []string{"updates-apt-refresh", "updates-apt-list", "updates-apt-upgrade",
		"updates-apt-run", "updates-reboot", "updates-self"} {
		if _, ok := actions[name]; !ok {
			t.Errorf("action %q is missing", name)
		}
	}
	for name := range actions {
		if strings.HasPrefix(name, "updates-") {
			switch name {
			case "updates-apt-refresh", "updates-apt-list", "updates-apt-upgrade", "updates-apt-run",
				"updates-reboot", "updates-self":
			default:
				t.Errorf("unexpected update action %q: every privileged update operation must be reviewed here", name)
			}
		}
	}
}

func TestUpdatesArgumentCounts(t *testing.T) {
	f := newUpdFakes(t)
	cases := map[string][][]string{
		"updates-apt-refresh": {{"x"}, {"--simulate"}, {"", ""}},
		"updates-apt-list":    {{"x"}, {"dist-upgrade"}, {"-y", "-y"}},
		"updates-apt-upgrade": {nil, {"upgrade"}, {"upgrade", "no-kernel"}, {"upgrade", "no-kernel", updJob, "x"}},
		"updates-apt-run":     {nil, {"upgrade"}, {"upgrade", "no-kernel"}, {"upgrade", "no-kernel", updJob, "x"}},
		"updates-reboot":      {nil, {updHost, updHost}, {updHost, "--force"}},
		"updates-self":        {nil, {"1.2.3"}, {"1.2.3", updSum}, {"1.2.3", updSum, updURL, "x"}},
	}
	for name, lists := range cases {
		for _, args := range lists {
			out, err := updRun(t, name, args...)
			if err == nil {
				t.Errorf("%s with %d arguments succeeded", name, len(args))
			}
			if out != "" {
				t.Errorf("%s: output %q", name, out)
			}
		}
	}
	f.wantNothingRun("wrong argument counts")
	if _, err := os.Lstat(f.state); err == nil {
		t.Error("the job directory was created by a refused call")
	}
}

func TestUpdatesUpgradeRejectsInvalidArguments(t *testing.T) {
	f := newUpdFakes(t)
	bad := [][]string{
		{"", "no-kernel", updJob}, {"Upgrade", "no-kernel", updJob}, {"UPGRADE", "no-kernel", updJob},
		{"dist-upgrade", "no-kernel", updJob}, {"full-upgrade", "no-kernel", updJob}, {"install", "no-kernel", updJob},
		{"autoremove", "no-kernel", updJob}, {"upgrade ", "no-kernel", updJob}, {" full", "no-kernel", updJob},
		{"full\n", "no-kernel", updJob}, {"-y", "no-kernel", updJob}, {"--allow-downgrades", "no-kernel", updJob},
		{"upgrade;reboot", "no-kernel", updJob}, {"$(reboot)", "no-kernel", updJob},
		{"upgrade", "", updJob}, {"upgrade", "yes", updJob}, {"upgrade", "true", updJob}, {"upgrade", "Kernel", updJob},
		{"upgrade", "kernel ", updJob}, {"upgrade", "no_kernel", updJob}, {"upgrade", "--kernel", updJob},
		{"upgrade", "kernel\n", updJob}, {"upgrade", "linux-image-generic", updJob},
		{"upgrade", "no-kernel", ""}, {"upgrade", "no-kernel", "0123456789abcde"}, {"upgrade", "no-kernel", updJob + "0"},
		{"upgrade", "no-kernel", "0123456789ABCDEF"}, {"upgrade", "no-kernel", "../../etc/passwd"},
		{"upgrade", "no-kernel", "--unit=evil-unit"}, {"upgrade", "no-kernel", updJob + "\n"},
		{"upgrade", "no-kernel", "0123456789abcde\n"}, {"upgrade", "no-kernel", "0123456789abcde;"},
	}
	for _, name := range []string{"updates-apt-upgrade", "updates-apt-run"} {
		for _, args := range bad {
			out, err := updRun(t, name, args...)
			if err == nil {
				t.Errorf("%s %q succeeded", name, args)
				continue
			}
			if updMessage(err) == "" {
				t.Errorf("%s %q: error %v is not a user message", name, args, err)
			}
			if out != "" {
				t.Errorf("%s %q: output %q", name, args, out)
			}
		}
	}
	f.wantNothingRun("invalid upgrade arguments")
	if _, err := os.Lstat(f.state); err == nil {
		t.Error("the job directory was created by a refused call")
	}
}

func TestUpdatesSelfRejectsInvalidArguments(t *testing.T) {
	f := newUpdFakes(t)
	bad := [][]string{
		{"", updSum, updURL}, {"1.2", updSum, updURL}, {"01.2.3", updSum, updURL}, {"latest", updSum, updURL},
		{"--from=/tmp/evil", updSum, updURL}, {"--skip-verify", updSum, updURL}, {"--help", updSum, updURL},
		{"-h", updSum, updURL}, {"1.2.3 --skip-verify", updSum, updURL}, {"1.2.3\n", updSum, updURL},
		{"1.2.3;reboot", updSum, updURL}, {"$(reboot)", updSum, updURL}, {"1.2.3+build", updSum, updURL},
		{"1.2.3-" + strings.Repeat("a", 64), updSum, updURL},
		{"1.2.3", "", updURL}, {"1.2.3", updSum[:63], updURL}, {"1.2.3", updSum + "0", updURL},
		{"1.2.3", strings.ToUpper(updSum), updURL}, {"1.2.3", "sha256:" + updSum, updURL},
		{"1.2.3", updSum[:63] + "\n", updURL}, {"1.2.3", updSum[:63] + "g", updURL},
		{"1.2.3", "--setenv=MYSERVER_UPDATE_URL=https://evil.example/x" + strings.Repeat("0", 14), updURL},
		{"1.2.3", updSum, ""}, {"1.2.3", updSum, "http://updates.example.com/x.tar.gz"},
		{"1.2.3", updSum, "https://user:parola@updates.example.com/x.tar.gz"},
		{"1.2.3", updSum, updURL + "?x=1"}, {"1.2.3", updSum, updURL + "#x"},
		{"1.2.3", updSum, "https://updates.example.com/../x.tar.gz"},
		{"1.2.3", updSum, "https://updates.example.com/a b.tar.gz"}, {"1.2.3", updSum, updURL + "\n"},
		{"1.2.3", updSum, updURL + " "}, {"1.2.3", updSum, "https://updates.example.com/x\x1b.tar.gz"},
		{"1.2.3", updSum, "--setenv=LD_PRELOAD=/tmp/x.so"}, {"1.2.3", updSum, "-o"},
		{"1.2.3", updSum, "file:///etc/shadow"}, {"1.2.3", updSum, "https://updates.example.com/"},
		{"1.2.3", updSum, "https://updates.example.com/" + strings.Repeat("a", 500)},
		{"1.2.3", updSum, updURL + ";reboot"}, {"1.2.3", updSum, "https://updates.example.com/$(id).tar.gz"},
	}
	for _, args := range bad {
		out, err := updRun(t, "updates-self", args...)
		if err == nil {
			t.Errorf("%q succeeded", args)
			continue
		}
		if updMessage(err) == "" {
			t.Errorf("%q: error %v is not a user message", args, err)
		}
		if out != "" {
			t.Errorf("%q: output %q", args, out)
		}
	}
	f.wantNothingRun("invalid self-update arguments")
}

/* ---------- reading the package state ---------- */

func TestUpdatesRefreshAndList(t *testing.T) {
	f := newUpdFakes(t)
	f.write("apt-sim-dist-upgrade.out", inst("libc6", "2.39-0ubuntu8.3", "2.39-0ubuntu8.4", updSecurity))
	if _, err := updRun(t, "updates-apt-refresh"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	out, err := updRun(t, "updates-apt-list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := updatescheck.ParseSimulation(out); len(got) != 1 || got[0].Name != "libc6" {
		t.Errorf("list output %q", out)
	}
	want := [][]string{
		{"apt-get", "update", "-o", "DPkg::Lock::Timeout=60"},
		{"apt-get", "--simulate", "dist-upgrade"},
	}
	if got := f.calls(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %q\nwant %q", got, want)
	}
}

func TestUpdatesRefreshReportsUnreachableRepositories(t *testing.T) {
	f := newUpdFakes(t)
	// apt-get update exits 0 when repositories cannot be reached.
	f.write("apt-run-update.out", "Err:1 http://archive.ubuntu.com/ubuntu noble InRelease\n"+
		"  Temporary failure resolving 'archive.ubuntu.com'\nReading package lists...\n"+
		"W: Failed to fetch http://archive.ubuntu.com/ubuntu/dists/noble/InRelease  Temporary failure resolving 'archive.ubuntu.com'\n"+
		"W: Some index files failed to download. They have been ignored, or old ones used instead.\n")
	_, err := updRun(t, "updates-apt-refresh")
	if msg := updMessage(err); !strings.Contains(msg, "Paket depolarına ulaşılamadı") {
		t.Errorf("error %v", err)
	}
}

func TestUpdatesAptFailuresAreClassified(t *testing.T) {
	cases := []struct{ output, want string }{
		{"E: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 2841 (unattended-upgr)\n" +
			"E: Unable to acquire the dpkg frontend lock (/var/lib/dpkg/lock-frontend), is another process using it?\n",
			"başka bir işlem tarafından kullanılıyor"},
		{"E: dpkg was interrupted, you must manually run 'dpkg --configure -a' to correct the problem.\n",
			"sudo dpkg --configure -a"},
		{"E: Unmet dependencies. Try 'apt --fix-broken install' with no packages (or specify a solution).\n",
			"sudo apt --fix-broken install"},
		{"Err:1 http://archive.ubuntu.com/ubuntu noble InRelease\n  Temporary failure resolving 'archive.ubuntu.com'\n",
			"Paket depolarına ulaşılamadı"},
		{"dpkg: error processing archive x.deb (--unpack):\n failed to write (No space left on device)\n",
			"yeterli boş alan yok"},
		{"E: Held packages were changed and -y was used without --allow-change-held-packages.\n",
			"sabitlenmiş"},
		{"E: Something nobody has seen before in /var/lib/secret\n", "Paket güncellemesi başarısız oldu."},
	}
	for _, c := range cases {
		f := newUpdFakes(t)
		f.write("apt-run-upgrade.out", c.output)
		f.write("apt-run-upgrade.rc", "100")
		var out bytes.Buffer
		err := aptUpgrade(context.Background(), &out, &out, updatescheck.ModeUpgrade, true)
		msg := updMessage(err)
		if !strings.Contains(msg, c.want) {
			t.Errorf("output %q: error %v, want a message containing %q", c.output, err, c.want)
		}
		if strings.Contains(msg, "/var/lib") || strings.Contains(msg, "E:") {
			t.Errorf("message leaks apt output: %q", msg)
		}
	}
}

/* ---------- kernel exclusion ---------- */

var updCommon = []string{"-y", "-o", "DPkg::Lock::Timeout=60",
	"-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold"}

func simWithKernel() string {
	return "The following packages will be upgraded:\n  libc6 libc6:i386 linux-firmware linux-generic linux-libc-dev vim\n" +
		inst("libc6", "2.39-0ubuntu8.3", "2.39-0ubuntu8.4", updSecurity) +
		inst("libc6:i386", "2.39-0ubuntu8.3", "2.39-0ubuntu8.4", updSecurity) +
		"Conf libc6 (2.39-0ubuntu8.4 " + updSecurity + " [amd64])\n" +
		inst("vim", "2:9.1.0016-1ubuntu7.5", "2:9.1.0016-1ubuntu7.6", "Ubuntu:24.04/noble-updates") +
		inst("linux-firmware", "20240318.git3b128b60-0ubuntu2.6", "20240318.git3b128b60-0ubuntu2.7", "Ubuntu:24.04/noble-updates") +
		inst("linux-libc-dev", "6.8.0-44.44", "6.8.0-45.45", updSecurity) +
		inst("libnew1", "", "1.0-1", "Ubuntu:24.04/noble-updates") +
		inst("linux-modules-6.8.0-45-generic", "", "6.8.0-45.45", updSecurity) +
		inst("linux-image-6.8.0-45-generic", "", "6.8.0-45.45", updSecurity) +
		inst("linux-headers-6.8.0-45-generic", "", "6.8.0-45.45", updSecurity) +
		inst("linux-modules-extra-6.8.0-45-generic", "", "6.8.0-45.45", updSecurity) +
		inst("linux-image-generic", "6.8.0-44.44", "6.8.0-45.45", updSecurity) +
		inst("linux-headers-generic", "6.8.0-44.44", "6.8.0-45.45", updSecurity) +
		inst("linux-generic", "6.8.0-44.44", "6.8.0-45.45", updSecurity)
}

func simInstallClean() string {
	return inst("libc6", "2.39-0ubuntu8.3", "2.39-0ubuntu8.4", updSecurity) +
		inst("libc6:i386", "2.39-0ubuntu8.3", "2.39-0ubuntu8.4", updSecurity) +
		inst("vim", "2:9.1.0016-1ubuntu7.5", "2:9.1.0016-1ubuntu7.6", "Ubuntu:24.04/noble-updates") +
		inst("linux-firmware", "20240318.git3b128b60-0ubuntu2.6", "20240318.git3b128b60-0ubuntu2.7", "Ubuntu:24.04/noble-updates") +
		inst("linux-libc-dev", "6.8.0-44.44", "6.8.0-45.45", updSecurity) +
		inst("libnew1", "", "1.0-1", "Ubuntu:24.04/noble-updates")
}

func TestUpdatesKernelExcluded(t *testing.T) {
	for _, mode := range []string{updatescheck.ModeUpgrade, updatescheck.ModeFull} {
		t.Run(mode, func(t *testing.T) {
			f := newUpdFakes(t)
			verb := "upgrade"
			if mode == updatescheck.ModeFull {
				verb = "dist-upgrade"
			}
			f.write("apt-sim-"+verb+".out", simWithKernel())
			f.write("apt-sim-install.out", simInstallClean())
			var out bytes.Buffer
			if err := aptUpgrade(context.Background(), &out, &out, mode, false); err != nil {
				t.Fatalf("aptUpgrade: %v", err)
			}
			calls := f.callsOf("apt-get")
			if len(calls) != 3 || len(f.calls()) != 3 {
				t.Fatalf("calls = %q", f.calls())
			}
			names := []string{"libc6", "libc6:i386", "vim", "linux-firmware", "linux-libc-dev"}

			wantSim := []string{"--simulate", "upgrade", "--with-new-pkgs"}
			wantOpts := []string{"install", "--only-upgrade", "--no-remove"}
			if mode == updatescheck.ModeFull {
				wantSim = []string{"--simulate", "dist-upgrade"}
				wantOpts = []string{"install", "--only-upgrade"}
			}
			if !reflect.DeepEqual(calls[0], wantSim) {
				t.Errorf("first simulation = %q, want %q", calls[0], wantSim)
			}
			// The dependency check simulates exactly what will be installed.
			if !contains(calls[1], "--simulate") {
				t.Errorf("second call is not a simulation: %q", calls[1])
			}
			for i, c := range calls[1:] {
				ops, ok := operands(c)
				if !ok {
					t.Fatalf("call %d has no \"--\" before the package names: %q", i+1, c)
				}
				if !reflect.DeepEqual(ops, names) {
					t.Errorf("call %d operands = %q, want %q", i+1, ops, names)
				}
				for _, o := range options(c) {
					if updatescheck.ValidPackageName(o) && o != "install" {
						t.Errorf("call %d: %q stands before \"--\"", i+1, o)
					}
				}
				for _, a := range c {
					name, _, _ := strings.Cut(a, ":")
					if updatescheck.IsKernelPackage(name) {
						t.Errorf("call %d contains the kernel package %s", i+1, a)
					}
					switch a {
					case "dist-upgrade", "full-upgrade", "upgrade", "autoremove", "--auto-remove", "--autoremove",
						"--purge", "remove", "purge", "--allow-downgrades", "--allow-remove-essential",
						"--allow-change-held-packages", "--allow-unauthenticated", "--force-yes":
						t.Errorf("call %d contains %q", i+1, a)
					}
				}
			}
			final := calls[2]
			if contains(final, "--simulate") {
				t.Errorf("final call is a simulation: %q", final)
			}
			want := append(append(append([]string{}, wantOpts...), updCommon...), append([]string{"--"}, names...)...)
			if !reflect.DeepEqual(final, want) {
				t.Errorf("final call = %q\nwant         %q", final, want)
			}
			if !strings.Contains(out.String(), "Çekirdek paketleri atlanıyor") ||
				!strings.Contains(out.String(), "linux-image-6.8.0-45-generic") {
				t.Errorf("the log does not name the skipped kernel packages: %q", out.String())
			}
		})
	}
}

func TestUpdatesKernelPulledInAsDependencyAborts(t *testing.T) {
	for _, mode := range []string{updatescheck.ModeUpgrade, updatescheck.ModeFull} {
		for _, kernelPkg := range []string{"linux-image-6.8.0-45-generic", "linux-modules-6.8.0-45-generic",
			"linux-headers-6.8.0-45-generic", "linux-modules-extra-6.8.0-45-generic", "linux-image-generic"} {
			t.Run(mode+"/"+kernelPkg, func(t *testing.T) {
				f := newUpdFakes(t)
				verb := "upgrade"
				if mode == updatescheck.ModeFull {
					verb = "dist-upgrade"
				}
				f.write("apt-sim-"+verb+".out", inst("zfsutils-linux", "2.2.2-0ubuntu9", "2.2.2-0ubuntu9.1", "Ubuntu:24.04/noble-updates")+
					inst("vim", "2:9.1.0016-1ubuntu7.5", "2:9.1.0016-1ubuntu7.6", "Ubuntu:24.04/noble-updates"))
				f.write("apt-sim-install.out", inst("zfsutils-linux", "2.2.2-0ubuntu9", "2.2.2-0ubuntu9.1", "Ubuntu:24.04/noble-updates")+
					inst("vim", "2:9.1.0016-1ubuntu7.5", "2:9.1.0016-1ubuntu7.6", "Ubuntu:24.04/noble-updates")+
					inst(kernelPkg, "", "6.8.0-45.45", updSecurity))
				var out bytes.Buffer
				err := aptUpgrade(context.Background(), &out, &out, mode, false)
				msg := updMessage(err)
				if !strings.Contains(msg, "çekirdek paketi") || !strings.Contains(msg, kernelPkg) ||
					!strings.Contains(msg, "seçeneğini işaretleyerek yeniden deneyin") {
					t.Fatalf("error %v, want the Turkish kernel message naming %s", err, kernelPkg)
				}
				if c := f.changes(); len(c) != 0 {
					t.Errorf("something was installed although the run had to abort: %q", c)
				}
				if n := len(f.callsOf("apt-get")); n != 2 {
					t.Errorf("%d apt-get calls, want the two simulations only", n)
				}
			})
		}
	}
}

func TestUpdatesKernelExcludedNothingElseToDo(t *testing.T) {
	f := newUpdFakes(t)
	f.write("apt-sim-upgrade.out", inst("linux-modules-6.8.0-45-generic", "", "6.8.0-45.45", updSecurity)+
		inst("linux-image-6.8.0-45-generic", "", "6.8.0-45.45", updSecurity)+
		inst("linux-image-generic", "6.8.0-44.44", "6.8.0-45.45", updSecurity)+
		inst("linux-generic", "6.8.0-44.44", "6.8.0-45.45", updSecurity))
	var out bytes.Buffer
	if err := aptUpgrade(context.Background(), &out, &out, updatescheck.ModeUpgrade, false); err != nil {
		t.Fatalf("aptUpgrade: %v", err)
	}
	if c := f.changes(); len(c) != 0 {
		t.Errorf("commands changed the system: %q", c)
	}
	if n := len(f.callsOf("apt-get")); n != 1 {
		t.Errorf("%d apt-get calls, want one simulation", n)
	}
	if !strings.Contains(out.String(), "Yüklenecek (çekirdek dışı) güncelleme yok.") {
		t.Errorf("log = %q", out.String())
	}
}

func TestUpdatesKernelExcludedSimulationFailureInstallsNothing(t *testing.T) {
	for _, failing := range []string{"apt-sim-upgrade", "apt-sim-install"} {
		f := newUpdFakes(t)
		f.write("apt-sim-upgrade.out", inst("vim", "2:9.1.0016-1ubuntu7.5", "2:9.1.0016-1ubuntu7.6", "Ubuntu:24.04/noble-updates"))
		f.write("apt-sim-install.out", "E: Unable to correct problems, you have held broken packages.\n")
		f.write(failing+".rc", "100")
		var out bytes.Buffer
		if err := aptUpgrade(context.Background(), &out, &out, updatescheck.ModeUpgrade, false); updMessage(err) == "" {
			t.Errorf("%s fails: error %v", failing, err)
		}
		if c := f.changes(); len(c) != 0 {
			t.Errorf("%s fails: something was installed: %q", failing, c)
		}
	}
}

func TestUpdatesKernelIncluded(t *testing.T) {
	cases := map[string][]string{
		updatescheck.ModeUpgrade: append([]string{"upgrade", "--with-new-pkgs"}, updCommon...),
		updatescheck.ModeFull:    append([]string{"dist-upgrade"}, updCommon...),
	}
	for mode, want := range cases {
		f := newUpdFakes(t)
		var out bytes.Buffer
		if err := aptUpgrade(context.Background(), &out, &out, mode, true); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if got := f.calls(); len(got) != 1 || !reflect.DeepEqual(got[0][1:], want) {
			t.Errorf("%s: calls = %q, want %q", mode, got, want)
		}
	}
}

// "upgrade" must never be able to remove a package, whatever the options.
func TestUpdatesUpgradeModeNeverRemoves(t *testing.T) {
	for _, withKernel := range []bool{false, true} {
		f := newUpdFakes(t)
		f.write("apt-sim-upgrade.out", simWithKernel())
		f.write("apt-sim-install.out", simInstallClean())
		var out bytes.Buffer
		if err := aptUpgrade(context.Background(), &out, &out, updatescheck.ModeUpgrade, withKernel); err != nil {
			t.Fatal(err)
		}
		for _, c := range f.callsOf("apt-get") {
			verb := c[0]
			if verb == "--simulate" {
				verb = c[1]
			}
			switch verb {
			case "upgrade": // apt-get upgrade never removes packages
			case "install":
				if !contains(options(c), "--no-remove") {
					t.Errorf("kernel=%v: install without --no-remove: %q", withKernel, c)
				}
			default:
				t.Errorf("kernel=%v: verb %q used in upgrade mode: %q", withKernel, verb, c)
			}
		}
	}
}

/* ---------- the job directory ---------- */

func TestUpdatesPrepareAptDir(t *testing.T) {
	f := newUpdFakes(t)
	if err := prepareAptDir(); err != nil {
		t.Fatalf("a missing directory must be created: %v", err)
	}
	fi, err := os.Lstat(f.state)
	if err != nil || !fi.IsDir() || fi.Mode().Perm()&0o022 != 0 {
		t.Fatalf("created directory: %v %v", fi, err)
	}
	if err := prepareAptDir(); err != nil {
		t.Fatalf("an existing good directory refused: %v", err)
	}

	for _, mode := range []os.FileMode{0o775, 0o757, 0o777, 0o772, 0o1777, 0o720} {
		if err := os.Chmod(f.state, mode); err != nil {
			t.Fatal(err)
		}
		if err := prepareAptDir(); updMessage(err) == "" {
			t.Errorf("mode %o accepted (%v)", mode, err)
		}
	}
	if err := os.Chmod(f.state, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, owner := range [][2]int{{1000, 0}, {1000, 1000}, {65534, 65534}} {
		if err := os.Chown(f.state, owner[0], owner[1]); err != nil {
			t.Fatal(err)
		}
		if err := prepareAptDir(); updMessage(err) == "" {
			t.Errorf("owner %v accepted (%v)", owner, err)
		}
	}
}

func TestUpdatesPrepareAptDirRefusesNonDirectories(t *testing.T) {
	f := newUpdFakes(t)
	real := filepath.Join(f.dir, "elsewhere")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, f.state); err != nil {
		t.Fatal(err)
	}
	if err := prepareAptDir(); updMessage(err) == "" {
		t.Errorf("a symbolic link to a directory was accepted (%v)", err)
	}
	os.Remove(f.state)
	if err := os.WriteFile(f.state, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepareAptDir(); updMessage(err) == "" {
		t.Errorf("a regular file was accepted (%v)", err)
	}
}

func TestUpdatesFilesDoNotFollowLinks(t *testing.T) {
	f := newUpdFakes(t)
	if err := prepareAptDir(); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(f.dir, "victim")
	const secret = "id=ffffffffffffffff\nstate=success\nroot:$6$secret\n"
	if err := os.WriteFile(victim, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	intact := func(what string) {
		t.Helper()
		if got := f.read(victim); got != secret {
			t.Errorf("%s: the link target was changed to %q", what, got)
		}
		if fi, _ := os.Stat(victim); fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: the link target's mode was changed to %o", what, fi.Mode().Perm())
		}
	}

	// Log file.
	if err := os.Symlink(victim, updatesLogFile); err != nil {
		t.Fatal(err)
	}
	for _, flags := range []int{os.O_WRONLY | os.O_CREATE | os.O_TRUNC, os.O_WRONLY | os.O_CREATE | os.O_APPEND} {
		if file, err := openRootFile(updatesLogFile, flags); err == nil {
			file.Close()
			t.Error("a symbolic link at the log path was opened")
		}
	}
	intact("log")
	// A dangling link must not be created through either.
	os.Remove(updatesLogFile)
	dangling := filepath.Join(f.dir, "created-through-link")
	if err := os.Symlink(dangling, updatesLogFile); err != nil {
		t.Fatal(err)
	}
	if file, err := openRootFile(updatesLogFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC); err == nil {
		file.Close()
		t.Error("a dangling link at the log path was opened")
	}
	if _, err := os.Lstat(dangling); err == nil {
		t.Error("a file was created through the link")
	}
	// A directory at the log path.
	os.Remove(updatesLogFile)
	if err := os.Mkdir(updatesLogFile, 0o755); err != nil {
		t.Fatal(err)
	}
	if file, err := openRootFile(updatesLogFile, os.O_RDONLY); err == nil {
		file.Close()
		t.Error("a directory at the log path was opened")
	}
	os.Remove(updatesLogFile)

	// Result file: reading.
	if err := os.Symlink(victim, updatesResFile); err != nil {
		t.Fatal(err)
	}
	if got := readAptResult(); len(got) != 0 {
		t.Errorf("the result was read through a link: %v", got)
	}
	// Result file: writing replaces the link itself.
	if err := os.Symlink(victim, updatesResFile+".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := writeAptResult(updJob, updatescheck.StateRunning, "", 100, 0); err != nil {
		t.Fatalf("writeAptResult: %v", err)
	}
	intact("result")
	fi, err := os.Lstat(updatesResFile)
	if err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("result file: %v %v", fi, err)
	}
	if _, err := os.Lstat(updatesResFile + ".tmp"); err == nil {
		t.Error("the temporary file was left behind")
	}
	if got := readAptResult(); got["id"] != updJob || got["state"] != "running" || got["started"] != "100" {
		t.Errorf("result = %v", got)
	}
}

func TestUpdatesResultFileRoundTrip(t *testing.T) {
	newUpdFakes(t)
	if err := prepareAptDir(); err != nil {
		t.Fatal(err)
	}
	if got := readAptResult(); len(got) != 0 {
		t.Errorf("missing file read as %v", got)
	}
	msg := "Satır bir\nstate=success\nid=ffffffffffffffff\r\nson"
	if err := writeAptResult(updJob, updatescheck.StateFailed, msg, 100, 200); err != nil {
		t.Fatal(err)
	}
	got := readAptResult()
	want := map[string]string{"id": updJob, "state": "failed", "started": "100", "finished": "200",
		"message": "Satır bir state=success id=ffffffffffffffff  son"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("result = %q\nwant     %q", got, want)
	}
	fi, _ := os.Lstat(updatesResFile)
	if fi.Mode().Perm()&0o022 != 0 {
		t.Errorf("result file mode %o", fi.Mode().Perm())
	}
}

/* ---------- starting the upgrade unit ---------- */

func TestUpdatesUpgradeStartsTransientUnit(t *testing.T) {
	cases := [][2]string{{"upgrade", "no-kernel"}, {"upgrade", "kernel"}, {"full", "no-kernel"}, {"full", "kernel"}}
	for _, c := range cases {
		f := newUpdFakes(t)
		if _, err := updRun(t, "updates-apt-upgrade", c[0], c[1], updJob); err != nil {
			t.Fatalf("%v: %v", c, err)
		}
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		want := [][]string{
			{"systemctl", "is-active", "myserver-apt-upgrade.service"},
			{"systemctl", "is-active", "myserver-update.service"},
			{"systemctl", "reset-failed", "myserver-apt-upgrade.service"},
			{"systemd-run", "--unit=myserver-apt-upgrade", "--collect", "--quiet", "--no-block",
				"--description=MyServer paket güncellemesi",
				"--", self, "updates-apt-run", c[0], c[1], updJob},
		}
		got := f.calls()
		for i := range got {
			if got[i][0] == "systemctl" && got[i][1] == "is-active" {
				got[i] = append([]string{"systemctl", "is-active"}, got[i][len(got[i])-1])
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%v: calls = %q\nwant %q", c, got, want)
		}
		if n := len(f.callsOf("apt-get")); n != 0 {
			t.Errorf("apt-get ran %d times in the caller's process tree", n)
		}
		res := readAptResult()
		if res["id"] != updJob || res["state"] != updatescheck.StateRunning || res["finished"] != "0" {
			t.Errorf("result = %v", res)
		}
		fi, err := os.Lstat(updatesLogFile)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() != 0 {
			t.Errorf("log file: %v %v", fi, err)
		}
	}
}

func TestUpdatesUpgradeTruncatesOldLog(t *testing.T) {
	newUpdFakes(t)
	if err := prepareAptDir(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(updatesLogFile, []byte("önceki işin çıktısı\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := updRun(t, "updates-apt-upgrade", "upgrade", "no-kernel", updJob); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(updatesLogFile); fi.Size() != 0 {
		t.Error("the previous job's output was kept")
	}
}

func TestUpdatesUpgradeRefusedWhileUnitActive(t *testing.T) {
	cases := []struct{ file, unit, want string }{
		{"units-active", "myserver-apt-upgrade.service", "Zaten çalışan bir paket güncellemesi var."},
		{"units-active", "myserver-update.service", "MyServer güncellemesi sürerken"},
		{"units-activating", "myserver-apt-upgrade.service", "Zaten çalışan bir paket güncellemesi var."},
		{"units-activating", "myserver-update.service", "MyServer güncellemesi sürerken"},
	}
	for _, c := range cases {
		f := newUpdFakes(t)
		f.write(c.file, c.unit+"\n")
		_, err := updRun(t, "updates-apt-upgrade", "upgrade", "no-kernel", updJob)
		if msg := updMessage(err); !strings.Contains(msg, c.want) {
			t.Errorf("%s %s: error %v", c.file, c.unit, err)
		}
		if c := f.changes(); len(c) != 0 {
			t.Errorf("commands ran: %q", c)
		}
		if _, err := os.Lstat(updatesResFile); err == nil {
			t.Error("the result file of the running job was overwritten")
		}
	}
}

// systemctl is-active exits 3 for an inactive unit and 4 for one that does
// not exist; neither is an error and neither means "active".
func TestUpdatesInactiveUnitExitCodes(t *testing.T) {
	f := newUpdFakes(t)
	f.write("units-unknown", "myserver-apt-upgrade.service\nmyserver-update.service\n")
	for _, unit := range []string{updatescheck.AptUnit, updatescheck.SelfUnit} {
		if unitActive(context.Background(), unit) {
			t.Errorf("%s: a unit that does not exist is reported active", unit)
		}
	}
	if _, err := updRun(t, "updates-apt-upgrade", "upgrade", "no-kernel", updJob); err != nil {
		t.Errorf("upgrade refused: %v", err)
	}
	// systemctl missing altogether: nothing can be active.
	systemctlBin = filepath.Join(f.dir, "no-such-systemctl")
	if unitActive(context.Background(), updatescheck.AptUnit) {
		t.Error("active without systemctl")
	}
}

func TestUpdatesUpgradeRefusesUnsafeDirectory(t *testing.T) {
	f := newUpdFakes(t)
	if err := os.Mkdir(f.state, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.state, 0o777); err != nil {
		t.Fatal(err)
	}
	_, err := updRun(t, "updates-apt-upgrade", "upgrade", "no-kernel", updJob)
	if msg := updMessage(err); !strings.Contains(msg, "güvenli değil") {
		t.Errorf("error %v", err)
	}
	if c := f.changes(); len(c) != 0 {
		t.Errorf("commands ran: %q", c)
	}
	if entries, _ := os.ReadDir(f.state); len(entries) != 0 {
		t.Errorf("files were written into the unsafe directory: %v", entries)
	}
}

func TestUpdatesUpgradeRefusesLinkAtLogPath(t *testing.T) {
	f := newUpdFakes(t)
	if err := prepareAptDir(); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(f.dir, "victim")
	if err := os.WriteFile(victim, []byte("dokunma"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, updatesLogFile); err != nil {
		t.Fatal(err)
	}
	_, err := updRun(t, "updates-apt-upgrade", "upgrade", "no-kernel", updJob)
	if msg := updMessage(err); msg != "Güncelleme günlüğü oluşturulamadı." {
		t.Errorf("error %v", err)
	}
	if got := f.read(victim); got != "dokunma" {
		t.Errorf("the link target was changed to %q", got)
	}
	if c := f.changes(); len(c) != 0 {
		t.Errorf("commands ran: %q", c)
	}
}

func TestUpdatesUpgradeStartFailure(t *testing.T) {
	f := newUpdFakes(t)
	f.write("systemd-run.rc", "1")
	_, err := updRun(t, "updates-apt-upgrade", "upgrade", "no-kernel", updJob)
	if msg := updMessage(err); msg != "Güncelleme işlemi başlatılamadı." {
		t.Errorf("error %v", err)
	}
	res := readAptResult()
	if res["id"] != updJob || res["state"] != updatescheck.StateFailed || res["message"] == "" {
		t.Errorf("result = %v", res)
	}
}

/* ---------- the body of the unit ---------- */

func TestUpdatesRunRefusesOutsideItsUnit(t *testing.T) {
	cgroups := []string{
		"0::/system.slice/myserver.service\n",
		"0::/user.slice/user-1000.slice/session-3.scope\n",
		"0::/system.slice/myserver-update.service\n",
		"0::/system.slice/myserver-apt-upgrade.scope\n",
		"0::/system.slice/myserver-apt-upgrade\n",
		"0::/system.slice/xmyserver-apt-upgrade.service\n",
		"0::/\n",
		"",
	}
	for _, cg := range cgroups {
		f := newUpdFakes(t)
		f.write("cgroup", cg)
		if err := prepareAptDir(); err != nil {
			t.Fatal(err)
		}
		if err := writeAptResult(updJob, updatescheck.StateRunning, "", 100, 0); err != nil {
			t.Fatal(err)
		}
		_, err := updRun(t, "updates-apt-run", "upgrade", "kernel", updJob)
		if msg := updMessage(err); msg != "Bu işlem yalnızca güncelleme birimi içinde çalıştırılabilir." {
			t.Errorf("cgroup %q: error %v", cg, err)
		}
		f.wantNothingRun("cgroup " + cg)
		if res := readAptResult(); res["state"] != updatescheck.StateRunning {
			t.Errorf("cgroup %q: the result was changed: %v", cg, res)
		}
	}
	// The cgroup file cannot be read at all.
	f := newUpdFakes(t)
	updatesCgroup = filepath.Join(f.dir, "missing")
	if _, err := updRun(t, "updates-apt-run", "upgrade", "kernel", updJob); updMessage(err) == "" {
		t.Errorf("error %v", err)
	}
	f.wantNothingRun("unreadable cgroup file")
}

func TestUpdatesRunNeedsItsJobRecord(t *testing.T) {
	records := []struct{ id, state string }{
		{"", ""},
		{"ffffffffffffffff", updatescheck.StateRunning},
		{updJob, updatescheck.StateSuccess},
		{updJob, updatescheck.StateFailed},
	}
	for _, r := range records {
		f := newUpdFakes(t)
		if err := prepareAptDir(); err != nil {
			t.Fatal(err)
		}
		if r.id != "" {
			if err := writeAptResult(r.id, r.state, "", 100, 0); err != nil {
				t.Fatal(err)
			}
		}
		_, err := updRun(t, "updates-apt-run", "upgrade", "kernel", updJob)
		if msg := updMessage(err); msg != "Güncelleme işi kaydı bulunamadı." {
			t.Errorf("record %+v: error %v", r, err)
		}
		f.wantNothingRun("job record " + r.id + " " + r.state)
	}
}

func TestUpdatesRunSuccess(t *testing.T) {
	f := newUpdFakes(t)
	f.write("apt-sim-upgrade.out", simWithKernel())
	f.write("apt-sim-install.out", simInstallClean())
	f.write("apt-run-install.out", "Setting up libc6:amd64 (2.39-0ubuntu8.4) ...\n")
	if err := prepareAptDir(); err != nil {
		t.Fatal(err)
	}
	if err := writeAptResult(updJob, updatescheck.StateRunning, "", 100, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(updatesLogFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := updRun(t, "updates-apt-run", "upgrade", "no-kernel", updJob)
	if err != nil || out != "" {
		t.Fatalf("run: %q %v", out, err)
	}
	res := readAptResult()
	if res["id"] != updJob || res["state"] != updatescheck.StateSuccess || res["started"] != "100" ||
		res["finished"] == "0" || res["message"] != "" {
		t.Errorf("result = %v", res)
	}
	log := f.read(updatesLogFile)
	for _, want := range []string{"Güncelleme başlatıldı (upgrade, no-kernel).", "Çekirdek paketleri atlanıyor",
		"Setting up libc6:amd64"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	// The simulations' package lists are not copied into the job output.
	if strings.Contains(log, "Inst ") {
		t.Errorf("simulation output in the log:\n%s", log)
	}
	if n := len(f.callsOf("systemd-run")) + len(f.callsOf("systemctl")); n != 0 {
		t.Errorf("the unit body called systemd %d times", n)
	}
}

func TestUpdatesRunFailure(t *testing.T) {
	f := newUpdFakes(t)
	f.write("apt-run-dist-upgrade.out", "E: dpkg was interrupted, you must manually run 'dpkg --configure -a' to correct the problem.\n")
	f.write("apt-run-dist-upgrade.rc", "100")
	if err := prepareAptDir(); err != nil {
		t.Fatal(err)
	}
	if err := writeAptResult(updJob, updatescheck.StateRunning, "", 100, 0); err != nil {
		t.Fatal(err)
	}
	_, err := updRun(t, "updates-apt-run", "full", "kernel", updJob)
	if msg := updMessage(err); !strings.Contains(msg, "dpkg --configure -a") {
		t.Fatalf("error %v", err)
	}
	res := readAptResult()
	if res["state"] != updatescheck.StateFailed || !strings.Contains(res["message"], "dpkg --configure -a") {
		t.Errorf("result = %v", res)
	}
	if got := updatescheck.CodeForMessage(res["message"], ""); got != "dpkg_interrupted" {
		t.Errorf("the stored message does not map back to its code: %q", res["message"])
	}
	if !strings.Contains(f.read(updatesLogFile), "dpkg was interrupted") {
		t.Error("apt's output is missing from the log")
	}
}

/* ---------- reboot ---------- */

func TestUpdatesRebootRequiresExactHostname(t *testing.T) {
	f := newUpdFakes(t)
	for _, h := range []string{"", " ", "sunucu", "sunucu-10", "sunucu-1 ", " sunucu-1", "SUNUCU-1", "Sunucu-1",
		"sunucu-1\n", "sunucu-1.local", "sunucu-1\x00", "localhost", "*", "--force", "-f", "evet", "true",
		"sunucu-1;reboot", "sunucu_1"} {
		out, err := updRun(t, "updates-reboot", h)
		if msg := updMessage(err); !strings.Contains(msg, "yeniden başlatma iptal edildi") {
			t.Errorf("hostname %q: error %v", h, err)
		}
		if out != "" {
			t.Errorf("hostname %q: output %q", h, out)
		}
	}
	f.wantNothingRun("wrong hostnames")

	updatesHostname = func() (string, error) { return "", nil }
	if _, err := updRun(t, "updates-reboot", ""); updMessage(err) == "" {
		t.Errorf("empty hostname on both sides accepted: %v", err)
	}
	updatesHostname = func() (string, error) { return "", errors.New("no hostname") }
	if _, err := updRun(t, "updates-reboot", updHost); updMessage(err) == "" {
		t.Errorf("unknown hostname accepted: %v", err)
	}
	f.wantNothingRun("unknown hostname")
}

func TestUpdatesRebootRefusedWhileUpdating(t *testing.T) {
	cases := []struct{ file, unit, want string }{
		{"units-active", "myserver-apt-upgrade.service", "Paket güncellemesi sürerken"},
		{"units-active", "myserver-update.service", "MyServer güncellemesi sürerken"},
		{"units-activating", "myserver-apt-upgrade.service", "Paket güncellemesi sürerken"},
		{"units-activating", "myserver-update.service", "MyServer güncellemesi sürerken"},
	}
	for _, c := range cases {
		f := newUpdFakes(t)
		f.write(c.file, c.unit+"\n")
		_, err := updRun(t, "updates-reboot", updHost)
		if msg := updMessage(err); !strings.Contains(msg, c.want) {
			t.Errorf("%s %s: error %v", c.file, c.unit, err)
		}
		if c := f.changes(); len(c) != 0 {
			t.Errorf("commands ran: %q", c)
		}
	}
}

func TestUpdatesRebootCommand(t *testing.T) {
	f := newUpdFakes(t)
	if _, err := updRun(t, "updates-reboot", updHost); err != nil {
		t.Fatalf("reboot: %v", err)
	}
	if got, want := f.changes(), [][]string{{"systemctl", "reboot"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %q, want %q", got, want)
	}
}

// Only the reboot action may ever ask for a reboot, and no action may shut
// the machine down in any other way.
func TestUpdatesOtherActionsNeverReboot(t *testing.T) {
	f := newUpdFakes(t)
	f.write("apt-sim-upgrade.out", simWithKernel())
	f.write("apt-sim-install.out", simInstallClean())
	if err := os.WriteFile(filepath.Join(f.dir, "reboot-required"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	steps := [][]string{
		{"updates-apt-refresh"}, {"updates-apt-list"},
		{"updates-apt-upgrade", "full", "kernel", updJob},
		{"updates-apt-run", "full", "kernel", updJob},
		{"updates-apt-run", "upgrade", "no-kernel", updJob},
		{"updates-self", "1.2.3", updSum, updURL},
	}
	for _, s := range steps {
		if s[0] == "updates-apt-run" {
			if err := writeAptResult(updJob, updatescheck.StateRunning, "", 100, 0); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := updRun(t, s[0], s[1:]...); err != nil {
			t.Fatalf("%v: %v", s, err)
		}
	}
	for _, c := range f.calls() {
		for _, a := range c[1:] {
			switch a {
			case "reboot", "poweroff", "halt", "kexec", "shutdown", "isolate", "emergency", "rescue":
				t.Errorf("%q in %q", a, c)
			}
		}
	}
}

/* ---------- self-update ---------- */

func TestUpdatesSelfStartsTransientUnit(t *testing.T) {
	for _, version := range []string{"1.2.3", "v1.2.3", "2.0.0-rc.1"} {
		f := newUpdFakes(t)
		if out, err := updRun(t, "updates-self", version, updSum, updURL); err != nil || out != "" {
			t.Fatalf("%s: %q %v", version, out, err)
		}
		want := [][]string{
			{"systemctl", "is-active", "myserver-update.service"},
			{"systemctl", "is-active", "myserver-apt-upgrade.service"},
			{"systemctl", "reset-failed", "myserver-update.service"},
			{"systemd-run", "--unit=myserver-update", "--collect", "--quiet", "--no-block",
				"--description=MyServer güncellemesi",
				"--setenv=MYSERVER_UPDATE_VERSION=" + version,
				"--setenv=MYSERVER_UPDATE_SHA256=" + updSum,
				"--setenv=MYSERVER_UPDATE_URL=" + updURL,
				"--", f.script, version},
		}
		got := f.calls()
		for i := range got {
			if got[i][0] == "systemctl" && got[i][1] == "is-active" {
				got[i] = append([]string{"systemctl", "is-active"}, got[i][len(got[i])-1])
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: calls = %q\nwant %q", version, got, want)
		}
	}
}

func TestUpdatesSelfRefusedWhileUpdating(t *testing.T) {
	cases := []struct{ file, unit, want string }{
		{"units-active", "myserver-update.service", "Bir MyServer güncellemesi zaten çalışıyor."},
		{"units-active", "myserver-apt-upgrade.service", "Paket güncellemesi sürerken MyServer güncellenemez."},
		{"units-activating", "myserver-update.service", "Bir MyServer güncellemesi zaten çalışıyor."},
		{"units-activating", "myserver-apt-upgrade.service", "Paket güncellemesi sürerken MyServer güncellenemez."},
	}
	for _, c := range cases {
		f := newUpdFakes(t)
		f.write(c.file, c.unit+"\n")
		_, err := updRun(t, "updates-self", "1.2.3", updSum, updURL)
		if msg := updMessage(err); msg != c.want {
			t.Errorf("%s %s: error %v", c.file, c.unit, err)
		}
		if c := f.changes(); len(c) != 0 {
			t.Errorf("commands ran: %q", c)
		}
	}
}

func TestUpdatesSelfChecksTheScript(t *testing.T) {
	type prep func(t *testing.T, f *updFakes)
	cases := map[string]prep{
		"missing":        func(t *testing.T, f *updFakes) { os.Remove(f.script) },
		"not executable": func(t *testing.T, f *updFakes) { os.Chmod(f.script, 0o644) },
		"group-writable": func(t *testing.T, f *updFakes) { os.Chmod(f.script, 0o775) },
		"world-writable": func(t *testing.T, f *updFakes) { os.Chmod(f.script, 0o757) },
		"owned by the panel user": func(t *testing.T, f *updFakes) {
			if err := os.Chown(f.script, 1000, 1000); err != nil {
				t.Fatal(err)
			}
		},
		"symbolic link": func(t *testing.T, f *updFakes) {
			target := filepath.Join(f.dir, "real-update.sh")
			if err := os.Rename(f.script, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, f.script); err != nil {
				t.Fatal(err)
			}
		},
		"directory": func(t *testing.T, f *updFakes) {
			os.Remove(f.script)
			if err := os.Mkdir(f.script, 0o755); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, prepare := range cases {
		t.Run(name, func(t *testing.T) {
			f := newUpdFakes(t)
			prepare(t, f)
			_, err := updRun(t, "updates-self", "1.2.3", updSum, updURL)
			if updMessage(err) == "" {
				t.Fatalf("error %v", err)
			}
			f.wantNothingRun(name)
		})
	}
}

func TestUpdatesSelfStartFailure(t *testing.T) {
	f := newUpdFakes(t)
	f.write("systemd-run.rc", "1")
	_, err := updRun(t, "updates-self", "1.2.3", updSum, updURL)
	if msg := updMessage(err); msg != "Güncelleme işlemi başlatılamadı." {
		t.Errorf("error %v", err)
	}
}

func TestUpdatesCapBuffer(t *testing.T) {
	c := &capBuffer{n: 10}
	for _, chunk := range []string{"12345", "6789", "abcdef", "ghi"} {
		if n, err := c.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
		}
	}
	if got := c.buf.String(); got != "123456789a" {
		t.Errorf("buffer = %q", got)
	}
}
