package services

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"myserver/internal/audit"
	"myserver/internal/auth"
	"myserver/internal/config"
	"myserver/internal/database"
	"myserver/internal/httpx"
	"myserver/internal/module"
	"myserver/internal/notify"
	"myserver/internal/privileged"
	"myserver/internal/settings"
	"myserver/migrations"
)

// The handler tests go through the real router, auth.Require and
// auth.RequireAdmin. The privileged runner is the real one, pointed at a
// recording shell script instead of the root helper, and systemctl is
// replaced by a script that prints the fixtures. Nothing on the host is
// started, stopped or queried.

const (
	adminToken = "admin-session-token"
	adminCSRF  = "admin-csrf-token"
	userToken  = "user-session-token"
	userCSRF   = "user-csrf-token"
)

const fakeHelperScript = `#!/bin/sh
{
	for a in "$@"; do printf '%s\n' "$a"; done
	printf '%s\n' '--end--'
} >> '@LOG@'
case "$1" in
services-logs)
	i=1
	while [ "$i" -le 600 ]; do
		echo "2026-09-29T10:00:00+0300 sunucu cron[812]: satır $i"
		i=$((i+1))
	done
	;;
services-logs-follow)
	echo "2026-09-29T10:00:01+0300 sunucu cron[812]: birinci"
	echo "2026-09-29T10:00:02+0300 sunucu cron[812]: ikinci"
	;;
services-control)
	if [ "$3" = "accounts-daemon.service" ]; then
		echo "MYSERVER_ERROR: Servis işlemi başarısız oldu. Ayrıntılar için servis loglarına bakın." >&2
		exit 3
	fi
	if [ "$3" = "apparmor.service" ]; then
		echo "internal detail /etc/secret" >&2
		exit 1
	fi
	;;
esac
exit 0
`

const fakeSystemctlScript = `#!/bin/sh
printf '%s\n' "$*" >> '@LOG@'
for a in "$@"; do
	case "$a" in
	--) break ;;
	list-units) cat '@DIR@/units.txt'; exit 0 ;;
	list-unit-files) cat '@DIR@/files.txt'; exit 0 ;;
	show) cat '@DIR@/show.txt'; exit 0 ;;
	esac
done
exit 1
`

type testEnv struct {
	t            *testing.T
	mod          *Module
	handler      http.Handler
	db           *sql.DB
	helperLog    string
	systemctlLog string
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	if os.Geteuid() != 0 {
		// The runner would go through sudo instead of running the fake.
		t.Skip("needs root: the privileged runner only executes the helper directly as root")
	}
	dir := t.TempDir()
	ctx := context.Background()

	db, err := database.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	initSQL, err := fs.ReadFile(migrations.FS, "0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, db, fstest.MapFS{"0001_init.sql": {Data: initSQL}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	for _, u := range []struct {
		id                int
		name, role        string
		token, csrf, addr string
	}{
		{1, "yonetici", auth.RoleAdmin, adminToken, adminCSRF, "192.0.2.1"},
		{2, "kullanici", auth.RoleUser, userToken, userCSRF, "192.0.2.1"},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO users (id, username, password_hash, role, disabled, created_at)
			VALUES (?, ?, 'x', ?, 0, ?)`, u.id, u.name, u.role, now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO sessions
			(token_hash, user_id, csrf_token, ip, user_agent, created_at, last_seen_at, expires_at)
			VALUES (?, ?, ?, ?, 'test', ?, ?, ?)`,
			tokenHash(u.token), u.id, u.csrf, u.addr, now, now, now+3600); err != nil {
			t.Fatal(err)
		}
	}

	e := &testEnv{
		t:            t,
		db:           db,
		helperLog:    filepath.Join(dir, "helper.log"),
		systemctlLog: filepath.Join(dir, "systemctl.log"),
	}
	helperPath := filepath.Join(dir, "fake-helper")
	writeFile(t, helperPath, strings.ReplaceAll(fakeHelperScript, "@LOG@", e.helperLog), 0o755)
	writeFile(t, filepath.Join(dir, "units.txt"), fixtureListUnits, 0o644)
	writeFile(t, filepath.Join(dir, "files.txt"), fixtureUnitFiles, 0o644)
	writeFile(t, filepath.Join(dir, "show.txt"), fixtureShow, 0o644)
	fakeSystemctl := filepath.Join(dir, "fake-systemctl")
	script := strings.ReplaceAll(fakeSystemctlScript, "@LOG@", e.systemctlLog)
	writeFile(t, fakeSystemctl, strings.ReplaceAll(script, "@DIR@", dir), 0o755)
	old := systemctlPath
	systemctlPath = fakeSystemctl
	t.Cleanup(func() { systemctlPath = old })

	store, err := settings.NewStore(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	al := audit.New(db)
	priv := privileged.New(helperPath)
	cfg := &config.Config{DataDir: dir, HelperPath: helperPath}
	authSvc, err := auth.NewService(db, cfg, store, al, priv)
	if err != nil {
		t.Fatal(err)
	}
	deps := module.Deps{
		Cfg: cfg, DB: db, Settings: store, Audit: al,
		Notify: notify.New(db), Priv: priv, Auth: authSvc,
	}
	mod, err := New(deps, settings.NewAPI(store, al, priv, auth.ActorFrom))
	if err != nil {
		t.Fatal(err)
	}
	e.mod = mod.(*Module)

	// Same wiring as internal/server/server.go.
	mux := http.NewServeMux()
	root := httpx.NewRouter(mux)
	api := root.Group("/api/v1", authSvc.Require)
	ws := root.Group("/api/v1", authSvc.RequireWebSocket)
	mod.Register(api, ws)
	e.handler = mux
	return e
}

