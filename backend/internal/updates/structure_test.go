package updates

import (
	"context"
	"embed"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The owner's rule: nothing is upgraded, restarted or replaced unless an
// administrator asks for it. It is checked twice: by running the scheduled
// path against fakes that record everything, and by reading the module's
// source.

/* ---------- behaviour ---------- */

func scheduleEnv(t *testing.T) (*testEnv, *fakeDocker, *releaseServer) {
	f, _ := mixedDocker(t)
	f.handle("POST /images/create", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the scheduled path pulled an image")
		io.WriteString(w, "{}")
	})
	s := newReleaseServer(t)
	e := newEnv(t, envOptions{dockerHost: f.host()})
	useManifest(e, s)
	s.serveBody("application/json", s.manifest("1.3.0"))
	// Everything that could tempt an automatic action is present.
	e.write("list.out", listKernel, 0o644)
	e.write("reboot-required", "*** System restart required ***\n", 0o644)
	e.write("reboot-required.pkgs", "linux-image-6.8.0-45-generic\n", 0o644)
	e.set(KeyInterval, "1")
	return e, f, s
}

func TestScheduleOnlyChecks(t *testing.T) {
	e, f, s := scheduleEnv(t)
	ctx := context.Background()
	e.mod.runSchedule(ctx)

	if got, want := e.helperActions(), []string{"updates-apt-refresh", "updates-apt-list"}; !reflect.DeepEqual(got, want) {
		t.Errorf("helper actions = %q, want %q", got, want)
	}
	for _, c := range e.helperCalls() {
		if len(c) != 1 {
			t.Errorf("helper call with arguments: %q", c)
		}
	}
	for _, r := range f.seen() {
		if !strings.HasPrefix(r, "GET ") {
			t.Errorf("Docker received %s", r)
		}
	}
	if got, want := s.seen(), []string{"GET /myserver/manifest.json"}; !reflect.DeepEqual(got, want) {
		t.Errorf("release server requests = %q", got)
	}
	if calls := e.systemctlCalls(); len(calls) != 0 {
		t.Errorf("systemctl was called: %q", calls)
	}
	var sum summaryView
	e.do("GET", "/updates/summary", "", "user").into(t, &sum)
	if sum.Apt.Count != 4 || sum.Apt.KernelCount != 2 || sum.Docker.Count != 1 || sum.Self.Count != 1 || sum.Total != 6 ||
		!sum.Apt.RebootRequired || sum.Apt.Running {
		t.Errorf("summary = %+v", sum)
	}
	if e.mod.jobs.latest(kindApt) != nil || e.mod.jobs.latest(kindDockerPull) != nil {
		t.Error("the scheduled path started a job")
	}
	var jobs int
	e.db.QueryRow(`SELECT COUNT(*) FROM updates_jobs`).Scan(&jobs)
	if jobs != 0 {
		t.Errorf("%d jobs stored", jobs)
	}
	// It tells the owner, and that is all.
	want := []string{"INFO Güncelleme mevcut", "INFO Güncelleme mevcut", "INFO Güncelleme mevcut"}
	if got := e.notificationTitles(); !reflect.DeepEqual(got, want) {
		t.Errorf("notifications = %q", got)
	}
	// Scheduled checks are not the administrator's actions.
	if rows := e.auditRows(); len(rows) != 0 {
		t.Errorf("audit = %+v", rows)
	}

	// Nothing is due yet: the next rounds do nothing at all.
	helperCalls, dockerCalls, releaseCalls := len(e.helperCalls()), len(f.seen()), len(s.seen())
	for i := 0; i < 5; i++ {
		e.mod.runSchedule(ctx)
	}
	if len(e.helperCalls()) != helperCalls || len(f.seen()) != dockerCalls || len(s.seen()) != releaseCalls {
		t.Error("checks ran again before they were due")
	}

	// Due again: still only checks.
	e.mod.mu.Lock()
	e.mod.apt.CheckedAt -= 7200
	e.mod.docker.CheckedAt -= 7200
	e.mod.self.CheckedAt -= 7200
	e.mod.mu.Unlock()
	e.mod.runSchedule(ctx)
	want2 := []string{"updates-apt-refresh", "updates-apt-list", "updates-apt-refresh", "updates-apt-list"}
	if got := e.helperActions(); !reflect.DeepEqual(got, want2) {
		t.Errorf("helper actions = %q", got)
	}
	for _, r := range f.seen() {
		if !strings.HasPrefix(r, "GET ") {
			t.Errorf("Docker received %s", r)
		}
	}
	if n := len(e.notifications()); n != 3 {
		t.Errorf("%d notifications after a second round with the same result", n)
	}
}

