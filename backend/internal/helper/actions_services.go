package helper

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"myserver/internal/services/servicescheck"
)

// Variables rather than constants only so that tests can point them at a
// recording fake; nothing else assigns them.
var (
	servicesSystemctl  = "/usr/bin/systemctl"
	servicesJournalctl = "/usr/bin/journalctl"
)

// servicesCanonical resolves aliases (sshd.service -> ssh.service,
// dbus-org.freedesktop.login1.service -> systemd-logind.service) so the
// deny-list cannot be bypassed through another name of the same unit.
func servicesCanonical(ctx context.Context, unit string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := Command(ctx, servicesSystemctl, "show", "--no-pager", "--property=Id", "--value", "--", unit)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	id := strings.TrimSpace(out.String())
	if !servicescheck.ValidUnit(id) {
		return "", Userf("Servis bilgisi okunamadı.")
	}
	return id, nil
}

func init() {
	// services-control <verb> <unit>
	Register("services-control", func(ctx context.Context, args []string, _ io.Reader, stdout io.Writer) error {
		if err := ArgCount(args, 2); err != nil {
			return err
		}
		verb, unit := args[0], args[1]
		if !servicescheck.ValidVerb(verb) {
			return Userf("Bilinmeyen servis işlemi.")
		}
		if !servicescheck.ValidUnit(unit) || servicescheck.IsTemplate(unit) {
			return Userf("Servis adı geçersiz.")
		}
		id, err := servicesCanonical(ctx, unit)
		if err != nil {
			return Userf("Servis bilgisi okunamadı; işlem yapılmadı.")
		}
		if servicescheck.Denied(verb, unit) || servicescheck.Denied(verb, id) {
			return Userf("Bu çekirdek sistem servisi panelden durdurulamaz, devre dışı bırakılamaz veya yeniden başlatılamaz.")
		}

		ctx, cancel := context.WithTimeout(ctx, 95*time.Second)
		defer cancel()
		cmdArgs := []string{"--no-pager", "--no-ask-password"}
		// Stopping or restarting the panel kills this helper's own process
		// tree; queue the job and return instead of waiting for it.
		if id == servicescheck.PanelUnit && (verb == servicescheck.VerbStop || verb == servicescheck.VerbRestart) {
			cmdArgs = append(cmdArgs, "--no-block")
		}
		cmdArgs = append(cmdArgs, verb, "--", unit)
		cmd := Command(ctx, servicesSystemctl, cmdArgs...)
		var errBuf bytes.Buffer
		cmd.Stdout = stdout
		cmd.Stderr = io.MultiWriter(os.Stderr, &errBuf)
		if err := cmd.Run(); err != nil {
			msg := errBuf.String()
			switch {
			case ctx.Err() != nil:
				return Userf("Servis işlemi zaman aşımına uğradı.")
			case strings.Contains(msg, "not found") || strings.Contains(msg, "does not exist"):
				return Userf("Servis bu sunucuda bulunamadı.")
			case strings.Contains(msg, "is masked"):
				return Userf("Servis maskelenmiş durumda; panel maskeyi değiştirmez.")
			case strings.Contains(msg, "not applicable") || strings.Contains(msg, "Job type reload"):
				return Userf("Bu servis yapılandırmayı yeniden yüklemeyi desteklemiyor.")
			case strings.Contains(msg, "refusing") || strings.Contains(msg, "may be requested by dependency only"):
				return Userf("systemd bu servis için elle işlem yapılmasına izin vermiyor.")
			case strings.Contains(msg, "journalctl -xeu") || strings.Contains(msg, "control process exited"):
				return Userf("Servis işlemi başarısız oldu. Ayrıntılar için servis loglarına bakın.")
			}
			return err
		}
		return nil
	})

	// services-logs <unit> <lines>
	Register("services-logs", func(ctx context.Context, args []string, _ io.Reader, stdout io.Writer) error {
		if err := ArgCount(args, 2); err != nil {
			return err
		}
		if !servicescheck.ValidUnit(args[0]) {
			return Userf("Servis adı geçersiz.")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 1 || n > 500 {
			return Userf("Satır sayısı 1 ile 500 arasında olmalıdır.")
		}
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		return Exec(ctx, stdout, servicesJournalctl,
			"--unit="+args[0], "--lines="+strconv.Itoa(n), "--no-pager", "--output=short-iso")
	})

	// services-logs-follow <unit>: follows new entries for a bounded time.
	Register("services-logs-follow", func(ctx context.Context, args []string, _ io.Reader, stdout io.Writer) error {
		if err := ArgCount(args, 1); err != nil {
			return err
		}
		if !servicescheck.ValidUnit(args[0]) {
			return Userf("Servis adı geçersiz.")
		}
		ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGHUP, os.Interrupt)
		defer stop()
		ctx, cancel := context.WithTimeout(ctx, 11*time.Minute)
		defer cancel()
		err := Exec(ctx, stdout, servicesJournalctl,
			"--unit="+args[0], "--follow", "--lines=0", "--no-pager", "--output=short-iso")
		if ctx.Err() != nil {
			return nil
		}
		return err
	})
}
