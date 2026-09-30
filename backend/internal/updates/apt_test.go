package updates

import (
	"context"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"myserver/internal/audit"
	"myserver/internal/updates/updatescheck"
)

const (
	listTwo = "Reading package lists...\n" +
		"Inst libc6 [2.39-0ubuntu8.3] (2.39-0ubuntu8.4 Ubuntu:24.04/noble-updates, Ubuntu:24.04/noble-security [amd64])\n" +
		"Conf libc6 (2.39-0ubuntu8.4 Ubuntu:24.04/noble-updates, Ubuntu:24.04/noble-security [amd64])\n" +
		"Inst vim [2:9.1.0016-1ubuntu7.5] (2:9.1.0016-1ubuntu7.6 Ubuntu:24.04/noble-updates [amd64])\n"
	listKernel = listTwo +
		"Inst linux-image-6.8.0-45-generic (6.8.0-45.45 Ubuntu:24.04/noble-updates, Ubuntu:24.04/noble-security [amd64])\n" +
		"Inst linux-generic [6.8.0-44.44] (6.8.0-45.45 Ubuntu:24.04/noble-updates, Ubuntu:24.04/noble-security [amd64])\n"
)

func hostname(t *testing.T) string {
	t.Helper()
	h, err := os.Hostname()
	if err != nil || h == "" {
		t.Fatalf("hostname: %q %v", h, err)
	}
	return h
}

/* ---------- checking ---------- */

func TestAptNothingCheckedYet(t *testing.T) {
	e := newEnv(t)
	res := e.do("GET", "/updates/apt", "", "user")
	if res.Status != 200 {
		t.Fatalf("status %d: %s", res.Status, res.Body)
	}
	var v aptView
	res.into(t, &v)
	if v.Count != 0 || v.CheckedAt != nil || v.Error != nil || v.Checking || v.RebootRequired || v.Job != nil {
		t.Errorf("view = %+v", v)
	}
	for _, field := range []string{`"packages":[]`, `"reboot_packages":[]`, `"checked_at":null`} {
		if !strings.Contains(res.Body, field) {
			t.Errorf("response lacks %s: %s", field, res.Body)
		}
	}
	// Reading never reaches the helper or systemd.
	e.do("GET", "/updates/summary", "", "user")
	e.wantNoHelperCalls("reading the state")
	if calls := e.systemctlCalls(); len(calls) != 0 {
		t.Errorf("systemctl was called: %q", calls)
	}
}

func TestAptCheck(t *testing.T) {
	e := newEnv(t)
	e.write("list.out", listKernel, 0o644)
	res := e.do("POST", "/updates/apt/check", "", "admin")
	if res.Status != 200 {
		t.Fatalf("status %d: %s", res.Status, res.Body)
	}
	var v aptView
	res.into(t, &v)
	if v.Count != 4 || v.SecurityCount != 3 || v.KernelCount != 2 || v.CheckedAt == nil || v.Error != nil {
		t.Errorf("view = %+v", v)
	}
	if len(v.Packages) != 4 || v.Packages[0].Name != "libc6" || !v.Packages[2].Kernel || !v.Packages[2].New {
		t.Errorf("packages = %+v", v.Packages)
	}
	if v.Hostname != hostname(t) {
		t.Errorf("hostname = %q", v.Hostname)
	}
	want := [][]string{{"updates-apt-refresh"}, {"updates-apt-list"}}
	if got := e.helperCalls(); !reflect.DeepEqual(got, want) {
		t.Errorf("helper calls = %q", got)
	}
	if got, want := e.auditRows(), []auditRow{{"yonetici", "updates.apt_check", "", "", true}}; !reflect.DeepEqual(got, want) {
		t.Errorf("audit = %+v", got)
	}

	var s summaryView
	e.do("GET", "/updates/summary", "", "user").into(t, &s)
	if s.Total != 4 || s.Apt.Count != 4 || s.Apt.SecurityCount != 3 || s.Apt.KernelCount != 2 || s.Apt.Running {
		t.Errorf("summary = %+v", s)
	}

	// The result survives a restart of the panel.
	m2, err := New(e.deps, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := m2.(*Module).aptView(true); got.Count != 4 || got.KernelCount != 2 {
		t.Errorf("restored view = %+v", got)
	}
}

func TestAptCheckFailures(t *testing.T) {
	locked, _ := updatescheck.Classify("E: Could not get lock /var/lib/dpkg/lock-frontend.")
	network, _ := updatescheck.Classify("Temporary failure resolving 'archive.ubuntu.com'")
	interrupted, _ := updatescheck.Classify("E: dpkg was interrupted, you must manually run 'dpkg --configure -a'")
	cases := []struct {
		name, file, content string
		status              int
		code, message       string
	}{
		{"locked", "updates-apt-refresh.err", locked.Message, http.StatusConflict, "apt_locked", locked.Message},
		{"network", "updates-apt-refresh.err", network.Message, http.StatusBadGateway, "apt_network", network.Message},
		{"interrupted", "updates-apt-list.err", interrupted.Message, http.StatusBadGateway, "dpkg_interrupted", interrupted.Message},
		{"refresh crash", "updates-apt-refresh.crash", "", http.StatusBadGateway, "apt_refresh_failed", "Paket listeleri yenilenemedi."},
		{"list crash", "updates-apt-list.crash", "", http.StatusBadGateway, "apt_list_failed", "Güncellenebilir paketler listelenemedi."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.write("list.out", listTwo, 0o644)
			if res := e.do("POST", "/updates/apt/check", "", "admin"); res.Status != 200 {
				t.Fatalf("first check: %d %s", res.Status, res.Body)
			}
			e.write(c.file, c.content, 0o644)
			res := e.do("POST", "/updates/apt/check", "", "admin")
			if res.Status != c.status || res.code() != c.code || res.message() != c.message {
				t.Errorf("status %d code %q message %q", res.Status, res.code(), res.message())
			}
			if strings.Contains(res.Body, "/etc/secret") || strings.Contains(res.Body, "panic") || strings.Contains(res.Body, "exit") {
				t.Errorf("internal error leaked: %s", res.Body)
			}
			var v aptView
			e.do("GET", "/updates/apt", "", "user").into(t, &v)
			if v.Error == nil || v.Error.Code != c.code || v.Error.Message != c.message {
				t.Errorf("stored error = %+v", v.Error)
			}
			// What was known before is not thrown away by a failed check.
			if strings.HasSuffix(c.file, "list.err") || strings.HasSuffix(c.file, "list.crash") {
				if v.Count != 2 {
					t.Errorf("count = %d after a failed listing, want the previous 2", v.Count)
				}
			}
			rows := e.auditRows()
			if last := rows[len(rows)-1]; last.Action != "updates.apt_check" || last.Success {
				t.Errorf("audit = %+v", last)
			}
		})
	}
}

