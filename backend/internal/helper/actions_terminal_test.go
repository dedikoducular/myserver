//go:build linux

package helper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"myserver/internal/privileged"
	"myserver/internal/terminal/termcheck"
)

// The tests use the accounts of the machine they run on. They are written
// for a stock ubuntu:24.04 container: root, the usual system accounts and
// the regular user "ubuntu" (uid 1000, /bin/bash).
const terminalTestUser = "ubuntu"

const terminalChildEnv = "MYSERVER_TEST_TERMINAL_CHILD"

func terminalRequireUser(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		t.Skipf("no account database: %v", err)
	}
	if _, reason := termcheck.Lookup(string(data), terminalTestUser); reason != termcheck.OK {
		t.Skipf("this machine has no regular user %q (%s)", terminalTestUser, reason)
	}
}

func terminalRequireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root, like the helper itself")
	}
}

func terminalStdinIsTTY() bool {
	_, err := unix.IoctlGetTermios(0, unix.TCGETS)
	return err == nil
}

func terminalAction(t *testing.T) Action {
	t.Helper()
	a, ok := actions["terminal-shell"]
	if !ok {
		t.Fatal("action terminal-shell is not registered")
	}
	return a
}

func TestTerminalActionArgumentCount(t *testing.T) {
	a := terminalAction(t)
	for _, args := range [][]string{
		nil, {}, {terminalTestUser, terminalTestUser}, {terminalTestUser, "-c", "id"},
		{"--", terminalTestUser}, {terminalTestUser, ""},
	} {
		var out bytes.Buffer
		err := a(context.Background(), args, strings.NewReader(""), &out)
		if err == nil {
			t.Errorf("arguments %q were accepted", args)
			continue
		}
		var ue *UserError
		if errors.As(err, &ue) {
			t.Errorf("arguments %q: a caller error is reported as a user message: %q", args, ue.Message)
		}
		if out.Len() != 0 {
			t.Errorf("arguments %q produced output %q", args, out.String())
		}
	}
}

func TestTerminalActionRejectsInvalidNames(t *testing.T) {
	a := terminalAction(t)
	names := []string{
		"", " ", "Ubuntu", "UBUNTU", "ubuntu ", " ubuntu", "ubuntu\n", "ubuntu\x00",
		"-u", "--login", "-", "0", "1000", "ubuntu;id", "ubuntu&&id", "$(id)", "`id`",
		"../../bin/sh", "/bin/sh", "ubuntu/..", "a:b", "ubuntu:x:0:0", "a b", "a\tb", "ü",
		strings.Repeat("a", 33), strings.Repeat("a", 100000),
	}
	for _, name := range names {
		err := a(context.Background(), []string{name}, strings.NewReader(""), io.Discard)
		var ue *UserError
		if !errors.As(err, &ue) {
			t.Errorf("name %q: error = %v, want a user error", name, err)
			continue
		}
		if ue.Message != termcheck.Message(termcheck.BadName) {
			t.Errorf("name %q: message = %q, want %q", name, ue.Message, termcheck.Message(termcheck.BadName))
		}
	}
}

func TestTerminalActionRejectsIneligibleAccounts(t *testing.T) {
	terminalRequireUser(t)
	a := terminalAction(t)
	cases := map[string]termcheck.Reason{
		"root":       termcheck.IsRoot,
		"daemon":     termcheck.SystemAccount,
		"bin":        termcheck.SystemAccount,
		"sync":       termcheck.SystemAccount,
		"www-data":   termcheck.SystemAccount,
		"_apt":       termcheck.SystemAccount,
		"nobody":     termcheck.SystemAccount,
		"nosuchuser": termcheck.NotFound,
		"ubunt":      termcheck.NotFound,
		"ubuntu1":    termcheck.NotFound,
	}
	for name, reason := range cases {
		err := a(context.Background(), []string{name}, strings.NewReader(""), io.Discard)
		var ue *UserError
		if !errors.As(err, &ue) {
			t.Errorf("account %q: error = %v, want a user error", name, err)
			continue
		}
		if ue.Message != termcheck.Message(reason) {
			t.Errorf("account %q: message = %q, want %q", name, ue.Message, termcheck.Message(reason))
		}
	}
}

// Without a terminal on stdin the action refuses even an eligible account.
func TestTerminalActionNeedsTerminal(t *testing.T) {
	terminalRequireUser(t)
	terminalRequireRoot(t)
	if terminalStdinIsTTY() {
		t.Skip("stdin is a terminal: the action would start a shell on it")
	}
	err := terminalAction(t)(context.Background(), []string{terminalTestUser}, strings.NewReader(""), io.Discard)
	var ue *UserError
	if !errors.As(err, &ue) {
		t.Fatalf("error = %v, want a user error", err)
	}
	if ue.Message != "Terminal aygıtı bulunamadı." {
		t.Errorf("message = %q", ue.Message)
	}
}

