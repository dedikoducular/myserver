package storagecheck

import (
	"encoding/json"
	"strings"
	"testing"
)

// Fixtures follow the structure of `smartctl --json -a` (smartmontools 7.x)
// and are written from knowledge of that format; they are not captured from
// a live system.

const smartATAHealthy = `{
  "json_format_version": [1, 0],
  "smartctl": {
    "version": [7, 4], "argv": ["smartctl", "--json", "-a", "--", "/dev/sda"],
    "exit_status": 0
  },
  "device": {"name": "/dev/sda", "info_name": "/dev/sda [SAT]", "type": "sat", "protocol": "ATA"},
  "model_family": "Western Digital Red",
  "model_name": "WDC WD40EFRX-68N32N0",
  "serial_number": "WD-WCC7K1234567",
  "firmware_version": "82.00A82",
  "user_capacity": {"blocks": 7814037168, "bytes": 4000787030016},
  "rotation_rate": 5400,
  "smart_support": {"available": true, "enabled": true},
  "smart_status": {"passed": true},
  "ata_smart_attributes": {
    "revision": 16,
    "table": [
      {"id": 1, "name": "Raw_Read_Error_Rate", "value": 200, "worst": 200, "thresh": 51, "when_failed": "",
       "flags": {"value": 47, "string": "POSR-K ", "prefailure": true, "updated_online": true},
       "raw": {"value": 0, "string": "0"}},
      {"id": 5, "name": "Reallocated_Sector_Ct", "value": 200, "worst": 200, "thresh": 140, "when_failed": "",
       "flags": {"value": 51, "string": "PO--CK ", "prefailure": true},
       "raw": {"value": 0, "string": "0"}},
      {"id": 9, "name": "Power_On_Hours", "value": 58, "worst": 58, "thresh": 0, "when_failed": "",
       "flags": {"value": 50, "string": "-O--CK ", "prefailure": false},
       "raw": {"value": 31337, "string": "31337"}},
      {"id": 194, "name": "Temperature_Celsius", "value": 117, "worst": 102, "thresh": 0, "when_failed": "",
       "flags": {"value": 34, "string": "-O---K ", "prefailure": false},
       "raw": {"value": 33, "string": "33"}},
      {"id": 197, "name": "Current_Pending_Sector", "value": 200, "worst": 200, "thresh": 0, "when_failed": "",
       "flags": {"value": 50, "string": "-O--CK ", "prefailure": false},
       "raw": {"value": 0, "string": "0"}},
      {"id": 198, "name": "Offline_Uncorrectable", "value": 100, "worst": 253, "thresh": 0, "when_failed": "",
       "flags": {"value": 48, "string": "----CK ", "prefailure": false},
       "raw": {"value": 0, "string": "0"}}
    ]
  },
  "power_on_time": {"hours": 31337},
  "power_cycle_count": 84,
  "temperature": {"current": 33}
}`

const smartNVMeHealthy = `{
  "json_format_version": [1, 0],
  "smartctl": {"version": [7, 4], "argv": ["smartctl", "--json", "-a", "--", "/dev/nvme0n1"], "exit_status": 0},
  "device": {"name": "/dev/nvme0n1", "info_name": "/dev/nvme0n1", "type": "nvme", "protocol": "NVMe"},
  "model_name": "Samsung SSD 970 EVO Plus 1TB",
  "serial_number": "S4EWNX0M123456B",
  "firmware_version": "2B2QEXM7",
  "nvme_total_capacity": 1000204886016,
  "smart_status": {"passed": true, "nvme": {"value": 0}},
  "nvme_smart_health_information_log": {
    "critical_warning": 0,
    "temperature": 41,
    "available_spare": 100,
    "available_spare_threshold": 10,
    "percentage_used": 3,
    "data_units_read": 12345678,
    "data_units_written": 23456789,
    "host_reads": 123456789,
    "host_writes": 234567890,
    "controller_busy_time": 321,
    "power_cycles": 412,
    "power_on_hours": 5120,
    "unsafe_shutdowns": 37,
    "media_errors": 0,
    "num_err_log_entries": 12,
    "temperature_sensors": [41, 48]
  },
  "temperature": {"current": 41},
  "power_cycle_count": 412,
  "power_on_time": {"hours": 5120}
}`

