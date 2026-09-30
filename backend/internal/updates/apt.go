package updates

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"myserver/internal/auth"
	"myserver/internal/httpx"
	"myserver/internal/notify"
	"myserver/internal/privileged"
	"myserver/internal/updates/updatescheck"
)

// Variables only so that the tests can use fixture files.
var (
	rebootFlag     = "/var/run/reboot-required"
	rebootPkgsFile = "/var/run/reboot-required.pkgs"
)

// aptState is the cached and persisted result of the last package check.
type aptState struct {
	Packages  []updatescheck.Package `json:"packages"`
	CheckedAt int64                  `json:"checked_at"`
	Error     *stateError            `json:"error"`
}

type aptView struct {
	Packages       []updatescheck.Package `json:"packages"`
	Count          int                    `json:"count"`
	SecurityCount  int                    `json:"security_count"`
	KernelCount    int                    `json:"kernel_count"`
	CheckedAt      *int64                 `json:"checked_at"`
	RefreshedAt    *int64                 `json:"lists_refreshed_at"`
	Error          *stateError            `json:"error"`
	Checking       bool                   `json:"checking"`
	AutoCheck      bool                   `json:"auto_check"`
	RebootRequired bool                   `json:"reboot_required"`
	RebootPackages []string               `json:"reboot_packages"`
	Hostname       string                 `json:"hostname"`
	Job            *JobMeta               `json:"job"`
}

// listsRefreshedAt reports when apt's package lists last changed on disk,
// whoever refreshed them.
func listsRefreshedAt() *int64 {
	var latest int64
	for _, p := range []string{"/var/lib/apt/periodic/update-success-stamp", "/var/lib/apt/lists"} {
		if fi, err := os.Stat(p); err == nil && fi.ModTime().Unix() > latest {
			latest = fi.ModTime().Unix()
		}
	}
	return timePtr(latest)
}

func rebootStatus() (bool, []string) {
	pkgs := []string{}
	if _, err := os.Stat(rebootFlag); err != nil {
		return false, pkgs
	}
	if b, err := os.ReadFile(rebootPkgsFile); err == nil {
		if len(b) > 16<<10 {
			b = b[:16<<10]
			// The cut may fall inside a name; drop the incomplete line.
			b = b[:strings.LastIndexByte(string(b), 10)+1]
		}
		seen := map[string]bool{}
		for _, l := range strings.Split(string(b), "\n") {
			l = strings.TrimSpace(l)
			if updatescheck.ValidPackageName(l) && !seen[l] {
				seen[l] = true
				pkgs = append(pkgs, l)
			}
		}
	}
	return true, pkgs
}

func (m *Module) aptView(full bool) aptView {
	m.mu.RLock()
	st, checking := m.apt, m.aptChecking
	m.mu.RUnlock()
	v := aptView{
		Packages:  []updatescheck.Package{},
		CheckedAt: timePtr(st.CheckedAt),
		Error:     st.Error,
		Checking:  checking,
		AutoCheck: m.deps.Settings.Bool(KeyAptAuto),
	}
	for _, p := range st.Packages {
		v.Count++
		if p.Security {
			v.SecurityCount++
		}
		if p.Kernel {
			v.KernelCount++
		}
	}
	v.RebootRequired, v.RebootPackages = rebootStatus()
	if full {
		v.Packages = append(v.Packages, st.Packages...)
		v.RefreshedAt = listsRefreshedAt()
		v.Hostname, _ = os.Hostname()
		if j := m.jobs.latest(kindApt); j != nil {
			meta := j.Meta()
			v.Job = &meta
		}
	}
	return v
}

func (m *Module) handleApt(w http.ResponseWriter, _ *http.Request) error {
	httpx.OK(w, m.aptView(true))
	return nil
}

func helperError(err error, code, fallback string) *stateError {
	msg := privileged.UserMessage(err, fallback)
	return &stateError{Code: updatescheck.CodeForMessage(msg, code), Message: msg}
}

