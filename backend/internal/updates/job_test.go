package updates

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"myserver/internal/audit"
	"myserver/internal/privileged"
)

var testActor = audit.Actor{Username: "yonetici", IP: "-"}

func addAptJob(e *testEnv) *Job {
	job := restoreJob(JobMeta{ID: testJobID, Kind: kindApt, Title: "Ubuntu paket güncellemesi",
		Detail: "Standart yükseltme, çekirdek hariç", Username: "yonetici", StartedAt: 100})
	e.mod.jobs.add(job)
	e.mod.store.saveJob(job.Meta(), "")
	return job
}

func result(id, state, message string) string {
	return "id=" + id + "\nstate=" + state + "\nstarted=100\nfinished=200\nmessage=" + message + "\n"
}

/* ---------- job output ---------- */

func TestJobWriteSplitsLines(t *testing.T) {
	j := newJob(kindApt, "t", "d", "u")
	for _, chunk := range []string{"bir\nik", "i\r\nüç\rdö", "rt", "\n\n\n", "beş"} {
		if n, err := j.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	lines, _ := j.Snapshot(0)
	if want := []string{"bir", "iki", "üç", "dört"}; !reflect.DeepEqual(lines, want) {
		t.Errorf("lines = %q, want %q", lines, want)
	}
	j.Finish("")
	lines, meta := j.Snapshot(0)
	if want := []string{"bir", "iki", "üç", "dört", "beş", "İşlem başarıyla tamamlandı."}; !reflect.DeepEqual(lines, want) {
		t.Errorf("lines = %q", lines)
	}
	if meta.Status != jobSuccess || meta.FinishedAt == nil {
		t.Errorf("meta = %+v", meta)
	}
	if got, _ := j.Snapshot(4); !reflect.DeepEqual(got, []string{"beş", "İşlem başarıyla tamamlandı."}) {
		t.Errorf("Snapshot(4) = %q", got)
	}
	for _, from := range []int{-1, 7, 1 << 40} {
		if got, _ := j.Snapshot(from); len(got) != 6 {
			t.Errorf("Snapshot(%d) returned %d lines", from, len(got))
		}
	}
}

func TestJobHostileOutput(t *testing.T) {
	j := newJob(kindApt, "t", "d", "u")
	input := "renkli \x1b[1;31mkırmızı\x1b[0m metin\n" +
		"ilerleme \x1b[2K\x1b[1G%50\n" +
		"başlık \x1b]0;evil\x07 sonu\n" +
		"geçersiz \xff\xfe\xc3 utf8\n" +
		"denetim \x00\x01\x02\x08\x0b\x0c\x7f karakterleri\n" +
		"sekme\tli\n" +
		privileged.UserMessagePrefix + "yardımcı iletisi\n" +
		"yarım kaçış \x1b[12\n" +
		"son kaçış \x1b\n" +
		strings.Repeat("u", 5000) + "\n" +
		strings.Repeat("ş", 2000) + "\n" +
		"data: {\"x\":1}\n" +
		"event: done\n" +
		" satır ayırıcı \n"
	if _, err := j.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	lines, _ := j.Snapshot(0)
	for i, l := range lines {
		if !utf8.ValidString(l) {
			t.Errorf("line %d is not valid UTF-8: %q", i, l)
		}
		if len(l) > maxLineLength {
			t.Errorf("line %d has %d bytes", i, len(l))
		}
		for _, r := range l {
			if r < 0x20 || r == 0x7f {
				t.Errorf("line %d contains the control character %U: %q", i, r, l)
			}
		}
		if strings.Contains(l, "[0m") || strings.Contains(l, "[1;31m") || strings.Contains(l, "[2K") {
			t.Errorf("line %d contains the remains of an escape sequence: %q", i, l)
		}
		if strings.Contains(l, "yardımcı iletisi") {
			t.Errorf("the helper's protocol line was copied into the output: %q", l)
		}
	}
	want := []string{"renkli kırmızı metin", "ilerleme %50"}
	if len(lines) < 2 || !reflect.DeepEqual(lines[:2], want) {
		t.Errorf("lines = %q", lines[:2])
	}
	if len(lines) != 13 {
		t.Errorf("%d lines: %q", len(lines), lines)
	}
}

func TestJobOutputIsBounded(t *testing.T) {
	j := newJob(kindApt, "t", "d", "u")
	var b strings.Builder
	for i := 0; i < maxJobLines+5000; i++ {
		b.WriteString("satır " + strconv.Itoa(i) + "\n")
	}
	if _, err := j.Write([]byte(b.String())); err != nil {
		t.Fatal(err)
	}
	// A single endless line must not grow without bound either.
	for i := 0; i < 200; i++ {
		if _, err := j.Write([]byte(strings.Repeat("x", 64<<10))); err != nil {
			t.Fatal(err)
		}
	}
	if len(j.partial) > maxLineLength {
		t.Errorf("the unfinished line holds %d bytes", len(j.partial))
	}
	j.Finish("Başarısız.")
	lines, meta := j.Snapshot(0)
	if len(lines) > maxJobLines+3 {
		t.Errorf("%d lines kept", len(lines))
	}
	if last := lines[len(lines)-1]; last != "HATA: Başarısız." {
		t.Errorf("last line = %q; the outcome must always be recorded", last)
	}
	if meta.Status != jobFailed || meta.Message != "Başarısız." {
		t.Errorf("meta = %+v", meta)
	}
	if !strings.Contains(strings.Join(lines[maxJobLines-2:], "\n"), "çıktı çok uzun") {
		t.Error("the cut is not marked")
	}
}

func TestJobSetKeepsRunningJobs(t *testing.T) {
	var s jobSet
	first := newJob(kindApt, "çalışan", "", "u")
	s.add(first)
	for i := 0; i < keepMemJobs*3; i++ {
		j := newJob(kindDockerPull, "biten", "", "u")
		j.Finish("")
		s.add(j)
	}
	if s.get(first.Meta().ID) == nil || s.running(kindApt) != first {
		t.Error("a running job was dropped from memory")
	}
	if s.running(kindDockerPull) != nil {
		t.Error("a finished job is reported running")
	}
	if s.latest(kindApt) != first || s.latest("other") != nil {
		t.Error("latest")
	}
	if len(newJob(kindApt, "", "", "").Meta().ID) != 16 {
		t.Error("job id length")
	}
}

/* ---------- SSE ---------- */

var hostileLines = []string{
	"normal satır",
	"\n\nevent: done\ndata: {\"status\":\"success\"}\n\n",
	"önce\n\nevent: done\ndata: {}\n\nsonra",
	"\r\n\r\nevent: done\r\ndata: {}\r\n\r\n",
	"event: done",
	"data: {\"id\":\"x\",\"status\":\"success\"}",
	"id: 999999",
	": ping",
	"retry: 1",
	"</script><script>alert(1)</script>",
	" event: done data: {}  ",
	"\x1b[31mrenk\x1b[0m \x00\x07 \xff\xfe",
	strings.Repeat("uzun ", 3000),
}

func checkStream(t *testing.T, body string, wantLines int, wantStatus string) {
	t.Helper()
	events := parseSSEStrict(t, body)
	names := []string{}
	total := 0
	for _, ev := range events {
		names = append(names, ev.Name)
		switch ev.Name {
		case "start":
			var v struct {
				Job  JobMeta `json:"job"`
				From int     `json:"from"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &v); err != nil || v.Job.ID != testJobID {
				t.Errorf("start event: %v %q", err, ev.Data)
			}
		case "lines":
			var v linesEvent
			if err := json.Unmarshal([]byte(ev.Data), &v); err != nil {
				t.Errorf("lines event: %v", err)
			}
			if v.From != total {
				t.Errorf("lines event starts at %d, want %d", v.From, total)
			}
			total += len(v.Lines)
			if ev.ID != strconv.Itoa(total) {
				t.Errorf("lines event id = %q, want %d", ev.ID, total)
			}
			for _, l := range v.Lines {
				if strings.ContainsAny(l, "\n\r") {
					t.Errorf("a line spans lines: %q", l)
				}
			}
		case "done":
			var v JobMeta
			if err := json.Unmarshal([]byte(ev.Data), &v); err != nil || v.ID != testJobID || v.Status != wantStatus {
				t.Errorf("done event: %v %q", err, ev.Data)
			}
		default:
			t.Errorf("unexpected event %q", ev.Name)
		}
	}
	if len(names) < 2 || names[0] != "start" || names[len(names)-1] != "done" {
		t.Errorf("events = %q", names)
	}
	if n := strings.Count(strings.Join(names, " "), "done"); n != 1 {
		t.Errorf("%d done events: %q", n, names)
	}
	if total != wantLines {
		t.Errorf("%d lines streamed, want %d", total, wantLines)
	}
	// Seen as raw bytes, too: exactly one line of the stream says "done".
	if n := strings.Count(body, "\nevent: done\n"); n != 1 {
		t.Errorf("the raw stream contains %d completion events", n)
	}
	if strings.Contains(body, "\r") {
		t.Error("the raw stream contains a carriage return")
	}
}

func TestJobStreamCannotBeForged(t *testing.T) {
	e := newEnv(t)
	job := addAptJob(e)
	for _, l := range hostileLines {
		if _, err := job.Write([]byte(l + "\n")); err != nil {
			t.Fatal(err)
		}
		job.Println(l)
	}
	job.Finish("Başarısız\n\nevent: done\ndata: {\"status\":\"success\"}\n\n")
	lines, _ := job.Snapshot(0)
	e.mod.store.saveJob(job.Meta(), job.Log())

	res := e.do("GET", "/updates/jobs/"+testJobID+"/stream", "", "admin")
	if res.Status != 200 {
		t.Fatalf("status %d: %s", res.Status, res.Body)
	}
	checkStream(t, res.Body, len(lines), jobFailed)

	// The same job served from the history after a restart.
	m2, err := New(e.deps, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.mod.jobs = jobSet{}
	_ = m2
	res = e.do("GET", "/updates/jobs/"+testJobID+"/stream", "", "admin")
	if res.Status != 200 {
		t.Fatalf("history: status %d: %s", res.Status, res.Body)
	}
	checkStream(t, res.Body, len(lines), jobFailed)

	var d jobDetail
	e.do("GET", "/updates/jobs/"+testJobID, "", "admin").into(t, &d)
	if !reflect.DeepEqual(d.Lines, lines) || d.Status != jobFailed {
		t.Errorf("history detail: %d lines, status %s", len(d.Lines), d.Status)
	}
}

func TestJobStreamResume(t *testing.T) {
	e := newEnv(t)
	job := addAptJob(e)
	for i := 0; i < 10; i++ {
		job.Println("satır " + strconv.Itoa(i))
	}
	job.Finish("")
	for _, c := range []struct {
		last string
		from int
	}{{"", 0}, {"4", 4}, {"11", 11}, {"12", 0}, {"-3", 0}, {"abc", 0}, {"99999999999999999999", 0}, {"4\n5", 0}} {
		res := e.do("GET", "/updates/jobs/"+testJobID+"/stream", "", "admin", "Last-Event-ID", c.last)
		if res.Status != 200 {
			t.Fatalf("Last-Event-ID %q: status %d", c.last, res.Status)
		}
		events := parseSSEStrict(t, res.Body)
		var start struct {
			From int `json:"from"`
		}
		if err := json.Unmarshal([]byte(events[0].Data), &start); err != nil || start.From != c.from {
			t.Errorf("Last-Event-ID %q: start from %d, want %d", c.last, start.From, c.from)
		}
		n := 0
		for _, ev := range events {
			if ev.Name == "lines" {
				var v linesEvent
				json.Unmarshal([]byte(ev.Data), &v)
				n += len(v.Lines)
			}
		}
		if n != 11-c.from {
			t.Errorf("Last-Event-ID %q: %d lines, want %d", c.last, n, 11-c.from)
		}
	}
}

func TestJobRoutesValidateTheID(t *testing.T) {
	e := newEnv(t)
	for _, id := range []string{"x", "0123456789abcde", "0123456789abcdef0", "0123456789ABCDEF", "..%2F..%2Fetc",
		"0123456789abcde%0A", "%27%20OR%201=1--%20aa", "0123456789abcdeg"} {
		for _, suffix := range []string{"", "/stream"} {
			res := e.do("GET", "/updates/jobs/"+id+suffix, "", "admin")
			if res.Status != http.StatusBadRequest && res.Status != http.StatusNotFound {
				t.Errorf("%q%s: status %d", id, suffix, res.Status)
			}
		}
	}
	res := e.do("GET", "/updates/jobs/ffffffffffffffff", "", "admin")
	if res.Status != http.StatusNotFound {
		t.Errorf("unknown job: status %d", res.Status)
	}
	res = e.do("GET", "/updates/jobs", "", "admin")
	if res.Status != 200 || string(res.Env.Data) != "[]" {
		t.Errorf("empty job list: %d %s", res.Status, res.Env.Data)
	}
}

func TestJobHistoryIsBounded(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < keepJobs+20; i++ {
		j := newJob(kindDockerPull, "t", "", "u")
		j.meta.StartedAt = int64(1000 + i)
		j.Finish("")
		e.mod.store.saveJob(j.Meta(), j.Log())
	}
	var list []JobMeta
	e.do("GET", "/updates/jobs", "", "admin").into(t, &list)
	if len(list) != keepJobs {
		t.Errorf("%d jobs listed", len(list))
	}
	if list[0].StartedAt != int64(1000+keepJobs+19) {
		t.Errorf("newest first: %+v", list[0])
	}
	var n int
	e.db.QueryRow(`SELECT COUNT(*) FROM updates_jobs`).Scan(&n)
	if n != keepJobs {
		t.Errorf("%d jobs stored", n)
	}
}

/* ---------- the log reader ---------- */

func TestCopyLog(t *testing.T) {
	e := newEnv(t)
	job := newJob(kindApt, "t", "", "u")
	count := func() int { l, _ := job.Snapshot(0); return len(l) }

	// The file does not exist yet.
	if off := copyLog(job, 0); off != 0 || count() != 0 {
		t.Errorf("missing file: offset %d, %d lines", off, count())
	}
	if off := copyLog(job, 500); off != 500 {
		t.Errorf("missing file: offset moved to %d", off)
	}
	// It grows.
	e.write("state/apt-upgrade.log", "bir\niki\nya", 0o644)
	off := copyLog(job, 0)
	if off != int64(len("bir\niki\nya")) || count() != 2 {
		t.Errorf("offset %d, %d lines", off, count())
	}
	if again := copyLog(job, off); again != off || count() != 2 {
		t.Errorf("unchanged file read again: offset %d, %d lines", again, count())
	}
	e.write("state/apt-upgrade.log", "bir\niki\nyarım satır tamamlandı\nüç\n", 0o644)
	off = copyLog(job, off)
	lines, _ := job.Snapshot(0)
	if want := []string{"bir", "iki", "yarım satır tamamlandı", "üç"}; !reflect.DeepEqual(lines, want) {
		t.Errorf("lines = %q", lines)
	}
	// It is replaced by a shorter one.
	e.write("state/apt-upgrade.log", "yeni\n", 0o644)
	off = copyLog(job, off)
	if off != int64(len("yeni\n")) || count() != 5 {
		t.Errorf("replaced file: offset %d, %d lines", off, count())
	}
	// Very long lines, invalid UTF-8 and terminal sequences.
	e.write("state/apt-upgrade.log", "yeni\n"+strings.Repeat("z", 300000)+"\n\xff\xfe\x1b[31mkırmızı\x1b[0m\x00\n", 0o644)
	copyLog(job, off)
	lines, _ = job.Snapshot(0)
	if len(lines) != 7 || len(lines[5]) != maxLineLength || lines[6] != "?kırmızı" {
		t.Errorf("%d lines, long line %d bytes, last %q", len(lines), len(lines[5]), lines[len(lines)-1])
	}
}

func TestCopyLogRefusesLinksAndStopsAtTheCap(t *testing.T) {
	e := newEnv(t)
	job := newJob(kindApt, "t", "", "u")
	e.write("secret", "root:$6$gizli\n", 0o600)
	if err := os.Symlink(e.path("secret"), aptLogFile); err != nil {
		t.Fatal(err)
	}
	if off := copyLog(job, 0); off != 0 {
		t.Errorf("offset %d", off)
	}
	if lines, _ := job.Snapshot(0); len(lines) != 0 {
		t.Errorf("a file was read through a symbolic link: %q", lines)
	}
	os.Remove(aptLogFile)
	if err := os.Mkdir(aptLogFile, 0o755); err != nil {
		t.Fatal(err)
	}
	if off := copyLog(job, 0); off != 0 {
		t.Errorf("directory: offset %d", off)
	}
	os.Remove(aptLogFile)

	f, err := os.Create(aptLogFile)
	if err != nil {
		t.Fatal(err)
	}
	chunk := []byte(strings.Repeat("x", 1023) + "\n")
	for i := 0; i < (aptMaxLogRead>>10)+2048; i++ {
		if _, err := f.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	off := copyLog(job, 0)
	if off < aptMaxLogRead || off > aptMaxLogRead+(64<<10) {
		t.Errorf("offset %d after an oversized log", off)
	}
	if again := copyLog(job, off); again != off {
		t.Errorf("reading continued past the cap: %d", again)
	}
}

func TestReadSmallFile(t *testing.T) {
	e := newEnv(t)
	if _, ok := readSmallFile(e.path("missing"), 100); ok {
		t.Error("missing file read")
	}
	e.write("small", "içerik", 0o644)
	if got, ok := readSmallFile(e.path("small"), 100); !ok || got != "içerik" {
		t.Errorf("got %q %v", got, ok)
	}
	e.write("big", strings.Repeat("a", 1000), 0o644)
	if got, ok := readSmallFile(e.path("big"), 100); !ok || len(got) != 100 {
		t.Errorf("got %d bytes %v", len(got), ok)
	}
	if err := os.Symlink(e.path("small"), e.path("link")); err != nil {
		t.Fatal(err)
	}
	if _, ok := readSmallFile(e.path("link"), 100); ok {
		t.Error("symbolic link followed")
	}
	if _, ok := readSmallFile(e.dir, 100); ok {
		t.Error("directory read")
	}
}

/* ---------- following the unit ---------- */

type followCase struct {
	name    string
	result  string // content of the result file; "" for none
	active  bool   // systemctl is-active says active ... until the result appears
	show    string // systemctl show output; "" for a unit systemd no longer knows
	status  string
	message string
}

const (
	showSuccess = "LoadState=loaded\nActiveState=inactive\nResult=success\nExecMainCode=1\nExecMainStatus=0\n"
	showFailed  = "LoadState=loaded\nActiveState=failed\nResult=exit-code\nExecMainCode=1\nExecMainStatus=100\n"
	showKilled  = "LoadState=loaded\nActiveState=inactive\nResult=signal\nExecMainCode=2\nExecMainStatus=9\n"
	showGone    = "LoadState=not-found\nActiveState=inactive\nResult=success\nExecMainCode=0\nExecMainStatus=0\n"
	showNever   = "LoadState=loaded\nActiveState=inactive\nResult=success\nExecMainCode=0\nExecMainStatus=0\n"

	msgUnexpected = "Paket güncellemesi beklenmedik biçimde sonlandı. Ayrıntılar için işlem çıktısına bakın."
	msgGeneric    = "Paket güncellemesi başarısız oldu. Ayrıntılar için işlem çıktısına bakın."
)

func TestFollowAptJobOutcome(t *testing.T) {
	other := "ffffffffffffffff"
	cases := []followCase{
		{name: "result success", result: result(testJobID, "success", ""), status: jobSuccess},
		{name: "result success although systemd says failed", result: result(testJobID, "success", ""), show: showFailed, status: jobSuccess},
		{name: "result failed with message", result: result(testJobID, "failed", "Diskte yeterli boş alan yok. Paket işlemi tamamlanamadı."),
			show: showSuccess, status: jobFailed, message: "Diskte yeterli boş alan yok. Paket işlemi tamamlanamadı."},
		{name: "result failed without message", result: result(testJobID, "failed", ""), status: jobFailed, message: msgGeneric},
		{name: "no result, systemd success", show: showSuccess, status: jobSuccess},
		{name: "no result, systemd failed", show: showFailed, status: jobFailed, message: msgUnexpected},
		{name: "no result, systemd killed", show: showKilled, status: jobFailed, message: msgUnexpected},
		{name: "no result, unit gone", show: showGone, status: jobFailed, message: msgUnknownResult},
		{name: "no result, unit never ran", show: showNever, status: jobFailed, message: msgUnknownResult},
		{name: "no result, no systemd answer", status: jobFailed, message: msgUnknownResult},
		{name: "still running per file, systemd success", result: result(testJobID, "running", ""), show: showSuccess, status: jobSuccess},
		{name: "still running per file, systemd failed", result: result(testJobID, "running", ""), show: showFailed, status: jobFailed, message: msgUnexpected},
		{name: "still running per file, unit gone", result: result(testJobID, "running", ""), status: jobFailed, message: msgUnknownResult},
		{name: "result of another job, systemd success", result: result(other, "failed", "başka iş"), show: showSuccess, status: jobSuccess},
		{name: "result of another job, unit gone", result: result(other, "success", ""), status: jobFailed, message: msgUnknownResult},
		{name: "unknown state, systemd success", result: result(testJobID, "bitti", "x"), show: showSuccess, status: jobSuccess},
		{name: "unknown state, unit gone", result: result(testJobID, "SUCCESS", ""), status: jobFailed, message: msgUnknownResult},
		{name: "state with padding", result: result(testJobID, "success ", ""), status: jobFailed, message: msgUnknownResult},
		{name: "garbage", result: "\x00\x01\x02 not a result file \xff\n=\n==\nstate\n", status: jobFailed, message: msgUnknownResult},
		{name: "empty file, systemd failed", result: "\n", show: showFailed, status: jobFailed, message: msgUnexpected},
		{name: "oversized file", result: strings.Repeat("x", 64<<10) + "\n" + result(testJobID, "success", ""), status: jobFailed, message: msgUnknownResult},
		{name: "oversized file, systemd success", result: strings.Repeat("junk=1\n", 20000) + result(testJobID, "failed", ""), show: showSuccess, status: jobSuccess},
		{name: "hostile message", result: result(testJobID, "failed", "<b>x</b>\x1b[31m\x00"+strings.Repeat("ç", 2000)), status: jobFailed,
			message: "<b>x</b>[31m" + strings.Repeat("ç", 244)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			if c.result != "" {
				e.write("state/apt-upgrade.result", c.result, 0o644)
			}
			if c.show != "" {
				e.write("show.txt", c.show, 0o644)
			}
			e.write("state/apt-upgrade.log", "apt çıktısı\n", 0o644)
			job := addAptJob(e)
			e.mod.followAptJob(job, testActor, 0)

			meta := job.Meta()
			if meta.Status != c.status || meta.Message != c.message {
				t.Errorf("status %q message %q\nwant   %q message %q", meta.Status, meta.Message, c.status, c.message)
			}
			if !utf8.ValidString(meta.Message) || len(meta.Message) > 500 {
				t.Errorf("message: %d bytes", len(meta.Message))
			}
			lines, _ := job.Snapshot(0)
			if len(lines) < 2 || lines[0] != "apt çıktısı" {
				t.Errorf("lines = %q", lines)
			}
			// Stored, audited and announced.
			stored, log, err := e.mod.store.getJob(context.Background(), testJobID)
			if err != nil || stored.Status != c.status || stored.Message != c.message || !strings.Contains(log, "apt çıktısı") {
				t.Errorf("stored job = %+v (%v)", stored, err)
			}
			rows := e.auditRows()
			if len(rows) != 1 || rows[0].Action != "updates.apt_upgrade" || rows[0].Success != (c.status == jobSuccess) ||
				rows[0].Username != "yonetici" {
				t.Errorf("audit = %+v", rows)
			}
			wantNote := "ERROR Güncelleme başarısız oldu"
			if c.status == jobSuccess {
				wantNote = "SUCCESS Güncelleme tamamlandı"
			}
			if got := e.notificationTitles(); !reflect.DeepEqual(got, []string{wantNote}) {
				t.Errorf("notifications = %q", got)
			}
			if c.status == jobFailed {
				if n := e.notifications()[0]; n.Message != c.message {
					t.Errorf("notification message = %q", n.Message)
				}
			}
			// Following only ever reads.
			if got := e.helperActions(); !reflect.DeepEqual(got, []string{"updates-apt-list"}) {
				t.Errorf("helper actions = %q", got)
			}
			for _, call := range e.systemctlCalls() {
				if !strings.HasPrefix(call, "is-active ") && !strings.HasPrefix(call, "show ") {
					t.Errorf("systemctl %s", call)
				}
			}
		})
	}
}

// While the unit runs the job stays open, whatever systemctl's exit code.
func TestFollowAptJobWaitsForTheUnit(t *testing.T) {
	for _, file := range []string{"units-active", "units-activating"} {
		t.Run(file, func(t *testing.T) {
			e := newEnv(t)
			e.write(file, "myserver-apt-upgrade.service\n", 0o644)
			e.write("state/apt-upgrade.result", result(testJobID, "running", ""), 0o644)
			job := addAptJob(e)
			wake, cancel := job.Subscribe()
			defer cancel()
			done := make(chan struct{})
			go func() {
				e.mod.followAptJob(job, testActor, 0)
				close(done)
			}()

			// The log appears only after a while, then grows.
			waitFor(t, "several looks at the unit", func() bool { return len(e.systemctlCalls()) >= 10 })
			if s := job.Meta().Status; s != jobRunning {
				t.Fatalf("the job was closed (%s) while its unit was running", s)
			}
			e.write("state/apt-upgrade.log", "birinci\n", 0o644)
			<-wake
			waitFor(t, "the first line", func() bool { l, _ := job.Snapshot(0); return len(l) == 1 })
			f, err := os.OpenFile(aptLogFile, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			f.WriteString("ikinci\n")
			f.Close()
			waitFor(t, "the second line", func() bool { l, _ := job.Snapshot(0); return len(l) == 2 })
			if s := job.Meta().Status; s != jobRunning {
				t.Fatalf("status %s", s)
			}

			f, _ = os.OpenFile(aptLogFile, os.O_WRONLY|os.O_APPEND, 0)
			f.WriteString("üçüncü\n")
			f.Close()
			e.write("state/apt-upgrade.result.tmp", result(testJobID, "success", ""), 0o644)
			if err := os.Rename(e.path("state/apt-upgrade.result.tmp"), aptResultFile); err != nil {
				t.Fatal(err)
			}
			<-done
			lines, meta := job.Snapshot(0)
			if want := []string{"birinci", "ikinci", "üçüncü", "İşlem başarıyla tamamlandı."}; !reflect.DeepEqual(lines, want) {
				t.Errorf("lines = %q", lines)
			}
			if meta.Status != jobSuccess {
				t.Errorf("meta = %+v", meta)
			}
			for _, call := range e.systemctlCalls() {
				if strings.HasPrefix(call, "show ") {
					t.Errorf("systemd was asked for the outcome although the result file had it: %s", call)
				}
			}
		})
	}
}

/* ---------- re-attaching after a restart ---------- */

func seedRunningJob(e *testEnv, id, kind string) {
	e.t.Helper()
	_, err := e.db.Exec(`INSERT INTO updates_jobs (id, kind, title, detail, username, started_at, status)
		VALUES (?, ?, 'Ubuntu paket güncellemesi', 'Standart yükseltme, çekirdek hariç', 'yonetici', 100, 'running')`, id, kind)
	if err != nil {
		e.t.Fatal(err)
	}
}

func TestReattachAfterRestart(t *testing.T) {
	var started *testEnv
	e := newEnv(t, envOptions{before: func(e *testEnv) {
		started = e
		seedRunningJob(e, testJobID, kindApt)
		seedRunningJob(e, "aaaaaaaaaaaaaaaa", kindDockerPull)
		seedRunningJob(e, "bbbbbbbbbbbbbbbb", kindApt)
		e.write("units-active", "myserver-apt-upgrade.service\n", 0o644)
		e.write("state/apt-upgrade.result", result(testJobID, "running", ""), 0o644)
		e.write("state/apt-upgrade.log", "yeniden başlamadan önce\n", 0o644)
	}})
	_ = started
	job := e.mod.jobs.get(testJobID)
	if job == nil {
		t.Fatal("the running upgrade was not re-attached")
	}
	if e.mod.jobs.running(kindApt) != job {
		t.Error("the re-attached job is not the running apt job")
	}
	waitFor(t, "the existing output", func() bool { l, _ := job.Snapshot(0); return len(l) == 1 })
	meta := job.Meta()
	if meta.Status != jobRunning || meta.Username != "yonetici" || meta.StartedAt != 100 || meta.Kind != kindApt {
		t.Errorf("meta = %+v", meta)
	}
	// A second upgrade is refused and the summary shows the running one.
	if res := e.do("POST", "/updates/apt/upgrade", `{"confirm":true}`, "admin"); res.Status != http.StatusConflict {
		t.Errorf("upgrade during a re-attached job: status %d", res.Status)
	}
	if res := e.do("POST", "/updates/reboot", `{"hostname":"`+hostname(t)+`"}`, "admin"); res.Status != http.StatusConflict {
		t.Errorf("reboot during a re-attached job: status %d", res.Status)
	}
	e.wantNoHelperCalls("re-attaching")

	// Jobs that cannot be followed any more are closed.
	for _, id := range []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"} {
		m, _, err := e.mod.store.getJob(context.Background(), id)
		if err != nil || m.Status != jobFailed || m.FinishedAt == nil ||
			m.Message != "Panel yeniden başlatıldığı için işlemin sonucu izlenemedi." {
			t.Errorf("%s = %+v (%v)", id, m, err)
		}
	}
	if m, _, _ := e.mod.store.getJob(context.Background(), testJobID); m.Status != jobRunning {
		t.Errorf("the re-attached job was closed in the history: %+v", m)
	}

	e.write("state/apt-upgrade.result", result(testJobID, "failed", "Diskte yeterli boş alan yok. Paket işlemi tamamlanamadı."), 0o644)
	e.waitIdle(job)
	if got := job.Meta(); got.Status != jobFailed || got.Message != "Diskte yeterli boş alan yok. Paket işlemi tamamlanamadı." {
		t.Errorf("finished job = %+v", got)
	}
	rows := e.auditRows()
	if len(rows) != 1 || rows[0].Username != "yonetici" || rows[0].Success {
		t.Errorf("audit = %+v", rows)
	}
}

// The upgrade ended while the panel was down: its recorded result is used.
func TestReattachFindsFinishedJob(t *testing.T) {
	e := newEnv(t, envOptions{before: func(e *testEnv) {
		seedRunningJob(e, testJobID, kindApt)
		e.write("state/apt-upgrade.result", result(testJobID, "success", ""), 0o644)
		e.write("state/apt-upgrade.log", "Setting up vim ...\n", 0o644)
	}})
	job := e.mod.jobs.get(testJobID)
	if job == nil {
		t.Fatal("the job was given up although its result is on disk")
	}
	e.waitIdle(job)
	if got := job.Meta(); got.Status != jobSuccess {
		t.Errorf("job = %+v", got)
	}
	if m, log, _ := e.mod.store.getJob(context.Background(), testJobID); m.Status != jobSuccess || !strings.Contains(log, "Setting up vim") {
		t.Errorf("stored job = %+v", m)
	}
}

func TestReattachRefusals(t *testing.T) {
	cases := map[string]func(e *testEnv){
		"no result file": func(e *testEnv) { seedRunningJob(e, testJobID, kindApt) },
		"result of an unknown job": func(e *testEnv) {
			seedRunningJob(e, testJobID, kindApt)
			e.write("state/apt-upgrade.result", result("cccccccccccccccc", "running", ""), 0o644)
		},
		"invalid id": func(e *testEnv) {
			seedRunningJob(e, testJobID, kindApt)
			e.write("state/apt-upgrade.result", result("' OR 1=1 --", "running", ""), 0o644)
		},
		"job of another kind": func(e *testEnv) {
			seedRunningJob(e, testJobID, kindDockerPull)
			e.write("state/apt-upgrade.result", result(testJobID, "running", ""), 0o644)
		},
		"job already finished": func(e *testEnv) {
			e.db.Exec(`INSERT INTO updates_jobs (id, kind, title, started_at, finished_at, status)
				VALUES (?, ?, 'x', 100, 200, 'success')`, testJobID, kindApt)
			e.write("state/apt-upgrade.result", result(testJobID, "running", ""), 0o644)
		},
		"result file is a link": func(e *testEnv) {
			seedRunningJob(e, testJobID, kindApt)
			e.write("elsewhere", result(testJobID, "running", ""), 0o644)
			if err := os.Symlink(e.path("elsewhere"), e.path("state/apt-upgrade.result")); err != nil {
				e.t.Fatal(err)
			}
		},
	}
	for name, before := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, envOptions{before: func(e *testEnv) {
				e.write("units-active", "myserver-apt-upgrade.service\n", 0o644)
				before(e)
			}})
			if e.mod.jobs.get(testJobID) != nil || e.mod.jobs.running(kindApt) != nil {
				t.Error("a job was re-attached")
			}
			m, _, err := e.mod.store.getJob(context.Background(), testJobID)
			if err != nil || m.Status == jobRunning {
				t.Errorf("stored job = %+v (%v)", m, err)
			}
			e.wantNoHelperCalls("start")
		})
	}
}