const smartStandbyDoc = `{
  "json_format_version": [1, 0],
  "smartctl": {
    "version": [7, 4], "argv": ["smartctl", "--json", "-a", "-n", "standby", "--", "/dev/sdb"],
    "messages": [{"string": "Device is in STANDBY mode, exit(2)", "severity": "information"}],
    "exit_status": 2
  },
  "device": {"name": "/dev/sdb", "info_name": "/dev/sdb [SAT]", "type": "sat", "protocol": "ATA"}
}`

const smartUSBBridgeDoc = `{
  "json_format_version": [1, 0],
  "smartctl": {
    "version": [7, 4], "argv": ["smartctl", "--json", "-a", "--", "/dev/sdc"],
    "messages": [{"string": "/dev/sdc: Unknown USB bridge [0x0781:0x5583 (0x100)]", "severity": "error"}],
    "exit_status": 1
  }
}`

// smartEdit parses a fixture, lets the test change it and encodes it again.
func smartEdit(t *testing.T, doc string, edit func(m map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatal(err)
	}
	edit(m)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func setExit(m map[string]any, code int) { m["smartctl"].(map[string]any)["exit_status"] = code }

func setATARaw(m map[string]any, id int, raw int64) {
	for _, row := range m["ata_smart_attributes"].(map[string]any)["table"].([]any) {
		r := row.(map[string]any)
		if int(r["id"].(float64)) == id {
			r["raw"].(map[string]any)["value"] = raw
		}
	}
}

func problemsContain(r SmartReport, part string) bool {
	for _, p := range r.Problems {
		if strings.Contains(p, part) {
			return true
		}
	}
	return false
}

func TestParseSmartATAHealthy(t *testing.T) {
	r := ParseSmart("sda", []byte(smartATAHealthy), 0, 55)
	if r.Status != SmartPassed {
		t.Fatalf("status = %s (%v), want passed", r.Status, r.Problems)
	}
	if r.Device != "sda" || r.Protocol != "ATA" || r.Model != "WDC WD40EFRX-68N32N0" ||
		r.Serial != "WD-WCC7K1234567" || r.Firmware != "82.00A82" {
		t.Errorf("identity parsed wrongly: %+v", r)
	}
	if r.Passed == nil || !*r.Passed {
		t.Error("passed flag lost")
	}
	if r.Temperature == nil || *r.Temperature != 33 {
		t.Errorf("temperature = %v", r.Temperature)
	}
	if r.PowerOnHours == nil || *r.PowerOnHours != 31337 || r.PowerCycles == nil || *r.PowerCycles != 84 {
		t.Errorf("power counters wrong: %v %v", r.PowerOnHours, r.PowerCycles)
	}
	for name, p := range map[string]*int64{"reallocated": r.Reallocated, "pending": r.Pending, "uncorrectable": r.Uncorrectable} {
		if p == nil || *p != 0 {
			t.Errorf("%s = %v, want 0", name, p)
		}
	}
	if len(r.Attributes) != 6 {
		t.Fatalf("%d attributes, want 6", len(r.Attributes))
	}
	a := r.Attributes[1]
	if a.ID != 5 || a.Name != "Reallocated_Sector_Ct" || a.Value != 200 || a.Worst != 200 || a.Threshold != 140 ||
		!a.Prefail || a.Flags != "PO--CK" || a.RawString != "0" {
		t.Errorf("attribute 5 parsed wrongly: %+v", a)
	}
	if r.NVMe != nil {
		t.Error("ATA report has an NVMe section")
	}
	if len(r.Problems) != 0 || r.Problems == nil || r.Messages == nil {
		t.Errorf("problems/messages: %#v %#v", r.Problems, r.Messages)
	}
}

func TestParseSmartNVMeHealthy(t *testing.T) {
	r := ParseSmart("nvme0n1", []byte(smartNVMeHealthy), 0, 55)
	if r.Status != SmartPassed {
		t.Fatalf("status = %s (%v), want passed", r.Status, r.Problems)
	}
	if r.Protocol != "NVMe" || r.Model != "Samsung SSD 970 EVO Plus 1TB" || r.Serial != "S4EWNX0M123456B" {
		t.Errorf("identity parsed wrongly: %+v", r)
	}
	n := r.NVMe
	if n == nil {
		t.Fatal("NVMe section missing")
	}
	if n.AvailableSpare != 100 || n.AvailableSpareThreshold != 10 || n.PercentageUsed != 3 || n.MediaErrors != 0 ||
		n.UnsafeShutdowns != 37 || n.DataUnitsRead != 12345678 || n.DataUnitsWritten != 23456789 || n.ErrorLogEntries != 12 {
		t.Errorf("NVMe log parsed wrongly: %+v", n)
	}
	if r.Temperature == nil || *r.Temperature != 41 || r.PowerOnHours == nil || *r.PowerOnHours != 5120 ||
		r.PowerCycles == nil || *r.PowerCycles != 412 {
		t.Errorf("counters wrong: %+v", r)
	}
	if r.Reallocated != nil || r.Pending != nil || r.Uncorrectable != nil || len(r.Attributes) != 0 {
		t.Error("NVMe report has ATA counters")
	}
}

func TestParseSmartNVMeWithoutTopLevelFields(t *testing.T) {
	raw := smartEdit(t, smartNVMeHealthy, func(m map[string]any) {
		delete(m, "temperature")
		delete(m, "power_on_time")
		delete(m, "power_cycle_count")
	})
	r := ParseSmart("nvme0n1", raw, 0, 55)
	if r.Temperature == nil || *r.Temperature != 41 || r.PowerOnHours == nil || *r.PowerOnHours != 5120 ||
		r.PowerCycles == nil || *r.PowerCycles != 412 {
		t.Errorf("values not taken from the health log: %+v", r)
	}
}

func TestParseSmartClassification(t *testing.T) {
	ata := func(edit func(m map[string]any)) []byte { return smartEdit(t, smartATAHealthy, edit) }
	nvme := func(edit func(m map[string]any)) []byte { return smartEdit(t, smartNVMeHealthy, edit) }
	nvmeLog := func(m map[string]any) map[string]any {
		return m["nvme_smart_health_information_log"].(map[string]any)
	}
	cases := []struct {
		name    string
		raw     []byte
		exit    int
		want    string
		problem string
	}{
		{"drive says failing", ata(func(m map[string]any) { m["smart_status"].(map[string]any)["passed"] = false }), 0, SmartFailed, "başarısız"},
		{"exit bit 3 disk failing", ata(func(m map[string]any) { setExit(m, 8) }), 8, SmartFailed, "başarısız"},
		{"exit bit 3 only as argument", ata(func(m map[string]any) { delete(m["smartctl"].(map[string]any), "exit_status") }), 8, SmartFailed, "başarısız"},
		{"failing wins over warnings", ata(func(m map[string]any) {
			m["smart_status"].(map[string]any)["passed"] = false
			setATARaw(m, 5, 12)
		}), 0, SmartFailed, "Yeniden ayrılmış sektör sayısı: 12."},
		{"reallocated sectors", ata(func(m map[string]any) { setATARaw(m, 5, 8) }), 0, SmartWarning, "Yeniden ayrılmış sektör sayısı: 8."},
		{"pending sectors", ata(func(m map[string]any) { setATARaw(m, 197, 3) }), 0, SmartWarning, "Bekleyen (okunamayan) sektör sayısı: 3."},
		{"uncorrectable sectors", ata(func(m map[string]any) { setATARaw(m, 198, 1) }), 0, SmartWarning, "Düzeltilemeyen sektör sayısı: 1."},
		{"packed raw value uses low 32 bits", ata(func(m map[string]any) { setATARaw(m, 5, 0x000300000002) }), 0, SmartWarning, "Yeniden ayrılmış sektör sayısı: 2."},
		{"exit bit 4 prefail", ata(func(m map[string]any) { setExit(m, 16) }), 16, SmartWarning, "eşik"},
		{"hot disk", ata(func(m map[string]any) { m["temperature"].(map[string]any)["current"] = 55 }), 0, SmartWarning, "55°C"},
		{"just below the temperature limit", ata(func(m map[string]any) { m["temperature"].(map[string]any)["current"] = 54 }), 0, SmartPassed, ""},
		{"nvme critical warning spare", nvme(func(m map[string]any) { nvmeLog(m)["critical_warning"] = 1 }), 0, SmartWarning, "NVMe kritik uyarısı: Yedek alan"},
		{"nvme critical warning read-only", nvme(func(m map[string]any) { nvmeLog(m)["critical_warning"] = 8 }), 0, SmartWarning, "salt okunur"},
		{"nvme unknown warning bit", nvme(func(m map[string]any) { nvmeLog(m)["critical_warning"] = 128 }), 0, SmartWarning, "NVMe kritik uyarısı"},
		{"nvme media errors", nvme(func(m map[string]any) { nvmeLog(m)["media_errors"] = 4 }), 0, SmartWarning, "Ortam (media) hatası sayısı: 4."},
		{"nvme spare below threshold", nvme(func(m map[string]any) { nvmeLog(m)["available_spare"] = 5 }), 0, SmartWarning, "%5"},
		{"nvme spare at threshold", nvme(func(m map[string]any) { nvmeLog(m)["available_spare"] = 10 }), 0, SmartPassed, ""},
		{"nvme worn out", nvme(func(m map[string]any) { nvmeLog(m)["percentage_used"] = 104 }), 0, SmartWarning, "%104"},
		{"nvme failing", nvme(func(m map[string]any) { m["smart_status"].(map[string]any)["passed"] = false }), 0, SmartFailed, "başarısız"},
		{"nvme hot, from the health log", nvme(func(m map[string]any) {
			delete(m, "temperature")
			nvmeLog(m)["temperature"] = 80
		}), 0, SmartWarning, "80°C"},
	}
	for _, c := range cases {
		r := ParseSmart("sdx", c.raw, c.exit, 55)
		if r.Status != c.want {
			t.Errorf("%s: status = %s (%v), want %s", c.name, r.Status, r.Problems, c.want)
		}
		if c.problem != "" && !problemsContain(r, c.problem) {
			t.Errorf("%s: problems %q do not mention %q", c.name, r.Problems, c.problem)
		}
		if c.want == SmartPassed && len(r.Problems) != 0 {
			t.Errorf("%s: passed with problems %q", c.name, r.Problems)
		}
	}
}

func TestParseSmartTemperatureCheckCanBeDisabled(t *testing.T) {
	raw := smartEdit(t, smartATAHealthy, func(m map[string]any) { m["temperature"].(map[string]any)["current"] = 70 })
	if r := ParseSmart("sda", raw, 0, 0); r.Status != SmartPassed {
		t.Errorf("status = %s, want passed when the threshold is 0", r.Status)
	}
}

// Bits 0 and 1 mean the device could not be read; the others come with a
// complete report, which must not be thrown away.
func TestParseSmartExitStatusBits(t *testing.T) {
	withExit := func(code int) []byte {
		return smartEdit(t, smartATAHealthy, func(m map[string]any) { setExit(m, code) })
	}
	for _, code := range []int{SmartExitCmdFailed, SmartExitPastPrefail, SmartExitErrorLog, SmartExitSelfTestLog,
		SmartExitErrorLog | SmartExitSelfTestLog, SmartExitCmdFailed | SmartExitErrorLog} {
		r := ParseSmart("sda", withExit(code), code, 55)
		if r.Status == SmartFailed || r.Status == SmartUnsupported || r.Status == SmartUnknown || r.Status == SmartStandby {
			t.Errorf("exit %d with a full healthy report: status = %s", code, r.Status)
		}
		if r.ExitStatus != code {
			t.Errorf("exit status %d reported as %d", code, r.ExitStatus)
		}
		if len(r.Attributes) != 6 {
			t.Errorf("exit %d: report data dropped", code)
		}
	}
	// All "health" bits together: failing.
	if r := ParseSmart("sda", withExit(0xf8), 0xf8, 55); r.Status != SmartFailed {
		t.Errorf("exit 0xf8: status = %s, want failed", r.Status)
	}
	// Bit 1 together with data (smartctl read the device after all).
	if r := ParseSmart("sda", withExit(2), 2, 55); r.Status != SmartPassed {
		t.Errorf("exit 2 with a full report: status = %s, want passed", r.Status)
	}
	// The status inside the document wins over the process exit code.
	if r := ParseSmart("sda", withExit(8), 0, 55); r.Status != SmartFailed || r.ExitStatus != 8 {
		t.Errorf("document exit status ignored: %s %d", r.Status, r.ExitStatus)
	}
}

func TestParseSmartUnreadableDevices(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		exit int
		want string
	}{
		{"standby", smartStandbyDoc, 2, SmartStandby},
		{"unknown usb bridge", smartUSBBridgeDoc, 1, SmartUnsupported},
		{"open failed, no message", `{"smartctl":{"exit_status":2},"device":{"protocol":"ATA"}}`, 2, SmartUnsupported},
		{"empty output", ``, 0, SmartUnsupported},
		{"not json", `smartctl 7.4 2023-08-01 r5530`, 0, SmartUnsupported},
		{"truncated json", smartATAHealthy[:200], 0, SmartUnsupported},
		{"json array", `[]`, 0, SmartUnsupported},
		{"empty document", `{}`, 0, SmartUnsupported},
		{"smart unavailable", `{"smartctl":{"exit_status":0},"smart_support":{"available":false,"enabled":false},"smart_status":{"passed":true}}`, 0, SmartUnsupported},
		{"smart disabled", `{"smartctl":{"exit_status":0},"smart_support":{"available":true,"enabled":false},"smart_status":{"passed":true}}`, 0, SmartDisabled},
		{"identity only", `{"smartctl":{"exit_status":0},"model_name":"QEMU HARDDISK","smart_support":{"available":true,"enabled":true}}`, 0, SmartUnsupported},
	}
	for _, c := range cases {
		r := ParseSmart("sdb", []byte(c.raw), c.exit, 55)
		if r.Status != c.want {
			t.Errorf("%s: status = %s, want %s", c.name, r.Status, c.want)
		}
		if r.Attributes == nil || r.Problems == nil || r.Messages == nil {
			t.Errorf("%s: nil list in the report (must encode as [])", c.name)
		}
		if r.Device != "sdb" {
			t.Errorf("%s: device lost", c.name)
		}
	}
	r := ParseSmart("sdb", []byte(smartStandbyDoc), 2, 55)
	if len(r.Messages) != 1 || !strings.Contains(r.Messages[0], "STANDBY") {
		t.Errorf("messages = %q", r.Messages)
	}
}