func TestTerminalLookupAccount(t *testing.T) {
	terminalRequireUser(t)
	acct, err := terminalLookup(terminalTestUser)
	if err != nil {
		t.Fatalf("terminalLookup: %v", err)
	}
	if acct.entry.Name != terminalTestUser || acct.entry.UID < termcheck.MinUID || acct.entry.UID == 0 {
		t.Errorf("entry = %+v", acct.entry)
	}
	if len(acct.groups) == 0 || acct.groups[0] != acct.entry.GID {
		t.Errorf("groups = %v, want the primary group %d first", acct.groups, acct.entry.GID)
	}
	seen := map[uint32]bool{}
	for _, g := range acct.groups {
		if g == 0 {
			t.Errorf("groups %v contain the root group", acct.groups)
		}
		if seen[g] {
			t.Errorf("groups %v contain %d twice", acct.groups, g)
		}
		seen[g] = true
	}
	if fi, err := os.Stat(acct.home); err != nil || !fi.IsDir() {
		t.Errorf("working directory %q is not a directory", acct.home)
	}
}

func TestTerminalCheckShell(t *testing.T) {
	terminalRequireRoot(t)
	shells, err := os.ReadFile("/etc/shells")
	if err != nil {
		t.Skipf("no /etc/shells: %v", err)
	}
	if !strings.Contains(string(shells), "\n/bin/bash\n") {
		t.Skip("/bin/bash is not listed in /etc/shells")
	}
	if err := terminalCheckShell("/bin/bash"); err != nil {
		t.Errorf("/bin/bash refused: %v", err)
	}

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bash, err := os.ReadFile("/bin/bash")
	if err != nil {
		t.Fatal(err)
	}
	make := func(name string, mode os.FileMode, uid int) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, bash, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(p, uid, 0); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	refused := map[string]string{
		"not listed in /etc/shells": make("unlisted", 0o755, 0),
		"not executable":            make("noexec", 0o644, 0),
		"group writable":            make("groupw", 0o775, 0),
		"world writable":            make("worldw", 0o757, 0),
		"owned by a user":           make("userowned", 0o755, 1000),
		"a directory":               dir,
		"missing":                   filepath.Join(dir, "missing"),
		"a comment of /etc/shells":  "# /etc/shells: valid login shells",
		"empty":                     "",
	}
	for what, shell := range refused {
		err := terminalCheckShell(shell)
		var ue *UserError
		if !errors.As(err, &ue) {
			t.Errorf("shell %s (%q): error = %v, want a user error", what, shell, err)
		}
	}
}

// TestTerminalHelperProcess is not a test: run by the tests below as a child
// process, it is the helper executing "terminal-shell <user>" the way
// helper.Main does.
func TestTerminalHelperProcess(t *testing.T) {
	name := os.Getenv(terminalChildEnv)
	if name == "" {
		return
	}
	err := terminalAction(t)(context.Background(), []string{name}, os.Stdin, os.Stdout)
	var ue *UserError
	if errors.As(err, &ue) {
		fmt.Fprintln(os.Stderr, privileged.UserMessagePrefix+ue.Message)
		os.Exit(privileged.ExitUser)
	}
	fmt.Fprintf(os.Stderr, "terminal-shell: %v\n", err)
	os.Exit(1)
}

type terminalLockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *terminalLockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *terminalLockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type terminalChild struct {
	t      *testing.T
	cmd    *exec.Cmd
	ptmx   *os.File
	stderr *terminalLockedBuffer
	out    chan []byte
	buf    string
	done   chan error
}

func terminalStart(t *testing.T, user string) *terminalChild {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestTerminalHelperProcess$")
	cmd.Env = []string{
		"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "LANG=C",
		terminalChildEnv + "=" + user,
		"MYSERVER_TEST_SECRET=must-not-reach-the-shell",
	}
	stderr := &terminalLockedBuffer{}
	cmd.Stderr = stderr
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 132, Rows: 43})
	if err != nil {
		t.Fatalf("start helper on a pseudo-terminal: %v", err)
	}
	// As the panel does: a master in non-blocking mode, so that closing it
	// takes effect while a read is waiting.
	fd, err := unix.FcntlInt(master.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	ptmx := os.NewFile(uintptr(fd), master.Name())
	master.Close()
	c := &terminalChild{t: t, cmd: cmd, ptmx: ptmx, stderr: stderr,
		out: make(chan []byte, 4096), done: make(chan error, 1)}
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				c.out <- append([]byte(nil), buf[:n]...)
			}
			if err != nil {
				close(c.out)
				return
			}
		}
	}()
	go func() { c.done <- cmd.Wait() }()
	t.Cleanup(func() {
		ptmx.Close()
		select {
		case <-c.done:
		case <-time.After(15 * time.Second):
			cmd.Process.Kill()
		}
	})
	return c
}