func TestAptCheckOneAtATime(t *testing.T) {
	e := newEnv(t)
	e.mod.mu.Lock()
	e.mod.aptChecking = true
	e.mod.mu.Unlock()
	res := e.do("POST", "/updates/apt/check", "", "admin")
	if res.Status != http.StatusConflict || res.code() != "conflict" {
		t.Errorf("status %d code %q", res.Status, res.code())
	}
	res = e.do("POST", "/updates/apt/upgrade", `{"confirm":true}`, "admin")
	if res.Status != http.StatusConflict {
		t.Errorf("upgrade during a check: status %d", res.Status)
	}
	e.mod.mu.Lock()
	e.mod.aptChecking = false
	e.mod.mu.Unlock()

	e.mod.jobs.add(newJob(kindApt, "x", "", "yonetici"))
	res = e.do("POST", "/updates/apt/check", "", "admin")
	if res.Status != http.StatusConflict {
		t.Errorf("check during an upgrade: status %d", res.Status)
	}
	e.wantNoHelperCalls("conflicting requests")
}

/* ---------- notifications ---------- */

func TestAptNotifiesOnceForNewUpdates(t *testing.T) {
	e := newEnv(t)
	e.write("list.out", listTwo, 0o644)
	check := func() {
		t.Helper()
		if err := e.mod.checkApt(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	check()
	list := e.notifications()
	if len(list) != 1 || list[0].Title != "Güncelleme mevcut" || list[0].Severity != "INFO" || list[0].Source != "updates" {
		t.Fatalf("notifications = %+v", list)
	}
	if want := "2 Ubuntu paketi için güncelleme mevcut. Bunların 1 tanesi güvenlik güncellemesi."; list[0].Message != want {
		t.Errorf("message = %q", list[0].Message)
	}
	for i := 0; i < 5; i++ {
		check()
	}
	if n := len(e.notifications()); n != 1 {
		t.Errorf("%d notifications after repeated checks with the same result", n)
	}
	// Even a panel that lost its memory does not repeat itself.
	e.mod.mu.Lock()
	e.mod.apt = aptState{}
	e.mod.mu.Unlock()
	check()
	if n := len(e.notifications()); n != 1 {
		t.Errorf("%d notifications after a restart with the same result", n)
	}
	// Fewer packages than before is not news.
	e.write("list.out", "Inst vim [2:9.1.0016-1ubuntu7.5] (2:9.1.0016-1ubuntu7.6 Ubuntu:24.04/noble-updates [amd64])\n", 0o644)
	check()
	if n := len(e.notifications()); n != 1 {
		t.Errorf("%d notifications after the list shrank", n)
	}
	// A new package is.
	e.write("list.out", listKernel, 0o644)
	check()
	list = e.notifications()
	if len(list) != 2 || list[1].Title != "Güncelleme mevcut" || !strings.HasPrefix(list[1].Message, "4 Ubuntu paketi") {
		t.Errorf("notifications = %+v", list)
	}
	// Nothing to update: nothing to say.
	e.write("list.out", "", 0o644)
	check()
	if n := len(e.notifications()); n != 2 {
		t.Errorf("%d notifications after an empty result", n)
	}
}

/* ---------- upgrade ---------- */

func TestAptUpgradeNeedsConfirmation(t *testing.T) {
	e := newEnv(t)
	bodies := []string{
		"", `{}`, `{"confirm":false}`, `{"mode":"upgrade"}`, `{"mode":"full","include_kernel":true}`,
		`{"confirm":"true"}`, `{"confirm":1}`, `{"confirm":null}`, `{"confirm":true,"force":true}`,
		`{"confirm":true,"packages":["linux-image-generic"]}`, `{"confirm":true} {"confirm":true}`,
		`{"mode":"dist-upgrade","confirm":true}`, `{"mode":"Full","confirm":true}`, `{"mode":"full ","confirm":true}`,
		`{"mode":"autoremove","confirm":true}`, `{"mode":"--allow-downgrades","confirm":true}`,
		`{"mode":"upgrade","include_kernel":"yes","confirm":true}`, `[`, `true`,
	}
	for _, body := range bodies {
		res := e.do("POST", "/updates/apt/upgrade", body, "admin")
		if res.Status != http.StatusBadRequest {
			t.Errorf("body %q: status %d: %s", body, res.Status, res.Body)
		}
	}
	e.wantNoHelperCalls("unconfirmed or malformed upgrade requests")
	if e.mod.jobs.latest(kindApt) != nil {
		t.Error("a refused request created a job")
	}
}

func TestAptUpgradeArguments(t *testing.T) {
	cases := []struct {
		body         string
		mode, kernel string
		detail       string
	}{
		{`{"confirm":true}`, "upgrade", "no-kernel", "Standart yükseltme, çekirdek hariç"},
		{`{"mode":"upgrade","confirm":true}`, "upgrade", "no-kernel", "Standart yükseltme, çekirdek hariç"},
		{`{"mode":"","include_kernel":false,"confirm":true}`, "upgrade", "no-kernel", "Standart yükseltme, çekirdek hariç"},
		{`{"mode":"upgrade","include_kernel":true,"confirm":true}`, "upgrade", "kernel", "Standart yükseltme, çekirdek dahil"},
		{`{"mode":"full","confirm":true}`, "full", "no-kernel", "Tam yükseltme, çekirdek hariç"},
		{`{"mode":"full","include_kernel":true,"confirm":true}`, "full", "kernel", "Tam yükseltme, çekirdek dahil"},
	}
	for _, c := range cases {
		t.Run(c.body, func(t *testing.T) {
			e := newEnv(t)
			e.write("units-active", "myserver-apt-upgrade.service\n", 0o644)
			res := e.do("POST", "/updates/apt/upgrade", c.body, "admin")
			if res.Status != http.StatusAccepted {
				t.Fatalf("status %d: %s", res.Status, res.Body)
			}
			var meta JobMeta
			res.into(t, &meta)
			if !updatescheck.ValidJobID(meta.ID) || meta.Kind != kindApt || meta.Status != jobRunning ||
				meta.Username != "yonetici" || meta.Detail != c.detail {
				t.Errorf("job = %+v", meta)
			}
			want := [][]string{{"updates-apt-upgrade", c.mode, c.kernel, meta.ID}}
			if got := e.helperCalls(); !reflect.DeepEqual(got, want) {
				t.Errorf("helper calls = %q, want %q", got, want)
			}

			// Only one apt job at a time.
			again := e.do("POST", "/updates/apt/upgrade", `{"confirm":true}`, "admin")
			if again.Status != http.StatusConflict || again.code() != "conflict" {
				t.Errorf("second upgrade: status %d code %q", again.Status, again.code())
			}
			if n := len(e.helperCalls()); n != 1 {
				t.Errorf("the second request reached the helper (%d calls)", n)
			}
			var s summaryView
			e.do("GET", "/updates/summary", "", "user").into(t, &s)
			if !s.Apt.Running {
				t.Error("summary does not report the running upgrade")
			}

			// The unit finishes.
			e.write("state/apt-upgrade.log", "Güncelleme başlatıldı.\nSetting up vim ...\n", 0o644)
			e.write("state/apt-upgrade.result", "id="+meta.ID+"\nstate=success\nstarted=100\nfinished=200\nmessage=\n", 0o644)
			job := e.mod.jobs.get(meta.ID)
			e.waitIdle(job)
			if got := job.Meta(); got.Status != jobSuccess || got.FinishedAt == nil || got.Message != "" {
				t.Errorf("finished job = %+v", got)
			}
			lines, _ := job.Snapshot(0)
			if !reflect.DeepEqual(lines, []string{"Güncelleme başlatıldı.", "Setting up vim ...", "İşlem başarıyla tamamlandı."}) {
				t.Errorf("lines = %q", lines)
			}
			wantAudit := []auditRow{
				{"yonetici", "updates.apt_upgrade", c.detail, "başlatıldı", true},
				{"yonetici", "updates.apt_upgrade", c.detail, "", true},
			}
			if got := e.auditRows(); !reflect.DeepEqual(got, wantAudit) {
				t.Errorf("audit = %+v", got)
			}
			if got := e.notificationTitles(); !reflect.DeepEqual(got, []string{"SUCCESS Güncelleme tamamlandı"}) {
				t.Errorf("notifications = %q", got)
			}
			// Afterwards the list is recomputed without refreshing from the network.
			if got := e.helperActions(); !reflect.DeepEqual(got, []string{"updates-apt-upgrade", "updates-apt-list"}) {
				t.Errorf("helper actions = %q", got)
			}
		})
	}
}

func TestAptUpgradeHelperRefuses(t *testing.T) {
	e := newEnv(t)
	e.write("updates-apt-upgrade.err", "Zaten çalışan bir paket güncellemesi var.", 0o644)
	res := e.do("POST", "/updates/apt/upgrade", `{"confirm":true}`, "admin")
	if res.Status != http.StatusBadGateway || res.code() != "apt_upgrade_failed" ||
		res.message() != "Zaten çalışan bir paket güncellemesi var." {
		t.Errorf("status %d code %q message %q", res.Status, res.code(), res.message())
	}
	job := e.mod.jobs.latest(kindApt)
	if job == nil || job.Meta().Status != jobFailed {
		t.Fatalf("job = %+v", job)
	}
	if e.mod.jobs.running(kindApt) != nil {
		t.Error("the failed start left a running job behind")
	}
	rows := e.auditRows()
	if len(rows) != 1 || rows[0].Success || rows[0].Action != "updates.apt_upgrade" {
		t.Errorf("audit = %+v", rows)
	}

	os.Remove(e.path("updates-apt-upgrade.err"))
	e.write("updates-apt-upgrade.crash", "", 0o644)
	res = e.do("POST", "/updates/apt/upgrade", `{"confirm":true}`, "admin")
	if res.Status != http.StatusBadGateway || res.message() != "Paket güncellemesi başlatılamadı." {
		t.Errorf("status %d message %q", res.Status, res.message())
	}
	if strings.Contains(res.Body, "/etc/secret") {
		t.Errorf("internal error leaked: %s", res.Body)
	}
}

func TestAptUpgradeRefusedWhileSelfUpdateRuns(t *testing.T) {
	for _, file := range []string{"units-active", "units-activating"} {
		e := newEnv(t)
		e.write(file, "myserver-update.service\n", 0o644)
		res := e.do("POST", "/updates/apt/upgrade", `{"confirm":true}`, "admin")
		if res.Status != http.StatusConflict {
			t.Errorf("%s: status %d: %s", file, res.Status, res.Body)
		}
		e.wantNoHelperCalls("upgrade during a self-update")
		if e.mod.jobs.latest(kindApt) != nil {
			t.Error("a refused request created a job")
		}
	}
}

/* ---------- reboot ---------- */

func TestRebootRequiresExactHostname(t *testing.T) {
	e := newEnv(t)
	host := hostname(t)
	bodies := []string{
		"", `{}`, `{"hostname":""}`, `{"hostname":" "}`, `{"hostname":"localhost"}`,
		`{"hostname":"` + strings.ToUpper(host) + `x"}`, `{"hostname":"` + host + `x"}`,
		`{"hostname":"` + host[:len(host)-1] + `"}`, `{"hostname":"` + host + `.local"}`,
		`{"hostname":"*"}`, `{"hostname":true}`, `{"hostname":["` + host + `"]}`, `{"confirm":true}`,
		`{"hostname":"` + host + `","force":true}`, `{"hostname":"` + host + `;reboot"}`,
	}
	for _, body := range bodies {
		res := e.do("POST", "/updates/reboot", body, "admin")
		if res.Status != http.StatusBadRequest {
			t.Errorf("body %q: status %d: %s", body, res.Status, res.Body)
		}
	}
	e.wantNoHelperCalls("reboot without the exact hostname")
	if rows := e.auditRows(); len(rows) != 0 {
		t.Errorf("audit = %+v", rows)
	}

	res := e.do("POST", "/updates/reboot", `{"hostname":"`+host+`"}`, "admin")
	if res.Status != 200 {
		t.Fatalf("status %d: %s", res.Status, res.Body)
	}
	if got, want := e.helperCalls(), [][]string{{"updates-reboot", host}}; !reflect.DeepEqual(got, want) {
		t.Errorf("helper calls = %q", got)
	}
	if got, want := e.auditRows(), []auditRow{{"yonetici", "updates.reboot", host, "", true}}; !reflect.DeepEqual(got, want) {
		t.Errorf("audit = %+v", got)
	}
}

func TestRebootRefusedWhileUpdating(t *testing.T) {
	body := func(t *testing.T) string { return `{"hostname":"` + hostname(t) + `"}` }
	for _, file := range []string{"units-active", "units-activating"} {
		for _, unit := range []string{"myserver-apt-upgrade.service", "myserver-update.service"} {
			t.Run(file+"/"+unit, func(t *testing.T) {
				e := newEnv(t)
				e.write(file, unit+"\n", 0o644)
				res := e.do("POST", "/updates/reboot", body(t), "admin")
				if res.Status != http.StatusConflict || res.code() != "conflict" {
					t.Errorf("status %d code %q: %s", res.Status, res.code(), res.Body)
				}
				e.wantNoHelperCalls("reboot during an update")
			})
		}
	}
	t.Run("running job", func(t *testing.T) {
		e := newEnv(t)
		e.mod.jobs.add(newJob(kindApt, "x", "", "yonetici"))
		res := e.do("POST", "/updates/reboot", body(t), "admin")
		if res.Status != http.StatusConflict {
			t.Errorf("status %d: %s", res.Status, res.Body)
		}
		e.wantNoHelperCalls("reboot during a job")
	})
	t.Run("helper refuses", func(t *testing.T) {
		e := newEnv(t)
		e.write("updates-reboot.err", "MyServer güncellemesi sürerken sunucu yeniden başlatılamaz.", 0o644)
		res := e.do("POST", "/updates/reboot", body(t), "admin")
		if res.Status != http.StatusBadGateway || res.code() != "reboot_failed" ||
			res.message() != "MyServer güncellemesi sürerken sunucu yeniden başlatılamaz." {
			t.Errorf("status %d code %q message %q", res.Status, res.code(), res.message())
		}
		rows := e.auditRows()
		if len(rows) != 1 || rows[0].Success {
			t.Errorf("audit = %+v", rows)
		}
	})
}

// systemctl is-active exits 3 for an inactive unit and 4 for one that does
// not exist. Neither is an error.
func TestUnitActiveExitCodes(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if unitActive(ctx, updatescheck.AptUnit) {
		t.Error("inactive unit (exit 3) reported active")
	}
	e.write("units-unknown", "myserver-apt-upgrade.service\n", 0o644)
	if unitActive(ctx, updatescheck.AptUnit) {
		t.Error("unknown unit (exit 4) reported active")
	}
	e.write("units-activating", "myserver-apt-upgrade.service\n", 0o644)
	os.Remove(e.path("units-unknown"))
	if !unitActive(ctx, updatescheck.AptUnit) {
		t.Error("activating unit reported inactive")
	}
	e.write("units-active", "myserver-apt-upgrade.service\n", 0o644)
	if !unitActive(ctx, updatescheck.AptUnit) {
		t.Error("active unit reported inactive")
	}
	if unitActive(ctx, updatescheck.SelfUnit) {
		t.Error("the other unit is reported active")
	}
	res := e.do("POST", "/updates/reboot", `{"hostname":"`+hostname(t)+`"}`, "admin")
	if res.Status != http.StatusConflict {
		t.Errorf("status %d", res.Status)
	}
	os.Remove(e.path("units-active"))
	os.Remove(e.path("units-activating"))
	e.write("units-unknown", "myserver-apt-upgrade.service\nmyserver-update.service\n", 0o644)
	res = e.do("POST", "/updates/reboot", `{"hostname":"`+hostname(t)+`"}`, "admin")
	if res.Status != 200 {
		t.Errorf("exit code 4 was treated as an error: status %d: %s", res.Status, res.Body)
	}
	for _, c := range e.systemctlCalls() {
		if !strings.HasPrefix(c, "is-active ") {
			t.Errorf("systemctl %s", c)
		}
	}
	systemctlBin = e.path("no-such-systemctl")
	if unitActive(ctx, updatescheck.AptUnit) {
		t.Error("active without systemctl")
	}
}

/* ---------- reboot-required detection ---------- */

func TestRebootRequiredDetection(t *testing.T) {
	e := newEnv(t)
	view := func() aptView {
		var v aptView
		e.do("GET", "/updates/apt", "", "user").into(t, &v)
		return v
	}
	if v := view(); v.RebootRequired || len(v.RebootPackages) != 0 {
		t.Errorf("no flag file: %+v", v)
	}
	// The package list alone does not mean anything.
	e.write("reboot-required.pkgs", "linux-image-6.8.0-45-generic\n", 0o644)
	if v := view(); v.RebootRequired || len(v.RebootPackages) != 0 {
		t.Errorf("package list without the flag: %+v", v)
	}
	e.write("reboot-required", "*** System restart required ***\n", 0o644)
	os.Remove(e.path("reboot-required.pkgs"))
	if v := view(); !v.RebootRequired || v.RebootPackages == nil || len(v.RebootPackages) != 0 {
		t.Errorf("flag without a package list: %+v", v)
	}
	e.write("reboot-required.pkgs", "linux-image-6.8.0-45-generic\nlinux-base\nlibc6\n\nlinux-base\n"+
		"  dbus  \n<script>alert(1)</script>\n--force\nlibc6; reboot\nUPPER\n"+strings.Repeat("a", 200)+"\n", 0o644)
	v := view()
	want := []string{"linux-image-6.8.0-45-generic", "linux-base", "libc6", "dbus"}
	if !v.RebootRequired || !reflect.DeepEqual(v.RebootPackages, want) {
		t.Errorf("reboot packages = %q, want %q", v.RebootPackages, want)
	}
	var s summaryView
	e.do("GET", "/updates/summary", "", "user").into(t, &s)
	if !s.Apt.RebootRequired {
		t.Error("summary does not report the required reboot")
	}
	// An absurdly large list is cut, not loaded whole.
	e.write("reboot-required.pkgs", strings.Repeat("libc6-"+strings.Repeat("x", 20)+"\n", 100000), 0o644)
	if v := view(); !v.RebootRequired || len(v.RebootPackages) > 1 {
		t.Errorf("%d packages from an oversized list", len(v.RebootPackages))
	}
	e.wantNoHelperCalls("reboot detection")
}

// A finished upgrade that needs a reboot says so and leaves it to the owner.
func TestAptUpgradeNeverRebootsByItself(t *testing.T) {
	e := newEnv(t)
	e.write("reboot-required", "", 0o644)
	e.write("reboot-required.pkgs", "linux-image-6.8.0-45-generic\n", 0o644)
	e.write("state/apt-upgrade.result", "id="+testJobID+"\nstate=success\n", 0o644)
	job := restoreJob(JobMeta{ID: testJobID, Kind: kindApt, Title: "x", Username: "yonetici", StartedAt: 1})
	e.mod.jobs.add(job)
	e.mod.followAptJob(job, audit.Actor{Username: "yonetici", IP: "-"}, 0)
	if got := e.helperActions(); !reflect.DeepEqual(got, []string{"updates-apt-list"}) {
		t.Errorf("helper actions = %q", got)
	}
	want := []string{"SUCCESS Güncelleme tamamlandı", "WARNING Yeniden başlatma gerekiyor"}
	if got := e.notificationTitles(); !reflect.DeepEqual(got, want) {
		t.Errorf("notifications = %q", got)
	}
	lines, _ := job.Snapshot(0)
	if !strings.Contains(strings.Join(lines, "\n"), "yeniden başlatılması gerekiyor") {
		t.Errorf("lines = %q", lines)
	}
}

/* ---------- authorisation ---------- */

func adminRoutes(t *testing.T) []struct{ method, path, body string } {
	host := hostname(t)
	return []struct{ method, path, body string }{
		{"POST", "/updates/apt/check", ""},
		{"POST", "/updates/apt/upgrade", `{"confirm":true}`},
		{"POST", "/updates/apt/upgrade", `{"mode":"full","include_kernel":true,"confirm":true}`},
		{"POST", "/updates/reboot", `{"hostname":"` + host + `"}`},
		{"POST", "/updates/docker/check", ""},
		{"POST", "/updates/docker/pull", `{"image":"nginx:latest"}`},
		{"POST", "/updates/self/check", ""},
		{"POST", "/updates/self/apply", `{"version":"1.3.0","confirm":true}`},
		{"GET", "/updates/jobs", ""},
		{"GET", "/updates/jobs/" + testJobID, ""},
		{"GET", "/updates/jobs/" + testJobID + "/stream", ""},
	}
}

func TestAdminOnlyRoutes(t *testing.T) {
	e := newEnv(t)
	// Everything is in place for the requests to succeed if they got through.
	e.set(KeySource, sourceURL)
	e.set(KeyManifestURL, "https://127.0.0.1:1/manifest.json")
	sum := strings.Repeat("ab", 32)
	e.mod.self = selfState{SourceID: e.mod.sourceID(), CheckedAt: 1, Latest: &Release{Version: "1.3.0",
		Assets: map[string]Asset{"amd64": {URL: "https://127.0.0.1:1/myserver-linux-amd64.tar.gz", SHA256: sum}}}}
	e.mod.docker = dockerState{CheckedAt: 1, Images: []imageStatus{{Ref: "nginx:latest", Status: imgUpdate}}}
	job := newJob(kindApt, "Ubuntu paket güncellemesi", "", "yonetici")
	job.meta.ID = testJobID
	job.Println("gizli çıktı")
	job.Finish("")
	e.mod.jobs.add(job)
	e.mod.store.saveJob(job.Meta(), job.Log())

	for _, who := range []string{"user", ""} {
		wantStatus, wantCode := http.StatusForbidden, "forbidden"
		if who == "" {
			wantStatus, wantCode = http.StatusUnauthorized, "unauthorized"
		}
		for _, c := range adminRoutes(t) {
			res := e.do(c.method, c.path, c.body, who)
			if res.Status != wantStatus || res.code() != wantCode {
				t.Errorf("%s %s as %q: status %d code %q", c.method, c.path, who, res.Status, res.code())
			}
			if strings.Contains(res.Body, "gizli") || strings.Contains(res.Body, testJobID) {
				t.Errorf("%s %s as %q: job data leaked: %s", c.method, c.path, who, res.Body)
			}
		}
	}
	e.wantNoHelperCalls("requests of a normal user")
	if rows := e.auditRows(); len(rows) != 0 {
		t.Errorf("refused requests wrote audit rows: %+v", rows)
	}
	if calls := e.systemctlCalls(); len(calls) != 0 {
		t.Errorf("systemctl was called: %q", calls)
	}
	if n := len(e.notifications()); n != 0 {
		t.Errorf("%d notifications", n)
	}
	if e.mod.jobs.running(kindApt) != nil || e.mod.jobs.running(kindDockerPull) != nil {
		t.Error("a job was started")
	}

	// The same routes do get through for an administrator.
	for _, c := range adminRoutes(t) {
		if c.method != "GET" {
			continue
		}
		if res := e.do(c.method, c.path, c.body, "admin"); res.Status != 200 {
			t.Errorf("%s %s as admin: status %d", c.method, c.path, res.Status)
		}
	}
}

func TestStateChangesRequireCSRF(t *testing.T) {
	e := newEnv(t)
	for _, c := range adminRoutes(t) {
		if c.method == "GET" {
			continue
		}
		res := e.do(c.method, c.path, c.body, "admin", "X-CSRF-Token", "")
		if res.Status != http.StatusForbidden {
			t.Errorf("%s %s without a CSRF token: status %d", c.method, c.path, res.Status)
		}
	}
	e.wantNoHelperCalls("requests without a CSRF token")
}

func TestReadOnlyRoutesForNormalUser(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/updates/summary", "/updates/apt", "/updates/docker", "/updates/self"} {
		if res := e.do("GET", p, "", "user"); res.Status != 200 {
			t.Errorf("%s: status %d", p, res.Status)
		}
		if res := e.do("GET", p, "", ""); res.Status != http.StatusUnauthorized {
			t.Errorf("%s without a session: status %d", p, res.Status)
		}
		for _, method := range []string{"POST", "PUT", "DELETE"} {
			if res := e.do(method, p, "{}", "admin"); res.Status == 200 || res.Status == 202 {
				t.Errorf("%s %s: status %d", method, p, res.Status)
			}
		}
	}
	e.wantNoHelperCalls("reading")
}

/* ---------- settings ---------- */

func TestSettingsValidators(t *testing.T) {
	e := newEnv(t)
	type kv struct{ key, value string }
	good := []struct {
		kv
		stored string
	}{
		{kv{KeyInterval, "1"}, "1"}, {kv{KeyInterval, "24"}, "24"}, {kv{KeyInterval, "720"}, "720"}, {kv{KeyInterval, " 12 "}, "12"},
		{kv{KeyAptAuto, "false"}, "false"}, {kv{KeyAptAuto, "true"}, "true"},
		{kv{KeyDockerAuto, "false"}, "false"}, {kv{KeySelfAuto, "false"}, "false"},
		{kv{KeySource, "github"}, "github"}, {kv{KeySource, "url"}, "url"}, {kv{KeySource, "none"}, "none"},
		{kv{KeyGitHubRepo, "owner/repo"}, "owner/repo"}, {kv{KeyGitHubRepo, " Owner-1/my.repo_x "}, "Owner-1/my.repo_x"},
		{kv{KeyGitHubRepo, ""}, ""},
		{kv{KeyManifestURL, "https://updates.example.com/myserver/manifest.json"}, "https://updates.example.com/myserver/manifest.json"},
		{kv{KeyManifestURL, ""}, ""},
	}
	for _, c := range good {
		res := e.do("PUT", "/settings", `{"`+c.key+`":"`+c.value+`"}`, "admin")
		if res.Status != 200 {
			t.Errorf("%s=%q refused: %d %s", c.key, c.value, res.Status, res.Body)
			continue
		}
		if got := e.deps.Settings.Get(c.key); got != c.stored {
			t.Errorf("%s=%q stored as %q", c.key, c.value, got)
		}
	}
	e.set(KeySource, sourceNone)
	e.set(KeyGitHubRepo, "")
	e.set(KeyManifestURL, "")
	e.set(KeyInterval, "24")
	bad := []kv{
		{KeyInterval, "0"}, {KeyInterval, "-1"}, {KeyInterval, "721"}, {KeyInterval, "1.5"}, {KeyInterval, "abc"},
		{KeyInterval, ""}, {KeyInterval, "1e2"}, {KeyInterval, "99999999999999999999"}, {KeyInterval, "0x10"},
		{KeyAptAuto, "yes"}, {KeyAptAuto, ""}, {KeyAptAuto, "2"}, {KeyDockerAuto, "evet"}, {KeySelfAuto, "on"},
		{KeySource, ""}, {KeySource, "GitHub"}, {KeySource, "http"}, {KeySource, "none "}, {KeySource, "file"},
		{KeyGitHubRepo, "owner"}, {KeyGitHubRepo, "owner/"}, {KeyGitHubRepo, "/repo"}, {KeyGitHubRepo, "owner/repo/extra"},
		{KeyGitHubRepo, "owner/.."}, {KeyGitHubRepo, "../repo"}, {KeyGitHubRepo, "owner/re..po"}, {KeyGitHubRepo, "-owner/repo"},
		{KeyGitHubRepo, "owner/repo?x=1"}, {KeyGitHubRepo, "owner/repo#x"}, {KeyGitHubRepo, "owner/re po"},
		{KeyGitHubRepo, "owner/re\\npo"}, {KeyGitHubRepo, "https://github.com/owner/repo"}, {KeyGitHubRepo, "owner@evil/repo"},
		{KeyGitHubRepo, "owner/repo%2f..%2f.."}, {KeyGitHubRepo, strings.Repeat("a", 40) + "/repo"},
		{KeyGitHubRepo, "owner/" + strings.Repeat("a", 101)}, {KeyGitHubRepo, "ownér/repo"},
		{KeyManifestURL, "http://updates.example.com/manifest.json"}, {KeyManifestURL, "ftp://updates.example.com/m.json"},
		{KeyManifestURL, "updates.example.com/manifest.json"}, {KeyManifestURL, "//updates.example.com/manifest.json"},
		{KeyManifestURL, "https://"}, {KeyManifestURL, "https:///manifest.json"}, {KeyManifestURL, "file:///etc/passwd"},
		{KeyManifestURL, "https://user:parola@updates.example.com/manifest.json"},
		{KeyManifestURL, "https://user@updates.example.com/manifest.json"},
		{KeyManifestURL, "https://updates.example.com/manifest.json#x"},
		{KeyManifestURL, "https://updates.example.com/mani\\nfest.json"},
		{KeyManifestURL, "https://updates.example.com/mani\\u0000fest.json"},
		{KeyManifestURL, "https://exa mple.com/manifest.json"},
		{KeyManifestURL, "javascript:alert(1)"}, {KeyManifestURL, "--config=/etc/x"},
		{KeyManifestURL, "https://updates.example.com/" + strings.Repeat("a", 500)},
	}
	for _, c := range bad {
		before := e.deps.Settings.Get(c.key)
		res := e.do("PUT", "/settings", `{"`+c.key+`":"`+c.value+`"}`, "admin")
		if res.Status != http.StatusBadRequest {
			t.Errorf("%s=%q: status %d: %s", c.key, c.value, res.Status, res.Body)
		}
		if got := e.deps.Settings.Get(c.key); got != before {
			t.Errorf("%s=%q was stored as %q", c.key, c.value, got)
		}
	}
	// Every key the module registers is writable, and nothing else of it.
	var all map[string]string
	e.do("GET", "/settings", "", "admin").into(t, &all)
	registered := []string{}
	for k := range all {
		if strings.HasPrefix(k, "updates.") {
			registered = append(registered, k)
		}
	}
	if len(registered) != 7 {
		t.Errorf("registered keys = %q", registered)
	}
	for _, k := range []string{KeyInterval, KeyAptAuto, KeyDockerAuto, KeySelfAuto, KeySource, KeyGitHubRepo, KeyManifestURL} {
		if _, ok := all[k]; !ok {
			t.Errorf("%s is not registered", k)
		}
	}
	if res := e.do("PUT", "/settings", `{"updates.auto_install":"true"}`, "admin"); res.Status != http.StatusBadRequest {
		t.Errorf("unknown key: status %d", res.Status)
	}
	if res := e.do("PUT", "/settings", `{"`+KeyInterval+`":"12"}`, "user"); res.Status != http.StatusForbidden {
		t.Errorf("normal user: status %d", res.Status)
	}
}

func TestSettingsDefaults(t *testing.T) {
	e := newEnv(t)
	want := map[string]string{KeyInterval: "24", KeyAptAuto: "true", KeyDockerAuto: "true", KeySelfAuto: "true",
		KeySource: "github", KeyGitHubRepo: "dedikoducular/myserver", KeyManifestURL: ""}
	for k, v := range want {
		if got := e.deps.Settings.Get(k); got != v {
			t.Errorf("default of %s = %q, want %q", k, got, v)
		}
	}
	// The project's own GitHub releases are the default source.
	if e.mod.source() == nil || e.mod.sourceID() == "" {
		t.Error("the default GitHub release source is not configured")
	}
}
