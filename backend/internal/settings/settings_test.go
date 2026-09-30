package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"myserver/internal/audit"
	"myserver/internal/database"
	"myserver/internal/httpx"
	"myserver/internal/privileged"
	"myserver/migrations"
)

type env struct {
	t     *testing.T
	db    *sql.DB
	store *Store
	api   *API
	h     http.Handler
	dir   string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open(filepath.Join(dir, "db", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	// The helper path does not exist: appliers fail without running anything.
	priv := privileged.New(filepath.Join(dir, "no-such-helper"))
	api := NewAPI(store, audit.New(db), priv, func(*http.Request) audit.Actor {
		return audit.Actor{Username: "admin", IP: "192.0.2.1"}
	})
	mux := http.NewServeMux()
	api.Register(httpx.NewRouter(mux).Group("/api/v1"), func(next http.Handler) http.Handler { return next })
	return &env{t: t, db: db, store: store, api: api, h: mux, dir: dir}
}

type result struct {
	Status int
	Raw    string
	Data   map[string]string
	Code   string
}

func (e *env) put(body any) result {
	e.t.Helper()
	var raw string
	switch b := body.(type) {
	case string:
		raw = b
	default:
		j, err := json.Marshal(b)
		if err != nil {
			e.t.Fatal(err)
		}
		raw = string(j)
	}
	return e.request("PUT", raw)
}

func (e *env) request(method, body string) result {
	e.t.Helper()
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, httptest.NewRequest(method, "/api/v1/settings", strings.NewReader(body)))
	var env struct {
		Data  map[string]string `json:"data"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		e.t.Fatalf("not an envelope: %v: %s", err, rec.Body.String())
	}
	res := result{Status: rec.Code, Raw: rec.Body.String(), Data: env.Data}
	if env.Error != nil {
		res.Code = env.Error.Code
	}
	return res
}

func (e *env) stored() map[string]string {
	e.t.Helper()
	rows, err := e.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			e.t.Fatal(err)
		}
		out[k] = v
	}
	return out
}

func TestStoreDefaultsAndPersistence(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if e.store.Bool(KeySetupComplete) {
		t.Fatal("setup is complete on an empty database")
	}
	if got := e.store.Int(KeySessionHours, 99); got != 12 {
		t.Fatalf("default session hours %d", got)
	}
	if got := e.store.Get("no.such.key"); got != "" {
		t.Fatalf("unknown key returned %q", got)
	}
	if got := e.store.Int("no.such.key", 7); got != 7 {
		t.Fatalf("fallback not used: %d", got)
	}
	if got := e.store.Strings(KeyAllowedRoots); len(got) != 4 || got[0] != "/home" {
		t.Fatalf("default roots %v", got)
	}
	if err := e.store.Set(ctx, "", "x"); err == nil {
		t.Fatal("empty key accepted")
	}
	if err := e.store.Set(ctx, KeySessionHours, "24"); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Set(ctx, KeySessionHours, "48"); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetStrings(ctx, KeyAllowedRoots, nil); err != nil {
		t.Fatal(err)
	}
	if got := e.store.Get(KeyAllowedRoots); got != "[]" {
		t.Fatalf("nil list stored as %q", got)
	}
	// A new store over the same database sees the same values.
	again, err := NewStore(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Int(KeySessionHours, 0); got != 48 {
		t.Fatalf("reloaded value %d", got)
	}
	if n := len(e.stored()); n != 2 {
		t.Fatalf("%d rows stored, want 2", n)
	}
	all := again.All()
	if all[KeySessionHours] != "48" || all[KeyLanguage] != "tr" {
		t.Fatalf("All() = %v", all)
	}
	// All returns a copy.
	all[KeyLanguage] = "xx"
	if again.Get(KeyLanguage) != "tr" {
		t.Fatal("All() exposes the internal map")
	}
}

func TestPutRefusesUnknownAndInternalKeys(t *testing.T) {
	e := newEnv(t)
	for _, key := range []string{
		KeySetupComplete, "setup.complete ", "SETUP.COMPLETE", "unknown.key", "", "terminal.user",
		"files.allowed_roots\x00", "security.session_hours; DROP TABLE settings",
	} {
		res := e.put(map[string]string{key: "true"})
		if res.Status != http.StatusBadRequest {
			t.Errorf("key %q: status %d, want 400 (%s)", key, res.Status, res.Raw)
		}
	}
	// An internal key next to a valid one refuses the whole request.
	for i := 0; i < 20; i++ {
		res := e.put(map[string]string{KeySessionHours: "24", KeySetupComplete: "true"})
		if res.Status != http.StatusBadRequest {
			t.Fatalf("status %d", res.Status)
		}
	}
	if e.store.Bool(KeySetupComplete) {
		t.Fatal("setup.complete was written through the API")
	}
	if got := e.stored(); len(got) != 0 {
		t.Fatalf("refused requests stored %v", got)
	}
	if n := e.store.Int(KeySessionHours, 0); n != 12 {
		t.Fatalf("session hours changed to %d", n)
	}
}

func TestGetExposesOnlyWritableKeys(t *testing.T) {
	e := newEnv(t)
	if err := e.store.Set(context.Background(), KeySetupComplete, "true"); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Set(context.Background(), "module.secret_token", "gizli-deger"); err != nil {
		t.Fatal(err)
	}
	res := e.request("GET", "")
	if res.Status != 200 {
		t.Fatalf("status %d", res.Status)
	}
	if _, ok := res.Data[KeySetupComplete]; ok {
		t.Error("internal key setup.complete is listed")
	}
	if strings.Contains(res.Raw, "gizli-deger") {
		t.Error("a key no module made writable is exposed")
	}
	for _, k := range []string{KeyHostname, KeyTimezone, KeyLanguage, KeyTerminalEnabled, KeyTerminalTimeout, KeySessionHours, KeyAllowedRoots} {
		if _, ok := res.Data[k]; !ok {
			t.Errorf("key %s missing", k)
		}
	}
}

func TestPutValidators(t *testing.T) {
	e := newEnv(t)
	bad := []map[string]string{
		{KeySessionHours: "0"},
		{KeySessionHours: "721"},
		{KeySessionHours: "-1"},
		{KeySessionHours: "12.5"},
		{KeySessionHours: "abc"},
		{KeySessionHours: ""},
		{KeySessionHours: "99999999999999999999"},
		{KeyTerminalTimeout: "0"},
		{KeyTerminalTimeout: "241"},
		{KeyTerminalEnabled: "yes"},
		{KeyTerminalEnabled: ""},
		{KeyTerminalEnabled: "2"},
		{KeyLanguage: "en"},
		{KeyLanguage: "TR"},
		{KeyLanguage: ""},
		{KeyHostname: "bad host"},
		{KeyHostname: "-host"},
		{KeyHostname: "host-"},
		{KeyHostname: "a.b"},
		{KeyHostname: "host;reboot"},
		{KeyHostname: "$(reboot)"},
		{KeyHostname: ""},
		{KeyHostname: strings.Repeat("a", 64)},
		{KeyTimezone: "../../etc/passwd"},
		{KeyTimezone: "/etc/passwd"},
		{KeyTimezone: "Europe/../../etc/passwd"},
		{KeyTimezone: "Mars/Olympus_Mons"},
		{KeyTimezone: ""},
		{KeyTimezone: "Europe/Istanbul; reboot"},
	}
	for _, b := range bad {
		res := e.put(b)
		if res.Status != http.StatusBadRequest {
			t.Errorf("%v: status %d, want 400 (%s)", b, res.Status, res.Raw)
		}
	}
	for _, raw := range []string{``, `{}`, `[]`, `{"security.session_hours":24}`, `{"terminal.enabled":true}`, `{"security.session_hours":null,"x":1}`, `not json`} {
		if res := e.request("PUT", raw); res.Status != http.StatusBadRequest {
			t.Errorf("body %q: status %d, want 400", raw, res.Status)
		}
	}
	if got := e.stored(); len(got) != 0 {
		t.Fatalf("refused requests stored %v", got)
	}

	res := e.put(map[string]string{KeySessionHours: " 24 ", KeyTerminalEnabled: "0", KeyTerminalTimeout: "240", KeyLanguage: "tr"})
	if res.Status != 200 {
		t.Fatalf("valid request: %d %s", res.Status, res.Raw)
	}
	// Values are stored normalized.
	if e.store.Get(KeySessionHours) != "24" || e.store.Get(KeyTerminalEnabled) != "false" || e.store.Get(KeyTerminalTimeout) != "240" {
		t.Fatalf("stored %v", e.stored())
	}
	if res.Data[KeySessionHours] != "24" || res.Data[KeyTerminalEnabled] != "false" {
		t.Fatalf("response %v", res.Data)
	}
}

func TestPutChangesNothingWhenAnyValueIsInvalid(t *testing.T) {
	e := newEnv(t)
	// Map iteration order is random; repeat so that every order occurs.
	for i := 0; i < 40; i++ {
		res := e.put(map[string]string{
			KeySessionHours:    "48",
			KeyTerminalEnabled: "false",
			KeyLanguage:        "tr",
			KeyAllowedRoots:    `["/srv/veri"]`,
			KeyTerminalTimeout: "9999", // invalid
		})
		if res.Status != http.StatusBadRequest {
			t.Fatalf("status %d", res.Status)
		}
		if got := e.stored(); len(got) != 0 {
			t.Fatalf("round %d: a request with one invalid value stored %v", i, got)
		}
		if e.store.Get(KeySessionHours) != "12" || e.store.Get(KeyTerminalEnabled) != "true" {
			t.Fatalf("round %d: cached values changed", i)
		}
	}
	if n := countAudit(t, e.db); n != 0 {
		t.Fatalf("%d audit records for refused requests", n)
	}
}

func countAudit(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPutAuditsChanges(t *testing.T) {
	e := newEnv(t)
	if res := e.put(map[string]string{KeySessionHours: "24"}); res.Status != 200 {
		t.Fatalf("status %d", res.Status)
	}
	var user, action, target string
	var ok bool
	if err := e.db.QueryRow(`SELECT username, action, target, success FROM audit_log`).Scan(&user, &action, &target, &ok); err != nil {
		t.Fatal(err)
	}
	if user != "admin" || action != "settings.update" || target != KeySessionHours || !ok {
		t.Fatalf("audit record %q %q %q %v", user, action, target, ok)
	}
	// Writing the same value again is not a change.
	if res := e.put(map[string]string{KeySessionHours: "24"}); res.Status != 200 {
		t.Fatalf("status %d", res.Status)
	}
	if n := countAudit(t, e.db); n != 1 {
		t.Fatalf("%d audit records, want 1", n)
	}
}

func TestApplierFailureStoresNothing(t *testing.T) {
	e := newEnv(t)
	res := e.put(map[string]string{KeyHostname: "yeni-sunucu"})
	if res.Status != http.StatusBadGateway || res.Code != "hostname_failed" {
		t.Fatalf("status %d code %q (%s)", res.Status, res.Code, res.Raw)
	}
	if !strings.Contains(res.Raw, "Sunucu adı değiştirilemedi.") {
		t.Fatalf("message: %s", res.Raw)
	}
	for _, leak := range []string{"no-such-helper", "sudo", "exit", "exec", e.dir, "helper"} {
		if strings.Contains(res.Raw, leak) {
			t.Fatalf("internal detail %q in the response: %s", leak, res.Raw)
		}
	}
	if _, ok := e.stored()[KeyHostname]; ok {
		t.Fatal("hostname stored although it could not be applied")
	}
	var ok bool
	if err := e.db.QueryRow(`SELECT success FROM audit_log WHERE target = ?`, KeyHostname).Scan(&ok); err != nil || ok {
		t.Fatalf("failure not audited: %v %v", err, ok)
	}
}

func TestAllowedRootsRefusals(t *testing.T) {
	e := newEnv(t)
	bad := map[string]string{
		"filesystem root":     `["/"]`,
		"etc":                 `["/etc"]`,
		"etc trailing slash":  `["/etc/"]`,
		"etc via dot":         `["/etc/."]`,
		"etc via dotdot":      `["/home/../etc"]`,
		"etc double slash":    `["//etc"]`,
		"root via dotdot":     `["/home/.."]`,
		"root via many":       `["/home/../../.."]`,
		"proc":                `["/proc"]`,
		"sys":                 `["/sys"]`,
		"dev":                 `["/dev"]`,
		"boot":                `["/boot"]`,
		"root home":           `["/root"]`,
		"run":                 `["/run"]`,
		"var":                 `["/var"]`,
		"usr":                 `["/usr"]`,
		"bin":                 `["/bin"]`,
		"sbin":                `["/sbin"]`,
		"lib":                 `["/lib"]`,
		"lib64":               `["/lib64"]`,
		"data dir":            `["/var/lib/myserver"]`,
		"data dir child":      `["/var/lib/myserver/backups"]`,
		"data dir via dotdot": `["/var/lib/x/../myserver/apps"]`,
		"relative":            `["home"]`,
		"relative dot":        `["./home"]`,
		"relative dotdot":     `["../etc"]`,
		"empty path":          `[""]`,
		"tilde":               `["~/x"]`,
		"NUL byte":            `["/home/a\u0000/etc"]`,
		"valid then invalid":  `["/srv/veri","/etc"]`,
		"invalid then valid":  `["/etc","/srv/veri"]`,
		"not a list":          `"/home"`,
		"object":              `{"a":"/home"}`,
		"list of numbers":     `[1,2]`,
		"nested list":         `[["/home"]]`,
		"not json":            `/home`,
		"empty string":        ``,
		"too many":            manyRoots(33),
	}
	for name, v := range bad {
		res := e.put(map[string]string{KeyAllowedRoots: v})
		if res.Status != http.StatusBadRequest {
			t.Errorf("%s (%s): status %d, want 400", name, v, res.Status)
		}
	}
	if got := e.stored(); len(got) != 0 {
		t.Fatalf("refused roots stored: %v", got)
	}
	if got := e.store.Strings(KeyAllowedRoots); len(got) != 4 {
		t.Fatalf("roots changed to %v", got)
	}
}

func manyRoots(n int) string {
	var l []string
	for i := 0; i < n; i++ {
		l = append(l, "/srv/veri"+strings.Repeat("x", i+1))
	}
	b, _ := json.Marshal(l)
	return string(b)
}

func TestAllowedRootsAccepted(t *testing.T) {
	e := newEnv(t)
	res := e.put(map[string]string{KeyAllowedRoots: `["/srv/veri/","/srv/veri","/srv//medya/./film","/srv/a/../b"]`})
	if res.Status != 200 {
		t.Fatalf("status %d %s", res.Status, res.Raw)
	}
	got := e.store.Strings(KeyAllowedRoots)
	want := []string{"/srv/veri", "/srv/medya/film", "/srv/b"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("stored roots %v, want cleaned and de-duplicated %v", got, want)
	}
	if res := e.put(map[string]string{KeyAllowedRoots: manyRoots(32)}); res.Status != 200 {
		t.Fatalf("32 roots refused: %d", res.Status)
	}
	if res := e.put(map[string]string{KeyAllowedRoots: `[]`}); res.Status != 200 || e.store.Get(KeyAllowedRoots) != "[]" {
		t.Fatalf("empty list: %d %q", res.Status, e.store.Get(KeyAllowedRoots))
	}
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symbolic links here: %v", err)
	}
}

// realTemp returns a temporary directory whose own path has no links in it.
func realTemp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCheckRootReal(t *testing.T) {
	dir := realTemp(t)
	real := filepath.Join(dir, "gercek")
	if err := os.MkdirAll(filepath.Join(real, "alt"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "baglanti")
	symlinkOrSkip(t, real, link)

	if err := CheckRootReal(real); err != nil {
		t.Errorf("real directory refused: %v", err)
	}
	if err := CheckRootReal(filepath.Join(real, "alt")); err != nil {
		t.Errorf("real sub-directory refused: %v", err)
	}
	if err := CheckRootReal(filepath.Join(dir, "yok")); err != nil {
		t.Errorf("missing directory refused: %v", err)
	}
	if err := CheckRootReal(link); err != ErrRootIsLink {
		t.Errorf("symbolic link: %v, want ErrRootIsLink", err)
	}
	if err := CheckRootReal(filepath.Join(link, "alt")); err != ErrRootIsLink {
		t.Errorf("path through a symbolic link: %v, want ErrRootIsLink", err)
	}
}

func TestAllowedRootsRefuseSymlinksAndFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("allowed roots are POSIX paths")
	}
	e := newEnv(t)
	dir := realTemp(t)
	target := filepath.Join(dir, "hedef")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	// Stands in for a system directory: "veri" looks harmless but leads
	// to it. (A path whose target does not exist is accepted by design and
	// re-checked by the file manager on every access.)
	hidden := filepath.Join(dir, "gizli")
	if err := os.MkdirAll(filepath.Join(hidden, "ssh"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "veri")
	symlinkOrSkip(t, hidden, link)
	file := filepath.Join(dir, "dosya")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]string{"link to a directory": link, "below a link": link + "/ssh", "regular file": file} {
		b, _ := json.Marshal([]string{p})
		if res := e.put(map[string]string{KeyAllowedRoots: string(b)}); res.Status != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (%s)", name, res.Status, res.Raw)
		}
	}
	b, _ := json.Marshal([]string{target})
	if res := e.put(map[string]string{KeyAllowedRoots: string(b)}); res.Status != 200 {
		t.Errorf("real directory refused: %d %s", res.Status, res.Raw)
	}
}

func TestValidHostname(t *testing.T) {
	for _, h := range []string{"a", "sunucu", "my-server-01", "0box", strings.Repeat("a", 63)} {
		if !ValidHostname(h) {
			t.Errorf("%q refused", h)
		}
	}
	for _, h := range []string{"", "-a", "a-", "A", "Sunucu", "a.b", "a_b", "a b", "a\n", "a\x00", "a;b", "a/b", "$(id)", "`id`", "--help",
		"sunucu\nreboot", "ş", strings.Repeat("a", 64)} {
		if ValidHostname(h) {
			t.Errorf("%q accepted", h)
		}
	}
}

func TestValidTimezoneRejectsEscapes(t *testing.T) {
	// These must be refused whatever is installed on the machine.
	for _, z := range []string{"", ".", "..", "../etc/passwd", "../../etc/shadow", "/etc/passwd", "Europe/../../etc/passwd", "Europe/./Istanbul",
		"Europe//Istanbul", "Europe/Istanbul/", "/Europe/Istanbul", "Europe/Istanbul\x00", "Europe/Istanbul\n", "Europe/Istanbul;id",
		"-Europe", "a/b/c/d", "Europe\\Istanbul", "zone1970.tab", "posix/../UTC", strings.Repeat("A", 65), "Mars/Olympus_Mons"} {
		if ValidTimezone(z) {
			t.Errorf("%q accepted", z)
		}
	}
}

func TestValidTimezoneInstalled(t *testing.T) {
	if _, err := os.Stat("/usr/share/zoneinfo/UTC"); err != nil {
		t.Skip("no zone database at /usr/share/zoneinfo")
	}
	if !ValidTimezone("UTC") {
		t.Error("UTC refused")
	}
	// A directory of the zone database is not a zone.
	if fi, err := os.Stat("/usr/share/zoneinfo/Europe"); err == nil && fi.IsDir() && ValidTimezone("Europe") {
		t.Error("directory accepted as a zone")
	}
	for _, z := range Timezones() {
		if z == "" || strings.Contains(z, "..") || strings.HasPrefix(z, "/") {
			t.Errorf("zone list contains %q", z)
		}
	}
}