func (c *terminalChild) waitOutput(want string) string {
	c.t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		if i := strings.Index(c.buf, want); i >= 0 {
			got := c.buf[:i]
			c.buf = c.buf[i+len(want):]
			return got
		}
		select {
		case b, ok := <-c.out:
			if !ok {
				c.t.Fatalf("terminal closed before %q; output %q; helper stderr %q", want, c.buf, c.stderr.String())
			}
			c.buf += string(b)
		case <-deadline:
			c.t.Fatalf("no %q in terminal output %q; helper stderr %q", want, c.buf, c.stderr.String())
		}
	}
}

// run executes a command in the shell and returns what it printed.
func (c *terminalChild) run(command string) string {
	c.t.Helper()
	if _, err := c.ptmx.WriteString("echo B$((1))B; " + command + "; echo E$((2))E\n"); err != nil {
		c.t.Fatalf("write to terminal: %v", err)
	}
	c.waitOutput("B1B")
	return strings.TrimSpace(strings.ReplaceAll(c.waitOutput("E2E"), "\r", ""))
}

func (c *terminalChild) wait() int {
	c.t.Helper()
	select {
	case err := <-c.done:
		c.done <- err
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		if err != nil {
			c.t.Fatalf("wait: %v", err)
		}
		return 0
	case <-time.After(20 * time.Second):
		c.t.Fatalf("the helper did not exit; helper stderr %q", c.stderr.String())
		return -1
	}
}

// terminalProcessAlive reports whether pid exists and is not a zombie.
func terminalProcessAlive(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	return i >= 0 && !strings.HasPrefix(strings.TrimSpace(s[i+1:]), "Z")
}

