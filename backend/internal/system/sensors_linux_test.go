//go:build linux

package system

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"myserver/internal/module"
	"myserver/internal/settings"

	_ "modernc.org/sqlite"
)

// The hwmon trees below are written from memory of real machines (Intel
// desktop with coretemp, AMD Ryzen with k10temp, NVMe drives, Raspberry Pi).

type sysTree struct {
	t    *testing.T
	root string
}

func newSysTree(t *testing.T) *sysTree {
	t.Helper()
	return &sysTree{t: t, root: t.TempDir()}
}

func (s *sysTree) write(rel, content string) string {
	s.t.Helper()
	path := filepath.Join(s.root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		s.t.Fatal(err)
	}
	return path
}

// hwmon adds class/hwmon/<dir> with a name and temperature inputs given as
// index -> {label, millidegrees}. An empty label writes no label file.
func (s *sysTree) hwmon(dir, name string, temps map[string][2]string) {
	base := "class/hwmon/" + dir + "/"
	s.write(base+"name", name+"\n")
	for idx, v := range temps {
		if v[0] != "" {
			s.write(base+"temp"+idx+"_label", v[0]+"\n")
		}
		s.write(base+"temp"+idx+"_input", v[1]+"\n")
	}
}

func (s *sysTree) nvme(dir, device, milli string) {
	s.hwmon(dir, "nvme", map[string][2]string{"1": {"Composite", milli}})
	target := filepath.Join(s.root, "devices/pci0000:00/nvme", device)
	if err := os.MkdirAll(target, 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(s.root, "class/hwmon", dir, "device")); err != nil {
		s.t.Fatal(err)
	}
}

func (s *sysTree) zone(dir, typ, milli string) {
	s.write("class/thermal/"+dir+"/type", typ+"\n")
	s.write("class/thermal/"+dir+"/temp", milli+"\n")
}

func (s *sysTree) collector() *collector {
	c := newCollector()
	c.sysRoot = s.root
	return c
}

func TestDefaultSysRoot(t *testing.T) {
	if got := newCollector().sysRoot; got != "/sys" {
		t.Fatalf("production collector reads %q, want /sys", got)
	}
}

func TestSensorsCoretemp(t *testing.T) {
	s := newSysTree(t)
	s.hwmon("hwmon0", "acpitz", map[string][2]string{"1": {"", "27800"}})
	s.hwmon("hwmon1", "coretemp", map[string][2]string{
		"2": {"Core 0", "61000"},
		"1": {"Package id 0", "45000"},
		"3": {"Core 1", "43000"},
	})
	temp := s.collector().temperature()
	if temp.CPU == nil || *temp.CPU != 45 {
		t.Fatalf("cpu = %v, want the package temperature 45", temp.CPU)
	}
	if temp.Source != "coretemp (Package id 0)" {
		t.Errorf("source = %q", temp.Source)
	}
	if temp.NVMe == nil || len(temp.NVMe) != 0 {
		t.Errorf("nvme = %v, want an empty list", temp.NVMe)
	}
}

func TestSensorsK10tempPrefersTdie(t *testing.T) {
	s := newSysTree(t)
	s.hwmon("hwmon2", "k10temp", map[string][2]string{
		"1": {"Tctl", "60000"},
		"2": {"Tdie", "50000"},
	})
	temp := s.collector().temperature()
	if temp.CPU == nil || *temp.CPU != 50 || temp.Source != "k10temp (Tdie)" {
		t.Fatalf("got %v %q", temp.CPU, temp.Source)
	}
}

func TestSensorsK10tempTctlOnly(t *testing.T) {
	s := newSysTree(t)
	s.hwmon("hwmon2", "k10temp", map[string][2]string{
		"1": {"Tctl", "48250"},
		"3": {"Tccd1", "41000"},
	})
	temp := s.collector().temperature()
	if temp.CPU == nil || *temp.CPU != 48.25 || temp.Source != "k10temp (Tctl)" {
		t.Fatalf("got %v %q", temp.CPU, temp.Source)
	}
}

func TestSensorsNVMe(t *testing.T) {
	s := newSysTree(t)
	s.nvme("hwmon3", "nvme1", "41850")
	s.nvme("hwmon1", "nvme0", "38850")
	s.hwmon("hwmon2", "coretemp", map[string][2]string{"1": {"Package id 0", "52000"}})
	temp := s.collector().temperature()
	if temp.CPU == nil || *temp.CPU != 52 {
		t.Errorf("cpu = %v", temp.CPU)
	}
	want := []NVMeTemp{{Name: "nvme0", Celsius: 38.85}, {Name: "nvme1", Celsius: 41.85}}
	if len(temp.NVMe) != 2 || temp.NVMe[0] != want[0] || temp.NVMe[1] != want[1] {
		t.Errorf("nvme = %+v, want %+v", temp.NVMe, want)
	}
}

