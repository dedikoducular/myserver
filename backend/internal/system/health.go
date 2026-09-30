package system

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"myserver/internal/module"
	"myserver/internal/notify"
)

// Settings keys owned by this module.
const (
	KeyDiskWarning  = "system.disk_warning_percent"
	KeyDiskCritical = "system.disk_critical_percent"
	KeyTempWarning  = "system.temp_warning_celsius"
	KeyTempCritical = "system.temp_critical_celsius"
)

const (
	loadWarnPerThread = 1.5
	loadCritPerThread = 3.0
	memWarnPercent    = 90.0
	memCritPercent    = 97.0
	unitsTTL          = 60 * time.Second
	unitsTimeout      = 5 * time.Second
	notifyWindow      = 6 * time.Hour
	watchInterval     = time.Minute
	systemctlPath     = "/usr/bin/systemctl"
)

type thresholds struct {
	diskWarn, diskCrit float64
	tempWarn, tempCrit float64
}

func (m *Module) thresholds() thresholds {
	s := m.deps.Settings
	t := thresholds{
		diskWarn: float64(s.Int(KeyDiskWarning, 85)),
		diskCrit: float64(s.Int(KeyDiskCritical, 95)),
		tempWarn: float64(s.Int(KeyTempWarning, 80)),
		tempCrit: float64(s.Int(KeyTempCritical, 90)),
	}
	// The keys are validated one by one, so guard against a warning level
	// stored above the critical one. The lower value wins: a reading above
	// the configured critical level must never be reported as healthy.
	if t.diskWarn > t.diskCrit {
		t.diskWarn = t.diskCrit
	}
	if t.tempWarn > t.tempCrit {
		t.tempWarn = t.tempCrit
	}
	return t
}

func level(value, warn, crit float64) module.HealthStatus {
	switch {
	case value >= crit:
		return module.CriticalLvl
	case value >= warn:
		return module.WarningLevel
	}
	return module.Healthy
}

type unitsCache struct {
	mu    sync.Mutex
	at    time.Time
	units []string
	ok    bool
}

