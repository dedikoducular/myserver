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
)

// The helper actions are run against recording shell scripts that stand in
// for systemctl and journalctl. The real binaries are never executed.

const servicesFakeTool = `#!/bin/sh
printf '%s\n' "@NAME@ $*" >> '@LOG@'
if [ "@NAME@" = "journalctl" ]; then
	echo "2026-09-29T10:00:00+0300 sunucu cron[812]: kayit"
	exit 0
fi
last=""
verb=""
for a in "$@"; do
	case "$a" in
	show|start|stop|restart|reload|enable|disable) [ -z "$verb" ] && verb="$a" ;;
	esac
	last="$a"
done
if [ "$verb" = "show" ]; then
	case "$last" in
	sshd.service) echo "ssh.service" ;;
	dbus-org.freedesktop.login1.service) echo "systemd-logind.service" ;;
	messagebus.service) echo "dbus.service" ;;
	panel-alias.service) echo "myserver.service" ;;
	garbage.service) echo "not a unit name" ;;
	empty.service) ;;
	showfails.service) exit 1 ;;
	*) echo "$last" ;;
	esac
	exit 0
fi
case "$last" in
masked.service) echo "Failed to $verb masked.service: Unit masked.service is masked." >&2; exit 1 ;;
missing.service) echo "Failed to $verb missing.service: Unit missing.service not found." >&2; exit 5 ;;
noreload.service) echo "Failed to reload noreload.service: Job type reload is not applicable for unit noreload.service." >&2; exit 1 ;;
broken.service) echo "Job for broken.service failed because the control process exited with error code." >&2; exit 1 ;;
refused.service) echo "Failed to start refused.service: Operation refused, unit refused.service may be requested by dependency only (it is configured to refuse manual start/stop)." >&2; exit 1 ;;
odd.service) echo "something unexpected /etc/secret" >&2; exit 1 ;;
esac
exit 0
`

type servicesFakes struct {
	t   *testing.T
	log string
}