func TestScheduleRespectsSwitches(t *testing.T) {
	e, f, s := scheduleEnv(t)
	e.set(KeyAptAuto, "false")
	e.set(KeyDockerAuto, "false")
	e.set(KeySelfAuto, "false")
	e.mod.runSchedule(context.Background())
	e.wantNoHelperCalls("schedule with every check switched off")
	if len(f.seen()) != 0 || len(s.seen()) != 0 {
		t.Errorf("requests: docker %q, release server %q", f.seen(), s.seen())
	}
	e.set(KeySelfAuto, "true")
	e.set(KeySource, sourceNone)
	e.mod.runSchedule(context.Background())
	if len(s.seen()) != 0 {
		t.Errorf("release server contacted with source none: %q", s.seen())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.set(KeyAptAuto, "true")
	e.mod.runSchedule(ctx)
	e.wantNoHelperCalls("schedule after shutdown")
}

func TestScheduleDuringAnUpgrade(t *testing.T) {
	e, _, _ := scheduleEnv(t)
	e.mod.jobs.add(newJob(kindApt, "x", "", "yonetici"))
	e.mod.runSchedule(context.Background())
	for _, a := range e.helperActions() {
		t.Errorf("helper action %s during a running upgrade", a)
	}
}

// Start waits before its first round and stops when asked to.
func TestStartStops(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.mod.Start(ctx)
		close(done)
	}()
	cancel()
	<-done
	e.wantNoHelperCalls("start and stop")
}

// Reading the state, as every page load does, never triggers anything.
func TestReadingNeverActs(t *testing.T) {
	e, f, s := scheduleEnv(t)
	for i := 0; i < 3; i++ {
		for _, p := range []string{"/updates/summary", "/updates/apt", "/updates/docker", "/updates/self", "/updates/jobs"} {
			if res := e.do("GET", p, "", "admin"); res.Status != 200 {
				t.Errorf("%s: status %d", p, res.Status)
			}
		}
	}
	e.wantNoHelperCalls("reading")
	if len(f.seen()) != 0 || len(s.seen()) != 0 {
		t.Errorf("requests: docker %q, release server %q", f.seen(), s.seen())
	}
	if calls := e.systemctlCalls(); len(calls) != 0 {
		t.Errorf("systemctl was called: %q", calls)
	}
}

/* ---------- source ---------- */

//go:embed *.go
var sources embed.FS

type funcInfo struct {
	file     string
	calls    map[string]bool // names of identifiers and selectors used
	strings  []string
	literals map[string]bool
}

func parseModule(t *testing.T) map[string]*funcInfo {
	t.Helper()
	entries, err := sources.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	funcs := map[string]*funcInfo{}
	fset := token.NewFileSet()
	n := 0
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := sources.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		n++
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			info := &funcInfo{file: name, calls: map[string]bool{}, literals: map[string]bool{}}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				switch v := node.(type) {
				case *ast.Ident:
					info.calls[v.Name] = true
				case *ast.SelectorExpr:
					info.calls[v.Sel.Name] = true
				case *ast.BasicLit:
					if v.Kind == token.STRING {
						if s, err := strconv.Unquote(v.Value); err == nil {
							info.literals[s] = true
						}
					}
				}
				return true
			})
			// The analysis goes by name: methods sharing a name are
			// treated as one function that does everything any of them does.
			if prev, dup := funcs[fn.Name.Name]; dup {
				for k := range info.calls {
					prev.calls[k] = true
				}
				for k := range info.literals {
					prev.literals[k] = true
				}
				continue
			}
			funcs[fn.Name.Name] = info
		}
	}
	if n < 6 || len(funcs) < 40 {
		t.Fatalf("parsed %d files and %d functions; the sources are not embedded", n, len(funcs))
	}
	return funcs
}

