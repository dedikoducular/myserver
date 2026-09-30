package system

import (
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"myserver/internal/settings"
)

const (
	diskTTL     = 10 * time.Second
	diskWait    = 2 * time.Second
	sensorTTL   = 60 * time.Second
	sysRoot     = "/sys"
	hwmonGlob   = "class/hwmon/hwmon*"
	thermalGlob = "class/thermal/thermal_zone*"
)

type nvmeSensor struct {
	name string
	path string
}

type sensorSet struct {
	cpuPath   string
	cpuSource string
	nvme      []nvmeSensor
}

// collector reads the host. Expensive or blocking reads are cached.
type collector struct {
	cpuMu sync.Mutex
	cpu   cpuInfo
	cpuOK bool

	diskMu   sync.Mutex
	diskList []Disk
	diskAt   time.Time
	diskBusy bool

	sensMu  sync.Mutex
	sensors sensorSet
	sensAt  time.Time
	sysRoot string // "/sys"; tests point it at a fixture tree

	virtMu sync.Mutex
	virt   map[string]bool
}

func newCollector() *collector {
	return &collector{virt: map[string]bool{}, sysRoot: sysRoot}
}

// cpuInfo is read once: the processor does not change while running.
func (c *collector) cpuInfo() cpuInfo {
	c.cpuMu.Lock()
	defer c.cpuMu.Unlock()
	if c.cpuOK {
		return c.cpu
	}
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return cpuInfo{}
	}
	c.cpu = parseCPUInfo(data)
	c.cpuOK = c.cpu.threads > 0
	return c.cpu
}

func readLoad() ([3]float64, bool) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return [3]float64{}, false
	}
	return parseLoadAvg(data)
}

func readMemory() (Memory, bool) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return Memory{}, false
	}
	return parseMemInfo(data)
}

func readUptime() int64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	v, _ := parseUptime(data)
	return int64(v)
}

func readCPUTimes() (cpuTimes, bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuTimes{}, false
	}
	return parseCPUStat(data)
}

func readNetCounters() (map[string]netCounters, bool) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return nil, false
	}
	return parseNetDev(data), true
}

// disks returns filesystem usage, refreshed at most every diskTTL. The read
// runs in its own goroutine so an unresponsive network mount cannot stall
// the caller; the previous values are returned meanwhile.
func (c *collector) disks() []Disk {
	c.diskMu.Lock()
	if c.diskBusy || (!c.diskAt.IsZero() && time.Since(c.diskAt) < diskTTL) {
		d := c.diskList
		c.diskMu.Unlock()
		return d
	}
	c.diskBusy = true
	c.diskMu.Unlock()

	done := make(chan []Disk, 1)
	go func() {
		d := readDisks()
		c.diskMu.Lock()
		c.diskList, c.diskAt, c.diskBusy = d, time.Now(), false
		c.diskMu.Unlock()
		done <- d
	}()
	timer := time.NewTimer(diskWait)
	defer timer.Stop()
	select {
	case d := <-done:
		return d
	case <-timer.C:
		c.diskMu.Lock()
		defer c.diskMu.Unlock()
		return c.diskList
	}
}

func readDisks() []Disk {
	out := []Disk{}
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return out
	}
	for _, e := range parseMountInfo(data) {
		total, used, avail, err := statFS(e.mount)
		if err != nil || total == 0 {
			continue
		}
		d := Disk{
			Mount: e.mount, Device: e.device, FSType: e.fstype,
			Total: total, Used: used, Free: avail,
			System: underPrefix(e.mount, "/boot") || underPrefix(e.mount, "/efi"),
		}
		// Same definition as df: reserved blocks count as unavailable.
		if used+avail > 0 {
			d.Percent = float64(used) / float64(used+avail) * 100
		}
		out = append(out, d)
	}
	return out
}

func rootDisk(disks []Disk) *Disk {
	for i := range disks {
		if disks[i].Mount == "/" {
			d := disks[i]
			return &d
		}
	}
	return nil
}

