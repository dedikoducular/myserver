//go:build linux

package helper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"myserver/internal/terminal/termcheck"
)

// terminalPath is the PATH of the login shell; the same one Ubuntu gives a
// regular user.
const terminalPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/games:/usr/local/games:/snap/bin"

const (
	terminalHangupGrace = 3 * time.Second
	terminalSweepGrace  = 2 * time.Second
)

func init() {
	// terminal-shell <username>: runs the login shell of a regular system
	// user on the terminal attached to stdin. The only input is the user
	// name; the program, its arguments and its environment are all derived
	// from the account database.
	Register("terminal-shell", func(_ context.Context, args []string, _ io.Reader, _ io.Writer) error {
		if err := ArgCount(args, 1); err != nil {
			return err
		}
		return terminalShell(args[0])
	})
}

type terminalAccount struct {
	entry  termcheck.Entry
	groups []uint32
	home   string
}

func terminalLookup(name string) (*terminalAccount, error) {
	if !termcheck.ValidUsername(name) {
		return nil, Userf("%s", termcheck.Message(termcheck.BadName))
	}
	passwd, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return nil, fmt.Errorf("read passwd: %w", err)
	}
	entry, reason := termcheck.Lookup(string(passwd), name)
	if reason != termcheck.OK {
		return nil, Userf("%s", termcheck.Message(reason))
	}

	u, err := user.Lookup(name)
	if err != nil {
		return nil, Userf("%s", termcheck.Message(termcheck.NotFound))
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("uid %q: %w", u.Uid, err)
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("gid %q: %w", u.Gid, err)
	}
	// Both views of the account must agree, and the privilege rules are
	// applied to the ids that will actually be used.
	if uint32(uid) != entry.UID || uint32(gid) != entry.GID {
		return nil, Userf("Sistem kullanıcısının kayıtları tutarsız.")
	}
	if uid == 0 {
		return nil, Userf("%s", termcheck.Message(termcheck.IsRoot))
	}
	if uid < termcheck.MinUID {
		return nil, Userf("%s", termcheck.Message(termcheck.SystemAccount))
	}

	ids, err := u.GroupIds()
	if err != nil {
		return nil, fmt.Errorf("group list: %w", err)
	}
	groups := []uint32{uint32(gid)}
	seen := map[uint32]bool{uint32(gid): true}
	for _, s := range ids {
		g, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("group id %q: %w", s, err)
		}
		if !seen[uint32(g)] {
			seen[uint32(g)] = true
			groups = append(groups, uint32(g))
		}
	}

	if err := terminalCheckShell(entry.Shell); err != nil {
		return nil, err
	}

	home := entry.Home
	if fi, err := os.Stat(home); err != nil || !fi.IsDir() {
		home = "/"
	}
	return &terminalAccount{entry: entry, groups: groups, home: home}, nil
}

// terminalCheckShell requires the shell to be a root-owned executable that
// only root can modify and, when /etc/shells exists, to be listed there.
func terminalCheckShell(shell string) error {
	fi, err := os.Stat(shell)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return Userf("Kullanıcının kabuğu (%s) çalıştırılabilir değil.", shell)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 || fi.Mode().Perm()&0o022 != 0 {
		return Userf("Kullanıcının kabuğu (%s) güvenli değil.", shell)
	}
	data, err := os.ReadFile("/etc/shells")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read shells: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == shell {
			return nil
		}
	}
	return Userf("Kullanıcının kabuğu (%s) /etc/shells içinde tanımlı değil.", shell)
}

