package settings

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const zoneinfoDir = "/usr/share/zoneinfo"

var (
	tzOnce sync.Once
	tzList []string
)

// Timezones returns the IANA zone names installed on the system.
func Timezones() []string {
	tzOnce.Do(func() {
		set := map[string]struct{}{"UTC": {}}
		for _, tab := range []string{"zone1970.tab", "zone.tab"} {
			f, err := os.Open(filepath.Join(zoneinfoDir, tab))
			if err != nil {
				continue
			}
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				line := sc.Text()
				if line == "" || line[0] == '#' {
					continue
				}
				if fields := strings.Split(line, "\t"); len(fields) >= 3 {
					set[fields[2]] = struct{}{}
				}
			}
			f.Close()
			break
		}
		for name := range set {
			tzList = append(tzList, name)
		}
		sort.Strings(tzList)
	})
	return tzList
}

var tzNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+-]*(/[A-Za-z0-9_+-]+){0,2}$`)

// ValidTimezone reports whether name is an installed zone. The name is
// pattern-checked first so it can never escape the zoneinfo directory.
func ValidTimezone(name string) bool {
	if len(name) > 64 || !tzNameRe.MatchString(name) {
		return false
	}
	fi, err := os.Stat(filepath.Join(zoneinfoDir, filepath.FromSlash(name)))
	return err == nil && fi.Mode().IsRegular()
}

// CurrentTimezone returns the system zone name, or "UTC" if unknown.
func CurrentTimezone() string {
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(target, "zoneinfo/"); i >= 0 {
			return target[i+len("zoneinfo/"):]
		}
	}
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		if name := strings.TrimSpace(string(b)); name != "" {
			return name
		}
	}
	return "UTC"
}

var hostnameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidHostname reports whether name is a valid single-label hostname.
func ValidHostname(name string) bool { return hostnameRe.MatchString(name) }