// reachable returns every function of the package that can be reached from
// the roots. Any mention of a function's name counts as a call, so the set
// can only be too large, never too small.
func reachable(funcs map[string]*funcInfo, roots ...string) map[string]bool {
	seen := map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		if seen[name] || funcs[name] == nil {
			return
		}
		seen[name] = true
		for callee := range funcs[name].calls {
			visit(callee)
		}
	}
	for _, r := range roots {
		visit(r)
	}
	return seen
}

var (
	// Helper actions that change the system, and the only function allowed
	// to name each of them.
	actingHelperActions = map[string]string{
		"updates-apt-upgrade": "handleAptUpgrade",
		"updates-reboot":      "handleReboot",
		"updates-self":        "handleSelfApply",
	}
	// Functions that act on an administrator's request.
	actingFunctions = []string{"handleAptUpgrade", "handleReboot", "handleSelfApply", "handleDockerPull", "runPull"}
	// Docker client methods that change anything.
	actingDockerCalls = []string{"ImagePull", "ImageRemove", "ImageTag", "ImagesPrune", "ContainerCreate", "ContainerStart",
		"ContainerStop", "ContainerRestart", "ContainerRemove", "ContainerKill", "ContainerRename", "ContainerUpdate",
		"ContainerExecCreate", "ContainerExecStart", "ImageLoad", "ImageImport", "ImageBuild"}
)

func TestSourceBackgroundPathsOnlyCheck(t *testing.T) {
	funcs := parseModule(t)
	for _, name := range append([]string{"Start", "runSchedule", "New", "reattachAptJob", "followAptJob", "completeAptJob",
		"checkApt", "checkDocker", "checkSelf", "refreshAptState"}, actingFunctions...) {
		if funcs[name] == nil {
			t.Fatalf("function %s no longer exists; update this test", name)
		}
	}
	// Everything that runs without a request: the schedule, the start of
	// the panel, and the follow-up of an upgrade that was asked for.
	background := reachable(funcs, "Start", "runSchedule", "New", "reattachAptJob", "followAptJob", "completeAptJob",
		"handleSummary", "handleApt", "handleDocker", "handleSelf", "handleJobs", "handleJob", "handleJobStream",
		"handleAptCheck", "handleDockerCheck", "handleSelfCheck", "Health")
	for _, want := range []string{"checkApt", "checkDocker", "checkSelf"} {
		if !background[want] {
			t.Errorf("%s is not reached from the schedule; the analysis is broken", want)
		}
	}
	names := []string{}
	for name := range background {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		info := funcs[name]
		if name == "Register" {
			continue
		}
		for _, acting := range actingFunctions {
			if name == acting {
				t.Errorf("%s is reachable without a request", name)
			}
		}
		for lit := range info.literals {
			if _, acting := actingHelperActions[lit]; acting {
				t.Errorf("%s (%s) names the helper action %q", name, info.file, lit)
			}
			for _, word := range []string{"reboot", "poweroff", "shutdown", "kexec", "systemd-run", "dist-upgrade", "full-upgrade"} {
				if lit == word {
					t.Errorf("%s (%s) contains %q", name, info.file, lit)
				}
			}
		}
		for _, call := range actingDockerCalls {
			if info.calls[call] {
				t.Errorf("%s (%s) calls %s", name, info.file, call)
			}
		}
	}
}