func terminalShell(name string) error {
	acct, err := terminalLookup(name)
	if err != nil {
		return err
	}

	// stdin must be a terminal opened for reading and writing: it becomes
	// the shell's stdin, stdout and stderr.
	if _, err := unix.IoctlGetTermios(0, unix.TCGETS); err != nil {
		return Userf("Terminal aygıtı bulunamadı.")
	}
	flags, err := unix.FcntlInt(0, unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_RDWR {
		return Userf("Terminal aygıtı kullanılamıyor.")
	}

	// Handle (not ignore) these signals: an ignored signal would stay
	// ignored in the shell, a handled one is reset to its default there.
	sigs := make(chan os.Signal, 16)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT,
		syscall.SIGTSTP, syscall.SIGTTIN, syscall.SIGTTOU)
	defer signal.Stop(sigs)

	syscall.Umask(0o022)

	e := acct.entry
	cmd := exec.Command(e.Shell)
	// A leading "-" makes it a login shell.
	cmd.Args = []string{"-" + filepath.Base(e.Shell)}
	cmd.Env = []string{
		"HOME=" + e.Home,
		"USER=" + e.Name,
		"LOGNAME=" + e.Name,
		"SHELL=" + e.Shell,
		"PATH=" + terminalPath,
		"TERM=xterm-256color",
		"LANG=C.UTF-8",
	}
	cmd.Dir = acct.home
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdin
	cmd.Stderr = os.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// The child calls setgroups, then setgid, then setuid, before exec.
		Credential: &syscall.Credential{Uid: e.UID, Gid: e.GID, Groups: acct.groups},
		// Own process group, made the foreground group of the controlling
		// terminal (fd 0) so the shell has job control and receives
		// window-size and hangup signals.
		Setpgid:    true,
		Foreground: true,
		Ctty:       0,
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start shell: %w", err)
	}
	pgid := cmd.Process.Pid
	fmt.Fprintln(os.Stderr, termcheck.StartedMarker)

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	hangup := make(chan struct{})
	go func() {
		fds := []unix.PollFd{{Fd: 0}}
		for {
			select {
			case <-done:
				return
			default:
			}
			fds[0].Revents = 0
			n, err := unix.Poll(fds, 1000)
			if err != nil && !errors.Is(err, unix.EINTR) {
				close(hangup)
				return
			}
			if n > 0 && fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
				close(hangup)
				return
			}
		}
	}()

	terminate := false
wait:
	for {
		select {
		case <-done:
			break wait
		case <-hangup:
			terminate = true
			break wait
		case s := <-sigs:
			switch s {
			case syscall.SIGTSTP, syscall.SIGTTIN, syscall.SIGTTOU:
				// Job-control signals are not for the helper.
			default:
				terminate = true
				break wait
			}
		}
	}

	if terminate {
		_ = syscall.Kill(-pgid, syscall.SIGHUP)
		_ = syscall.Kill(-pgid, syscall.SIGCONT)
		terminalSweep(syscall.SIGHUP)
		select {
		case <-done:
		case <-time.After(terminalHangupGrace):
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			terminalSweep(syscall.SIGKILL)
			<-done
		}
	}
	// The shell is gone and reaped. Whatever it left behind in this
	// session (background jobs) is ended too, so closing a web terminal
	// never leaves processes attached to a dead terminal.
	if terminalSweep(syscall.SIGHUP) > 0 {
		time.Sleep(terminalSweepGrace)
		terminalSweep(syscall.SIGKILL)
	}

	code := 0
	if ps := cmd.ProcessState; ps != nil {
		if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			code = 128 + int(ws.Signal())
		} else {
			code = ps.ExitCode()
		}
	}
	os.Exit(code)
	return nil
}

// terminalSweep signals every process of the helper's own session except the
// helper and its ancestors (sudo), and returns how many it signalled. The
// session holds nothing but the shell and its descendants.
func terminalSweep(sig syscall.Signal) int {
	self := os.Getpid()
	sid, err := unix.Getsid(0)
	if err != nil {
		return 0
	}
	skip := map[int]bool{self: true, 1: true}
	for pid, i := os.Getppid(), 0; pid > 1 && i < 64; i++ {
		skip[pid] = true
		st, ok := terminalStat(pid)
		if !ok {
			break
		}
		pid = st.PPID
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	n := 0
	for _, de := range entries {
		pid, err := strconv.Atoi(de.Name())
		if err != nil || skip[pid] {
			continue
		}
		st, ok := terminalStat(pid)
		if !ok || st.Session != sid {
			continue
		}
		if err := syscall.Kill(pid, sig); err == nil {
			n++
			if sig == syscall.SIGHUP {
				_ = syscall.Kill(pid, syscall.SIGCONT)
			}
		}
	}
	return n
}

func terminalStat(pid int) (termcheck.ProcStat, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return termcheck.ProcStat{}, false
	}
	return termcheck.ParseStat(string(data))
}