func readTrim(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func readTemp(path string) (float64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	return parseMilliCelsius(data)
}

var cpuHwmonNames = []string{"coretemp", "k10temp", "zenpower", "cpu_thermal", "cpu-thermal", "soc_thermal"}

func labelRank(label string) int {
	l := strings.ToLower(label)
	switch {
	case strings.HasPrefix(l, "package"):
		return 0
	case l == "tdie":
		return 1
	case l == "tctl":
		return 2
	}
	return 3
}

// discoverSensors locates the temperature files once; reading them later
// is a single small file read per sensor.
func discoverSensors(root string) sensorSet {
	var set sensorSet
	dirs, _ := filepath.Glob(filepath.Join(root, hwmonGlob))
	sort.Strings(dirs)
	bestName := len(cpuHwmonNames)
	for _, dir := range dirs {
		name := readTrim(filepath.Join(dir, "name"))
		if name == "nvme" {
			path := filepath.Join(dir, "temp1_input")
			if _, ok := readTemp(path); ok {
				label := filepath.Base(dir)
				if target, err := filepath.EvalSymlinks(filepath.Join(dir, "device")); err == nil {
					label = filepath.Base(target)
				}
				set.nvme = append(set.nvme, nvmeSensor{name: label, path: path})
			}
			continue
		}
		rank := -1
		for i, n := range cpuHwmonNames {
			if n == name {
				rank = i
				break
			}
		}
		if rank < 0 || rank >= bestName {
			continue
		}
		inputs, _ := filepath.Glob(filepath.Join(dir, "temp*_input"))
		sort.Strings(inputs)
		bestPath, bestLabel, bestRank := "", "", 4
		for _, in := range inputs {
			if _, ok := readTemp(in); !ok {
				continue
			}
			label := readTrim(strings.TrimSuffix(in, "_input") + "_label")
			if r := labelRank(label); r < bestRank {
				bestPath, bestLabel, bestRank = in, label, r
			}
		}
		if bestPath == "" {
			continue
		}
		bestName = rank
		set.cpuPath = bestPath
		set.cpuSource = name
		if bestLabel != "" {
			set.cpuSource = name + " (" + bestLabel + ")"
		}
	}
	sort.Slice(set.nvme, func(i, j int) bool { return set.nvme[i].name < set.nvme[j].name })
	if set.cpuPath != "" {
		return set
	}

	zones, _ := filepath.Glob(filepath.Join(root, thermalGlob))
	sort.Strings(zones)
	bestRank := 3
	for _, z := range zones {
		path := filepath.Join(z, "temp")
		if _, ok := readTemp(path); !ok {
			continue
		}
		typ := readTrim(filepath.Join(z, "type"))
		l := strings.ToLower(typ)
		rank := 2
		switch {
		case l == "x86_pkg_temp":
			rank = 0
		case strings.Contains(l, "cpu") || strings.Contains(l, "soc"):
			rank = 1
		}
		if rank < bestRank {
			bestRank = rank
			set.cpuPath = path
			set.cpuSource = typ
		}
	}
	return set
}

func (c *collector) temperature() Temperature {
	c.sensMu.Lock()
	if c.sensAt.IsZero() || time.Since(c.sensAt) > sensorTTL {
		c.sensors = discoverSensors(c.sysRoot)
		c.sensAt = time.Now()
	}
	set := c.sensors
	c.sensMu.Unlock()

	t := Temperature{NVMe: []NVMeTemp{}}
	if set.cpuPath != "" {
		if v, ok := readTemp(set.cpuPath); ok {
			t.CPU = &v
			t.Source = set.cpuSource
		}
	}
	for _, n := range set.nvme {
		if v, ok := readTemp(n.path); ok {
			t.NVMe = append(t.NVMe, NVMeTemp{Name: n.name, Celsius: v})
		}
	}
	return t
}

// isVirtual reports whether an interface is excluded from traffic totals.
func (c *collector) isVirtual(name string) bool {
	if virtualByName(name) {
		return true
	}
	c.virtMu.Lock()
	defer c.virtMu.Unlock()
	if v, ok := c.virt[name]; ok {
		return v
	}
	if len(c.virt) > 512 {
		c.virt = map[string]bool{}
	}
	_, err := os.Stat("/sys/devices/virtual/net/" + name)
	c.virt[name] = err == nil
	return err == nil
}

// netTotals sums the counters of the physical interfaces.
func (c *collector) netTotals(counters map[string]netCounters) (rx, tx uint64) {
	for name, v := range counters {
		if c.isVirtual(name) {
			continue
		}
		rx += v.rx
		tx += v.tx
	}
	return rx, tx
}

func defaultInterface() string {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return ""
	}
	return parseDefaultRoute(data)
}

func interfaceIPv4(name string) string {
	if name == "" {
		return ""
	}
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return ""
	}
	addrs, err := ifc.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if ip := n.IP.To4(); ip != nil && !ip.IsLoopback() {
				return ip.String()
			}
		}
	}
	return ""
}

func (c *collector) info() Info {
	ci := c.cpuInfo()
	info := Info{
		Kernel:     readTrim("/proc/sys/kernel/osrelease"),
		CPUModel:   ci.model,
		CPUCores:   ci.cores,
		CPUThreads: ci.threads,
		Uptime:     readUptime(),
		Time:       time.Now().Unix(),
		Timezone:   settings.CurrentTimezone(),
	}
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		info.OS = parseOSRelease(data)
	}
	if m, ok := readMemory(); ok {
		info.MemoryTotal = m.Total
	}
	if h, err := os.Hostname(); err == nil {
		info.Hostname = h
	}
	info.Interface = defaultInterface()
	info.IP = interfaceIPv4(info.Interface)
	info.Load, _ = readLoad()
	return info
}
