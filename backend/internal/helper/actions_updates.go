//go:build linux

package helper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"myserver/internal/updates/updatescheck"
)

const aptLockOpt = "DPkg::Lock::Timeout=60"

// Tools and files of the update actions. They are variables only so that
// the tests can use recording scripts and a temporary directory; the helper
// never changes them.
var (
	aptGetBin       = "/usr/bin/apt-get"
	systemctlBin    = "/usr/bin/systemctl"
	systemdRunBin   = "/usr/bin/systemd-run"
	updatesAptDir   = updatescheck.AptDir
	updatesLogFile  = updatescheck.AptLogFile
	updatesResFile  = updatescheck.AptResultFile
	updatesCgroup   = "/proc/self/cgroup"
	updatesScript   = updatescheck.UpdateScript
	updatesHostname = os.Hostname
)

// capBuffer keeps at most n bytes; apt output is only inspected for
// well-known error phrases and "Inst" lines.
type capBuffer struct {
	buf bytes.Buffer
	n   int
}

func (c *capBuffer) Write(p []byte) (int, error) {
	if room := c.n - c.buf.Len(); room > 0 {
		q := p
		if len(q) > room {
			q = q[:room]
		}
		c.buf.Write(q)
	}
	return len(p), nil
}

// runApt runs apt-get, forwarding output live and returning a copy of it.
// stdout may be nil to suppress forwarding of standard output.
func runApt(ctx context.Context, stdout, stderr io.Writer, args ...string) (string, error) {
	cmd := Command(ctx, aptGetBin, args...)
	cmd.Env = append(append([]string{}, cleanEnv...), "APT_LISTCHANGES_FRONTEND=none")
	out := &capBuffer{n: 4 << 20}
	if stdout != nil {
		cmd.Stdout = io.MultiWriter(stdout, out)
	} else {
		cmd.Stdout = out
	}
	cmd.Stderr = io.MultiWriter(stderr, out)
	err := cmd.Run()
	return out.buf.String(), err
}

// aptFailure converts a failed apt run into a user-facing error when the
// cause is recognised.
func aptFailure(output string, fallback string) error {
	if p, ok := updatescheck.Classify(output); ok {
		return Userf("%s", p.Message)
	}
	if _, statErr := os.Stat(aptGetBin); statErr != nil {
		return Userf("Bu sistemde apt paket yöneticisi bulunamadı.")
	}
	return Userf("%s", fallback)
}

// unitActive reports whether a unit is running, starting or stopping.
// systemctl is-active exits 0 only for "active" (3 for the other states, 4
// for a unit that does not exist), so the printed state decides.
func unitActive(ctx context.Context, unit string) bool {
	out, _ := Command(ctx, systemctlBin, "is-active", unit+".service").Output()
	switch strings.TrimSpace(string(out)) {
	case "active", "activating", "deactivating", "reloading":
		return true
	}
	return false
}

func validUpgradeArgs(mode, kernel string) error {
	if mode != updatescheck.ModeUpgrade && mode != updatescheck.ModeFull {
		return Userf("Güncelleme türü geçersiz.")
	}
	if kernel != updatescheck.KernelInclude && kernel != updatescheck.KernelExclude {
		return Userf("Çekirdek seçeneği geçersiz.")
	}
	return nil
}