// failedUnits lists failed systemd units, cached for unitsTTL.
func (m *Module) failedUnits(ctx context.Context) ([]string, bool) {
	c := &m.units
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && time.Since(c.at) < unitsTTL {
		return c.units, c.ok
	}
	ctx, cancel := context.WithTimeout(ctx, unitsTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, systemctlPath, "--failed", "--no-legend", "--plain", "--no-pager")
	cmd.Env = []string{"LC_ALL=C", "PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	out, err := cmd.Output()
	c.at = time.Now()
	if err != nil {
		c.units, c.ok = nil, false
		return nil, false
	}
	c.units, c.ok = parseFailedUnits(out), true
	return c.units, true
}

func diskLabel(d Disk) string {
	if d.Mount == "/" {
		return "Kök disk"
	}
	return "Disk " + d.Mount
}

// Health implements module.HealthReporter. Every probe is a few small file
// reads or a cached value.
func (m *Module) Health(ctx context.Context) []module.HealthCheck {
	th := m.thresholds()
	checks := []module.HealthCheck{}

	if load, ok := readLoad(); ok {
		threads := m.col.cpuInfo().threads
		if threads < 1 {
			threads = 1
		}
		per := load[1] / float64(threads)
		checks = append(checks, module.HealthCheck{
			ID: "system.cpu", Name: "İşlemci yükü",
			Status:  level(per, loadWarnPerThread, loadCritPerThread),
			Message: fmt.Sprintf("5 dakikalık yük ortalaması %.2f (%d iş parçacığı).", load[1], threads),
		})
	}

	if mem, ok := readMemory(); ok {
		checks = append(checks, module.HealthCheck{
			ID: "system.memory", Name: "Bellek",
			Status:  level(mem.Percent, memWarnPercent, memCritPercent),
			Message: fmt.Sprintf("Bellek kullanımı %%%.0f.", mem.Percent),
		})
	}

	for _, d := range m.col.disks() {
		id := "system.disk:" + d.Mount
		if d.Mount == "/" {
			id = "system.disk.root"
		}
		checks = append(checks, module.HealthCheck{
			ID: id, Name: diskLabel(d),
			Status:  level(d.Percent, th.diskWarn, th.diskCrit),
			Message: fmt.Sprintf("%s doluluk oranı %%%.0f.", d.Mount, d.Percent),
		})
	}

	temp := m.col.temperature()
	tc := module.HealthCheck{ID: "system.temperature", Name: "Sıcaklık", Status: module.Healthy}
	hottest, found := 0.0, false
	var parts []string
	if temp.CPU != nil {
		hottest, found = *temp.CPU, true
		parts = append(parts, fmt.Sprintf("İşlemci %.0f°C", *temp.CPU))
	}
	for _, n := range temp.NVMe {
		if !found || n.Celsius > hottest {
			hottest, found = n.Celsius, true
		}
		parts = append(parts, fmt.Sprintf("%s %.0f°C", n.Name, n.Celsius))
	}
	if found {
		tc.Status = level(hottest, th.tempWarn, th.tempCrit)
		tc.Message = strings.Join(parts, ", ") + "."
	} else {
		tc.Message = "Sıcaklık sensörü bulunamadı."
	}
	checks = append(checks, tc)

	if synced, known := timeSynced(); known {
		c := module.HealthCheck{ID: "system.time_sync", Name: "Zaman eşitleme", Status: module.Healthy, Message: "Sistem saati eşitlenmiş."}
		if !synced {
			c.Status = module.WarningLevel
			c.Message = "Sistem saati bir zaman sunucusuyla eşitlenmemiş."
		}
		checks = append(checks, c)
	}

	checks = append(checks, networkCheck())

	if units, ok := m.failedUnits(ctx); ok {
		c := module.HealthCheck{ID: "system.units", Name: "Sistem servisleri", Status: module.Healthy, Message: "Başarısız servis yok."}
		if n := len(units); n > 0 {
			c.Status = module.WarningLevel
			shown := units
			if len(shown) > 5 {
				shown = shown[:5]
			}
			c.Message = fmt.Sprintf("%d servis başarısız durumda: %s", n, strings.Join(shown, ", "))
			if n > len(shown) {
				c.Message += " ve diğerleri"
			}
			c.Message += "."
		}
		checks = append(checks, c)
	}
	return checks
}

func networkCheck() module.HealthCheck {
	c := module.HealthCheck{ID: "system.network", Name: "Ağ bağlantısı", Status: module.Healthy}
	ifc := defaultInterface()
	if ifc == "" {
		c.Status = module.CriticalLvl
		c.Message = "Varsayılan ağ geçidi (IPv4) tanımlı değil."
		return c
	}
	// Some drivers report "unknown" while the link works.
	if state := readTrim("/sys/class/net/" + ifc + "/operstate"); state != "up" && state != "unknown" {
		c.Status = module.CriticalLvl
		c.Message = fmt.Sprintf("%s ağ arayüzü bağlı değil.", ifc)
		return c
	}
	c.Message = fmt.Sprintf("%s arayüzü bağlı, varsayılan ağ geçidi tanımlı.", ifc)
	return c
}

// watch is the slow loop: it keeps the network history alive while nobody
// is watching and raises notifications for disk usage and temperature.
func (m *Module) watch(ctx context.Context) {
	ticker := time.NewTicker(watchInterval)
	defer ticker.Stop()
	for {
		m.watchOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Module) watchOnce(ctx context.Context) {
	if counters, ok := readNetCounters(); ok {
		m.sampler.recordNet(time.Now(), counters)
	}
	th := m.thresholds()
	for _, d := range m.col.disks() {
		switch level(d.Percent, th.diskWarn, th.diskCrit) {
		case module.CriticalLvl:
			m.deps.Notify.PublishOnce(ctx, notify.Critical, "system", "Disk dolmak üzere",
				fmt.Sprintf("%s doluluk oranı %%%.0f. Yer açmazsanız servisler durabilir.", d.Mount, d.Percent),
				"system.disk.critical:"+d.Mount, notifyWindow)
		case module.WarningLevel:
			m.deps.Notify.PublishOnce(ctx, notify.Warning, "system", "Disk kullanımı yüksek",
				fmt.Sprintf("%s doluluk oranı %%%.0f.", d.Mount, d.Percent),
				"system.disk.warning:"+d.Mount, notifyWindow)
		}
	}
	temp := m.col.temperature()
	if temp.CPU != nil {
		m.notifyTemp(ctx, th, "cpu", "İşlemci", *temp.CPU)
	}
	for _, n := range temp.NVMe {
		m.notifyTemp(ctx, th, n.Name, n.Name, n.Celsius)
	}
}

func (m *Module) notifyTemp(ctx context.Context, th thresholds, key, label string, celsius float64) {
	switch level(celsius, th.tempWarn, th.tempCrit) {
	case module.CriticalLvl:
		m.deps.Notify.PublishOnce(ctx, notify.Critical, "system", "Sıcaklık kritik seviyede",
			fmt.Sprintf("%s sıcaklığı %.0f°C. Soğutmayı kontrol edin.", label, celsius),
			"system.temp.critical:"+key, notifyWindow)
	case module.WarningLevel:
		m.deps.Notify.PublishOnce(ctx, notify.Warning, "system", "Sıcaklık yüksek",
			fmt.Sprintf("%s sıcaklığı %.0f°C.", label, celsius),
			"system.temp.warning:"+key, notifyWindow)
	}
}
