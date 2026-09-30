// Package system reads live host metrics, system information and health
// state from /proc and /sys.
package system

// CPU describes processor usage. Percent is nil until two readings exist.
type CPU struct {
	Percent *float64 `json:"percent"`
	Load1   float64  `json:"load1"`
	Load5   float64  `json:"load5"`
	Load15  float64  `json:"load15"`
	Cores   int      `json:"cores"`
	Threads int      `json:"threads"`
	Model   string   `json:"model"`
}

// Memory sizes are bytes.
type Memory struct {
	Total     uint64  `json:"total"`
	Used      uint64  `json:"used"`
	Available uint64  `json:"available"`
	Percent   float64 `json:"percent"`
	SwapTotal uint64  `json:"swap_total"`
	SwapUsed  uint64  `json:"swap_used"`
}

// Disk is the usage of one mounted filesystem. System marks boot partitions,
// which dashboards may hide.
type Disk struct {
	Mount   string  `json:"mount"`
	Device  string  `json:"device"`
	FSType  string  `json:"fstype"`
	Total   uint64  `json:"total"`
	Used    uint64  `json:"used"`
	Free    uint64  `json:"free"`
	Percent float64 `json:"percent"`
	System  bool    `json:"system"`
}

// NVMeTemp is the temperature of one NVMe drive.
type NVMeTemp struct {
	Name    string  `json:"name"`
	Celsius float64 `json:"celsius"`
}

// Temperature holds sensor readings. CPU is nil when no sensor exists.
type Temperature struct {
	CPU    *float64   `json:"cpu"`
	Source string     `json:"source"`
	NVMe   []NVMeTemp `json:"nvme"`
}

// Interface is the traffic of one network interface; rates are bytes/second.
type Interface struct {
	Name    string  `json:"name"`
	RxBytes uint64  `json:"rx_bytes"`
	TxBytes uint64  `json:"tx_bytes"`
	RxRate  float64 `json:"rx_rate"`
	TxRate  float64 `json:"tx_rate"`
	Virtual bool    `json:"virtual"`
}

// Network totals exclude loopback and virtual interfaces.
type Network struct {
	RxRate     float64     `json:"rx_rate"`
	TxRate     float64     `json:"tx_rate"`
	Interfaces []Interface `json:"interfaces"`
}

// Snapshot is one sample of the host.
type Snapshot struct {
	Time        int64       `json:"time"`
	Uptime      int64       `json:"uptime"`
	CPU         CPU         `json:"cpu"`
	Memory      Memory      `json:"memory"`
	Disks       []Disk      `json:"disks"`
	RootDisk    *Disk       `json:"root_disk"`
	Temperature Temperature `json:"temperature"`
	Network     Network     `json:"network"`
}

// FinePoint is one entry of the short history used for sparklines.
type FinePoint struct {
	T           int64    `json:"t"`
	CPU         float64  `json:"cpu"`
	Memory      float64  `json:"memory"`
	Disk        *float64 `json:"disk"`
	Temperature *float64 `json:"temperature"`
	RxRate      float64  `json:"rx_rate"`
	TxRate      float64  `json:"tx_rate"`
}

// NetPoint is one entry of the coarse network history: the average rate
// over the interval ending at T.
type NetPoint struct {
	T      int64   `json:"t"`
	RxRate float64 `json:"rx_rate"`
	TxRate float64 `json:"tx_rate"`
}

// Info is the static and slow-changing description of the host.
type Info struct {
	OS          string     `json:"os"`
	Kernel      string     `json:"kernel"`
	CPUModel    string     `json:"cpu_model"`
	CPUCores    int        `json:"cpu_cores"`
	CPUThreads  int        `json:"cpu_threads"`
	MemoryTotal uint64     `json:"memory_total"`
	Hostname    string     `json:"hostname"`
	IP          string     `json:"ip"`
	Interface   string     `json:"interface"`
	Uptime      int64      `json:"uptime"`
	Load        [3]float64 `json:"load"`
	Time        int64      `json:"time"`
	Timezone    string     `json:"timezone"`
}
