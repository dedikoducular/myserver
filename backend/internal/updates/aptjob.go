package updates

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"myserver/internal/audit"
	"myserver/internal/notify"
	"myserver/internal/updates/updatescheck"
)

// The package upgrade runs in the transient unit myserver-apt-upgrade,
// started by the helper. The panel only follows it: it tails the log file
// the unit writes and reads the result file the unit leaves behind, so the
// upgrade is unaffected by the panel stopping or restarting.

const (
	aptFollowLimit   = 12 * time.Hour
	aptMaxLogRead    = 8 << 20
	msgUnknownResult = "Güncelleme işleminin sonucu izlenemedi. Sunucuda paket durumunu denetleyin."
)

// Locations and polling periods. They are variables only so that the tests
// can use a temporary directory, a recording script and short periods; the
// panel never changes them.
var (
	systemctlBin    = "/usr/bin/systemctl"
	aptLogFile      = updatescheck.AptLogFile
	aptResultFile   = updatescheck.AptResultFile
	aptTailPeriod   = 700 * time.Millisecond
	aptResultPeriod = 2 * time.Second
	aptActivePeriod = 6 * time.Second
)

// readSmallFile reads a bounded regular file without following a symlink
// at its final component.
func readSmallFile(path string, limit int64) (string, bool) {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return "", false
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func aptResult() map[string]string {
	text, ok := readSmallFile(aptResultFile, 16<<10)
	if !ok {
		return map[string]string{}
	}
	return updatescheck.ParseKV(text)
}

func systemctl(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, systemctlBin, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "LANG=C"}
	out, err := cmd.Output()
	return string(out), err
}

// unitActive reports whether a transient unit is running or starting.
func unitActive(ctx context.Context, unit string) bool {
	out, _ := systemctl(ctx, "is-active", unit+".service")
	s := strings.TrimSpace(out)
	return s == "active" || s == "activating" || s == "deactivating" || s == "reloading"
}

// unitOutcome asks systemd how a finished unit ended. known is false when
// systemd no longer has the unit loaded or the answer is inconclusive.
func unitOutcome(ctx context.Context, unit string) (ok, known bool) {
	out, err := systemctl(ctx, "show", "-p", "LoadState,ActiveState,Result,ExecMainStatus,ExecMainCode", unit+".service")
	if err != nil {
		return false, false
	}
	kv := updatescheck.ParseKV(out)
	if kv["LoadState"] != "loaded" {
		return false, false
	}
	switch kv["ActiveState"] {
	case "failed":
		return false, true
	case "inactive":
		// ExecMainCode 1 = exited; only then is the status meaningful.
		if kv["Result"] == "success" && kv["ExecMainCode"] == "1" && kv["ExecMainStatus"] == "0" {
			return true, true
		}
		if kv["Result"] != "" && kv["Result"] != "success" {
			return false, true
		}
	}
	return false, false
}

// reattachAptJob re-attaches to an upgrade that was running when the panel
// last stopped. It returns the job id it took over, or "".
func (m *Module) reattachAptJob() string {
	res := aptResult()
	id := res["id"]
	if !updatescheck.ValidJobID(id) {
		return ""
	}
	ctx, cancel := dbCtx()
	meta, _, err := m.store.getJob(ctx, id)
	cancel()
	if err != nil || meta.Kind != kindApt || meta.Status != jobRunning {
		return ""
	}
	job := restoreJob(meta)
	m.jobs.add(job)
	slog.Info("süren paket güncellemesine yeniden bağlanıldı", "job", id)
	go m.followAptJob(job, audit.Actor{Username: meta.Username, IP: "-"}, 0)
	return id
}

