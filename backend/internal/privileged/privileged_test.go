package privileged

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var ctx = context.Background()

func TestInvalidActionNamesAreRejected(t *testing.T) {
	r := New(filepath.Join(t.TempDir(), "no-such-helper"))
	bad := []string{
		"", "a", "A", "Ping", "PING", "1abc", "-abc", "--help", "-n", "storage_mount", "storage mount", "storage.mount",
		"storage/mount", "../helper", "ping;id", "ping&&id", "ping|id", "$(id)", "`id`", "ping\n", "ping\x00", "ping\tx",
		"ping ", " ping", "şifre", "list-actions\n", strings.Repeat("a", 42),
	}
	for _, name := range bad {
		if cmd, err := r.Command(ctx, name); err == nil || cmd != nil {
			t.Errorf("action %q accepted", name)
		}
		// None of the entry points may start a process for such a name.
		if _, err := r.Run(ctx, name); err == nil || isHelperError(err) {
			t.Errorf("Run(%q): %v, want a validation error", name, err)
		}
		if _, err := r.RunInput(ctx, strings.NewReader("x"), name); err == nil || isHelperError(err) {
			t.Errorf("RunInput(%q): %v, want a validation error", name, err)
		}
		if err := r.Stream(ctx, nil, nil, name); err == nil || isHelperError(err) {
			t.Errorf("Stream(%q): %v, want a validation error", name, err)
		}
	}
	for _, name := range []string{"ping", "hostname-set", "storage-mount", "a1", "x-2-y", strings.Repeat("a", 41)} {
		if _, err := r.Command(ctx, name); err != nil {
			t.Errorf("action %q refused: %v", name, err)
		}
	}
}

