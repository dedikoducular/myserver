package servicescheck

import (
	"strings"
	"testing"
)

func TestValidUnitAccepts(t *testing.T) {
	valid := []string{
		"ssh.service",
		"docker.service",
		"NetworkManager.service",
		"user@1000.service",
		"getty@tty1.service",
		"systemd-fsck@dev-disk-by\\x2duuid-1A2B\\x2d3C4D.service",
		"systemd-cryptsetup@luks\\x2d0f1e2d3c.service",
		"dbus-org.freedesktop.login1.service",
		"snap.lxd.daemon.service",
		"a.service",
		"foo_bar:baz.service",
		strings.Repeat("a", 128) + ".service",
	}
	for _, name := range valid {
		if !ValidUnit(name) {
			t.Errorf("ValidUnit(%q) = false, want true", name)
		}
	}
}

func TestValidUnitRejects(t *testing.T) {
	invalid := []string{
		"",
		".service",
		"ssh",
		"ssh.socket",
		"ssh.service.socket",
		"ssh.servicex",
		"SSH.SERVICE",
		"--now.service",
		"-f.service",
		"--version",
		"/etc/systemd/system/ssh.service",
		"../ssh.service",
		"a/b.service",
		"ssh .service",
		" ssh.service",
		"ssh.service ",
		"ssh\t.service",
		"ssh.service\n",
		"ssh\n.service",
		"ssh\x00.service",
		"ssh;reboot.service",
		"ssh|cat.service",
		"ssh&.service",
		"$(id).service",
		"`id`.service",
		"ssh'.service",
		"ssh\".service",
		"ssh*.service",
		"ssh?.service",
		"ssh>.service",
		"ssh<.service",
		"ssh!.service",
		"ssh{a,b}.service",
		"ssh~.service",
		"ssh#.service",
		"ssh%41.service",
		"ssh=.service",
		"ssh,docker.service",
		"servis-ş.service",
		// A backslash is only valid as part of a systemd \xNN escape.
		"ssh\\.service",
		"ssh\\n.service",
		"ssh\\x2.service",
		"ssh\\xZZ.service",
		"ssh\\\\x2d.service",
		strings.Repeat("a", 129) + ".service",
		strings.Repeat("a", 5000) + ".service",
		strings.Repeat("\\x2d", 100) + ".service",
	}
	for _, name := range invalid {
		if ValidUnit(name) {
			t.Errorf("ValidUnit(%q) = true, want false", name)
		}
	}
}