// checkApt refreshes the package lists and recomputes the upgradable set.
func (m *Module) checkApt(ctx context.Context) error {
	m.mu.Lock()
	if m.jobs.running(kindApt) != nil {
		m.mu.Unlock()
		return httpx.Conflict("Paket güncellemesi sürerken denetleme yapılamaz.")
	}
	if m.aptChecking {
		m.mu.Unlock()
		return httpx.Conflict("Paket denetimi zaten sürüyor.")
	}
	m.aptChecking = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.aptChecking = false
		m.mu.Unlock()
	}()
	return m.refreshAptState(ctx, true)
}

// refreshAptState lists upgradable packages, optionally refreshing the
// package lists first, and stores the result.
func (m *Module) refreshAptState(ctx context.Context, refresh bool) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Minute)
	defer cancel()

	var problem *stateError
	if refresh {
		if _, err := m.deps.Priv.Run(ctx, "updates-apt-refresh"); err != nil {
			problem = helperError(err, "apt_refresh_failed", "Paket listeleri yenilenemedi.")
			slog.Warn("paket listeleri yenilenemedi", "error", err.Error())
		}
	}
	out, err := m.deps.Priv.Run(ctx, "updates-apt-list")

	m.mu.Lock()
	prev := m.apt
	next := aptState{Packages: prev.Packages, CheckedAt: time.Now().Unix(), Error: problem}
	if err != nil {
		next.Error = helperError(err, "apt_list_failed", "Güncellenebilir paketler listelenemedi.")
		slog.Warn("güncellenebilir paketler listelenemedi", "error", err.Error())
	} else {
		next.Packages = updatescheck.ParseSimulation(string(out))
	}
	m.apt = next
	m.mu.Unlock()
	m.store.saveState(areaApt, next.CheckedAt, next)

	if err == nil {
		m.notifyNewPackages(ctx, prev.Packages, next.Packages)
	}
	if next.Error != nil {
		status := http.StatusBadGateway
		if next.Error.Code == "apt_locked" {
			status = http.StatusConflict
		}
		return httpx.NewError(status, next.Error.Code, next.Error.Message)
	}
	return nil
}

func digestKey(prefix string, names []string) string {
	sort.Strings(names)
	sum := sha256.Sum256([]byte(strings.Join(names, "\n")))
	return prefix + hex.EncodeToString(sum[:8])
}

func (m *Module) notifyNewPackages(ctx context.Context, prev, next []updatescheck.Package) {
	old := map[string]bool{}
	for _, p := range prev {
		old[p.Operand()+"="+p.Candidate] = true
	}
	fresh := false
	names := make([]string, 0, len(next))
	security := 0
	for _, p := range next {
		id := p.Operand() + "=" + p.Candidate
		names = append(names, id)
		if !old[id] {
			fresh = true
		}
		if p.Security {
			security++
		}
	}
	if !fresh || len(next) == 0 {
		return
	}
	msg := itoa(len(next)) + " Ubuntu paketi için güncelleme mevcut."
	if security > 0 {
		msg += " Bunların " + itoa(security) + " tanesi güvenlik güncellemesi."
	}
	m.deps.Notify.PublishOnce(ctx, notify.Info, notifySource, "Güncelleme mevcut", msg,
		digestKey("updates.apt.", names), 7*24*time.Hour)
}

func (m *Module) handleAptCheck(w http.ResponseWriter, r *http.Request) error {
	err := m.checkApt(r.Context())
	if isConflict(err) {
		return err
	}
	m.deps.Audit.Log(r.Context(), auth.ActorFrom(r), "updates.apt_check", "", "", err == nil)
	if err != nil {
		return err
	}
	httpx.OK(w, m.aptView(true))
	return nil
}