func TestSensorsNVMeOnlyIsNotACPUTemperature(t *testing.T) {
	s := newSysTree(t)
	s.nvme("hwmon0", "nvme0", "38850")
	temp := s.collector().temperature()
	if temp.CPU != nil || temp.Source != "" {
		t.Errorf("cpu = %v source = %q, want none", *temp.CPU, temp.Source)
	}
	if len(temp.NVMe) != 1 {
		t.Errorf("nvme = %+v", temp.NVMe)
	}
}

func TestSensorsNone(t *testing.T) {
	for name, s := range map[string]*sysTree{
		"empty tree": newSysTree(t),
		"unrelated sensors only": func() *sysTree {
			s := newSysTree(t)
			s.hwmon("hwmon0", "BAT0", map[string][2]string{"1": {"", "30000"}})
			s.hwmon("hwmon1", "iwlwifi_1", map[string][2]string{"1": {"", "41000"}})
			s.write("class/thermal/cooling_device0/type", "Processor\n")
			return s
		}(),
		"sensor with impossible readings": func() *sysTree {
			s := newSysTree(t)
			s.hwmon("hwmon0", "coretemp", map[string][2]string{"1": {"Package id 0", "0"}})
			s.hwmon("hwmon1", "k10temp", map[string][2]string{"1": {"Tctl", "-128000"}})
			s.zone("thermal_zone0", "x86_pkg_temp", "255000")
			return s
		}(),
	} {
		temp := s.collector().temperature()
		if temp.CPU != nil || temp.Source != "" {
			t.Errorf("%s: cpu = %v source = %q, want none", name, *temp.CPU, temp.Source)
		}
		if temp.NVMe == nil || len(temp.NVMe) != 0 {
			t.Errorf("%s: nvme = %v, want an empty non-nil list", name, temp.NVMe)
		}
	}
}

func TestSensorsSkipBrokenInput(t *testing.T) {
	s := newSysTree(t)
	s.hwmon("hwmon1", "coretemp", map[string][2]string{
		"1": {"Package id 0", "garbage"},
		"2": {"Core 0", "47000"},
	})
	temp := s.collector().temperature()
	if temp.CPU == nil || *temp.CPU != 47 || temp.Source != "coretemp (Core 0)" {
		t.Fatalf("got %v %q", temp.CPU, temp.Source)
	}
}

func TestSensorsRaspberryPi(t *testing.T) {
	s := newSysTree(t)
	s.hwmon("hwmon0", "cpu_thermal", map[string][2]string{"1": {"", "51121"}})
	s.zone("thermal_zone0", "cpu-thermal", "51121")
	temp := s.collector().temperature()
	if temp.CPU == nil || *temp.CPU != 51.121 || temp.Source != "cpu_thermal" {
		t.Fatalf("got %v %q", temp.CPU, temp.Source)
	}
}

func TestSensorsThermalZoneFallback(t *testing.T) {
	s := newSysTree(t)
	s.zone("thermal_zone0", "acpitz", "27800")
	s.zone("thermal_zone1", "INT3400 Thermal", "20000")
	s.zone("thermal_zone2", "x86_pkg_temp", "44000")
	temp := s.collector().temperature()
	if temp.CPU == nil || *temp.CPU != 44 || temp.Source != "x86_pkg_temp" {
		t.Fatalf("got %v %q", temp.CPU, temp.Source)
	}
}

func TestSensorsHwmonPreferredOverThermalZone(t *testing.T) {
	s := newSysTree(t)
	s.zone("thermal_zone0", "x86_pkg_temp", "44000")
	s.hwmon("hwmon4", "coretemp", map[string][2]string{"1": {"Package id 0", "46000"}})
	temp := s.collector().temperature()
	if temp.CPU == nil || *temp.CPU != 46 {
		t.Fatalf("got %v %q", temp.CPU, temp.Source)
	}
}

func TestSensorReadingFollowsTheFile(t *testing.T) {
	s := newSysTree(t)
	s.hwmon("hwmon1", "coretemp", map[string][2]string{"1": {"Package id 0", "45000"}})
	c := s.collector()
	if temp := c.temperature(); temp.CPU == nil || *temp.CPU != 45 {
		t.Fatalf("first reading: %v", temp.CPU)
	}
	s.write("class/hwmon/hwmon1/temp1_input", "71000\n")
	if temp := c.temperature(); temp.CPU == nil || *temp.CPU != 71 {
		t.Fatalf("second reading: %v", temp.CPU)
	}
	// The sensor disappears (module unloaded): no value, no stale one.
	if err := os.Remove(filepath.Join(s.root, "class/hwmon/hwmon1/temp1_input")); err != nil {
		t.Fatal(err)
	}
	if temp := c.temperature(); temp.CPU != nil {
		t.Fatalf("reading after removal: %v", *temp.CPU)
	}
}

// ---- health ----

