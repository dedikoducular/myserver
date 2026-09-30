// Package servicescheck holds the portable validation and risk rules for
// systemd service units. It is shared by the panel module and the root
// helper, so it must not import anything but the standard library.
package servicescheck

import (
	"regexp"
	"strings"
)

// Verbs accepted by the panel and the helper. Masking is deliberately absent.
const (
	VerbStart   = "start"
	VerbStop    = "stop"
	VerbRestart = "restart"
	VerbReload  = "reload"
	VerbEnable  = "enable"
	VerbDisable = "disable"
)

// Risk levels.
const (
	RiskNormal    = "normal"
	RiskCritical  = "critical"
	RiskProtected = "protected"
)

// Risk kinds select the warning shown to the user.
const (
	KindNone      = ""
	KindSSH       = "ssh"
	KindPanel     = "panel"
	KindNetwork   = "network"
	KindContainer = "container"
	KindCore      = "core"
)

// PanelUnit is the unit the panel itself runs as.
const PanelUnit = "myserver.service"

// A backslash is accepted only as part of a systemd "\xNN" escape, which
// instance names derived from paths contain
// ("systemd-fsck@dev-disk-by\x2duuid-....service").
var unitRe = regexp.MustCompile(`^(?:[A-Za-z0-9@_.:\-]|\\x[0-9a-f]{2})+\.service$`)

const maxUnitLen = 128 + len(".service")

// ValidUnit reports whether name is an acceptable service unit name.
func ValidUnit(name string) bool {
	return len(name) <= maxUnitLen && unitRe.MatchString(name) && !strings.HasPrefix(name, "-")
}

// IsTemplate reports whether name is a template ("foo@.service"), which
// cannot be started without an instance name.
func IsTemplate(name string) bool {
	return strings.HasSuffix(name, "@.service")
}

// ValidVerb reports whether verb is one of the supported actions.
func ValidVerb(verb string) bool {
	switch verb {
	case VerbStart, VerbStop, VerbRestart, VerbReload, VerbEnable, VerbDisable:
		return true
	}
	return false
}

var critical = map[string]string{
	"ssh.service":              KindSSH,
	"sshd.service":             KindSSH,
	"systemd-networkd.service": KindNetwork,
	"NetworkManager.service":   KindNetwork,
	"systemd-resolved.service": KindNetwork,
	"docker.service":           KindContainer,
	"containerd.service":       KindContainer,
	PanelUnit:                  KindPanel,
}

// protected units may never be stopped or disabled through the panel.
var protected = map[string]bool{
	"dbus.service":             true,
	"dbus-broker.service":      true,
	"systemd-journald.service": true,
	"systemd-logind.service":   true,
	"systemd-udevd.service":    true,
}

// noRestart units additionally refuse a restart: restarting the message bus
// on a running system breaks logins and service management.
var noRestart = map[string]bool{
	"dbus.service":        true,
	"dbus-broker.service": true,
}

// Classify returns the risk level and kind of a unit.
func Classify(unit string) (level, kind string) {
	if protected[unit] {
		return RiskProtected, KindCore
	}
	if k, ok := critical[unit]; ok {
		return RiskCritical, k
	}
	return RiskNormal, KindNone
}

// Disruptive reports whether verb can take a running service away.
func Disruptive(verb string) bool {
	return verb == VerbStop || verb == VerbDisable || verb == VerbRestart
}

// Denied reports whether the action is refused outright.
func Denied(verb, unit string) bool {
	if !protected[unit] {
		return false
	}
	if verb == VerbStop || verb == VerbDisable {
		return true
	}
	return verb == VerbRestart && noRestart[unit]
}

// NeedsConfirm reports whether the action requires the user to type the unit
// name as an explicit acknowledgement.
func NeedsConfirm(verb, unit string) bool {
	level, _ := Classify(unit)
	return level != RiskNormal && Disruptive(verb)
}