func (m *Module) handleAptUpgrade(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Mode          string `json:"mode"`
		IncludeKernel bool   `json:"include_kernel"`
		Confirm       bool   `json:"confirm"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	if req.Mode == "" {
		req.Mode = updatescheck.ModeUpgrade
	}
	if req.Mode != updatescheck.ModeUpgrade && req.Mode != updatescheck.ModeFull {
		return httpx.BadRequest("Güncelleme türü geçersiz.")
	}
	if !req.Confirm {
		return httpx.BadRequest("Güncelleme için açık onay gerekiyor.")
	}
	kernel := updatescheck.KernelExclude
	detail := "Standart yükseltme"
	if req.Mode == updatescheck.ModeFull {
		detail = "Tam yükseltme"
	}
	if req.IncludeKernel {
		kernel = updatescheck.KernelInclude
		detail += ", çekirdek dahil"
	} else {
		detail += ", çekirdek hariç"
	}

	if unitActive(r.Context(), updatescheck.SelfUnit) {
		return httpx.Conflict("MyServer güncellemesi sürerken paket güncellemesi başlatılamaz.")
	}

	m.mu.Lock()
	if m.aptChecking {
		m.mu.Unlock()
		return httpx.Conflict("Paket denetimi sürüyor. Bittikten sonra yeniden deneyin.")
	}
	if m.jobs.running(kindApt) != nil {
		m.mu.Unlock()
		return httpx.Conflict("Zaten çalışan bir paket güncellemesi var.")
	}
	actor := auth.ActorFrom(r)
	job := newJob(kindApt, "Ubuntu paket güncellemesi", detail, actor.Username)
	m.jobs.add(job)
	m.mu.Unlock()

	// The helper starts the upgrade in its own systemd unit and returns.
	if _, err := m.deps.Priv.Run(r.Context(), "updates-apt-upgrade", req.Mode, kernel, job.Meta().ID); err != nil {
		msg := privileged.UserMessage(err, "Paket güncellemesi başlatılamadı.")
		m.finishJob(job, msg)
		m.deps.Audit.Log(r.Context(), actor, "updates.apt_upgrade", detail, msg, false)
		return httpx.NewError(http.StatusBadGateway, "apt_upgrade_failed", msg).Wrap(err)
	}
	m.store.saveJob(job.Meta(), "")
	m.deps.Audit.Log(r.Context(), actor, "updates.apt_upgrade", detail, "başlatıldı", true)
	go m.followAptJob(job, actor, 0)
	httpx.JSON(w, http.StatusAccepted, job.Meta())
	return nil
}

func (m *Module) handleReboot(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Hostname string `json:"hostname"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		return err
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		return httpx.NewError(http.StatusInternalServerError, "hostname_unknown", "Sunucu adı okunamadı.")
	}
	if strings.TrimSpace(req.Hostname) != host {
		return httpx.BadRequest("Sunucu adı eşleşmiyor. Yeniden başlatma iptal edildi.")
	}
	if m.jobs.running(kindApt) != nil || unitActive(r.Context(), updatescheck.AptUnit) {
		return httpx.Conflict("Paket güncellemesi sürerken sunucu yeniden başlatılamaz.")
	}
	if unitActive(r.Context(), updatescheck.SelfUnit) {
		return httpx.Conflict("MyServer güncellemesi sürerken sunucu yeniden başlatılamaz.")
	}
	actor := auth.ActorFrom(r)
	if _, err := m.deps.Priv.Run(r.Context(), "updates-reboot", host); err != nil {
		m.deps.Audit.Log(r.Context(), actor, "updates.reboot", host, "başarısız", false)
		return httpx.NewError(http.StatusBadGateway, "reboot_failed",
			privileged.UserMessage(err, "Sunucu yeniden başlatılamadı.")).Wrap(err)
	}
	m.deps.Audit.Log(r.Context(), actor, "updates.reboot", host, "", true)
	httpx.OK(w, map[string]bool{"rebooting": true})
	return nil
}

// isConflict reports whether err only says that another operation is running.
func isConflict(err error) bool {
	var e *httpx.Error
	return errors.As(err, &e) && e.Code == "conflict"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
