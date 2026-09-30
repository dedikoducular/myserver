package netcheck

import (
	"regexp"
	"strings"
)

// RedactionMask replaces every secret value in displayed configuration.
const RedactionMask = "******** (gizlendi)"

// NetplanFile is one file of /etc/netplan as returned by the helper. Content
// is always redacted.
type NetplanFile struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Mode      string `json:"mode"`
	Modified  int64  `json:"modified"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
	// Redacted is the number of values that were hidden.
	Redacted int `json:"redacted"`
	// Error is a Turkish explanation when the file was skipped.
	Error string `json:"error"`
}

// NetplanResult is the output of the network-netplan-read helper action.
type NetplanResult struct {
	Files []NetplanFile `json:"files"`
}

var (
	keyLineRe = regexp.MustCompile(`^(\s*(?:-\s+)*)(["']?)([A-Za-z0-9_.\-]+)(["']?)(\s*:)(\s*)(.*)$`)
	inlineRe  = regexp.MustCompile(`(["']?)([A-Za-z0-9_.\-]+)(["']?)(\s*:\s*)("(?:[^"\\]|\\.)*"|'[^']*'|[^,{}\[\]\s][^,{}\[\]]*)`)
	// A WireGuard key is 32 bytes in base64: 43 characters and one '='.
	wgKeyRe = regexp.MustCompile(`[A-Za-z0-9+/]{43}=`)
)

var secretExact = map[string]bool{
	"key": true, "private": true, "shared": true, "psk": true, "pin": true,
	"password": true, "passphrase": true, "secret": true, "token": true,
}

// IsSecretKey reports whether a YAML key names a secret value.
func IsSecretKey(key string) bool {
	k := strings.ToLower(strings.ReplaceAll(key, "_", "-"))
	if secretExact[k] {
		return true
	}
	for _, part := range []string{"password", "passphrase", "secret", "psk", "token", "private"} {
		if strings.Contains(k, part) {
			return true
		}
	}
	if strings.HasSuffix(k, "-key") && !strings.Contains(k, "public") {
		return true
	}
	return false
}

func indentOf(line string) int {
	n := 0
	for _, c := range line {
		if c == ' ' || c == '\t' {
			n++
			continue
		}
		break
	}
	return n
}

// RedactNetplan hides secret values in netplan YAML: wifi passwords,
// 802.1x credentials, WireGuard private and pre-shared keys, and anything
// that looks like a WireGuard key wherever it appears. It works on lines
// rather than on a parsed document so that the layout the administrator
// wrote is kept. It returns the redacted text and the number of values hidden.
func RedactNetplan(text string) (string, int) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	count := 0
	block := false
	blockIndent := 0
	for i, line := range lines {
		if block {
			if strings.TrimSpace(line) == "" {
				continue
			}
			if indentOf(line) > blockIndent {
				lines[i] = strings.Repeat(" ", indentOf(line)) + RedactionMask
				count++
				continue
			}
			block = false
		}
		secretLine := false
		if m := keyLineRe.FindStringSubmatch(line); m != nil && IsSecretKey(m[3]) {
			secretLine = true
			rest := strings.TrimSpace(m[7])
			head := m[1] + m[2] + m[3] + m[4] + m[5]
			// Whatever follows and is indented deeper than the key itself
			// (the column after any "- " list markers) belongs to the
			// value: a block scalar, a nested mapping or the continuation
			// lines of a multi-line scalar.
			block, blockIndent = true, len(m[1])
			switch {
			case rest == "" || strings.HasPrefix(rest, "#"):
			case rest[0] == '|' || rest[0] == '>':
			default:
				lines[i] = head + " " + RedactionMask
				count++
				continue
			}
		}
		if !secretLine && strings.Contains(line, "{") {
			// Flow style. The whole line is searched, because the text in
			// front of the braces need not be a simple key: an SSID with
			// spaces, a list marker, or nothing at all.
			lines[i] = inlineRe.ReplaceAllStringFunc(line, func(s string) string {
				sm := inlineRe.FindStringSubmatch(s)
				if sm == nil || !IsSecretKey(sm[2]) {
					return s
				}
				count++
				return sm[1] + sm[2] + sm[3] + sm[4] + `"` + RedactionMask + `"`
			})
		}
		if wgKeyRe.MatchString(lines[i]) {
			lines[i] = wgKeyRe.ReplaceAllStringFunc(lines[i], func(string) string {
				count++
				return RedactionMask
			})
		}
	}
	return strings.Join(lines, "\n"), count
}