func newStore(t *testing.T, values map[string]string) *settings.Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	st, err := settings.NewStore(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range values {
		if err := st.Set(context.Background(), k, v); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func newTestModule(t *testing.T, values map[string]string, tree *sysTree) *Module {
	t.Helper()
	mod, err := New(module.Deps{Settings: newStore(t, values)}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m := mod.(*Module)
	if tree != nil {
		m.col.sysRoot = tree.root
	}
	return m
}

func checkByID(checks []module.HealthCheck, id string) (module.HealthCheck, bool) {
	for _, c := range checks {
		if c.ID == id {
			return c, true
		}
	}
	return module.HealthCheck{}, false
}

func TestLevelBoundaries(t *testing.T) {
	cases := []struct {
		value, warn, crit float64
		want              module.HealthStatus
	}{
		{0, 85, 95, module.Healthy},
		{84.999, 85, 95, module.Healthy},
		{85, 85, 95, module.WarningLevel},
		{94.999, 85, 95, module.WarningLevel},
		{95, 85, 95, module.CriticalLvl},
		{100, 85, 95, module.CriticalLvl},
		{90, 90, 90, module.CriticalLvl},
		{89.9, 90, 90, module.Healthy},
	}
	for _, c := range cases {
		if got := level(c.value, c.warn, c.crit); got != c.want {
			t.Errorf("level(%v, %v, %v) = %s, want %s", c.value, c.warn, c.crit, got, c.want)
		}
	}
}

func TestThresholdDefaults(t *testing.T) {
	m := newTestModule(t, nil, nil)
	if got, want := m.thresholds(), (thresholds{85, 95, 80, 90}); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	m = newTestModule(t, map[string]string{KeyDiskWarning: "abc", KeyTempCritical: ""}, nil)
	if got, want := m.thresholds(), (thresholds{85, 95, 80, 90}); got != want {
		t.Errorf("unreadable values: got %+v, want %+v", got, want)
	}
}

func TestThresholdsInverted(t *testing.T) {
	m := newTestModule(t, map[string]string{
		KeyDiskWarning: "98", KeyDiskCritical: "60",
		KeyTempWarning: "95", KeyTempCritical: "70",
	}, nil)
	th := m.thresholds()
	if th.diskWarn > th.diskCrit || th.tempWarn > th.tempCrit {
		t.Fatalf("warning above critical: %+v", th)
	}
	// Whatever the repair, a value above the configured critical level must
	// not be reported as healthy.
	if got := level(75, th.diskWarn, th.diskCrit); got != module.CriticalLvl {
		t.Errorf("disk at 75%% with critical=60: %s", got)
	}
	if got := level(80, th.tempWarn, th.tempCrit); got != module.CriticalLvl {
		t.Errorf("80 degrees with critical=70: %s", got)
	}
	if got := level(59, th.diskWarn, th.diskCrit); got != module.Healthy {
		t.Errorf("disk at 59%%: %s", got)
	}
}

func TestHealthTemperatureBoundaries(t *testing.T) {
	s := newSysTree(t)
	s.hwmon("hwmon1", "coretemp", map[string][2]string{"1": {"Package id 0", "45000"}})
	m := newTestModule(t, nil, s) // defaults: warning 80, critical 90
	cases := []struct {
		milli string
		want  module.HealthStatus
	}{
		{"45000", module.Healthy},
		{"79999", module.Healthy},
		{"80000", module.WarningLevel},
		{"89999", module.WarningLevel},
		{"90000", module.CriticalLvl},
		{"104000", module.CriticalLvl},
	}
	for _, c := range cases {
		s.write("class/hwmon/hwmon1/temp1_input", c.milli+"\n")
		check, ok := checkByID(m.Health(context.Background()), "system.temperature")
		if !ok {
			t.Fatal("no temperature check")
		}
		if check.Status != c.want {
			t.Errorf("%s: status %s, want %s (%s)", c.milli, check.Status, c.want, check.Message)
		}
	}
}

func TestHealthHottestSensorDecides(t *testing.T) {
	s := newSysTree(t)
	s.hwmon("hwmon1", "coretemp", map[string][2]string{"1": {"Package id 0", "45000"}})
	s.nvme("hwmon2", "nvme0", "84000")
	m := newTestModule(t, nil, s)
	check, _ := checkByID(m.Health(context.Background()), "system.temperature")
	if check.Status != module.WarningLevel {
		t.Errorf("status %s, want WARNING (%s)", check.Status, check.Message)
	}
	if check.Message != "İşlemci 45°C, nvme0 84°C." {
		t.Errorf("message = %q", check.Message)
	}
}

func TestHealthWithoutSensors(t *testing.T) {
	m := newTestModule(t, nil, newSysTree(t))
	checks := m.Health(context.Background())
	check, ok := checkByID(checks, "system.temperature")
	if !ok {
		t.Fatal("the temperature check should state that no sensor exists")
	}
	if check.Status != module.Healthy {
		t.Errorf("a missing sensor produced %s", check.Status)
	}
	if check.Message != "Sıcaklık sensörü bulunamadı." {
		t.Errorf("message = %q", check.Message)
	}
	for _, c := range checks {
		if c.ID == "" || c.Name == "" || c.Message == "" || c.Status == "" {
			t.Errorf("incomplete check: %+v", c)
		}
	}
	// In the test container the root filesystem is an overlay; it must be
	// present as the root disk.
	if _, ok := checkByID(checks, "system.disk.root"); !ok {
		t.Errorf("no root disk check in %+v", checks)
	}
}
