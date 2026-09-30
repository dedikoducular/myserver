// Package helper implements the root helper's actions. The helper is a
// separate binary run through sudo by the panel; it is the security
// boundary, so every action validates its own arguments and never trusts
// the caller. Each module contributes actions in its own actions_*.go file.
package helper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"time"

	"myserver/internal/privileged"
)

// Action is one privileged operation.
type Action func(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error

var actions = map[string]Action{}

// Register adds an action; call it from init().
func Register(name string, a Action) {
	if _, dup := actions[name]; dup {
		panic("helper: duplicate action " + name)
	}
	actions[name] = a
}

// UserError is a failure whose message may be shown to the end user.
type UserError struct{ Message string }

func (e *UserError) Error() string { return e.Message }

// Userf builds a UserError.
func Userf(format string, a ...any) error {
	return &UserError{Message: fmt.Sprintf(format, a...)}
}

// ArgCount validates the number of arguments.
func ArgCount(args []string, n int) error {
	if len(args) != n {
		return fmt.Errorf("expected %d arguments, got %d", n, len(args))
	}
	return nil
}

// cleanEnv is the fixed environment for every child process.
var cleanEnv = []string{
	"PATH=/usr/sbin:/usr/bin:/sbin:/bin",
	"LC_ALL=C", "LANG=C",
	"DEBIAN_FRONTEND=noninteractive",
}

// Command builds a child command with a fixed environment. name must be an
// absolute path: the helper never resolves binaries through PATH.
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = cleanEnv
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// Exec runs a child command, streaming its output to stdout/stderr.
func Exec(ctx context.Context, stdout io.Writer, name string, args ...string) error {
	cmd := Command(ctx, name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Main dispatches os.Args and returns the process exit code.
func Main(argv []string) int {
	if len(argv) < 2 {
		fmt.Fprintln(os.Stderr, "usage: myserver-helper <action> [args...]")
		return 2
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "myserver-helper must run as root")
		return 2
	}
	name := argv[1]
	if name == "list-actions" {
		names := make([]string, 0, len(actions))
		for n := range actions {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Println(n)
		}
		return 0
	}
	a, ok := actions[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown action %q\n", name)
		return 2
	}
	err := a(context.Background(), argv[2:], os.Stdin, os.Stdout)
	if err == nil {
		return 0
	}
	var ue *UserError
	if errors.As(err, &ue) {
		fmt.Fprintln(os.Stderr, privileged.UserMessagePrefix+ue.Message)
		return privileged.ExitUser
	}
	fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
	return 1
}