func newServicesFakes(t *testing.T) *servicesFakes {
	t.Helper()
	dir := t.TempDir()
	f := &servicesFakes{t: t, log: filepath.Join(dir, "calls.log")}
	write := func(name string) string {
		body := strings.ReplaceAll(servicesFakeTool, "@NAME@", name)
		body = strings.ReplaceAll(body, "@LOG@", f.log)
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	oldS, oldJ := servicesSystemctl, servicesJournalctl
	servicesSystemctl, servicesJournalctl = write("systemctl"), write("journalctl")
	t.Cleanup(func() { servicesSystemctl, servicesJournalctl = oldS, oldJ })
	return f
}

func (f *servicesFakes) calls() []string {
	f.t.Helper()
	b, err := os.ReadFile(f.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// changes returns the recorded invocations other than the read-only
// "systemctl show" used to resolve aliases.
func (f *servicesFakes) changes() []string {
	var out []string
	for _, c := range f.calls() {
		if strings.HasPrefix(c, "systemctl show ") {
			continue
		}
		out = append(out, c)
	}
	return out
}

func runServicesAction(t *testing.T, name string, args ...string) (string, error) {
	t.Helper()
	a, ok := actions[name]
	if !ok {
		t.Fatalf("action %q is not registered", name)
	}
	var out bytes.Buffer
	err := a(context.Background(), args, strings.NewReader(""), &out)
	return out.String(), err
}

func userMessage(err error) string {
	var ue *UserError
	if errors.As(err, &ue) {
		return ue.Message
	}
	return ""
}

func TestServicesActionsRegistered(t *testing.T) {
	for _, name := range []string{"services-control", "services-logs", "services-logs-follow"} {
		if _, ok := actions[name]; !ok {
			t.Errorf("action %q is not registered", name)
		}
	}
	for name := range actions {
		if strings.HasPrefix(name, "services-") && strings.Contains(name, "mask") {
			t.Errorf("a masking action exists: %q", name)
		}
	}
}

func TestServicesControlRejectsArgumentCount(t *testing.T) {
	f := newServicesFakes(t)
	for _, args := range [][]string{
		nil,
		{"stop"},
		{"cron.service"},
		{"stop", "cron.service", "extra"},
		{"stop", "cron.service", "--now"},
		{"stop", "--", "cron.service"},
		{"stop", "cron.service", "nginx.service"},
	} {
		if _, err := runServicesAction(t, "services-control", args...); err == nil {
			t.Errorf("args %q accepted", args)
		}
	}
	if calls := f.calls(); len(calls) != 0 {
		t.Errorf("commands were executed: %q", calls)
	}
}

func TestServicesControlRejectsVerbs(t *testing.T) {
	f := newServicesFakes(t)
	for _, verb := range []string{
		"", "mask", "unmask", "kill", "edit", "isolate", "daemon-reload", "daemon-reexec", "reenable",
		"try-restart", "reload-or-restart", "link", "preset", "revert", "set-property", "poweroff",
		"reboot", "halt", "START", "Stop", " stop", "stop ", "stop\n", "--now", "-f", "--force",
		"enable --now", "stop;reboot", "show", "status", "cat",
	} {
		_, err := runServicesAction(t, "services-control", verb, "cron.service")
		if err == nil {
			t.Errorf("verb %q accepted", verb)
			continue
		}
		if userMessage(err) != "Bilinmeyen servis işlemi." {
			t.Errorf("verb %q: error = %v", verb, err)
		}
	}
	if calls := f.calls(); len(calls) != 0 {
		t.Errorf("commands were executed: %q", calls)
	}
}

func TestServicesControlRejectsUnits(t *testing.T) {
	f := newServicesFakes(t)
	for _, unit := range []string{
		"", "cron", "cron.socket", "cron.timer", "--now.service", "-f.service", "--help", "-H",
		"--host=evil.service", "/etc/systemd/system/cron.service", "../cron.service", "./cron.service",
		"a/b.service", "cron .service", " cron.service", "cron.service ", "cron\t.service",
		"cron.service\n", "cron\n.service", "cron.service\nssh.service", "cron;reboot.service",
		"cron|id.service", "cron&.service", "$(id).service", "`id`.service", "cron'.service",
		"cron\".service", "cron*.service", "cron?.service", "[a-z].service", "cron\\.service",
		"cron\\n.service", "cron nginx.service", "cron.service nginx.service", "CRON.SERVICE",
		"getty@.service", "user@.service", strings.Repeat("a", 129) + ".service",
		strings.Repeat("a", 100000) + ".service",
	} {
		for _, verb := range []string{"start", "stop", "restart", "reload", "enable", "disable"} {
			_, err := runServicesAction(t, "services-control", verb, unit)
			if err == nil {
				t.Errorf("%s %q accepted", verb, unit)
				continue
			}
			if userMessage(err) != "Servis adı geçersiz." {
				t.Errorf("%s %q: error = %v", verb, unit, err)
			}
		}
	}
	if calls := f.calls(); len(calls) != 0 {
		t.Errorf("commands were executed: %q", calls)
	}
}

func TestServicesControlProtectedUnits(t *testing.T) {
	denied := map[string][]string{
		"dbus.service":             {"stop", "disable", "restart"},
		"dbus-broker.service":      {"stop", "disable", "restart"},
		"systemd-journald.service": {"stop", "disable"},
		"systemd-logind.service":   {"stop", "disable"},
		"systemd-udevd.service":    {"stop", "disable"},
	}
	f := newServicesFakes(t)
	for unit, verbs := range denied {
		for _, verb := range verbs {
			_, err := runServicesAction(t, "services-control", verb, unit)
			if err == nil {
				t.Errorf("%s %s accepted", verb, unit)
				continue
			}
			if !strings.Contains(userMessage(err), "çekirdek sistem servisi") {
				t.Errorf("%s %s: error = %v", verb, unit, err)
			}
		}
	}
	if got := f.changes(); len(got) != 0 {
		t.Errorf("state-changing commands were executed: %q", got)
	}
}

func TestServicesControlAliasesOfProtectedUnits(t *testing.T) {
	f := newServicesFakes(t)
	cases := []struct{ verb, alias string }{
		{"stop", "dbus-org.freedesktop.login1.service"},
		{"disable", "dbus-org.freedesktop.login1.service"},
		{"stop", "messagebus.service"},
		{"disable", "messagebus.service"},
		{"restart", "messagebus.service"},
	}
	for _, c := range cases {
		_, err := runServicesAction(t, "services-control", c.verb, c.alias)
		if err == nil {
			t.Errorf("%s %s accepted although it is another name of a protected unit", c.verb, c.alias)
			continue
		}
		if !strings.Contains(userMessage(err), "çekirdek sistem servisi") {
			t.Errorf("%s %s: error = %v", c.verb, c.alias, err)
		}
	}
	if got := f.changes(); len(got) != 0 {
		t.Errorf("state-changing commands were executed: %q", got)
	}
	// Verbs that are allowed on the protected unit stay allowed by alias.
	if _, err := runServicesAction(t, "services-control", "start", "messagebus.service"); err != nil {
		t.Errorf("start by alias: %v", err)
	}
}

func TestServicesControlRefusesWhenIdentityUnknown(t *testing.T) {
	f := newServicesFakes(t)
	for _, unit := range []string{"garbage.service", "empty.service", "showfails.service"} {
		_, err := runServicesAction(t, "services-control", "stop", unit)
		if err == nil {
			t.Errorf("stop %s accepted although its identity could not be resolved", unit)
			continue
		}
		if userMessage(err) == "" {
			t.Errorf("stop %s: error is not user-presentable: %v", unit, err)
		}
	}
	if got := f.changes(); len(got) != 0 {
		t.Errorf("state-changing commands were executed: %q", got)
	}

	// A missing systemctl must refuse as well.
	servicesSystemctl = filepath.Join(t.TempDir(), "missing")
	if _, err := runServicesAction(t, "services-control", "stop", "cron.service"); err == nil {
		t.Error("stop accepted without systemctl")
	}
}

func TestServicesControlRunsSystemctl(t *testing.T) {
	cases := []struct {
		verb, unit string
		want       string
	}{
		{"start", "cron.service", "systemctl --no-pager --no-ask-password start -- cron.service"},
		{"stop", "cron.service", "systemctl --no-pager --no-ask-password stop -- cron.service"},
		{"restart", "cron.service", "systemctl --no-pager --no-ask-password restart -- cron.service"},
		{"reload", "cron.service", "systemctl --no-pager --no-ask-password reload -- cron.service"},
		{"enable", "cron.service", "systemctl --no-pager --no-ask-password enable -- cron.service"},
		{"disable", "cron.service", "systemctl --no-pager --no-ask-password disable -- cron.service"},
		{"stop", "ssh.service", "systemctl --no-pager --no-ask-password stop -- ssh.service"},
		{"restart", "user@1000.service", "systemctl --no-pager --no-ask-password restart -- user@1000.service"},
		{"start", `systemd-fsck@dev-disk-by\x2duuid-1A2B.service`,
			`systemctl --no-pager --no-ask-password start -- systemd-fsck@dev-disk-by\x2duuid-1A2B.service`},
		{"restart", "systemd-journald.service", "systemctl --no-pager --no-ask-password restart -- systemd-journald.service"},
		{"start", "dbus.service", "systemctl --no-pager --no-ask-password start -- dbus.service"},
		// The panel cannot wait for its own stop or restart.
		{"restart", "myserver.service", "systemctl --no-pager --no-ask-password --no-block restart -- myserver.service"},
		{"stop", "myserver.service", "systemctl --no-pager --no-ask-password --no-block stop -- myserver.service"},
		{"restart", "panel-alias.service", "systemctl --no-pager --no-ask-password --no-block restart -- panel-alias.service"},
		{"disable", "myserver.service", "systemctl --no-pager --no-ask-password disable -- myserver.service"},
		{"start", "myserver.service", "systemctl --no-pager --no-ask-password start -- myserver.service"},
	}
	for _, c := range cases {
		t.Run(c.verb+"/"+c.unit, func(t *testing.T) {
			f := newServicesFakes(t)
			if _, err := runServicesAction(t, "services-control", c.verb, c.unit); err != nil {
				t.Fatalf("error: %v", err)
			}
			if got := f.changes(); !reflect.DeepEqual(got, []string{c.want}) {
				t.Errorf("executed %q, want %q", got, c.want)
			}
			all := f.calls()
			wantShow := "systemctl show --no-pager --property=Id --value -- " + c.unit
			if len(all) != 2 || all[0] != wantShow {
				t.Errorf("calls = %q, want the alias lookup %q first", all, wantShow)
			}
		})
	}
}

func TestServicesControlFailureMessages(t *testing.T) {
	cases := []struct{ verb, unit, want string }{
		{"start", "masked.service", "Servis maskelenmiş durumda; panel maskeyi değiştirmez."},
		{"start", "missing.service", "Servis bu sunucuda bulunamadı."},
		{"reload", "noreload.service", "Bu servis yapılandırmayı yeniden yüklemeyi desteklemiyor."},
		{"restart", "broken.service", "Servis işlemi başarısız oldu. Ayrıntılar için servis loglarına bakın."},
		{"start", "refused.service", "systemd bu servis için elle işlem yapılmasına izin vermiyor."},
	}
	newServicesFakes(t)
	for _, c := range cases {
		_, err := runServicesAction(t, "services-control", c.verb, c.unit)
		if got := userMessage(err); got != c.want {
			t.Errorf("%s %s: message = %q (%v), want %q", c.verb, c.unit, got, err, c.want)
		}
	}
	// An unrecognised failure is an internal error, never shown to the user.
	_, err := runServicesAction(t, "services-control", "start", "odd.service")
	if err == nil {
		t.Fatal("failure of systemctl reported as success")
	}
	if msg := userMessage(err); msg != "" {
		t.Errorf("unrecognised failure turned into the user message %q", msg)
	}
}

func TestServicesLogsRejectsArguments(t *testing.T) {
	f := newServicesFakes(t)
	bad := [][]string{
		nil,
		{"cron.service"},
		{"cron.service", "10", "extra"},
		{"cron.service", "10", "--follow"},
		{"cron.service", "0"},
		{"cron.service", "-1"},
		{"cron.service", "501"},
		{"cron.service", "100000"},
		{"cron.service", "99999999999999999999"},
		{"cron.service", ""},
		{"cron.service", "abc"},
		{"cron.service", "10abc"},
		{"cron.service", "1.5"},
		{"cron.service", "1e2"},
		{"cron.service", " 10"},
		{"cron.service", "10 "},
		{"cron.service", "10\n"},
		{"cron.service", "0x10"},
		{"cron.service", "all"},
		{"cron.service", "10 --file=/etc/shadow"},
		{"", "10"},
		{"cron", "10"},
		{"cron.socket", "10"},
		{"--file=/var/log/x.service", "10"},
		{"-f.service", "10"},
		{"/var/log/journal/x.service", "10"},
		{"cron.service --file=/x.service", "10"},
		{"cron.service\n", "10"},
		{"cron;id.service", "10"},
		{"$(id).service", "10"},
		{"*.service", "10"},
		{"10", "cron.service"},
	}
	for _, args := range bad {
		if out, err := runServicesAction(t, "services-logs", args...); err == nil {
			t.Errorf("args %q accepted (output %q)", args, out)
		}
	}
	if calls := f.calls(); len(calls) != 0 {
		t.Errorf("commands were executed: %q", calls)
	}
}

func TestServicesLogsRunsJournalctl(t *testing.T) {
	cases := []struct {
		unit, lines, want string
	}{
		{"cron.service", "1", "journalctl --unit=cron.service --lines=1 --no-pager --output=short-iso"},
		{"cron.service", "200", "journalctl --unit=cron.service --lines=200 --no-pager --output=short-iso"},
		{"cron.service", "500", "journalctl --unit=cron.service --lines=500 --no-pager --output=short-iso"},
		{"cron.service", "+7", "journalctl --unit=cron.service --lines=7 --no-pager --output=short-iso"},
		{"cron.service", "007", "journalctl --unit=cron.service --lines=7 --no-pager --output=short-iso"},
		{"user@1000.service", "50", "journalctl --unit=user@1000.service --lines=50 --no-pager --output=short-iso"},
	}
	for _, c := range cases {
		f := newServicesFakes(t)
		out, err := runServicesAction(t, "services-logs", c.unit, c.lines)
		if err != nil {
			t.Errorf("%s %s: %v", c.unit, c.lines, err)
			continue
		}
		if !strings.Contains(out, "cron[812]: kayit") {
			t.Errorf("journal output not passed through: %q", out)
		}
		if got := f.calls(); !reflect.DeepEqual(got, []string{c.want}) {
			t.Errorf("executed %q, want %q", got, c.want)
		}
	}
}

func TestServicesLogsFollowRejectsArguments(t *testing.T) {
	f := newServicesFakes(t)
	bad := [][]string{
		nil,
		{"cron.service", "10"},
		{"cron.service", "--file=/etc/shadow"},
		{""},
		{"cron"},
		{"--file=/x.service"},
		{"-f.service"},
		{"/etc/cron.service"},
		{"cron.service\n"},
		{"cron nginx.service"},
		{"cron;id.service"},
		{strings.Repeat("a", 129) + ".service"},
	}
	for _, args := range bad {
		if _, err := runServicesAction(t, "services-logs-follow", args...); err == nil {
			t.Errorf("args %q accepted", args)
		}
	}
	if calls := f.calls(); len(calls) != 0 {
		t.Errorf("commands were executed: %q", calls)
	}
}

func TestServicesLogsFollowRunsJournalctl(t *testing.T) {
	f := newServicesFakes(t)
	out, err := runServicesAction(t, "services-logs-follow", "cron.service")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "cron[812]: kayit") {
		t.Errorf("journal output not passed through: %q", out)
	}
	want := []string{"journalctl --unit=cron.service --follow --lines=0 --no-pager --output=short-iso"}
	if got := f.calls(); !reflect.DeepEqual(got, want) {
		t.Errorf("executed %q, want %q", got, want)
	}
}
