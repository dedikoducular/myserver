package storagecheck

import (
	"encoding/json"
	"strings"
)

// SmartctlBinary is the absolute path of smartctl (package smartmontools).
const SmartctlBinary = "/usr/sbin/smartctl"

// smartctl exit status bits (see smartctl(8), EXIT STATUS).
const (
	SmartExitCmdLine     = 1 << 0 // command line did not parse
	SmartExitOpenFailed  = 1 << 1 // device open failed, or low-power mode with -n
	SmartExitCmdFailed   = 1 << 2 // a SMART command failed or checksum error
	SmartExitDiskFailing = 1 << 3 // SMART status: DISK FAILING
	SmartExitPrefail     = 1 << 4 // prefail attributes at or below threshold
	SmartExitPastPrefail = 1 << 5 // attributes were below threshold in the past
	SmartExitErrorLog    = 1 << 6 // device error log contains records
	SmartExitSelfTestLog = 1 << 7 // self-test log contains errors
)

// SMART states.
const (
	SmartPassed       = "passed"
	SmartWarning      = "warning"
	SmartFailed       = "failed"
	SmartStandby      = "standby"
	SmartDisabled     = "disabled"
	SmartUnsupported  = "unsupported"
	SmartNotInstalled = "not_installed"
	SmartUnknown      = "unknown"
)

// HelperSmartOutput is what the storage-smart helper action prints.
type HelperSmartOutput struct {
	Installed  bool            `json:"installed"`
	ExitStatus int             `json:"exit_status"`
	Output     json.RawMessage `json:"output"`
}

// SmartAttribute is one row of the ATA attribute table.
type SmartAttribute struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Value      int    `json:"value"`
	Worst      int    `json:"worst"`
	Threshold  int    `json:"threshold"`
	Raw        int64  `json:"raw"`
	RawString  string `json:"raw_string"`
	Flags      string `json:"flags"`
	WhenFailed string `json:"when_failed"`
	Prefail    bool   `json:"prefail"`
}

// SmartNVMe is the NVMe health log.
type SmartNVMe struct {
	CriticalWarning         int   `json:"critical_warning"`
	AvailableSpare          int   `json:"available_spare"`
	AvailableSpareThreshold int   `json:"available_spare_threshold"`
	PercentageUsed          int   `json:"percentage_used"`
	MediaErrors             int64 `json:"media_errors"`
	UnsafeShutdowns         int64 `json:"unsafe_shutdowns"`
	DataUnitsRead           int64 `json:"data_units_read"`
	DataUnitsWritten        int64 `json:"data_units_written"`
	ErrorLogEntries         int64 `json:"error_log_entries"`
}

// SmartReport is the interpreted result of one smartctl run.
type SmartReport struct {
	Device     string `json:"device"`
	Status     string `json:"status"`
	Protocol   string `json:"protocol"`
	Model      string `json:"model"`
	Serial     string `json:"serial"`
	Firmware   string `json:"firmware"`
	ExitStatus int    `json:"exit_status"`
	// Passed is the drive's own overall verdict; null when not reported.
	Passed       *bool  `json:"passed"`
	Temperature  *int   `json:"temperature"`
	PowerOnHours *int64 `json:"power_on_hours"`
	PowerCycles  *int64 `json:"power_cycles"`
	// ATA counters; null when the drive does not report the attribute.
	Reallocated   *int64           `json:"reallocated_sectors"`
	Pending       *int64           `json:"pending_sectors"`
	Uncorrectable *int64           `json:"uncorrectable_sectors"`
	NVMe          *SmartNVMe       `json:"nvme"`
	Attributes    []SmartAttribute `json:"attributes"`
	// Problems are Turkish sentences describing every finding.
	Problems  []string `json:"problems"`
	Messages  []string `json:"messages"`
	CheckedAt int64    `json:"checked_at"`
}