// A sleeping disk answers nothing; a failing verdict must never be hidden
// behind the standby state when data is present.
func TestParseSmartStandbyDoesNotHideData(t *testing.T) {
	raw := smartEdit(t, smartATAHealthy, func(m map[string]any) {
		s := m["smartctl"].(map[string]any)
		s["exit_status"] = 10
		s["messages"] = []any{map[string]any{"string": "Device is in STANDBY mode", "severity": "information"}}
		m["smart_status"].(map[string]any)["passed"] = false
	})
	if r := ParseSmart("sda", raw, 10, 55); r.Status != SmartFailed {
		t.Errorf("status = %s, want failed", r.Status)
	}
}

func TestParseUevent(t *testing.T) {
	msg := "add@/devices/pci0000:00/usb1/1-1/host6/target6:0:0/6:0:0:0/block/sdb\x00ACTION=add\x00" +
		"DEVPATH=/devices/pci0000:00/block/sdb\x00SUBSYSTEM=block\x00DEVNAME=sdb\x00DEVTYPE=disk\x00SEQNUM=4242\x00"
	ev, ok := ParseUevent([]byte(msg))
	if !ok || ev.Action != "add" || ev.Subsystem != "block" || ev.DevName != "sdb" || ev.DevType != "disk" {
		t.Errorf("uevent = %+v, %v", ev, ok)
	}
	for _, bad := range []string{"", "libudev\x00\xfe\xed\xca\xfe", "garbage", "add@/x", "add@/x\x00FOO=bar\x00"} {
		if _, ok := ParseUevent([]byte(bad)); ok {
			t.Errorf("ParseUevent(%q) accepted", bad)
		}
	}
}
