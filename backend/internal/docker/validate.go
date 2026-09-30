package docker

import (
	"regexp"
	"strings"
	"time"
)

// Identifier patterns. Every identifier is checked before it reaches the SDK.
var (
	// Container ID (hex) or name.
	containerRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	// Image ID, optionally with the digest algorithm prefix.
	imageIDRe = regexp.MustCompile(`^(sha256:)?[a-f0-9]{12,64}$`)
	// Image reference: [registry[:port]/]path[:tag][@sha256:digest]
	imageRefRe = regexp.MustCompile(`^[a-z0-9]+(?:[._-]+[a-z0-9]+)*(?::[0-9]{1,5})?` +
		`(?:/[a-z0-9]+(?:[._-]+[a-z0-9]+)*)*` +
		`(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?` +
		`(?:@sha256:[a-f0-9]{64})?$`)
	volumeRe  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,254}$`)
	networkRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	jobRe     = regexp.MustCompile(`^[a-f0-9]{16}$`)
	hex64Re   = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

func validContainerID(s string) bool { return containerRe.MatchString(s) }
func validImageID(s string) bool     { return imageIDRe.MatchString(s) }
func validVolumeName(s string) bool  { return volumeRe.MatchString(s) }
func validNetworkID(s string) bool   { return networkRe.MatchString(s) }
func validJobID(s string) bool       { return jobRe.MatchString(s) }

func validImageRef(s string) bool {
	return len(s) > 0 && len(s) <= 255 && imageRefRe.MatchString(s)
}

const masked = "••••••••"

// sensitiveWords mark a variable, label or option name as secret. PWD covers
// names such as MYSQL_PWD.
var sensitiveWords = []string{"PASS", "PWD", "SECRET", "TOKEN", "KEY", "CREDENTIAL", "PRIVATE"}

func sensitiveName(name string) bool {
	u := strings.ToUpper(name)
	for _, w := range sensitiveWords {
		if strings.Contains(u, w) {
			return true
		}
	}
	return false
}

// urlCredentialRe matches the password part of scheme://user:password@host.
// The password runs to the last "@" of the authority, so a password that
// itself contains "@" is hidden completely.
var urlCredentialRe = regexp.MustCompile(`(://[^:/@\s]*):[^/\s]+@`)

// maskValue hides a value when its name is sensitive and always hides
// passwords embedded in URLs.
func maskValue(name, value string) string {
	if value == "" {
		return value
	}
	if sensitiveName(name) {
		return masked
	}
	return urlCredentialRe.ReplaceAllString(value, "${1}:"+masked+"@")
}

// EnvVar is one environment variable with its value possibly masked.
type EnvVar struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Masked bool   `json:"masked"`
}

func maskEnv(env []string) []EnvVar {
	out := make([]EnvVar, 0, len(env))
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		mv := maskValue(name, value)
		out = append(out, EnvVar{Name: name, Value: mv, Masked: mv != value})
	}
	return out
}

func maskMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = maskValue(k, v)
	}
	return out
}

// maskArgs hides secrets passed on a command line, both as "--name=value"
// and as "--name value".
func maskArgs(args []string) []string {
	out := make([]string, 0, len(args))
	hideNext := false
	for _, a := range args {
		switch {
		case hideNext:
			hideNext = false
			out = append(out, masked)
		case strings.Contains(a, "="):
			name, value, _ := strings.Cut(a, "=")
			out = append(out, name+"="+maskValue(name, value))
		default:
			if strings.HasPrefix(a, "-") && sensitiveName(a) {
				hideNext = true
			}
			out = append(out, urlCredentialRe.ReplaceAllString(a, "${1}:"+masked+"@"))
		}
	}
	return out
}

// parseDockerTime converts the API's RFC 3339 timestamps to Unix seconds.
// The zero time ("0001-01-01T00:00:00Z") and unparsable values yield nil.
func parseDockerTime(s string) *int64 {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.IsZero() || t.Year() < 1971 {
		return nil
	}
	u := t.Unix()
	return &u
}

// exitCodeFromStatus reads the exit code from a container list status such
// as "Exited (137) 3 hours ago".
func exitCodeFromStatus(status string) (int, bool) {
	const prefix = "Exited ("
	if !strings.HasPrefix(status, prefix) {
		return 0, false
	}
	rest := status[len(prefix):]
	end := strings.IndexByte(rest, ')')
	if end <= 0 {
		return 0, false
	}
	n := 0
	for _, c := range rest[:end] {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
		if n > 1<<20 {
			return 0, false
		}
	}
	return n, true
}

// signalExit reports exit codes that normally result from a deliberate stop
// (SIGINT, SIGKILL, SIGTERM) rather than from a failure of the program.
func signalExit(code int) bool {
	return code == 130 || code == 137 || code == 143
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
