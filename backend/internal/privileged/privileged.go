// Package privileged is the only path from the unprivileged panel process
// to root. It runs the fixed helper binary through sudo with an action name
// and separate arguments; nothing is ever interpreted by a shell. The helper
// re-validates every argument, so a compromised panel process is still
// limited to the helper's action list.
package privileged

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Error is a failed helper action. Message is the helper's Turkish,
// user-presentable explanation when it provided one.
type Error struct {
	Action   string
	ExitCode int
	Message  string
}

func (e *Error) Error() string {
	return fmt.Sprintf("helper %s: exit %d: %s", e.Action, e.ExitCode, e.Message)
}

// UserMessagePrefix marks a helper stderr line meant for the end user.
const UserMessagePrefix = "MYSERVER_ERROR: "

type Runner struct {
	helper string
	sudo   string
}

func New(helperPath string) *Runner {
	return &Runner{helper: helperPath, sudo: "/usr/bin/sudo"}
}

var actionRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)

func (r *Runner) command(ctx context.Context, action string, args []string) (*exec.Cmd, error) {
	if !actionRe.MatchString(action) {
		return nil, fmt.Errorf("privileged: invalid action %q", action)
	}
	for _, a := range args {
		if strings.ContainsRune(a, 0) {
			return nil, errors.New("privileged: NUL byte in argument")
		}
	}
	var cmd *exec.Cmd
	if os.Geteuid() == 0 {
		cmd = exec.CommandContext(ctx, r.helper, append([]string{action}, args...)...)
	} else {
		// "--" stops sudo option parsing; -n never prompts.
		full := append([]string{"-n", "--", r.helper, action}, args...)
		cmd = exec.CommandContext(ctx, r.sudo, full...)
	}
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "LANG=C"}
	cmd.WaitDelay = 5 * time.Second
	return cmd, nil
}

// Run executes an action and returns its stdout. A default timeout of two
// minutes applies when ctx has no deadline.
func (r *Runner) Run(ctx context.Context, action string, args ...string) ([]byte, error) {
	return r.RunInput(ctx, nil, action, args...)
}

// RunInput is Run with data supplied on the helper's stdin.
func (r *Runner) RunInput(ctx context.Context, stdin io.Reader, action string, args ...string) ([]byte, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
	}
	cmd, err := r.command(ctx, action, args)
	if err != nil {
		return nil, err
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdin = stdin
	cmd.Stdout = &stdout
	cmd.Stderr = &limitedWriter{w: &stderr, n: 64 << 10}
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), toError(action, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// Stream executes an action, copying stdout and stderr to the given writers
// as they are produced. Used for long operations such as package upgrades.
func (r *Runner) Stream(ctx context.Context, stdout, stderr io.Writer, action string, args ...string) error {
	cmd, err := r.command(ctx, action, args)
	if err != nil {
		return err
	}
	var errBuf bytes.Buffer
	cmd.Stdout = stdout
	if stderr != nil {
		cmd.Stderr = io.MultiWriter(stderr, &limitedWriter{w: &errBuf, n: 64 << 10})
	} else {
		cmd.Stderr = &limitedWriter{w: &errBuf, n: 64 << 10}
	}
	if err := cmd.Run(); err != nil {
		return toError(action, err, errBuf.String())
	}
	return nil
}

// Command returns an unstarted command for an action, for callers that need
// to manage the process themselves (for example attaching a PTY).
func (r *Runner) Command(ctx context.Context, action string, args ...string) (*exec.Cmd, error) {
	return r.command(ctx, action, args)
}

// Available reports whether the helper can be invoked.
func (r *Runner) Available(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := r.Run(ctx, "ping")
	return err
}

func toError(action string, err error, stderr string) error {
	e := &Error{Action: action, ExitCode: -1}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		e.ExitCode = ee.ExitCode()
	}
	for _, line := range strings.Split(stderr, "\n") {
		if msg, ok := strings.CutPrefix(strings.TrimSpace(line), UserMessagePrefix); ok {
			e.Message = msg
		}
	}
	if e.Message == "" {
		e.Message = strings.TrimSpace(stderr)
		if e.Message == "" {
			e.Message = err.Error()
		}
		if len(e.Message) > 2000 {
			e.Message = e.Message[len(e.Message)-2000:]
		}
	}
	return e
}

// UserMessage returns the helper's user-facing message for err, or fallback
// when err carries none.
func UserMessage(err error, fallback string) string {
	var e *Error
	if errors.As(err, &e) && e.ExitCode == ExitUser && e.Message != "" {
		return e.Message
	}
	return fallback
}

// ExitUser is the helper exit code for a failure whose message is safe and
// meaningful to show to the user.
const ExitUser = 3

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		q := p
		if len(q) > l.n {
			q = q[:l.n]
		}
		l.n -= len(q)
		_, _ = l.w.Write(q)
	}
	return len(p), nil
}
