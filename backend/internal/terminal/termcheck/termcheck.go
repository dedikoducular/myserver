// Package termcheck holds the portable validation shared by the terminal
// module and the root helper: which system accounts may own a web terminal
// shell. It has no dependency on the rest of the panel so the helper can
// import it, and its parsers take text as input so they can be tested on any
// platform.
package termcheck

import (
	"path"
	"regexp"
	"strconv"
	"strings"
)

// MinUID is the first uid of a regular (non-system) account on Debian/Ubuntu.
const MinUID = 1000

// nobodyUID is the overflow account; it is never a login user.
const nobodyUID = 65534

// invalidID is (uid_t)-1 and (gid_t)-1: the kernel reserves it to mean "no
// change" and refuses it as an identity, so a record carrying it is malformed.
const invalidID = 1<<32 - 1

var usernameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// ValidUsername reports whether name is a syntactically acceptable system
// account name.
func ValidUsername(name string) bool { return usernameRe.MatchString(name) }

// Entry is one /etc/passwd record.
type Entry struct {
	Name  string
	UID   uint32
	GID   uint32
	Gecos string
	Home  string
	Shell string
}

// ParsePasswd parses /etc/passwd content. Malformed lines, comments and NIS
// compatibility entries are skipped.
func ParsePasswd(data string) []Entry {
	var out []Entry
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || line[0] == '#' || line[0] == '+' || line[0] == '-' {
			continue
		}
		f := strings.Split(line, ":")
		if len(f) != 7 {
			continue
		}
		uid, err := strconv.ParseUint(f[2], 10, 32)
		if err != nil {
			continue
		}
		gid, err := strconv.ParseUint(f[3], 10, 32)
		if err != nil {
			continue
		}
		if uid == invalidID || gid == invalidID {
			continue
		}
		out = append(out, Entry{
			Name: f[0], UID: uint32(uid), GID: uint32(gid),
			Gecos: f[4], Home: f[5], Shell: f[6],
		})
	}
	return out
}

// Find returns the first entry with the given name, as getpwnam would.
func Find(entries []Entry, name string) (Entry, bool) {
	for _, e := range entries {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

// FullName extracts the display name from the GECOS field.
func (e Entry) FullName() string {
	name, _, _ := strings.Cut(e.Gecos, ",")
	return strings.TrimSpace(name)
}

// noLoginShells are programs that are configured as a shell precisely to
// prevent interactive logins.
var noLoginShells = map[string]bool{
	"nologin": true, "false": true, "true": true, "sync": true,
	"halt": true, "shutdown": true, "git-shell": true,
}

// ValidShell reports whether shell is a plausible interactive login shell
// path. It does not touch the file system.
func ValidShell(shell string) bool {
	if shell == "" || shell == "/" || !path.IsAbs(shell) || path.Clean(shell) != shell {
		return false
	}
	if strings.ContainsAny(shell, "\x00\n\r ") {
		return false
	}
	return !noLoginShells[path.Base(shell)]
}

// Reason explains why an account may not own a terminal.
type Reason string

const (
	OK            Reason = ""
	BadName       Reason = "bad_name"
	NotFound      Reason = "not_found"
	IsRoot        Reason = "root"
	SystemAccount Reason = "system_account"
	NoShell       Reason = "no_shell"
	BadHome       Reason = "bad_home"
)

// Check decides whether the account described by e may own a web terminal
// shell: a regular user (uid >= 1000), never root, with a real login shell.
func Check(e Entry) Reason {
	switch {
	case !ValidUsername(e.Name):
		return BadName
	case e.UID == 0 || e.Name == "root":
		return IsRoot
	case e.UID < MinUID || e.UID == nobodyUID:
		return SystemAccount
	case !ValidShell(e.Shell):
		return NoShell
	case !path.IsAbs(e.Home) || strings.ContainsRune(e.Home, 0):
		return BadHome
	}
	return OK
}

// Lookup validates name and checks the matching entry of passwd content.
func Lookup(passwd, name string) (Entry, Reason) {
	if !ValidUsername(name) {
		return Entry{}, BadName
	}
	e, ok := Find(ParsePasswd(passwd), name)
	if !ok {
		return Entry{}, NotFound
	}
	return e, Check(e)
}

// Eligible returns the accounts in passwd content that pass Check, without
// duplicates, in file order.
func Eligible(passwd string) []Entry {
	out := []Entry{}
	seen := map[string]bool{}
	for _, e := range ParsePasswd(passwd) {
		if seen[e.Name] {
			continue
		}
		seen[e.Name] = true
		if Check(e) == OK {
			out = append(out, e)
		}
	}
	return out
}

// Message is the Turkish explanation of a Reason.
func Message(r Reason) string {
	switch r {
	case OK:
		return ""
	case BadName:
		return "Sistem kullanıcısının adı geçersiz."
	case NotFound:
		return "Sistem kullanıcısı bulunamadı."
	case IsRoot:
		return "Terminal root kullanıcısıyla açılamaz."
	case SystemAccount:
		return "Sistem hesapları (uid 1000'den küçük) terminal için kullanılamaz."
	case NoShell:
		return "Bu kullanıcının oturum açabileceği bir kabuğu yok."
	case BadHome:
		return "Bu kullanıcının ev dizini geçersiz."
	}
	return "Sistem kullanıcısı terminal için uygun değil."
}

// Terminal size bounds accepted from clients.
const (
	MinSize = 2
	MaxSize = 500
)

// ValidSize reports whether a terminal dimension is within bounds.
func ValidSize(cols, rows int) bool {
	return cols >= MinSize && cols <= MaxSize && rows >= MinSize && rows <= MaxSize
}

// StartedMarker is written by the helper to its stderr (a pipe, not the
// terminal) once the shell has been started. From then on the helper's exit
// code is the shell's exit code rather than a helper failure.
const StartedMarker = "MYSERVER_SHELL_STARTED"

// ProcStat is the part of /proc/<pid>/stat the helper needs.
type ProcStat struct {
	PPID    int
	PGRP    int
	Session int
}

// ParseStat parses the content of /proc/<pid>/stat. The command name may
// contain spaces and parentheses, so fields are counted from the last ')'.
func ParseStat(stat string) (ProcStat, bool) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return ProcStat{}, false
	}
	// After ")": state ppid pgrp session ...
	f := strings.Fields(stat[i+1:])
	if len(f) < 4 {
		return ProcStat{}, false
	}
	var p ProcStat
	var err error
	if p.PPID, err = strconv.Atoi(f[1]); err != nil {
		return ProcStat{}, false
	}
	if p.PGRP, err = strconv.Atoi(f[2]); err != nil {
		return ProcStat{}, false
	}
	if p.Session, err = strconv.Atoi(f[3]); err != nil {
		return ProcStat{}, false
	}
	return p, true
}