func TestIsTemplate(t *testing.T) {
	cases := map[string]bool{
		"getty@.service":     true,
		"user@.service":      true,
		"getty@tty1.service": false,
		"ssh.service":        false,
		"user@1000.service":  false,
	}
	for name, want := range cases {
		if got := IsTemplate(name); got != want {
			t.Errorf("IsTemplate(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestValidVerb(t *testing.T) {
	for _, v := range []string{"start", "stop", "restart", "reload", "enable", "disable"} {
		if !ValidVerb(v) {
			t.Errorf("ValidVerb(%q) = false, want true", v)
		}
	}
	for _, v := range []string{
		"", "mask", "unmask", "kill", "edit", "daemon-reload", "reenable", "isolate",
		"try-restart", "reload-or-restart", "preset", "link", "revert", "set-property",
		"Start", "STOP", " stop", "stop ", "stop\n", "--now", "enable --now", "poweroff", "reboot",
	} {
		if ValidVerb(v) {
			t.Errorf("ValidVerb(%q) = true, want false", v)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		unit, level, kind string
	}{
		{"ssh.service", RiskCritical, KindSSH},
		{"sshd.service", RiskCritical, KindSSH},
		{"systemd-networkd.service", RiskCritical, KindNetwork},
		{"NetworkManager.service", RiskCritical, KindNetwork},
		{"systemd-resolved.service", RiskCritical, KindNetwork},
		{"docker.service", RiskCritical, KindContainer},
		{"containerd.service", RiskCritical, KindContainer},
		{"myserver.service", RiskCritical, KindPanel},
		{"dbus.service", RiskProtected, KindCore},
		{"dbus-broker.service", RiskProtected, KindCore},
		{"systemd-journald.service", RiskProtected, KindCore},
		{"systemd-logind.service", RiskProtected, KindCore},
		{"systemd-udevd.service", RiskProtected, KindCore},
		{"nginx.service", RiskNormal, KindNone},
		{"cron.service", RiskNormal, KindNone},
	}
	for _, c := range cases {
		level, kind := Classify(c.unit)
		if level != c.level || kind != c.kind {
			t.Errorf("Classify(%q) = %q,%q want %q,%q", c.unit, level, kind, c.level, c.kind)
		}
	}
}

func TestDenied(t *testing.T) {
	protectedUnits := []string{
		"dbus.service", "dbus-broker.service", "systemd-journald.service",
		"systemd-logind.service", "systemd-udevd.service",
	}
	for _, u := range protectedUnits {
		for _, v := range []string{VerbStop, VerbDisable} {
			if !Denied(v, u) {
				t.Errorf("Denied(%q, %q) = false, want true", v, u)
			}
		}
		for _, v := range []string{VerbStart, VerbEnable, VerbReload} {
			if Denied(v, u) {
				t.Errorf("Denied(%q, %q) = true, want false", v, u)
			}
		}
	}
	// Both names of the message bus refuse a restart.
	for _, u := range []string{"dbus.service", "dbus-broker.service"} {
		if !Denied(VerbRestart, u) {
			t.Errorf("Denied(restart, %q) = false, want true", u)
		}
	}
	for _, u := range []string{"systemd-journald.service", "systemd-logind.service", "systemd-udevd.service"} {
		if Denied(VerbRestart, u) {
			t.Errorf("Denied(restart, %q) = true, want false", u)
		}
	}
	for _, u := range []string{"ssh.service", "docker.service", "nginx.service", "myserver.service"} {
		for _, v := range []string{VerbStart, VerbStop, VerbRestart, VerbReload, VerbEnable, VerbDisable} {
			if Denied(v, u) {
				t.Errorf("Denied(%q, %q) = true, want false", v, u)
			}
		}
	}
}

func TestNeedsConfirm(t *testing.T) {
	risky := []string{
		"ssh.service", "sshd.service", "docker.service", "containerd.service", "myserver.service",
		"systemd-networkd.service", "NetworkManager.service", "systemd-resolved.service",
		"systemd-journald.service", "systemd-logind.service", "systemd-udevd.service",
		"dbus.service", "dbus-broker.service",
	}
	for _, u := range risky {
		for _, v := range []string{VerbStop, VerbDisable, VerbRestart} {
			if !NeedsConfirm(v, u) {
				t.Errorf("NeedsConfirm(%q, %q) = false, want true", v, u)
			}
		}
		for _, v := range []string{VerbStart, VerbEnable, VerbReload} {
			if NeedsConfirm(v, u) {
				t.Errorf("NeedsConfirm(%q, %q) = true, want false", v, u)
			}
		}
	}
	for _, v := range []string{VerbStart, VerbStop, VerbRestart, VerbReload, VerbEnable, VerbDisable} {
		if NeedsConfirm(v, "nginx.service") {
			t.Errorf("NeedsConfirm(%q, nginx.service) = true, want false", v)
		}
	}
}

func TestDisruptive(t *testing.T) {
	want := map[string]bool{
		VerbStart: false, VerbStop: true, VerbRestart: true,
		VerbReload: false, VerbEnable: false, VerbDisable: true,
	}
	for v, w := range want {
		if got := Disruptive(v); got != w {
			t.Errorf("Disruptive(%q) = %v, want %v", v, got, w)
		}
	}
}