func init() {
	Register("updates-apt-refresh", func(ctx context.Context, args []string, _ io.Reader, stdout io.Writer) error {
		if err := ArgCount(args, 0); err != nil {
			return err
		}
		out, err := runApt(ctx, stdout, os.Stderr, "update", "-o", aptLockOpt)
		if err != nil {
			return aptFailure(out, "Paket listeleri yenilenemedi.")
		}
		// apt-get update exits 0 even when some repositories fail.
		if p, ok := updatescheck.Classify(out); ok && p.Code == "apt_network" {
			return Userf("%s", p.Message)
		}
		return nil
	})

	Register("updates-apt-list", func(ctx context.Context, args []string, _ io.Reader, stdout io.Writer) error {
		if err := ArgCount(args, 0); err != nil {
			return err
		}
		out, err := runApt(ctx, stdout, os.Stderr, "--simulate", "dist-upgrade")
		if err != nil {
			return aptFailure(out, "Güncellenebilir paketler listelenemedi.")
		}
		return nil
	})

	// updates-apt-upgrade <upgrade|full> <kernel|no-kernel> <job id>
	// Starts the upgrade in its own transient systemd unit, so that it does
	// not depend on the panel process, and returns immediately.
	Register("updates-apt-upgrade", func(ctx context.Context, args []string, _ io.Reader, stdout io.Writer) error {
		if err := ArgCount(args, 3); err != nil {
			return err
		}
		if err := validUpgradeArgs(args[0], args[1]); err != nil {
			return err
		}
		if !updatescheck.ValidJobID(args[2]) {
			return Userf("İşlem kimliği geçersiz.")
		}
		if unitActive(ctx, updatescheck.AptUnit) {
			return Userf("Zaten çalışan bir paket güncellemesi var.")
		}
		if unitActive(ctx, updatescheck.SelfUnit) {
			return Userf("MyServer güncellemesi sürerken paket güncellemesi başlatılamaz.")
		}
		self, err := os.Executable()
		if err != nil || !strings.HasPrefix(self, "/") {
			return Userf("Yardımcı programın yolu belirlenemedi.")
		}
		if err := prepareAptDir(); err != nil {
			return err
		}
		logFile, err := openRootFile(updatesLogFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
		if err != nil {
			return Userf("Güncelleme günlüğü oluşturulamadı.")
		}
		_ = logFile.Close()
		started := time.Now().Unix()
		if err := writeAptResult(args[2], updatescheck.StateRunning, "", started, 0); err != nil {
			return Userf("Güncelleme durumu kaydedilemedi.")
		}
		// A failed earlier run may have left the unit loaded.
		_ = Command(ctx, systemctlBin, "reset-failed", updatescheck.AptUnit+".service").Run()
		err = Exec(ctx, stdout, systemdRunBin,
			"--unit="+updatescheck.AptUnit, "--collect", "--quiet", "--no-block",
			"--description=MyServer paket güncellemesi",
			"--", self, "updates-apt-run", args[0], args[1], args[2])
		if err != nil {
			_ = writeAptResult(args[2], updatescheck.StateFailed, "Güncelleme işlemi başlatılamadı.", started, time.Now().Unix())
			return Userf("Güncelleme işlemi başlatılamadı.")
		}
		return nil
	})

	// updates-apt-run is the body of the transient unit. It refuses to run
	// anywhere else, so it can never again be a child of the panel.
	Register("updates-apt-run", func(ctx context.Context, args []string, _ io.Reader, _ io.Writer) error {
		if err := ArgCount(args, 3); err != nil {
			return err
		}
		if err := validUpgradeArgs(args[0], args[1]); err != nil {
			return err
		}
		if !updatescheck.ValidJobID(args[2]) {
			return Userf("İşlem kimliği geçersiz.")
		}
		cg, err := os.ReadFile(updatesCgroup)
		if err != nil || !strings.Contains(string(cg), "/"+updatescheck.AptUnit+".service") {
			return Userf("Bu işlem yalnızca güncelleme birimi içinde çalıştırılabilir.")
		}
		if err := prepareAptDir(); err != nil {
			return err
		}
		prev := readAptResult()
		if prev["id"] != args[2] || prev["state"] != updatescheck.StateRunning {
			return Userf("Güncelleme işi kaydı bulunamadı.")
		}
		started, _ := strconv.ParseInt(prev["started"], 10, 64)
		logFile, err := openRootFile(updatesLogFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND)
		if err != nil {
			_ = writeAptResult(args[2], updatescheck.StateFailed, "Güncelleme günlüğü açılamadı.", started, time.Now().Unix())
			return Userf("Güncelleme günlüğü açılamadı.")
		}
		defer logFile.Close()

		fmt.Fprintf(logFile, "Güncelleme başlatıldı (%s, %s).\n", args[0], args[1])
		runErr := aptUpgrade(ctx, logFile, logFile, args[0], args[1] == updatescheck.KernelInclude)
		state, message := updatescheck.StateSuccess, ""
		if runErr != nil {
			state = updatescheck.StateFailed
			message = "Paket güncellemesi başarısız oldu. Ayrıntılar için işlem çıktısına bakın."
			var ue *UserError
			if errors.As(runErr, &ue) {
				message = ue.Message
			}
		}
		_ = logFile.Sync()
		if err := writeAptResult(args[2], state, message, started, time.Now().Unix()); err != nil {
			return err
		}
		return runErr
	})

	Register("updates-reboot", func(ctx context.Context, args []string, _ io.Reader, stdout io.Writer) error {
		if err := ArgCount(args, 1); err != nil {
			return err
		}
		host, err := updatesHostname()
		if err != nil || host == "" || args[0] != host {
			return Userf("Sunucu adı doğrulanamadı; yeniden başlatma iptal edildi.")
		}
		if unitActive(ctx, updatescheck.AptUnit) {
			return Userf("Paket güncellemesi sürerken sunucu yeniden başlatılamaz.")
		}
		if unitActive(ctx, updatescheck.SelfUnit) {
			return Userf("MyServer güncellemesi sürerken sunucu yeniden başlatılamaz.")
		}
		return Exec(ctx, stdout, systemctlBin, "reboot")
	})

	// updates-self <version> <sha256> <tarball url>
	Register("updates-self", func(ctx context.Context, args []string, _ io.Reader, stdout io.Writer) error {
		if err := ArgCount(args, 3); err != nil {
			return err
		}
		version, sum, tarball := args[0], args[1], args[2]
		if !updatescheck.ValidReleaseVersion(version) {
			return Userf("Sürüm numarası geçersiz.")
		}
		if !updatescheck.ValidSHA256(sum) {
			return Userf("Sürüm sağlama toplamı geçersiz.")
		}
		if !updatescheck.ValidUpdateURL(tarball) {
			return Userf("Sürüm arşivinin adresi geçersiz.")
		}
		if err := checkUpdateScript(updatesScript); err != nil {
			return err
		}
		if unitActive(ctx, updatescheck.SelfUnit) {
			return Userf("Bir MyServer güncellemesi zaten çalışıyor.")
		}
		if unitActive(ctx, updatescheck.AptUnit) {
			return Userf("Paket güncellemesi sürerken MyServer güncellenemez.")
		}
		_ = Command(ctx, systemctlBin, "reset-failed", updatescheck.SelfUnit+".service").Run()
		err := Exec(ctx, stdout, systemdRunBin,
			"--unit="+updatescheck.SelfUnit, "--collect", "--quiet", "--no-block",
			"--description=MyServer güncellemesi",
			"--setenv=MYSERVER_UPDATE_VERSION="+version,
			"--setenv=MYSERVER_UPDATE_SHA256="+sum,
			"--setenv=MYSERVER_UPDATE_URL="+tarball,
			"--", updatesScript, version)
		if err != nil {
			return Userf("Güncelleme işlemi başlatılamadı.")
		}
		return nil
	})
}

func rootOwned(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0 && fi.Mode().Perm()&0o022 == 0
}

// prepareAptDir makes sure the job directory exists, is a real directory
// and can be written by root only.
func prepareAptDir() error {
	if err := os.Mkdir(updatesAptDir, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return Userf("Güncelleme dizini oluşturulamadı.")
	}
	fi, err := os.Lstat(updatesAptDir)
	if err != nil || !fi.IsDir() || !rootOwned(fi) {
		return Userf("Güncelleme dizininin sahipliği veya izinleri güvenli değil; işlem başlatılmadı.")
	}
	return nil
}

// openRootFile opens a file in the job directory without following links.
func openRootFile(path string, flags int) (*os.File, error) {
	f, err := os.OpenFile(path, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o644)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, errors.New("not a regular file")
	}
	_ = f.Chmod(0o644)
	return f, nil
}