type smartDoc struct {
	Smartctl struct {
		ExitStatus *int `json:"exit_status"`
		Messages   []struct {
			String   string `json:"string"`
			Severity string `json:"severity"`
		} `json:"messages"`
	} `json:"smartctl"`
	Device struct {
		Protocol string `json:"protocol"`
		Type     string `json:"type"`
	} `json:"device"`
	ModelName    string `json:"model_name"`
	SerialNumber string `json:"serial_number"`
	Firmware     string `json:"firmware_version"`
	SmartSupport *struct {
		Available bool `json:"available"`
		Enabled   bool `json:"enabled"`
	} `json:"smart_support"`
	SmartStatus *struct {
		Passed *bool `json:"passed"`
	} `json:"smart_status"`
	Temperature *struct {
		Current *int `json:"current"`
	} `json:"temperature"`
	PowerOnTime *struct {
		Hours *int64 `json:"hours"`
	} `json:"power_on_time"`
	PowerCycleCount *int64 `json:"power_cycle_count"`
	ATA             *struct {
		Table []struct {
			ID         int    `json:"id"`
			Name       string `json:"name"`
			Value      int    `json:"value"`
			Worst      int    `json:"worst"`
			Thresh     int    `json:"thresh"`
			WhenFailed string `json:"when_failed"`
			Flags      struct {
				String     string `json:"string"`
				Prefailure bool   `json:"prefailure"`
			} `json:"flags"`
			Raw struct {
				Value  int64  `json:"value"`
				String string `json:"string"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
	NVMe *struct {
		CriticalWarning  int   `json:"critical_warning"`
		Temperature      *int  `json:"temperature"`
		AvailableSpare   int   `json:"available_spare"`
		SpareThreshold   int   `json:"available_spare_threshold"`
		PercentageUsed   int   `json:"percentage_used"`
		MediaErrors      int64 `json:"media_errors"`
		UnsafeShutdowns  int64 `json:"unsafe_shutdowns"`
		DataUnitsRead    int64 `json:"data_units_read"`
		DataUnitsWritten int64 `json:"data_units_written"`
		PowerCycles      int64 `json:"power_cycles"`
		PowerOnHours     int64 `json:"power_on_hours"`
		ErrorLogEntries  int64 `json:"num_err_log_entries"`
	} `json:"nvme_smart_health_information_log"`
}

// ParseSmart interprets smartctl's JSON output and exit status. A non-zero
// exit status frequently comes with a complete, valid report: only bits 0
// and 1 mean that the device could not be read. tempWarn is the warning
// threshold in °C (0 disables the check).
func ParseSmart(device string, raw []byte, exit int, tempWarn int) SmartReport {
	r := SmartReport{
		Device: device, Status: SmartUnknown, ExitStatus: exit,
		Attributes: []SmartAttribute{}, Problems: []string{}, Messages: []string{},
	}
	var doc smartDoc
	if len(raw) == 0 || json.Unmarshal(raw, &doc) != nil {
		r.Status = SmartUnsupported
		return r
	}
	if doc.Smartctl.ExitStatus != nil {
		r.ExitStatus = *doc.Smartctl.ExitStatus
		exit = r.ExitStatus
	}
	standby := false
	for _, m := range doc.Smartctl.Messages {
		r.Messages = append(r.Messages, m.String)
		u := strings.ToUpper(m.String)
		if strings.Contains(u, "STANDBY") || strings.Contains(u, "SLEEP") || strings.Contains(u, "IDLE MODE") {
			standby = true
		}
	}
	r.Protocol = doc.Device.Protocol
	r.Model = doc.ModelName
	r.Serial = doc.SerialNumber
	r.Firmware = doc.Firmware

	hasData := doc.SmartStatus != nil || doc.ATA != nil || doc.NVMe != nil
	if exit&SmartExitOpenFailed != 0 && standby && !hasData {
		r.Status = SmartStandby
		return r
	}
	if exit&(SmartExitCmdLine|SmartExitOpenFailed) != 0 && !hasData {
		r.Status = SmartUnsupported
		return r
	}
	if doc.SmartSupport != nil {
		if !doc.SmartSupport.Available {
			r.Status = SmartUnsupported
			return r
		}
		if !doc.SmartSupport.Enabled {
			r.Status = SmartDisabled
			return r
		}
	}
	if !hasData {
		r.Status = SmartUnsupported
		return r
	}

	if doc.SmartStatus != nil {
		r.Passed = doc.SmartStatus.Passed
	}
	if doc.Temperature != nil && doc.Temperature.Current != nil {
		r.Temperature = doc.Temperature.Current
	}
	if doc.PowerOnTime != nil {
		r.PowerOnHours = doc.PowerOnTime.Hours
	}
	r.PowerCycles = doc.PowerCycleCount

	if doc.ATA != nil {
		for _, a := range doc.ATA.Table {
			raw := a.Raw.Value
			r.Attributes = append(r.Attributes, SmartAttribute{
				ID: a.ID, Name: a.Name, Value: a.Value, Worst: a.Worst, Threshold: a.Thresh,
				Raw: raw, RawString: a.Raw.String, Flags: strings.TrimSpace(a.Flags.String),
				WhenFailed: a.WhenFailed, Prefail: a.Flags.Prefailure,
			})
			// Some drives pack several counters into the 48-bit raw
			// value; the sector counters use the low 32 bits.
			v := raw & 0xffffffff
			switch a.ID {
			case 5:
				r.Reallocated = &v
			case 197:
				r.Pending = &v
			case 198:
				r.Uncorrectable = &v
			}
		}
	}
	if n := doc.NVMe; n != nil {
		r.NVMe = &SmartNVMe{
			CriticalWarning: n.CriticalWarning, AvailableSpare: n.AvailableSpare,
			AvailableSpareThreshold: n.SpareThreshold, PercentageUsed: n.PercentageUsed,
			MediaErrors: n.MediaErrors, UnsafeShutdowns: n.UnsafeShutdowns,
			DataUnitsRead: n.DataUnitsRead, DataUnitsWritten: n.DataUnitsWritten,
			ErrorLogEntries: n.ErrorLogEntries,
		}
		if r.Temperature == nil {
			r.Temperature = n.Temperature
		}
		if r.PowerOnHours == nil {
			h := n.PowerOnHours
			r.PowerOnHours = &h
		}
		if r.PowerCycles == nil {
			c := n.PowerCycles
			r.PowerCycles = &c
		}
	}

	failed := false
	if (r.Passed != nil && !*r.Passed) || exit&SmartExitDiskFailing != 0 {
		failed = true
		r.Problems = append(r.Problems, "Disk kendi sağlık testinde başarısız oldu; arıza bekleniyor.")
	}
	if r.Reallocated != nil && *r.Reallocated > 0 {
		r.Problems = append(r.Problems, "Yeniden ayrılmış sektör sayısı: "+itoa64(*r.Reallocated)+".")
	}
	if r.Pending != nil && *r.Pending > 0 {
		r.Problems = append(r.Problems, "Bekleyen (okunamayan) sektör sayısı: "+itoa64(*r.Pending)+".")
	}
	if r.Uncorrectable != nil && *r.Uncorrectable > 0 {
		r.Problems = append(r.Problems, "Düzeltilemeyen sektör sayısı: "+itoa64(*r.Uncorrectable)+".")
	}
	if exit&SmartExitPrefail != 0 {
		r.Problems = append(r.Problems, "Kritik bir SMART özniteliği eşik değerinin altına düştü.")
	}
	if n := r.NVMe; n != nil {
		if n.CriticalWarning != 0 {
			r.Problems = append(r.Problems, nvmeWarnings(n.CriticalWarning)...)
		}
		if n.MediaErrors > 0 {
			r.Problems = append(r.Problems, "Ortam (media) hatası sayısı: "+itoa64(n.MediaErrors)+".")
		}
		if n.AvailableSpareThreshold > 0 && n.AvailableSpare < n.AvailableSpareThreshold {
			r.Problems = append(r.Problems, "Yedek alan eşik değerinin altında: %"+itoa(n.AvailableSpare)+".")
		}
		if n.PercentageUsed >= 100 {
			r.Problems = append(r.Problems, "Diskin öngörülen yazma ömrü doldu (%"+itoa(n.PercentageUsed)+").")
		}
	}
	if tempWarn > 0 && r.Temperature != nil && *r.Temperature >= tempWarn {
		r.Problems = append(r.Problems, "Disk sıcaklığı yüksek: "+itoa(*r.Temperature)+"°C.")
	}
	switch {
	case failed:
		r.Status = SmartFailed
	case len(r.Problems) > 0:
		r.Status = SmartWarning
	case r.Passed != nil || r.NVMe != nil || len(r.Attributes) > 0:
		r.Status = SmartPassed
	}
	return r
}

func nvmeWarnings(bits int) []string {
	names := []string{
		"Yedek alan eşik değerinin altına düştü.",
		"Sıcaklık eşik değerinin dışında.",
		"Disk güvenilirliği azaldı.",
		"Disk salt okunur moda geçti.",
		"Geçici bellek yedekleme aygıtı arızalı.",
		"Kalıcı bellek bölgesi salt okunur veya güvenilmez.",
	}
	var out []string
	for i, n := range names {
		if bits&(1<<i) != 0 {
			out = append(out, "NVMe kritik uyarısı: "+n)
		}
	}
	if len(out) == 0 {
		out = append(out, "NVMe kritik uyarısı bildirildi.")
	}
	return out
}

func itoa64(n int64) string {
	if n < 0 {
		return "-" + itoa64(-n)
	}
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// Uevent is one kernel block-device event.
type Uevent struct {
	Action    string
	Subsystem string
	DevName   string
	DevType   string
}

// ParseUevent parses a kernel uevent netlink message:
// "add@/devices/...\0ACTION=add\0SUBSYSTEM=block\0DEVNAME=sdb\0...".
// Messages from udev ("libudev" header) are rejected.
func ParseUevent(msg []byte) (Uevent, bool) {
	var ev Uevent
	parts := strings.Split(string(msg), "\x00")
	if len(parts) < 2 || !strings.Contains(parts[0], "@") {
		return ev, false
	}
	for _, p := range parts[1:] {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			continue
		}
		switch k {
		case "ACTION":
			ev.Action = v
		case "SUBSYSTEM":
			ev.Subsystem = v
		case "DEVNAME":
			ev.DevName = strings.TrimPrefix(v, "/dev/")
		case "DEVTYPE":
			ev.DevType = v
		}
	}
	return ev, ev.Action != "" && ev.Subsystem != ""
}
