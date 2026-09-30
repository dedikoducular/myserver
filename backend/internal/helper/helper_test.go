package helper

import (
	"bytes"
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"myserver/internal/privileged"
)

// These tests only ever reach code that returns before a command would be
// executed: unknown actions, wrong argument counts and invalid values. No
// test passes a valid hostname or time zone to an action.

var ctx = context.Background()

func run(t *testing.T, name string, args ...string) (string, error) {
	t.Helper()
	a, ok := actions[name]
	if !ok {
		t.Fatalf("action %q is not registered", name)
	}
	var out bytes.Buffer
	err := a(ctx, args, strings.NewReader(""), &out)
	return out.String(), err
}

func isUserError(err error) bool {
	var ue *UserError
	return errors.As(err, &ue)
}

func TestPing(t *testing.T) {
	out, err := run(t, "ping")
	if err != nil || out != "pong\n" {
		t.Fatalf("ping: %q %v", out, err)
	}
	if _, err := run(t, "ping", "extra"); err == nil {
		t.Fatal("ping accepted an argument")
	}
}

func TestSystemActionsCheckArgumentCount(t *testing.T) {
	for _, name := range []string{"hostname-set", "timezone-set"} {
		// All values are invalid as well, so nothing can run even if the
		// count check were missing.
		for _, args := range [][]string{nil, {}, {"bad host!", "bad host!"}, {"bad host!", "x", "y"}, {"", ""}} {
			out, err := run(t, name, args...)
			if err == nil {
				t.Errorf("%s with %d arguments succeeded", name, len(args))
				continue
			}
			if isUserError(err) && len(args) != 1 {
				t.Errorf("%s with %d arguments: reached value validation (%v); the count must be checked first", name, len(args), err)
			}
			if out != "" {
				t.Errorf("%s: output %q", name, out)
			}
		}
	}
}

func TestHostnameSetRefusesInvalidNames(t *testing.T) {
	bad := []string{"", " ", "bad host", "-rf", "--static", "--help", "host-", "a.b", "a_b", "UPPER", "host;reboot", "$(reboot)", "`reboot`",
		"host\nreboot", "host\x00", "../etc", "/etc/hostname", strings.Repeat("a", 64), "şemsiye", "a b"}
	for _, h := range bad {
		out, err := run(t, "hostname-set", h)
		if err == nil {
			t.Fatalf("hostname %q accepted", h)
		}
		if !isUserError(err) {
			t.Errorf("hostname %q: error %v (%T); an invalid name must be refused by validation, not by a failing command", h, err, err)
		}
		if out != "" {
			t.Errorf("hostname %q: output %q", h, out)
		}
	}
}

func TestTimezoneSetRefusesInvalidNames(t *testing.T) {
	bad := []string{"", " ", "..", "../../etc/passwd", "/etc/passwd", "Europe/../../etc/shadow", "Europe/Istanbul/../../../etc/passwd",
		"Europe//Istanbul", "Europe/Istanbul;reboot", "$(reboot)", "--help", "-h", "Europe/Istanbul\x00", "Europe/Istanbul\n",
		"Mars/Olympus_Mons", "Europe", "zone1970.tab", strings.Repeat("A", 65), "a/b/c/d"}
	for _, z := range bad {
		out, err := run(t, "timezone-set", z)
		if err == nil {
			t.Fatalf("time zone %q accepted", z)
		}
		if !isUserError(err) {
			t.Errorf("time zone %q: error %v (%T); an invalid zone must be refused by validation, not by a failing command", z, err, err)
		}
		if out != "" {
			t.Errorf("time zone %q: output %q", z, out)
		}
	}
}

func TestMainRefusals(t *testing.T) {
	// Silence the usage text.
	saved := os.Stderr
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = devnull
	defer func() { os.Stderr = saved; devnull.Close() }()

	root := os.Geteuid() == 0
	cases := []struct {
		name     string
		argv     []string
		asRoot   int
		asOthers int
	}{
		{"no arguments", nil, 2, 2},
		{"program name only", []string{"myserver-helper"}, 2, 2},
		{"unknown action", []string{"myserver-helper", "no-such-action"}, 2, 2},
		{"empty action", []string{"myserver-helper", ""}, 2, 2},
		{"shell text", []string{"myserver-helper", "ping; reboot"}, 2, 2},
		{"path as action", []string{"myserver-helper", "/bin/sh"}, 2, 2},
		{"option as action", []string{"myserver-helper", "--help"}, 2, 2},
		{"action in another case", []string{"myserver-helper", "PING"}, 2, 2},
		{"action with padding", []string{"myserver-helper", " ping"}, 2, 2},
		{"wrong argument count", []string{"myserver-helper", "ping", "extra"}, 1, 2},
		{"missing argument", []string{"myserver-helper", "hostname-set"}, 1, 2},
		{"invalid hostname", []string{"myserver-helper", "hostname-set", "bad host!"}, privileged.ExitUser, 2},
		{"invalid time zone", []string{"myserver-helper", "timezone-set", "../../etc/passwd"}, privileged.ExitUser, 2},
	}
	for _, c := range cases {
		want := c.asOthers
		if root {
			want = c.asRoot
		}
		if got := Main(c.argv); got != want {
			t.Errorf("%s: exit code %d, want %d", c.name, got, want)
		}
	}
}

func TestUserErrors(t *testing.T) {
	err := Userf("Disk %s bağlı değil.", "sdb1")
	if err.Error() != "Disk sdb1 bağlı değil." || !isUserError(err) {
		t.Fatalf("Userf: %v", err)
	}
	if isUserError(ArgCount(nil, 1)) {
		t.Fatal("an argument count failure is reported as a user message")
	}
	if ArgCount([]string{"a"}, 1) != nil || ArgCount(nil, 0) != nil {
		t.Fatal("correct argument count refused")
	}
}

func TestRegisterRefusesDuplicates(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("a second action with the same name replaced the first")
		}
	}()
	Register("ping", actions["ping"])
}

// Every action name must be one the panel-side runner accepts, otherwise
// the action is unreachable; and no name may look like an option.
func TestRegisteredActionNames(t *testing.T) {
	re := regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)
	for name := range actions {
		if !re.MatchString(name) {
			t.Errorf("action name %q does not match the runner's pattern", name)
		}
		if name == "list-actions" {
			t.Errorf("action %q is shadowed by the built-in listing", name)
		}
	}
	for _, core := range []string{"ping", "hostname-set", "timezone-set"} {
		if _, ok := actions[core]; !ok {
			t.Errorf("core action %q is missing", core)
		}
	}
}

func TestChildEnvironmentIsFixed(t *testing.T) {
	t.Setenv("MYSERVER_TEST_LEAK", "gizli")
	cmd := Command(ctx, "/usr/bin/true", "a b", "$(id)")
	if len(cmd.Args) != 3 || cmd.Args[1] != "a b" || cmd.Args[2] != "$(id)" {
		t.Fatalf("arguments %q", cmd.Args)
	}
	if len(cmd.Env) == 0 {
		t.Fatal("child processes inherit the caller's environment")
	}
	for _, e := range cmd.Env {
		k, _, _ := strings.Cut(e, "=")
		switch k {
		case "PATH", "LC_ALL", "LANG", "DEBIAN_FRONTEND":
		default:
			t.Errorf("unexpected environment variable %q", e)
		}
	}
}