func TestSourceActingCodeIsWhereItBelongs(t *testing.T) {
	funcs := parseModule(t)
	found := map[string]bool{}
	for name, info := range funcs {
		for lit := range info.literals {
			// Every helper action the module uses is known to this test.
			if strings.HasPrefix(lit, "updates-") {
				switch lit {
				case "updates-apt-refresh", "updates-apt-list":
				default:
					owner, ok := actingHelperActions[lit]
					if !ok {
						t.Errorf("%s uses the unknown helper action %q", name, lit)
					} else if owner != name {
						t.Errorf("%s uses %q; only %s may", name, lit, owner)
					}
					found[lit] = true
				}
			}
		}
		for _, call := range actingDockerCalls {
			if info.calls[call] && !(call == "ImagePull" && name == "runPull") {
				t.Errorf("%s calls %s", name, call)
			}
		}
		// The panel never runs a package manager or a shell itself.
		for lit := range info.literals {
			for _, tool := range []string{"apt-get", "/usr/bin/apt", "dpkg", "/bin/sh", "/bin/bash", "sh", "bash", "sudo", "-c"} {
				if lit == tool || strings.HasSuffix(lit, "/"+tool) {
					t.Errorf("%s contains %q", name, lit)
				}
			}
		}
		// Only one function starts processes, and only systemctl.
		if (info.calls["CommandContext"] || info.calls["Command"] || info.calls["StartProcess"]) && name != "systemctl" {
			t.Errorf("%s starts a process", name)
		}
	}
	for action := range actingHelperActions {
		if !found[action] {
			t.Errorf("no function uses %q any more; update this test", action)
		}
	}
	// What the panel asks systemd is read-only.
	for name, info := range funcs {
		if !info.calls["systemctl"] || name == "systemctl" {
			continue
		}
		if name != "unitActive" && name != "unitOutcome" {
			t.Errorf("%s calls systemctl", name)
		}
		for lit := range info.literals {
			switch lit {
			case "start", "stop", "restart", "reboot", "kill", "enable", "disable", "isolate", "reset-failed", "daemon-reload":
				t.Errorf("%s passes %q to systemctl", name, lit)
			}
		}
	}
}

// The acting handlers are mounted on the administrators' group, behind
// POST, and nowhere else.
func TestSourceRoutes(t *testing.T) {
	src, err := sources.ReadFile("module.go")
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "module.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	type route struct{ group, method, path, handler string }
	var routes []route
	groups := map[string]bool{} // group variable -> admin only
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Register" {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			switch v := node.(type) {
			case *ast.AssignStmt:
				call, ok := v.Rhs[0].(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Group" {
					admin := false
					for _, arg := range call.Args[1:] {
						if s, ok := arg.(*ast.SelectorExpr); ok && s.Sel.Name == "RequireAdmin" {
							admin = true
						}
					}
					groups[v.Lhs[0].(*ast.Ident).Name] = admin
				}
			case *ast.CallExpr:
				sel, ok := v.Fun.(*ast.SelectorExpr)
				if !ok || len(v.Args) < 2 {
					return true
				}
				recv, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "Get", "Post", "Put", "Delete", "Handle", "Raw":
					path, _ := strconv.Unquote(v.Args[0].(*ast.BasicLit).Value)
					h := v.Args[len(v.Args)-1].(*ast.SelectorExpr).Sel.Name
					routes = append(routes, route{recv.Name, sel.Sel.Name, path, h})
				}
			}
			return true
		})
	}
	if len(routes) != 14 {
		t.Errorf("%d routes: %+v", len(routes), routes)
	}
	open := map[string]bool{"handleSummary": true, "handleApt": true, "handleDocker": true, "handleSelf": true}
	for _, r := range routes {
		admin, known := groups[r.group]
		if !known {
			t.Errorf("route %s %s is mounted on %q", r.method, r.path, r.group)
		}
		if open[r.handler] {
			if r.method != "Get" {
				t.Errorf("%s is mounted with %s", r.handler, r.method)
			}
			continue
		}
		if !admin {
			t.Errorf("%s %s (%s) is open to every signed-in user", r.method, r.path, r.handler)
		}
		if strings.HasPrefix(r.handler, "handleJob") {
			if r.method != "Get" {
				t.Errorf("%s is mounted with %s", r.handler, r.method)
			}
		} else if r.method != "Post" {
			t.Errorf("%s is mounted with %s; a state change must need the CSRF token", r.handler, r.method)
		}
	}
}

func TestSourceIsEmbeddedFromThisPackage(t *testing.T) {
	if _, err := sources.ReadFile("aptjob.go"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("/nonexistent"); err == nil {
		t.Fatal("unexpected")
	}
}