type response struct {
	Status int
	Body   string
	Env    struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
		Error   *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
}

func (r response) code() string {
	if r.Env.Error == nil {
		return ""
	}
	return r.Env.Error.Code
}

// do sends a request as "admin", "user" or "" (no session). A state-changing
// request carries the session's CSRF token unless who ends in "-nocsrf".
func (e *testEnv) do(method, path, body, who string) response {
	e.t.Helper()
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	var req *http.Request
	if rd != nil {
		req = httptest.NewRequest(method, "/api/v1"+path, rd)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, "/api/v1"+path, nil)
	}
	sendCSRF := !strings.HasSuffix(who, "-nocsrf")
	switch strings.TrimSuffix(who, "-nocsrf") {
	case "admin":
		req.AddCookie(&http.Cookie{Name: "myserver_session", Value: adminToken})
		if sendCSRF {
			req.Header.Set("X-CSRF-Token", adminCSRF)
		}
	case "user":
		req.AddCookie(&http.Cookie{Name: "myserver_session", Value: userToken})
		if sendCSRF {
			req.Header.Set("X-CSRF-Token", userCSRF)
		}
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	out := response{Status: rec.Code, Body: rec.Body.String()}
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(rec.Body.Bytes(), &out.Env); err != nil {
			e.t.Fatalf("%s %s: response is not an envelope: %v\n%s", method, path, err, out.Body)
		}
	}
	return out
}

// helperCalls returns every invocation of the fake helper as its argument
// list (action first).
func (e *testEnv) helperCalls() [][]string {
	e.t.Helper()
	b, err := os.ReadFile(e.helperLog)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		e.t.Fatal(err)
	}
	var calls [][]string
	var cur []string
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if line == "--end--" {
			calls = append(calls, cur)
			cur = nil
			continue
		}
		cur = append(cur, line)
	}
	return calls
}

func (e *testEnv) wantNoHelperCalls(what string) {
	e.t.Helper()
	if calls := e.helperCalls(); len(calls) != 0 {
		e.t.Errorf("%s: the privileged runner was reached: %v", what, calls)
	}
}

func (e *testEnv) wantHelperCalls(what string, want ...[]string) {
	e.t.Helper()
	if got := e.helperCalls(); !reflect.DeepEqual(got, want) {
		e.t.Errorf("%s: helper calls = %v, want %v", what, got, want)
	}
}