func readAptResult() map[string]string {
	f, err := os.OpenFile(updatesResFile, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return map[string]string{}
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, 16<<10))
	return updatescheck.ParseKV(string(b))
}

// writeAptResult replaces the result file atomically.
func writeAptResult(id, state, message string, started, finished int64) error {
	tmp := updatesResFile + ".tmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "id=%s\nstate=%s\nstarted=%d\nfinished=%d\nmessage=%s\n",
		id, state, started, finished, updatescheck.OneLine(message))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, updatesResFile)
}

// checkUpdateScript refuses to run a script that is missing or that anyone
// other than root could have modified.
func checkUpdateScript(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return Userf("Güncelleme betiği bulunamadı (%s). MyServer kurulumu eksik olabilir.", path)
	}
	if !fi.Mode().IsRegular() || !rootOwned(fi) || fi.Mode().Perm()&0o100 == 0 {
		return Userf("Güncelleme betiğinin sahipliği veya izinleri güvenli değil; güncelleme başlatılmadı.")
	}
	return nil
}

func aptUpgrade(ctx context.Context, stdout, stderr io.Writer, mode string, withKernel bool) error {
	common := []string{"-y", "-o", aptLockOpt,
		"-o", "Dpkg::Options::=--force-confdef",
		"-o", "Dpkg::Options::=--force-confold"}
	var verb []string
	if mode == updatescheck.ModeFull {
		verb = []string{"dist-upgrade"}
	} else {
		verb = []string{"upgrade", "--with-new-pkgs"}
	}
	const failed = "Paket güncellemesi başarısız oldu. Ayrıntılar için işlem çıktısına bakın."

	if withKernel {
		out, err := runApt(ctx, stdout, stderr, append(append([]string{}, verb...), common...)...)
		if err != nil {
			return aptFailure(out, failed)
		}
		return nil
	}

	// Kernel packages excluded: find what the chosen upgrade would install,
	// drop the kernel packages and upgrade the rest by name.
	sim, err := runApt(ctx, nil, stderr, append([]string{"--simulate"}, verb...)...)
	if err != nil {
		return aptFailure(sim, failed)
	}
	var names, skipped []string
	for _, p := range updatescheck.ParseSimulation(sim) {
		switch {
		case p.Kernel:
			skipped = append(skipped, p.Operand())
		case p.New:
			// Newly installed dependencies are pulled in by apt as needed.
		default:
			names = append(names, p.Operand())
		}
	}
	if len(skipped) > 0 {
		fmt.Fprintf(stdout, "Çekirdek paketleri atlanıyor: %s\n", strings.Join(skipped, " "))
	}
	if len(names) == 0 {
		fmt.Fprintln(stdout, "Yüklenecek (çekirdek dışı) güncelleme yok.")
		return nil
	}
	install := []string{"install", "--only-upgrade"}
	if mode != updatescheck.ModeFull {
		install = append(install, "--no-remove")
	}
	operands := append([]string{"--"}, names...)

	// Verify that the explicit list does not drag a kernel in as a dependency.
	check, err := runApt(ctx, nil, stderr, append(append(append([]string{"--simulate"}, install...), "-o", "Dpkg::Options::=--force-confold"), operands...)...)
	if err != nil {
		return aptFailure(check, failed)
	}
	for _, p := range updatescheck.ParseSimulation(check) {
		if p.Kernel {
			return Userf("Bazı güncellemeler çekirdek paketi (%s) gerektiriyor. Çekirdek güncellemelerini de yükleme seçeneğini işaretleyerek yeniden deneyin.", p.Name)
		}
	}
	out, err := runApt(ctx, stdout, stderr, append(append(install, common...), operands...)...)
	if err != nil {
		return aptFailure(out, failed)
	}
	return nil
}
