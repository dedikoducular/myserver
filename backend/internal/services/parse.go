package services

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"myserver/internal/services/servicescheck"
)

// Display states sent to the UI.
const (
	StateRunning      = "running"
	StateActive       = "active" // active without a running process (oneshot, exited)
	StateStopped      = "stopped"
	StateFailed       = "failed"
	StateDisabled     = "disabled"
	StateActivating   = "activating"
	StateDeactivating = "deactivating"
	StateMasked       = "masked"
	StateNotInstalled = "not_installed"
)

// Service is one systemd service unit as shown in the panel.
type Service struct {
	Unit          string   `json:"unit"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	LoadState     string   `json:"load_state"`
	ActiveState   string   `json:"active_state"`
	SubState      string   `json:"sub_state"`
	UnitFileState string   `json:"unit_file_state"`
	State         string   `json:"state"`
	Installed     bool     `json:"installed"`
	MainPID       *int64   `json:"main_pid"`
	MemoryBytes   *int64   `json:"memory_bytes"`
	ActiveSince   *int64   `json:"active_since"`
	CanReload     bool     `json:"can_reload"`
	TriggeredBy   []string `json:"triggered_by"`
	Risk          string   `json:"risk"`
	RiskKind      string   `json:"risk_kind"`
	Featured      bool     `json:"featured"`
}

type listedUnit struct {
	Unit, Load, Active, Sub, Description string
}

// parseListUnits parses `systemctl list-units --no-legend --plain` output.
func parseListUnits(text string) []listedUnit {
	var out []listedUnit
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		// Without --plain a status glyph precedes the unit name.
		if len(f) > 0 && !strings.HasSuffix(f[0], ".service") {
			f = f[1:]
		}
		if len(f) < 4 || !servicescheck.ValidUnit(f[0]) {
			continue
		}
		out = append(out, listedUnit{
			Unit: f[0], Load: f[1], Active: f[2], Sub: f[3],
			Description: cleanText(strings.Join(f[4:], " "), 300),
		})
	}
	return out
}

// parseUnitFiles parses `systemctl list-unit-files --no-legend --plain`
// output into unit name -> unit file state.
func parseUnitFiles(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || !servicescheck.ValidUnit(f[0]) {
			continue
		}
		out[f[0]] = f[1]
	}
	return out
}

// parseShow parses `systemctl show` output for several units (blocks
// separated by blank lines) into Id -> property -> value.
func parseShow(text string) map[string]map[string]string {
	out := map[string]map[string]string{}
	cur := map[string]string{}
	flush := func() {
		if id := cur["Id"]; id != "" {
			out[id] = cur
		}
		cur = map[string]string{}
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		cur[k] = v
	}
	flush()
	return out
}

// parseTimestamp understands "@<unix seconds>" (--timestamp=unix) and the
// classic "Tue 2026-09-29 01:45:06 +03" form, read in the host's zone.
func parseTimestamp(v string, loc *time.Location) *int64 {
	v = strings.TrimSpace(v)
	if v == "" || v == "n/a" || v == "0" {
		return nil
	}
	if rest, ok := strings.CutPrefix(v, "@"); ok {
		if i := strings.IndexByte(rest, '.'); i >= 0 {
			rest = rest[:i]
		}
		n, err := strconv.ParseInt(rest, 10, 64)
		if err != nil || n <= 0 {
			return nil
		}
		return &n
	}
	f := strings.Fields(v)
	if len(f) < 3 {
		return nil
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", f[1]+" "+f[2], loc)
	if err != nil {
		return nil
	}
	n := t.Unix()
	if n <= 0 {
		return nil
	}
	return &n
}

func parsePositive(v string) *int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n <= 0 {
		// "[not set]", "infinity" and the uint64 maximum all mean "unknown".
		return nil
	}
	return &n
}

func cleanText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return s
}

// mapState reduces systemd's states to the ones shown in the panel.
func mapState(load, active, sub, fileState string) string {
	switch {
	case load == "not-found":
		return StateNotInstalled
	case load == "masked" || fileState == "masked" || fileState == "masked-runtime":
		return StateMasked
	}
	switch active {
	case "failed":
		return StateFailed
	case "activating":
		return StateActivating
	case "deactivating":
		return StateDeactivating
	case "reloading", "refreshing":
		return StateRunning
	case "active":
		if sub == "running" {
			return StateRunning
		}
		return StateActive
	}
	if fileState == "disabled" {
		return StateDisabled
	}
	return StateStopped
}

// assemble merges the three systemctl outputs into the service list, sorted
// by unit name.
func assemble(listed []listedUnit, files map[string]string, details map[string]map[string]string, loc *time.Location) []Service {
	byUnit := map[string]*Service{}
	for _, u := range listed {
		if servicescheck.IsTemplate(u.Unit) {
			continue
		}
		byUnit[u.Unit] = &Service{
			Unit: u.Unit, Description: u.Description,
			LoadState: u.Load, ActiveState: u.Active, SubState: u.Sub,
		}
	}
	for name, state := range files {
		if servicescheck.IsTemplate(name) || state == "alias" {
			continue
		}
		s, ok := byUnit[name]
		if !ok {
			// Installed but not loaded into the manager: it is not running.
			s = &Service{Unit: name, LoadState: "unloaded", ActiveState: "inactive", SubState: "dead"}
			byUnit[name] = s
		}
		s.UnitFileState = state
	}
	out := make([]Service, 0, len(byUnit))
	for _, s := range byUnit {
		if d := details[s.Unit]; d != nil {
			if s.UnitFileState == "" {
				s.UnitFileState = d["UnitFileState"]
			}
			if s.Description == "" {
				s.Description = cleanText(d["Description"], 300)
			}
			s.CanReload = d["CanReload"] == "yes"
			for _, t := range strings.Fields(d["TriggeredBy"]) {
				if len(t) <= 160 {
					s.TriggeredBy = append(s.TriggeredBy, cleanText(t, 160))
				}
			}
			if s.ActiveState == "active" || s.ActiveState == "reloading" || s.ActiveState == "deactivating" {
				s.MainPID = parsePositive(d["MainPID"])
				s.MemoryBytes = parsePositive(d["MemoryCurrent"])
				s.ActiveSince = parseTimestamp(d["ActiveEnterTimestamp"], loc)
			}
		}
		if s.TriggeredBy == nil {
			s.TriggeredBy = []string{}
		}
		s.Installed = s.LoadState != "not-found"
		s.State = mapState(s.LoadState, s.ActiveState, s.SubState, s.UnitFileState)
		s.Name = displayName(s.Unit)
		s.Risk, s.RiskKind = servicescheck.Classify(s.Unit)
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Unit) < strings.ToLower(out[j].Unit)
	})
	return out
}