func (e *testEnv) resetHelperLog() {
	e.t.Helper()
	if err := os.Remove(e.helperLog); err != nil && !os.IsNotExist(err) {
		e.t.Fatal(err)
	}
}

type auditRow struct {
	Username, Action, Target, Detail string
	Success                          bool
}

func (e *testEnv) auditRows() []auditRow {
	e.t.Helper()
	rows, err := e.db.Query(`SELECT username, action, target, detail, success FROM audit_log ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		if err := rows.Scan(&r.Username, &r.Action, &r.Target, &r.Detail, &r.Success); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func confirmBody(v string) string {
	return mustJSON(map[string]string{"confirm": v})
}

func TestServicesListForNormalUser(t *testing.T) {
	e := newEnv(t)
	res := e.do("GET", "/services", "", "user")
	if res.Status != 200 || !res.Env.Success {
		t.Fatalf("status %d: %s", res.Status, res.Body)
	}
	var data struct {
		Featured  []Service `json:"featured"`
		Services  []Service `json:"services"`
		UpdatedAt int64     `json:"updated_at"`
	}
	if err := json.Unmarshal(res.Env.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Services) != 24 {
		t.Errorf("listed %d services, want 24", len(data.Services))
	}
	if data.UpdatedAt <= 0 {
		t.Errorf("updated_at = %d", data.UpdatedAt)
	}
	for _, unit := range []string{"user@1000.service", `systemd-fsck@dev-disk-by\x2duuid-1A2B\x2d3C4D.service`, "rsync.service"} {
		mustService(t, data.Services, unit)
	}
	if s := mustService(t, data.Services, "ssh.service"); !s.Featured || s.State != StateRunning {
		t.Errorf("ssh.service = %+v", s)
	}
	if s := mustService(t, data.Services, "cron.service"); s.Featured {
		t.Errorf("cron.service must not be featured by default")
	}
	var featured []string
	for _, s := range data.Featured {
		featured = append(featured, s.Unit+"="+s.State)
	}
	want := []string{
		"docker.service=running", "ssh.service=running", "chrony.service=not_installed",
		"ufw.service=active", "nginx.service=failed", "tailscaled.service=not_installed",
		"smbd.service=not_installed", "nfs-server.service=not_installed", "myserver.service=running",
	}
	if !reflect.DeepEqual(featured, want) {
		t.Errorf("featured = %v\nwant %v", featured, want)
	}
	if strings.Contains(res.Body, `"triggered_by":null`) {
		t.Error("triggered_by must be [] rather than null")
	}

	res = e.do("GET", "/services/featured", "", "user")
	if res.Status != 200 {
		t.Fatalf("featured: status %d: %s", res.Status, res.Body)
	}
	var list []Service
	if err := json.Unmarshal(res.Env.Data, &list); err != nil || len(list) != 9 {
		t.Errorf("featured endpoint returned %d entries (%v), want 9", len(list), err)
	}
	e.wantNoHelperCalls("listing")

	// Listing must only ever read.
	b, err := os.ReadFile(e.systemctlLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		verb := ""
		for _, f := range strings.Fields(line) {
			if !strings.HasPrefix(f, "-") {
				verb = f
				break
			}
		}
		if verb != "list-units" && verb != "list-unit-files" && verb != "show" {
			t.Errorf("unexpected systemctl invocation: %s", line)
		}
	}
}

func TestServicesListIsCached(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		if res := e.do("GET", "/services", "", "user"); res.Status != 200 {
			t.Fatalf("status %d", res.Status)
		}
	}
	b, err := os.ReadFile(e.systemctlLog)
	if err != nil {
		t.Fatal(err)
	}
	// One rebuild: list-units, list-unit-files and one show chunk.
	if n := strings.Count(string(b), "\n"); n != 3 {
		t.Errorf("systemctl ran %d times for five requests, want 3:\n%s", n, b)
	}
}

func TestServicesSystemdUnavailable(t *testing.T) {
	e := newEnv(t)
	systemctlPath = filepath.Join(t.TempDir(), "missing-systemctl")
	res := e.do("GET", "/services", "", "user")
	if res.Status != http.StatusServiceUnavailable || res.code() != "systemd_unavailable" {
		t.Errorf("list: status %d code %q: %s", res.Status, res.code(), res.Body)
	}
	if strings.Contains(res.Body, "missing-systemctl") {
		t.Errorf("internal error leaked to the user: %s", res.Body)
	}
	res = e.do("POST", "/services/cron.service/restart", "", "admin")
	if res.Status != http.StatusServiceUnavailable {
		t.Errorf("action: status %d: %s", res.Status, res.Body)
	}
	e.wantNoHelperCalls("action without a service list")
}

func TestServicesRequireSession(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/services"},
		{"GET", "/services/featured"},
		{"GET", "/services/cron.service/logs"},
		{"GET", "/services/cron.service/logs/stream"},
		{"POST", "/services/cron.service/restart"},
	} {
		res := e.do(c.method, c.path, "", "")
		if res.Status != http.StatusUnauthorized || res.code() != "unauthorized" {
			t.Errorf("%s %s without a session: status %d code %q", c.method, c.path, res.Status, res.code())
		}
	}
	e.wantNoHelperCalls("requests without a session")
}

func TestServicesAdminOnlyRoutesRefuseNormalUser(t *testing.T) {
	e := newEnv(t)
	cases := []struct{ method, path, body string }{
		{"GET", "/services/cron.service/logs", ""},
		{"GET", "/services/cron.service/logs?lines=10", ""},
		{"GET", "/services/cron.service/logs/stream", ""},
		{"POST", "/services/cron.service/start", ""},
		{"POST", "/services/cron.service/stop", ""},
		{"POST", "/services/cron.service/restart", ""},
		{"POST", "/services/cron.service/reload", ""},
		{"POST", "/services/cron.service/enable", ""},
		{"POST", "/services/cron.service/disable", ""},
		{"POST", "/services/ssh.service/stop", confirmBody("ssh.service")},
	}
	for _, c := range cases {
		res := e.do(c.method, c.path, c.body, "user")
		if res.Status != http.StatusForbidden || res.code() != "forbidden" {
			t.Errorf("%s %s as a normal user: status %d code %q", c.method, c.path, res.Status, res.code())
		}
	}
	e.wantNoHelperCalls("requests of a normal user")
	if rows := e.auditRows(); len(rows) != 0 {
		t.Errorf("refused requests wrote audit rows claiming an action: %+v", rows)
	}
}

func TestServicesActionRequiresCSRF(t *testing.T) {
	e := newEnv(t)
	res := e.do("POST", "/services/cron.service/restart", "", "admin-nocsrf")
	if res.Status != http.StatusForbidden || res.code() != "csrf_invalid" {
		t.Errorf("status %d code %q", res.Status, res.code())
	}
	e.wantNoHelperCalls("request without a CSRF token")
}

func TestServicesActionNormalUnit(t *testing.T) {
	for _, verb := range []string{"start", "stop", "restart", "enable", "disable"} {
		t.Run(verb, func(t *testing.T) {
			e := newEnv(t)
			res := e.do("POST", "/services/cron.service/"+verb, "", "admin")
			if res.Status != 200 {
				t.Fatalf("status %d: %s", res.Status, res.Body)
			}
			var s Service
			if err := json.Unmarshal(res.Env.Data, &s); err != nil || s.Unit != "cron.service" {
				t.Errorf("response data = %s (%v)", res.Env.Data, err)
			}
			e.wantHelperCalls(verb, []string{"services-control", verb, "cron.service"})
			want := []auditRow{{"yonetici", "services." + verb, "cron.service", "", true}}
			if got := e.auditRows(); !reflect.DeepEqual(got, want) {
				t.Errorf("audit = %+v, want %+v", got, want)
			}
		})
	}
}

func TestServicesActionOddUnitNames(t *testing.T) {
	e := newEnv(t)
	res := e.do("POST", "/services/user@1000.service/restart", "", "admin")
	if res.Status != 200 {
		t.Errorf("user@1000.service: status %d: %s", res.Status, res.Body)
	}
	res = e.do("POST", `/services/systemd-fsck@dev-disk-by%5Cx2duuid-1A2B%5Cx2d3C4D.service/start`, "", "admin")
	if res.Status != 200 {
		t.Errorf("escaped instance: status %d: %s", res.Status, res.Body)
	}
	e.wantHelperCalls("odd names",
		[]string{"services-control", "restart", "user@1000.service"},
		[]string{"services-control", "start", `systemd-fsck@dev-disk-by\x2duuid-1A2B\x2d3C4D.service`})
}

func TestServicesCriticalUnitNeedsExactConfirmation(t *testing.T) {
	critical := []string{
		"ssh.service", "docker.service", "containerd.service", "myserver.service",
		"systemd-networkd.service", "systemd-resolved.service",
	}
	for _, unit := range critical {
		for _, verb := range []string{"stop", "disable", "restart"} {
			t.Run(unit+"/"+verb, func(t *testing.T) {
				e := newEnv(t)
				path := "/services/" + unit + "/" + verb
				bodies := []string{
					"",
					"{}",
					confirmBody(""),
					confirmBody(strings.TrimSuffix(unit, ".service")),
					confirmBody(strings.ToUpper(unit)),
					confirmBody(" " + unit),
					confirmBody(unit + " "),
					confirmBody(unit + "\n"),
					confirmBody("evet"),
					confirmBody("true"),
					confirmBody("cron.service"),
				}
				for _, body := range bodies {
					res := e.do("POST", path, body, "admin")
					if res.Status != http.StatusConflict || res.code() != "confirmation_required" {
						t.Errorf("body %q: status %d code %q", body, res.Status, res.code())
					}
					if res.Env.Error != nil && !strings.Contains(res.Env.Error.Message, unit) {
						t.Errorf("body %q: message does not name the unit: %q", body, res.Env.Error.Message)
					}
				}
				e.wantNoHelperCalls("unconfirmed " + verb + " of " + unit)

				res := e.do("POST", path, confirmBody(unit), "admin")
				if res.Status != 200 {
					t.Fatalf("confirmed: status %d: %s", res.Status, res.Body)
				}
				e.wantHelperCalls("confirmed", []string{"services-control", verb, unit})
				want := []auditRow{{"yonetici", "services." + verb, unit, "kritik servis, kullanıcı onayı alındı", true}}
				if got := e.auditRows(); !reflect.DeepEqual(got, want) {
					t.Errorf("audit = %+v, want %+v", got, want)
				}
			})
		}
	}
}

func TestServicesCriticalUnitHarmlessVerbsNeedNoConfirmation(t *testing.T) {
	e := newEnv(t)
	for _, verb := range []string{"start", "enable", "reload"} {
		res := e.do("POST", "/services/ssh.service/"+verb, "", "admin")
		if res.Status != 200 {
			t.Errorf("%s: status %d: %s", verb, res.Status, res.Body)
		}
	}
	e.wantHelperCalls("harmless verbs",
		[]string{"services-control", "start", "ssh.service"},
		[]string{"services-control", "enable", "ssh.service"},
		[]string{"services-control", "reload", "ssh.service"})
}

func TestServicesProtectedUnitsRefused(t *testing.T) {
	denied := map[string][]string{
		"dbus.service":             {"stop", "disable", "restart"},
		"dbus-broker.service":      {"stop", "disable", "restart"},
		"systemd-journald.service": {"stop", "disable"},
		"systemd-logind.service":   {"stop", "disable"},
		"systemd-udevd.service":    {"stop", "disable"},
	}
	e := newEnv(t)
	n := 0
	for unit, verbs := range denied {
		for _, verb := range verbs {
			for _, body := range []string{"", confirmBody(unit)} {
				res := e.do("POST", "/services/"+unit+"/"+verb, body, "admin")
				if res.Status != http.StatusForbidden || res.code() != "protected_unit" {
					t.Errorf("%s %s (body %q): status %d code %q", verb, unit, body, res.Status, res.code())
				}
				n++
			}
		}
	}
	e.wantNoHelperCalls("protected units")
	rows := e.auditRows()
	if len(rows) != n {
		t.Errorf("%d audit rows for %d refused requests", len(rows), n)
	}
	for _, r := range rows {
		if r.Success || r.Username != "yonetici" {
			t.Errorf("refusal audited as %+v", r)
		}
	}
}

func TestServicesProtectedUnitRestartNeedsConfirmation(t *testing.T) {
	e := newEnv(t)
	res := e.do("POST", "/services/systemd-journald.service/restart", "", "admin")
	if res.Status != http.StatusConflict || res.code() != "confirmation_required" {
		t.Errorf("status %d code %q", res.Status, res.code())
	}
	e.wantNoHelperCalls("unconfirmed restart of journald")
	res = e.do("POST", "/services/systemd-journald.service/restart", confirmBody("systemd-journald.service"), "admin")
	if res.Status != 200 {
		t.Errorf("confirmed: status %d: %s", res.Status, res.Body)
	}
	e.wantHelperCalls("confirmed", []string{"services-control", "restart", "systemd-journald.service"})
}

func TestServicesAliasesCannotBypassRules(t *testing.T) {
	e := newEnv(t)
	cases := []struct{ unit, verb string }{
		{"sshd.service", "stop"},
		{"sshd.service", "disable"},
		{"sshd.service", "restart"},
		{"dbus-org.freedesktop.login1.service", "stop"},
		{"dbus-org.freedesktop.login1.service", "disable"},
	}
	for _, c := range cases {
		for _, body := range []string{"", confirmBody(c.unit), confirmBody("ssh.service")} {
			res := e.do("POST", "/services/"+c.unit+"/"+c.verb, body, "admin")
			if res.Status == 200 {
				t.Errorf("%s %s (body %q) was accepted", c.verb, c.unit, body)
			}
		}
	}
	// An alias name of a critical unit must itself demand confirmation.
	res := e.do("POST", "/services/sshd.service/stop", "", "admin")
	if res.Status < 400 || res.Status >= 500 {
		t.Errorf("sshd.service stop: status %d", res.Status)
	}
	e.wantNoHelperCalls("alias names")
}

func TestServicesMaskingIsImpossible(t *testing.T) {
	e := newEnv(t)
	for _, verb := range []string{"mask", "unmask", "kill", "edit", "isolate", "daemon-reload",
		"reenable", "try-restart", "STOP", "stop%20--now", "--now", "logs", "poweroff"} {
		for _, unit := range []string{"cron.service", "ssh.service"} {
			res := e.do("POST", "/services/"+unit+"/"+verb, confirmBody(unit), "admin")
			if res.Status != http.StatusBadRequest && res.Status != http.StatusNotFound &&
				res.Status != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: status %d: %s", verb, unit, res.Status, res.Body)
			}
		}
	}
	e.wantNoHelperCalls("unsupported verbs")
}

func TestServicesInvalidUnitNamesRefused(t *testing.T) {
	e := newEnv(t)
	units := []string{
		"cron", "cron.socket", "--now.service", "-f.service", "getty@.service", "user@.service",
		"a%20b.service", "cron.service%20", "%20cron.service", "cron%0A.service", "cron.service%0A",
		"cron%3Breboot.service", "%24(id).service", "cron%7Cid.service", "cron%26.service",
		"cron*.service", "..%2Fcron.service", "%2Fetc%2Fcron.service", "cron%00.service",
		"cron%5C.service", "cron%5Cn.service", strings.Repeat("a", 129) + ".service", "CRON.SERVICE",
	}
	for _, unit := range units {
		res := e.do("POST", "/services/"+unit+"/restart", "", "admin")
		if res.Status < 400 || res.Status >= 500 {
			t.Errorf("action on %q: status %d: %s", unit, res.Status, res.Body)
		}
		res = e.do("GET", "/services/"+unit+"/logs", "", "admin")
		// Logs of a template name are harmless; everything else is refused.
		if strings.HasSuffix(unit, "@.service") {
			continue
		}
		if res.Status < 400 || res.Status >= 500 {
			t.Errorf("logs of %q: status %d: %s", unit, res.Status, res.Body)
		}
	}
	calls := e.helperCalls()
	for _, c := range calls {
		if len(c) == 3 && c[0] == "services-logs" && strings.HasSuffix(c[1], "@.service") {
			continue
		}
		t.Errorf("invalid name reached the privileged runner: %v", c)
	}
}

func TestServicesActionRejectsBadBody(t *testing.T) {
	e := newEnv(t)
	for _, body := range []string{
		`{"confirm":"ssh.service","force":true}`,
		`{"confirm":["ssh.service"]}`,
		`{"confirm":"ssh.service"} {"confirm":"x"}`,
		`ssh.service`,
		`[`,
	} {
		res := e.do("POST", "/services/ssh.service/stop", body, "admin")
		if res.Status != http.StatusBadRequest {
			t.Errorf("body %q: status %d: %s", body, res.Status, res.Body)
		}
	}
	e.wantNoHelperCalls("malformed bodies")
}

func TestServicesActionStateChecks(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		unit, verb string
		status     int
	}{
		{"auditd.service", "start", http.StatusNotFound},        // known name, not installed
		{"nosuchthing.service", "start", http.StatusNotFound},   // unknown
		{"kilitli.service", "start", http.StatusConflict},       // masked
		{"kilitli.service", "restart", http.StatusConflict},     // masked
		{"kilitli.service", "enable", http.StatusConflict},      // masked
		{"cron.service", "reload", http.StatusBadRequest},       // CanReload=no
		{"ufw.service", "reload", http.StatusBadRequest},        // CanReload=no
		{"getty@tty1.service", "reload", http.StatusBadRequest}, // no details known
	}
	for _, c := range cases {
		res := e.do("POST", "/services/"+c.unit+"/"+c.verb, "", "admin")
		if res.Status != c.status {
			t.Errorf("%s %s: status %d, want %d: %s", c.verb, c.unit, res.Status, c.status, res.Body)
		}
	}
	e.wantNoHelperCalls("state checks")
}

func TestServicesActionHelperFailure(t *testing.T) {
	e := newEnv(t)
	res := e.do("POST", "/services/accounts-daemon.service/restart", "", "admin")
	if res.Status != http.StatusBadGateway || res.code() != "service_action_failed" {
		t.Fatalf("status %d code %q: %s", res.Status, res.code(), res.Body)
	}
	if want := "Servis işlemi başarısız oldu. Ayrıntılar için servis loglarına bakın."; res.Env.Error.Message != want {
		t.Errorf("message = %q, want the helper's message", res.Env.Error.Message)
	}
	res = e.do("POST", "/services/apparmor.service/restart", "", "admin")
	if res.Status != http.StatusBadGateway {
		t.Fatalf("status %d: %s", res.Status, res.Body)
	}
	if res.Env.Error.Message != "Servis yeniden başlatılamadı." {
		t.Errorf("message = %q, want the generic Turkish sentence", res.Env.Error.Message)
	}
	if strings.Contains(res.Body, "/etc/secret") || strings.Contains(res.Body, "exit") {
		t.Errorf("internal error leaked: %s", res.Body)
	}
	want := []auditRow{
		{"yonetici", "services.restart", "accounts-daemon.service", "başarısız", false},
		{"yonetici", "services.restart", "apparmor.service", "başarısız", false},
	}
	if got := e.auditRows(); !reflect.DeepEqual(got, want) {
		t.Errorf("audit = %+v, want %+v", got, want)
	}
}

func TestServicesActionRefreshesList(t *testing.T) {
	e := newEnv(t)
	if res := e.do("GET", "/services", "", "admin"); res.Status != 200 {
		t.Fatalf("status %d", res.Status)
	}
	before, _ := os.ReadFile(e.systemctlLog)
	if res := e.do("POST", "/services/cron.service/restart", "", "admin"); res.Status != 200 {
		t.Fatalf("status %d", res.Status)
	}
	after, _ := os.ReadFile(e.systemctlLog)
	if len(after) <= len(before) {
		t.Error("the cached list was not rebuilt after an action")
	}
}

func logLines(t *testing.T, res response) (lines []string, requested int) {
	t.Helper()
	var data struct {
		Unit      string   `json:"unit"`
		Lines     []string `json:"lines"`
		Requested int      `json:"requested"`
	}
	if err := json.Unmarshal(res.Env.Data, &data); err != nil {
		t.Fatalf("log response: %v: %s", err, res.Body)
	}
	if data.Lines == nil {
		t.Error("lines must be [] rather than null")
	}
	return data.Lines, data.Requested
}

func TestServicesLogsLineCap(t *testing.T) {
	cases := []struct {
		query     string
		wantLines int
	}{
		{"", 200},
		{"?lines=1", 1},
		{"?lines=10", 10},
		{"?lines=500", 500},
		{"?lines=501", 500},
		{"?lines=100000", 500},
		{"?lines=9223372036854775807", 500},
	}
	for _, c := range cases {
		t.Run("lines"+c.query, func(t *testing.T) {
			e := newEnv(t)
			res := e.do("GET", "/services/cron.service/logs"+c.query, "", "admin")
			if res.Status != 200 {
				t.Fatalf("status %d: %s", res.Status, res.Body)
			}
			e.wantHelperCalls("logs", []string{"services-logs", "cron.service", strconv.Itoa(c.wantLines)})
			lines, requested := logLines(t, res)
			if requested != c.wantLines {
				t.Errorf("requested = %d, want %d", requested, c.wantLines)
			}
			// The fake helper prints 600 lines whatever it is asked for.
			if len(lines) != c.wantLines {
				t.Fatalf("got %d lines, want %d", len(lines), c.wantLines)
			}
			if last := lines[len(lines)-1]; !strings.HasSuffix(last, "satır 600") {
				t.Errorf("last line = %q, want the newest entry", last)
			}
		})
	}
}

func TestServicesLogsRejectBadLineCount(t *testing.T) {
	e := newEnv(t)
	for _, q := range []string{"0", "-1", "-500", "abc", "10abc", "1.5", "1e3", "%20", "99999999999999999999999"} {
		res := e.do("GET", "/services/cron.service/logs?lines="+q, "", "admin")
		if res.Status != http.StatusBadRequest {
			t.Errorf("lines=%s: status %d: %s", q, res.Status, res.Body)
		}
	}
	e.wantNoHelperCalls("invalid line counts")
}

func TestServicesLogStream(t *testing.T) {
	e := newEnv(t)
	res := e.do("GET", "/services/cron.service/logs/stream", "", "admin")
	if res.Status != 200 {
		t.Fatalf("status %d: %s", res.Status, res.Body)
	}
	e.wantHelperCalls("stream", []string{"services-logs-follow", "cron.service"})
	var events []string
	for _, block := range strings.Split(res.Body, "\n\n") {
		if strings.HasPrefix(block, "event: ") {
			events = append(events, block)
		}
	}
	want := []string{
		"event: log\ndata: " + mustJSON("2026-09-29T10:00:01+0300 sunucu cron[812]: birinci"),
		"event: log\ndata: " + mustJSON("2026-09-29T10:00:02+0300 sunucu cron[812]: ikinci"),
		"event: end\ndata: \"closed\"",
	}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("events = %q\nwant %q", events, want)
	}
	if len(e.mod.followers) != 0 {
		t.Errorf("follower slot not released: %d in use", len(e.mod.followers))
	}
}

func TestServicesLogStreamFollowerCap(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < maxFollowers; i++ {
		e.mod.followers <- struct{}{}
	}
	res := e.do("GET", "/services/cron.service/logs/stream", "", "admin")
	if res.Status != http.StatusTooManyRequests {
		t.Errorf("status %d: %s", res.Status, res.Body)
	}
	e.wantNoHelperCalls("stream over the follower cap")
	if len(e.mod.followers) != maxFollowers {
		t.Errorf("refused stream changed the follower count to %d", len(e.mod.followers))
	}
}