// followAptJob copies the unit's log into the job and closes the job when
// the unit's result is known.
func (m *Module) followAptJob(job *Job, actor audit.Actor, offset int64) {
	ctx, cancel := context.WithTimeout(context.Background(), aptFollowLimit)
	defer cancel()
	meta := job.Meta()

	tail := time.NewTicker(aptTailPeriod)
	defer tail.Stop()
	var lastResult, lastActive time.Time
	inactive := 0
	state, message := "", ""

loop:
	for {
		offset = copyLog(job, offset)
		now := time.Now()
		if now.Sub(lastResult) >= aptResultPeriod {
			lastResult = now
			res := aptResult()
			if res["id"] == meta.ID && (res["state"] == updatescheck.StateSuccess || res["state"] == updatescheck.StateFailed) {
				state, message = res["state"], res["message"]
				break loop
			}
			if now.Sub(lastActive) >= aptActivePeriod {
				lastActive = now
				if unitActive(ctx, updatescheck.AptUnit) {
					inactive = 0
				} else {
					inactive++
				}
				// Not running and no result after repeated looks: the unit
				// ended without recording one (killed, power loss, reboot).
				if inactive >= 3 {
					res = aptResult()
					// Only a recognised final state counts; anything else in
					// the file is inconclusive and systemd is asked instead.
					if res["id"] == meta.ID && (res["state"] == updatescheck.StateSuccess || res["state"] == updatescheck.StateFailed) {
						state, message = res["state"], res["message"]
					} else if ok, known := unitOutcome(ctx, updatescheck.AptUnit); known && ok {
						state = updatescheck.StateSuccess
					} else if known {
						state, message = updatescheck.StateFailed, "Paket güncellemesi beklenmedik biçimde sonlandı. Ayrıntılar için işlem çıktısına bakın."
					} else {
						state, message = updatescheck.StateFailed, msgUnknownResult
					}
					break loop
				}
			}
		}
		select {
		case <-ctx.Done():
			state, message = updatescheck.StateFailed, msgUnknownResult
			break loop
		case <-tail.C:
		}
	}
	copyLog(job, offset)
	m.completeAptJob(job, actor, state == updatescheck.StateSuccess, cleanText(message, 500))
}

// copyLog appends the log file's new content to the job and returns the
// new offset.
func copyLog(job *Job, offset int64) int64 {
	fi, err := os.Lstat(aptLogFile)
	if err != nil || !fi.Mode().IsRegular() {
		return offset
	}
	if fi.Size() < offset {
		offset = 0 // the file was replaced
	}
	if fi.Size() == offset || offset >= aptMaxLogRead {
		return offset
	}
	f, err := os.Open(aptLogFile)
	if err != nil {
		return offset
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset
	}
	buf := make([]byte, 64<<10)
	for offset < aptMaxLogRead {
		n, err := f.Read(buf)
		if n > 0 {
			_, _ = job.Write(buf[:n])
			offset += int64(n)
		}
		if err != nil || n == 0 {
			break
		}
	}
	return offset
}

func (m *Module) completeAptJob(job *Job, actor audit.Actor, ok bool, failure string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if ok {
		failure = ""
	} else if failure == "" {
		failure = "Paket güncellemesi başarısız oldu. Ayrıntılar için işlem çıktısına bakın."
	}
	reboot, _ := rebootStatus()
	if reboot {
		job.Println("Değişikliklerin etkin olması için sunucunun yeniden başlatılması gerekiyor.")
	}
	meta := m.finishJob(job, failure)
	m.deps.Audit.Log(ctx, actor, "updates.apt_upgrade", meta.Detail, failure, ok)
	if ok {
		m.deps.Notify.Publish(ctx, notify.Success, notifySource, "Güncelleme tamamlandı", "Ubuntu paketleri güncellendi.")
	} else {
		slog.Error("paket güncellemesi başarısız", "job", meta.ID, "message", failure)
		m.deps.Notify.Publish(ctx, notify.Error, notifySource, "Güncelleme başarısız oldu", failure)
	}
	if reboot {
		m.deps.Notify.PublishOnce(ctx, notify.Warning, notifySource, "Yeniden başlatma gerekiyor",
			"Yüklenen güncellemelerin etkin olması için sunucu yeniden başlatılmalıdır. Panel sunucuyu kendiliğinden yeniden başlatmaz.",
			"updates.reboot_required", 24*time.Hour)
	}

	// Recompute what is still upgradable, without touching the network.
	m.mu.Lock()
	busy := m.aptChecking
	m.aptChecking = true
	m.mu.Unlock()
	if !busy {
		_ = m.refreshAptState(ctx, false)
		m.mu.Lock()
		m.aptChecking = false
		m.mu.Unlock()
	}
}