func terminalWaitGone(t *testing.T, what string, pid int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for terminalProcessAlive(pid) {
		if time.Now().After(deadline) {
			cmdline, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
			t.Fatalf("%s (pid %d, %q) is still running", what, pid, bytes.ReplaceAll(cmdline, []byte{0}, []byte{' '}))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func terminalPid(t *testing.T, s string) int {
	t.Helper()
	// Job control prints "[1] 64" before the command's own output.
	lines := strings.Split(strings.TrimSpace(s), "\n")
	pid, err := strconv.Atoi(strings.TrimSpace(lines[len(lines)-1]))
	if err != nil || pid <= 1 {
		t.Fatalf("not a pid: %q", s)
	}
	return pid
}

func TestTerminalShellEndToEnd(t *testing.T) {
	terminalRequireRoot(t)
	terminalRequireUser(t)
	c := terminalStart(t, terminalTestUser)

	for command, want := range map[string]string{
		"id -u":                        "1000",
		"id -g":                        "1000",
		"id -un":                       terminalTestUser,
		"stty size":                    "43 132",
		"echo $0":                      "-bash",
		"echo $HOME":                   "/home/ubuntu",
		"pwd":                          "/home/ubuntu",
		"echo $USER":                   terminalTestUser,
		"echo $LOGNAME":                terminalTestUser,
		"echo $SHELL":                  "/bin/bash",
		"echo $TERM":                   "xterm-256color",
		"umask":                        "0022",
		"env | grep -c MYSERVER; true": "0",
		"env | grep -c SUDO; true":     "0",
		// The shell holds the terminal and nothing else (ls itself
		// holds 3 for the directory).
		"ls /proc/self/fd | tr '\\n' ' '": "0 1 2 3",
		// No privilege is left to return to.
		"grep -E '^(Uid|Gid):' /proc/self/status | tr -s '\\t\\n' ' '": "Uid: 1000 1000 1000 1000 Gid: 1000 1000 1000 1000",
		"grep -E '^CapEff:' /proc/self/status | tr -s '\\t' ' '":       "CapEff: 0000000000000000",
	} {
		if got := c.run(command); got != want {
			t.Errorf("%s = %q, want %q", command, got, want)
		}
	}
	if got := c.run("id -G"); strings.Contains(" "+got+" ", " 0 ") {
		t.Errorf("groups %q contain the root group", got)
	}
	if got := c.run("tty"); !strings.HasPrefix(got, "/dev/pts/") {
		t.Errorf("tty = %q, want a pseudo-terminal", got)
	}
	// Job control works: the shell is the foreground group of its terminal.
	if got := c.run("ps -o pgid= -o tpgid= -p $$ | tr -s ' '"); got != "" {
		f := strings.Fields(got)
		if len(f) != 2 || f[0] == "" {
			t.Errorf("ps output = %q", got)
		}
	}
	sc, err := c.ptmx.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var sizeErr error
	if err := sc.Control(func(fd uintptr) {
		sizeErr = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Col: 100, Row: 30})
	}); err != nil || sizeErr != nil {
		t.Fatal(err, sizeErr)
	}
	if got := c.run("stty size"); got != "30 100" {
		t.Errorf("size after resize = %q, want \"30 100\"", got)
	}
	if !strings.Contains(c.stderr.String(), termcheck.StartedMarker) {
		t.Errorf("helper stderr %q lacks the start marker", c.stderr.String())
	}

	if _, err := c.ptmx.WriteString("exit 5\n"); err != nil {
		t.Fatal(err)
	}
	if code := c.wait(); code != 5 {
		t.Errorf("helper exit code = %d, want the shell's 5; stderr %q", code, c.stderr.String())
	}
	if s := c.stderr.String(); strings.Contains(s, privileged.UserMessagePrefix) {
		t.Errorf("helper reported an error: %q", s)
	}
}

func TestTerminalShellHangup(t *testing.T) {
	terminalRequireRoot(t)
	terminalRequireUser(t)
	c := terminalStart(t, terminalTestUser)

	shell := terminalPid(t, c.run("echo $$"))
	// A background job, one that ignores the hangup, and a foreground
	// program: none may outlive the terminal.
	background := terminalPid(t, c.run("sleep 300 & echo $!"))
	stubborn := terminalPid(t, c.run("(trap '' HUP TERM INT; exec sleep 301) & echo $!"))
	detached := terminalPid(t, c.run("nohup sleep 302 >/dev/null 2>&1 & echo $!"))
	if _, err := c.ptmx.WriteString("sleep 303\n"); err != nil {
		t.Fatal(err)
	}
	c.waitOutput("sleep 303")
	for what, pid := range map[string]int{"shell": shell, "background job": background, "stubborn job": stubborn, "nohup job": detached} {
		if !terminalProcessAlive(pid) {
			t.Fatalf("%s (pid %d) is not running before the hangup", what, pid)
		}
	}

	start := time.Now()
	c.ptmx.Close()
	c.wait()
	if d := time.Since(start); d > 12*time.Second {
		t.Errorf("the helper took %v to end after the hangup", d)
	}
	terminalWaitGone(t, "shell", shell)
	terminalWaitGone(t, "background job", background)
	terminalWaitGone(t, "job ignoring SIGHUP", stubborn)
	terminalWaitGone(t, "nohup job", detached)
}

func TestTerminalShellEndsOnSignal(t *testing.T) {
	terminalRequireRoot(t)
	terminalRequireUser(t)
	c := terminalStart(t, terminalTestUser)
	shell := terminalPid(t, c.run("echo $$"))
	background := terminalPid(t, c.run("sleep 300 & echo $!"))
	if err := c.cmd.Process.Signal(unix.SIGTERM); err != nil {
		t.Fatal(err)
	}
	c.wait()
	terminalWaitGone(t, "shell", shell)
	terminalWaitGone(t, "background job", background)
}

func TestTerminalShellRefusedAccounts(t *testing.T) {
	terminalRequireRoot(t)
	for user, want := range map[string]string{
		"root":       termcheck.Message(termcheck.IsRoot),
		"nobody":     termcheck.Message(termcheck.SystemAccount),
		"nosuchuser": termcheck.Message(termcheck.NotFound),
		"-u":         termcheck.Message(termcheck.BadName),
	} {
		c := terminalStart(t, user)
		if code := c.wait(); code != privileged.ExitUser {
			t.Errorf("user %q: exit code %d, want %d", user, code, privileged.ExitUser)
		}
		stderr := c.stderr.String()
		if !strings.Contains(stderr, privileged.UserMessagePrefix+want) {
			t.Errorf("user %q: stderr %q, want message %q", user, stderr, want)
		}
		if strings.Contains(stderr, termcheck.StartedMarker) {
			t.Errorf("user %q: the helper announced a shell", user)
		}
	}
}