// isHelperError reports whether a process was started (or attempted).
func isHelperError(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

func TestNULBytesInArgumentsAreRejected(t *testing.T) {
	r := New(filepath.Join(t.TempDir(), "no-such-helper"))
	for _, args := range [][]string{{"\x00"}, {"a\x00b"}, {"ok", "bad\x00"}, {"/dev/sdb1\x00/etc/shadow"}} {
		if cmd, err := r.Command(ctx, "storage-mount", args...); err == nil || cmd != nil {
			t.Errorf("arguments %q accepted", args)
		}
		if _, err := r.Run(ctx, "storage-mount", args...); err == nil || isHelperError(err) {
			t.Errorf("Run with %q: %v, want a validation error", args, err)
		}
		if err := r.Stream(ctx, nil, nil, "storage-mount", args...); err == nil || isHelperError(err) {
			t.Errorf("Stream with %q: %v, want a validation error", args, err)
		}
	}
}

func TestCommandLine(t *testing.T) {
	helper := filepath.Join(t.TempDir(), "myserver-helper")
	r := New(helper)
	t.Setenv("MYSERVER_TEST_LEAK", "gizli")
	t.Setenv("LD_PRELOAD", "/tmp/evil.so")
	hostile := []string{"--help", "-rf", "; reboot", "$(id)", "a b", "", "*"}
	cmd, err := r.Command(ctx, "storage-mount", hostile...)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	if os.Geteuid() == 0 {
		want = append([]string{helper, "storage-mount"}, hostile...)
	} else {
		want = append([]string{"/usr/bin/sudo", "-n", "--", helper, "storage-mount"}, hostile...)
		if cmd.Path != "/usr/bin/sudo" {
			t.Errorf("sudo is resolved as %q, want the absolute path", cmd.Path)
		}
	}
	if len(cmd.Args) != len(want) {
		t.Fatalf("command line %q, want %q", cmd.Args, want)
	}
	for i := range want {
		if cmd.Args[i] != want[i] {
			t.Fatalf("argument %d is %q, want %q (each value must stay one separate argument)", i, cmd.Args[i], want[i])
		}
	}
	for _, a := range cmd.Args {
		if a == "sh" || a == "/bin/sh" || a == "bash" || a == "-c" {
			t.Fatalf("a shell is involved: %q", cmd.Args)
		}
	}
	// The child gets a fixed environment, nothing inherited.
	if len(cmd.Env) == 0 {
		t.Fatal("the environment is inherited from the panel process")
	}
	for _, e := range cmd.Env {
		k, _, _ := strings.Cut(e, "=")
		switch k {
		case "PATH", "LC_ALL", "LANG":
		default:
			t.Errorf("unexpected environment variable %q", e)
		}
		if strings.Contains(e, "gizli") || strings.Contains(e, "evil") {
			t.Errorf("inherited value in the environment: %q", e)
		}
	}
}

func TestUserMessageOnlyForUserExitCode(t *testing.T) {
	const fallback = "Disk bağlanamadı."
	const helperMsg = "Disk zaten bağlı."
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"user error", &Error{Action: "x", ExitCode: ExitUser, Message: helperMsg}, helperMsg},
		{"wrapped user error", fmt.Errorf("mount: %w", &Error{Action: "x", ExitCode: ExitUser, Message: helperMsg}), helperMsg},
		{"user error without message", &Error{Action: "x", ExitCode: ExitUser}, fallback},
		{"internal failure", &Error{Action: "x", ExitCode: 1, Message: "mount: /dev/sdb1: permission denied"}, fallback},
		{"usage failure", &Error{Action: "x", ExitCode: 2, Message: `unknown action "x"`}, fallback},
		{"not started", &Error{Action: "x", ExitCode: -1, Message: "fork/exec /usr/bin/sudo: no such file"}, fallback},
		{"killed", &Error{Action: "x", ExitCode: 137, Message: "signal: killed"}, fallback},
		{"success code with message", &Error{Action: "x", ExitCode: 0, Message: "x"}, fallback},
		{"other error", errors.New("context deadline exceeded"), fallback},
		{"nil", nil, fallback},
	}
	for _, c := range cases {
		if got := UserMessage(c.err, fallback); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestToErrorParsesStderr(t *testing.T) {
	plain := errors.New("exit status 3")
	e := toError("storage-mount", plain, "mount: warning\n  "+UserMessagePrefix+"Disk zaten bağlı.  \ntrailing noise\n").(*Error)
	if e.Message != "Disk zaten bağlı." {
		t.Errorf("message %q", e.Message)
	}
	if e.Action != "storage-mount" {
		t.Errorf("action %q", e.Action)
	}
	if e.ExitCode != -1 {
		t.Errorf("exit code %d for an error that is not an exit status", e.ExitCode)
	}
	// The last marked line wins.
	e = toError("x", plain, UserMessagePrefix+"bir\n"+UserMessagePrefix+"iki\n").(*Error)
	if e.Message != "iki" {
		t.Errorf("message %q", e.Message)
	}
	// Without a marker the raw text is kept for the log only.
	e = toError("x", plain, "  mount: bad superblock\n").(*Error)
	if e.Message != "mount: bad superblock" {
		t.Errorf("message %q", e.Message)
	}
	if got := UserMessage(e, "yedek"); got != "yedek" {
		t.Errorf("raw stderr reached the user: %q", got)
	}
	e = toError("x", plain, "").(*Error)
	if e.Message != "exit status 3" {
		t.Errorf("message %q", e.Message)
	}
	e = toError("x", plain, strings.Repeat("a", 5000)+"SON").(*Error)
	if len(e.Message) != 2000 || !strings.HasSuffix(e.Message, "SON") {
		t.Errorf("long stderr kept as %d bytes", len(e.Message))
	}
	// The marker must start the line; text that merely contains it is not
	// a user message.
	e = toError("x", plain, "cat: file says "+UserMessagePrefix+"sahte\n").(*Error)
	if e.Message == "sahte" {
		t.Errorf("marker inside a line was accepted")
	}
}

func TestLimitedWriter(t *testing.T) {
	var sb strings.Builder
	w := &limitedWriter{w: &sb, n: 10}
	for _, chunk := range []string{"12345", "6789012345", "more"} {
		n, err := w.Write([]byte(chunk))
		if err != nil || n != len(chunk) {
			t.Fatalf("Write(%q) = %d, %v; a short write would kill the child process", chunk, n, err)
		}
	}
	if sb.String() != "1234567890" {
		t.Fatalf("kept %q", sb.String())
	}
}

func TestMissingHelperIsAnError(t *testing.T) {
	r := New(filepath.Join(t.TempDir(), "no-such-helper"))
	if err := r.Available(ctx); err == nil {
		t.Fatal("a missing helper is reported as available")
	}
	out, err := r.Run(ctx, "ping")
	if err == nil {
		t.Fatalf("Run succeeded without a helper: %q", out)
	}
	if got := UserMessage(err, "yedek"); got != "yedek" {
		t.Fatalf("internal failure text reached the user: %q", got)
	}
}

// fakeHelper writes a script that stands in for the helper binary. It is
// only used when the test runs as root (in a container), where the runner
// executes the helper directly instead of through sudo.
func fakeHelper(t *testing.T) *Runner {
	t.Helper()
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("needs Linux and uid 0 (container): otherwise the runner goes through sudo")
	}
	path := filepath.Join(t.TempDir(), "fake-helper")
	script := `#!/bin/sh
action="$1"; shift
case "$action" in
  echo-args) for a in "$@"; do printf '[%s]\n' "$a"; done ;;
  show-env) env ;;
  read-stdin) cat ;;
  user-fail) echo "some tool: noise" >&2; echo "MYSERVER_ERROR: Disk zaten bağlı." >&2; exit 3 ;;
  internal-fail) echo "MYSERVER_ERROR: bu mesaj gösterilmemeli" >&2; echo "/etc/shadow: permission denied" >&2; exit 1 ;;
  noisy-fail) head -c 200000 /dev/zero | tr '\0' 'x' >&2; exit 1 ;;
  out-and-err) echo visible; echo hidden >&2 ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return New(path)
}

func TestRunPassesArgumentsLiterally(t *testing.T) {
	r := fakeHelper(t)
	marker := filepath.Join(t.TempDir(), "created-by-injection")
	args := []string{"a b", "$(touch " + marker + ")", "`touch " + marker + "`", "; touch " + marker, "| touch " + marker,
		"&& touch " + marker, "*", "--", "-n", "", "çğş", "new\nline"}
	out, err := r.Run(ctx, "echo-args", args...)
	if err != nil {
		t.Fatal(err)
	}
	var want strings.Builder
	for _, a := range args {
		want.WriteString("[" + a + "]\n")
	}
	if string(out) != want.String() {
		t.Fatalf("helper received\n%s\nwant\n%s", out, want.String())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("an argument was interpreted by a shell")
	}
}

func TestRunEnvironmentIsClean(t *testing.T) {
	r := fakeHelper(t)
	t.Setenv("MYSERVER_TEST_LEAK", "gizli-deger")
	t.Setenv("LD_PRELOAD", "")
	out, err := r.Run(ctx, "show-env")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "gizli-deger") || strings.Contains(string(out), "MYSERVER_TEST_LEAK") {
		t.Fatalf("the panel's environment reached the helper:\n%s", out)
	}
	if !strings.Contains(string(out), "PATH=/usr/sbin:/usr/bin:/sbin:/bin") {
		t.Fatalf("PATH is not the fixed one:\n%s", out)
	}
}

func TestRunErrors(t *testing.T) {
	r := fakeHelper(t)
	_, err := r.Run(ctx, "user-fail")
	var e *Error
	if !errors.As(err, &e) || e.ExitCode != ExitUser || e.Message != "Disk zaten bağlı." {
		t.Fatalf("user failure: %+v", err)
	}
	if got := UserMessage(err, "yedek"); got != "Disk zaten bağlı." {
		t.Fatalf("user message %q", got)
	}

	_, err = r.Run(ctx, "internal-fail")
	if !errors.As(err, &e) || e.ExitCode != 1 {
		t.Fatalf("internal failure: %+v", err)
	}
	if got := UserMessage(err, "yedek"); got != "yedek" {
		t.Fatalf("message of a failure with exit code 1 reached the user: %q", got)
	}

	_, err = r.Run(ctx, "noisy-fail")
	if !errors.As(err, &e) || len(e.Message) > 2000 {
		t.Fatalf("noisy failure kept %d bytes", len(e.Message))
	}

	out, err := r.Run(ctx, "out-and-err")
	if err != nil || string(out) != "visible\n" {
		t.Fatalf("stdout %q err %v", out, err)
	}
	out, err = r.RunInput(ctx, strings.NewReader("girdi"), "read-stdin")
	if err != nil || string(out) != "girdi" {
		t.Fatalf("stdin round trip: %q %v", out, err)
	}
	var so, se strings.Builder
	if err := r.Stream(ctx, &so, &se, "out-and-err"); err != nil || so.String() != "visible\n" || se.String() != "hidden\n" {
		t.Fatalf("stream: %q %q %v", so.String(), se.String(), err)
	}
	if err := r.Stream(ctx, &so, nil, "user-fail"); UserMessage(err, "yedek") != "Disk zaten bağlı." {
		t.Fatalf("stream user failure: %v", err)
	}
}

func TestRunHonoursCancellation(t *testing.T) {
	r := fakeHelper(t)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := r.Run(cctx, "echo-args", "x"); err == nil {
		t.Fatal("the helper ran although the request was cancelled")
	}
}
