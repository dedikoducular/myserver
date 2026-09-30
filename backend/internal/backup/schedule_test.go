package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	_ "time/tzdata" // the test image has no time zone database
)

func zone(t testing.TB, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("time zone %s: %v", name, err)
	}
	return loc
}

func at(loc *time.Location, s string) time.Time {
	tm, err := time.ParseInLocation("2006-01-02 15:04:05", s, loc)
	if err != nil {
		panic(err)
	}
	return tm
}

func show(t time.Time) string { return t.Format("2006-01-02 15:04:05 MST") }

func TestNextAndPreviousRun(t *testing.T) {
	ist := zone(t, "Europe/Istanbul")
	daily := &Schedule{Enabled: true, Frequency: freqDaily, Hour: 3, Minute: 15}
	weekly := &Schedule{Enabled: true, Frequency: freqWeekly, Hour: 23, Minute: 59, Weekday: 0} // Sunday
	m31 := &Schedule{Enabled: true, Frequency: freqMonthly, Hour: 4, Minute: 0, Monthday: 31}
	m30 := &Schedule{Enabled: true, Frequency: freqMonthly, Hour: 4, Minute: 0, Monthday: 30}
	m29 := &Schedule{Enabled: true, Frequency: freqMonthly, Hour: 4, Minute: 0, Monthday: 29}
	m1 := &Schedule{Enabled: true, Frequency: freqMonthly, Hour: 0, Minute: 0, Monthday: 1}
	cases := []struct {
		name       string
		sc         *Schedule
		now        string
		next, prev string
	}{
		{"daily, before", daily, "2026-05-10 03:14:59", "2026-05-10 03:15:00", "2026-05-09 03:15:00"},
		{"daily, at the time", daily, "2026-05-10 03:15:00", "2026-05-11 03:15:00", "2026-05-10 03:15:00"},
		{"daily, after", daily, "2026-05-10 03:15:01", "2026-05-11 03:15:00", "2026-05-10 03:15:00"},
		{"daily, end of month", daily, "2026-01-31 12:00:00", "2026-02-01 03:15:00", "2026-01-31 03:15:00"},
		{"daily, end of year", daily, "2026-12-31 23:59:59", "2027-01-01 03:15:00", "2026-12-31 03:15:00"},
		{"daily, leap day", daily, "2028-02-28 12:00:00", "2028-02-29 03:15:00", "2028-02-28 03:15:00"},
		{"daily, no leap day", daily, "2027-02-28 12:00:00", "2027-03-01 03:15:00", "2027-02-28 03:15:00"},
		{"weekly, on the day before", weekly, "2026-05-10 12:00:00", "2026-05-10 23:59:00", "2026-05-03 23:59:00"},
		{"weekly, on the day after", weekly, "2026-05-10 23:59:30", "2026-05-17 23:59:00", "2026-05-10 23:59:00"},
		{"weekly, mid week", weekly, "2026-05-13 00:00:00", "2026-05-17 23:59:00", "2026-05-10 23:59:00"},
		{"weekly, across the year", weekly, "2026-12-30 00:00:00", "2027-01-03 23:59:00", "2026-12-27 23:59:00"},
		{"monthly 31, in a month of 31", m31, "2026-01-15 00:00:00", "2026-01-31 04:00:00", "2025-12-31 04:00:00"},
		{"monthly 31, February", m31, "2026-02-01 00:00:00", "2026-02-28 04:00:00", "2026-01-31 04:00:00"},
		{"monthly 31, leap February", m31, "2028-02-10 00:00:00", "2028-02-29 04:00:00", "2028-01-31 04:00:00"},
		{"monthly 31, after the end of February", m31, "2026-02-28 04:00:01", "2026-03-31 04:00:00", "2026-02-28 04:00:00"},
		{"monthly 31, April", m31, "2026-04-01 00:00:00", "2026-04-30 04:00:00", "2026-03-31 04:00:00"},
		{"monthly 31, from the 31st", m31, "2026-03-31 12:00:00", "2026-04-30 04:00:00", "2026-03-31 04:00:00"},
		{"monthly 30, February", m30, "2026-02-10 00:00:00", "2026-02-28 04:00:00", "2026-01-30 04:00:00"},
		{"monthly 30, leap February", m30, "2028-02-10 00:00:00", "2028-02-29 04:00:00", "2028-01-30 04:00:00"},
		{"monthly 30, March", m30, "2026-03-01 00:00:00", "2026-03-30 04:00:00", "2026-02-28 04:00:00"},
		{"monthly 29, February", m29, "2027-02-10 00:00:00", "2027-02-28 04:00:00", "2027-01-29 04:00:00"},
		{"monthly 29, leap February", m29, "2028-02-10 00:00:00", "2028-02-29 04:00:00", "2028-01-29 04:00:00"},
		{"monthly 29, century rule", m29, "2100-02-10 00:00:00", "2100-02-28 04:00:00", "2100-01-29 04:00:00"},
		{"monthly 29, year 2000 rule", m29, "2400-02-10 00:00:00", "2400-02-29 04:00:00", "2400-01-29 04:00:00"},
		{"monthly 1, midnight", m1, "2026-12-31 23:59:59", "2027-01-01 00:00:00", "2026-12-01 00:00:00"},
		{"monthly 1, at midnight", m1, "2027-01-01 00:00:00", "2027-02-01 00:00:00", "2027-01-01 00:00:00"},
		{"monthly 31, from January 31 23:00", m31, "2026-01-31 23:00:00", "2026-02-28 04:00:00", "2026-01-31 04:00:00"},
	}
	for _, c := range cases {
		now := at(ist, c.now)
		if got := nextDue(c.sc, now); show(got) != show(at(ist, c.next)) {
			t.Errorf("%s: next = %s, want %s", c.name, show(got), c.next)
		}
		if got := prevDue(c.sc, now); show(got) != show(at(ist, c.prev)) {
			t.Errorf("%s: previous = %s, want %s", c.name, show(got), c.prev)
		}
	}
}

func TestNextRunIsAlwaysAfterNowAndPreviousNeverIs(t *testing.T) {
	for _, zn := range []string{"Europe/Berlin", "Europe/Istanbul", "America/New_York", "Australia/Lord_Howe", "Pacific/Apia", "UTC"} {
		loc := zone(t, zn)
		for _, sc := range []*Schedule{
			{Frequency: freqDaily, Hour: 2, Minute: 30},
			{Frequency: freqDaily, Hour: 0, Minute: 0},
			{Frequency: freqDaily, Hour: 23, Minute: 59},
			{Frequency: freqWeekly, Hour: 2, Minute: 30, Weekday: 0},
			{Frequency: freqWeekly, Hour: 1, Minute: 45, Weekday: 6},
			{Frequency: freqMonthly, Hour: 2, Minute: 30, Monthday: 31},
			{Frequency: freqMonthly, Hour: 2, Minute: 30, Monthday: 29},
		} {
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, loc)
			end := now.AddDate(2, 2, 0)
			for now.Before(end) {
				next, prev := nextDue(sc, now), prevDue(sc, now)
				if next.IsZero() || !next.After(now) {
					t.Fatalf("%s %+v at %s: next = %s", zn, sc, show(now), show(next))
				}
				if prev.IsZero() || prev.After(now) {
					t.Fatalf("%s %+v at %s: previous = %s", zn, sc, show(now), show(prev))
				}
				limit := 8 * 24 * time.Hour
				if sc.Frequency == freqDaily {
					limit = 26 * time.Hour
				} else if sc.Frequency == freqMonthly {
					limit = 32 * 24 * time.Hour
				}
				if next.Sub(now) > limit || now.Sub(prev) > limit {
					t.Fatalf("%s %+v at %s: next %s, previous %s", zn, sc, show(now), show(next), show(prev))
				}
				// Nothing is scheduled between the two.
				if p2 := prevDue(sc, next.Add(-time.Second)); !p2.Equal(prev) && next.Sub(now) > time.Second {
					t.Fatalf("%s %+v at %s: previous %s, but just before the next run (%s) it is %s", zn, sc, show(now), show(prev), show(next), show(p2))
				}
				now = now.Add(7*time.Hour + 13*time.Minute)
			}
		}
	}
}

// runner simulates the scheduler loop: every tick it asks isDue and, when a
// run is due, records it and stores the start as the last run.
func simulate(sc *Schedule, from, to time.Time, tick time.Duration) []time.Time {
	var runs []time.Time
	for now := from; now.Before(to); now = now.Add(tick) {
		if isDue(sc, now) {
			runs = append(runs, now)
			sc.LastRunAt = now.Unix()
		}
	}
	return runs
}

func TestDaylightSavingTransitions(t *testing.T) {
	berlin := zone(t, "Europe/Berlin")
	// 2026-03-29: 02:00 CET becomes 03:00 CEST; 02:30 does not exist.
	// 2026-10-25: 03:00 CEST becomes 02:00 CET; 02:30 exists twice.
	for _, c := range []struct {
		name     string
		sc       Schedule
		from, to string
		days     []string
	}{
		{"daily 02:30, spring forward", Schedule{Frequency: freqDaily, Hour: 2, Minute: 30},
			"2026-03-27 12:00:00", "2026-03-31 12:00:00", []string{"2026-03-28", "2026-03-29", "2026-03-30", "2026-03-31"}},
		{"daily 02:30, fall back", Schedule{Frequency: freqDaily, Hour: 2, Minute: 30},
			"2026-10-23 12:00:00", "2026-10-27 12:00:00", []string{"2026-10-24", "2026-10-25", "2026-10-26", "2026-10-27"}},
		{"daily 02:00, spring forward", Schedule{Frequency: freqDaily, Hour: 2, Minute: 0},
			"2026-03-28 12:00:00", "2026-03-30 12:00:00", []string{"2026-03-29", "2026-03-30"}},
		{"daily 03:00, fall back", Schedule{Frequency: freqDaily, Hour: 3, Minute: 0},
			"2026-10-24 12:00:00", "2026-10-26 12:00:00", []string{"2026-10-25", "2026-10-26"}},
		{"daily 02:59, fall back", Schedule{Frequency: freqDaily, Hour: 2, Minute: 59},
			"2026-10-24 12:00:00", "2026-10-26 12:00:00", []string{"2026-10-25", "2026-10-26"}},
		{"weekly Sunday 02:30, spring forward", Schedule{Frequency: freqWeekly, Hour: 2, Minute: 30, Weekday: 0},
			"2026-03-23 12:00:00", "2026-04-06 12:00:00", []string{"2026-03-29", "2026-04-05"}},
		{"weekly Sunday 02:30, fall back", Schedule{Frequency: freqWeekly, Hour: 2, Minute: 30, Weekday: 0},
			"2026-10-19 12:00:00", "2026-11-02 12:00:00", []string{"2026-10-25", "2026-11-01"}},
		{"monthly 29th 02:30, spring forward", Schedule{Frequency: freqMonthly, Hour: 2, Minute: 30, Monthday: 29},
			"2026-03-01 12:00:00", "2026-04-30 12:00:00", []string{"2026-03-29", "2026-04-29"}},
		{"monthly 25th 02:30, fall back", Schedule{Frequency: freqMonthly, Hour: 2, Minute: 30, Monthday: 25},
			"2026-10-01 12:00:00", "2026-11-26 12:00:00", []string{"2026-10-25", "2026-11-25"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			sc := c.sc
			sc.Enabled = true
			sc.UpdatedAt = at(berlin, c.from).Unix()
			runs := simulate(&sc, at(berlin, c.from), at(berlin, c.to), 30*time.Second)
			var days []string
			for _, r := range runs {
				days = append(days, r.Format("2006-01-02"))
			}
			if fmt.Sprint(days) != fmt.Sprint(c.days) {
				shown := []string{}
				for _, r := range runs {
					shown = append(shown, show(r))
				}
				t.Fatalf("runs on %v (%v), want exactly one on each of %v", days, shown, c.days)
			}
			for _, r := range runs {
				// Never earlier than the wall clock time asked for, and
				// within an hour and a tick of it.
				h, m, _ := r.Clock()
				mins := h*60 + m
				want := sc.Hour*60 + sc.Minute
				if mins < want-60 || mins > want+61 {
					t.Errorf("run at %s for a schedule of %02d:%02d", show(r), sc.Hour, sc.Minute)
				}
			}
		})
	}
}

func TestOneRunPerDayThroughAWholeYear(t *testing.T) {
	for _, zn := range []string{"Europe/Berlin", "America/New_York", "Australia/Lord_Howe", "Europe/Istanbul"} {
		loc := zone(t, zn)
		for _, hm := range [][2]int{{2, 30}, {1, 30}, {3, 0}, {0, 0}, {23, 59}} {
			sc := &Schedule{Enabled: true, Frequency: freqDaily, Hour: hm[0], Minute: hm[1]}
			from := time.Date(2026, 1, 1, 12, 0, 0, 0, loc)
			sc.UpdatedAt = from.Unix()
			runs := simulate(sc, from, time.Date(2027, 1, 1, 12, 0, 0, 0, loc), time.Minute)
			if len(runs) != 365 {
				t.Errorf("%s %02d:%02d: %d runs in 365 days", zn, hm[0], hm[1], len(runs))
			}
			seen := map[string]bool{}
			for _, r := range runs {
				day := r.Format("2006-01-02")
				if seen[day] {
					t.Errorf("%s %02d:%02d: two runs on %s", zn, hm[0], hm[1], day)
				}
				seen[day] = true
			}
		}
	}
}

func TestMissedRunsAreMadeUpOnce(t *testing.T) {
	ist := zone(t, "Europe/Istanbul")
	for _, sc := range []*Schedule{
		{Frequency: freqDaily, Hour: 3},
		{Frequency: freqWeekly, Hour: 3, Weekday: 2},
		{Frequency: freqMonthly, Hour: 3, Monthday: 15},
	} {
		sc.Enabled = true
		sc.UpdatedAt = at(ist, "2026-01-01 00:00:00").Unix()
		sc.LastRunAt = at(ist, "2026-01-20 03:00:10").Unix()
		// The server was off for half a year.
		back := at(ist, "2026-07-20 10:00:00")
		if !isDue(sc, back) {
			t.Fatalf("%s: nothing is due after the downtime", sc.Frequency)
		}
		runs := simulate(sc, back, back.Add(10*time.Hour), 30*time.Second)
		if len(runs) != 1 || !runs[0].Equal(back) {
			t.Fatalf("%s: %d runs after the downtime, want exactly one, at once", sc.Frequency, len(runs))
		}
	}
	// A schedule that never ran does not run for a time that had passed
	// when it was created or changed.
	sc := &Schedule{Enabled: true, Frequency: freqDaily, Hour: 3, UpdatedAt: at(ist, "2026-05-10 09:00:00").Unix()}
	if isDue(sc, at(ist, "2026-05-10 09:00:30")) || isDue(sc, at(ist, "2026-05-11 02:59:59")) {
		t.Fatal("a new schedule ran for a time before its creation")
	}
	if !isDue(sc, at(ist, "2026-05-11 03:00:00")) {
		t.Fatal("a new schedule does not run at its first time")
	}
	sc.Enabled = false
	if isDue(sc, at(ist, "2026-05-12 03:00:00")) {
		t.Fatal("a disabled schedule is due")
	}
	// The clock was set back: no second run for the same scheduled time.
	sc = &Schedule{Enabled: true, Frequency: freqDaily, Hour: 3, LastRunAt: at(ist, "2026-05-11 03:00:05").Unix()}
	if isDue(sc, at(ist, "2026-05-11 03:00:01")) || isDue(sc, at(ist, "2026-05-10 12:00:00")) {
		t.Fatal("a run is due although the last run is newer than the scheduled time")
	}
}

/* ---------- the scheduler loop with an injected clock ---------- */

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

func (e *testEnv) schedule(sc Schedule) {
	e.t.Helper()
	sc.Slug = "fotolar"
	sc.Enabled = true
	if err := e.m.store.saveSchedule(context.Background(), &sc); err != nil {
		e.t.Fatal(err)
	}
}

func TestSchedulerRunsMissedBackupOnce(t *testing.T) {
	e := newEnv(t)
	e.standardApp(true)
	berlin := zone(t, "Europe/Berlin")
	clock := &fakeClock{}
	e.m.clock = clock.Now
	e.schedule(Schedule{Frequency: freqDaily, Hour: 2, Minute: 30, KeepLast: 5, IncludeBinds: true,
		UpdatedAt: at(berlin, "2026-03-09 12:00:00").Unix()})
	ctx := context.Background()

	clock.set(at(berlin, "2026-03-10 02:29:59"))
	e.m.runSchedules(ctx)
	if len(e.records("")) != 0 {
		t.Fatal("ran before the time")
	}
	_ = e.m.store.setLastRun(ctx, "fotolar", at(berlin, "2026-03-10 02:30:00").Unix())

	// Back after 18 days, on the night the clock jumps forward.
	clock.set(at(berlin, "2026-03-29 03:10:00"))
	for i := 0; i < 5; i++ {
		e.m.runSchedules(ctx)
		clock.set(clock.Now().Add(30 * time.Second))
	}
	recs := e.records("fotolar")
	if len(recs) != 1 || recs[0].Trigger != triggerScheduled || recs[0].Status != statusSuccess {
		t.Fatalf("%d backups after the downtime, want one: %+v", len(recs), recs)
	}
	if e.ev.count("apps.stop") != 1 || e.ev.count("apps.start") != 1 {
		t.Fatalf("events %q", e.ev.all())
	}
	sc, _ := e.m.store.schedule(ctx, "fotolar")
	if sc.LastRunAt != at(berlin, "2026-03-29 03:10:00").Unix() {
		t.Fatalf("last run %s", show(time.Unix(sc.LastRunAt, 0).In(berlin)))
	}
	if a := e.auditOf("backup.create"); len(a) != 1 || a[0].User != "zamanlayıcı" {
		t.Fatalf("audit %+v", a)
	}
	if n := e.notifications(); len(n) != 1 || !strings.HasPrefix(n[0], "SUCCESS: Zamanlanmış yedekleme tamamlandı") {
		t.Fatalf("notifications %q", n)
	}
	time.Sleep(1100 * time.Millisecond)
	clock.set(at(berlin, "2026-03-30 02:30:00"))
	e.m.runSchedules(ctx)
	e.m.runSchedules(ctx)
	if len(e.records("fotolar")) != 2 {
		t.Fatalf("%d backups, want 2", len(e.records("fotolar")))
	}
	res := e.do(e.admin, "GET", "/schedules/fotolar", nil)
	if !strings.Contains(string(res.Data), fmt.Sprintf(`"next_run_at":%d`, at(berlin, "2026-03-31 02:30:00").Unix())) {
		t.Fatalf("next run: %s", res.Data)
	}
}

func TestSchedulerSkipsBusyAndRemovedApps(t *testing.T) {
	e := newEnv(t)
	e.standardApp(false)
	ist := zone(t, "Europe/Istanbul")
	clock := &fakeClock{now: at(ist, "2026-05-10 03:00:30")}
	e.m.clock = clock.Now
	e.schedule(Schedule{Frequency: freqDaily, Hour: 3, KeepLast: 5, UpdatedAt: at(ist, "2026-05-01 00:00:00").Unix()})
	ctx := context.Background()

	// Another job holds the application: the run is postponed, not lost.
	_, release, ok := e.m.jobs.create(ctx, "fotolar", &Job{Kind: jobVerify, Slug: "fotolar"})
	if !ok {
		t.Fatal("lock")
	}
	e.m.runSchedules(ctx)
	if len(e.records("")) != 0 {
		t.Fatal("a scheduled backup ran beside another job")
	}
	release()
	clock.set(clock.Now().Add(30 * time.Second))
	e.m.runSchedules(ctx)
	if len(e.records("fotolar")) != 1 {
		t.Fatal("the postponed run did not happen")
	}

	// The application was removed: the schedule stays and does not run.
	e.apps.mu.Lock()
	delete(e.apps.cfgs, "fotolar")
	e.apps.mu.Unlock()
	clock.set(at(ist, "2026-05-11 03:00:30"))
	e.m.runSchedules(ctx)
	if len(e.records("fotolar")) != 1 {
		t.Fatal("a backup of a removed application was attempted")
	}
	if sc, _ := e.m.store.schedule(ctx, "fotolar"); sc == nil || !sc.Enabled {
		t.Fatal("the schedule was removed")
	}

	// A failed scheduled backup is not retried every 30 seconds.
	e.standardApp(false)
	e.docker.setFail("read", 500)
	clock.set(at(ist, "2026-05-12 03:00:30"))
	for i := 0; i < 4; i++ {
		e.m.runSchedules(ctx)
		clock.set(clock.Now().Add(30 * time.Second))
	}
	failed := 0
	for _, r := range e.records("fotolar") {
		if r.Status == statusFailed {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("%d failed attempts for one scheduled time", failed)
	}
	health := e.m.Health(ctx)
	if health[0].ID != "backup.scheduled" || health[0].Status == "HEALTHY" || !strings.Contains(health[0].Message, "Fotolar") {
		t.Fatalf("health %+v", health[0])
	}
}

/* ---------- retention ---------- */

// seed stores a finished backup with a file and returns its record.
func (e *testEnv) seed(trigger, status string, created time.Time) *Record {
	e.t.Helper()
	ctx := context.Background()
	if err := e.m.ensureDirs("fotolar"); err != nil {
		e.t.Fatal(err)
	}
	rec := &Record{Slug: "fotolar", AppName: "Fotolar", CreatedAt: created.Unix(), Status: status,
		Consistency: consistencyStopped, Trigger: trigger}
	if status == statusSuccess {
		name, err := e.m.uniqueName(ctx, "fotolar", created, false)
		if err != nil {
			e.t.Fatal(err)
		}
		rec.FileName = name
		if err := os.WriteFile(filepath.Join(e.m.appDir("fotolar"), name), []byte(trigger), 0o600); err != nil {
			e.t.Fatal(err)
		}
	}
	id, err := e.m.store.insert(ctx, rec)
	if err != nil {
		e.t.Fatal(err)
	}
	rec.ID = id
	return rec
}

func (e *testEnv) remaining() map[int64]bool {
	out := map[int64]bool{}
	for _, r := range e.records("fotolar") {
		out[r.ID] = true
		if r.Status == statusSuccess {
			if _, err := os.Stat(filepath.Join(e.m.appDir("fotolar"), r.FileName)); err != nil {
				e.t.Errorf("record %d has no file any more", r.ID)
			}
		}
	}
	return out
}

func TestRetention(t *testing.T) {
	day := func(n int) time.Time { return time.Unix(1_750_000_000, 0).Add(time.Duration(n) * 24 * time.Hour) }
	t.Run("keeps the newest scheduled backups and nothing else is touched", func(t *testing.T) {
		e := newEnv(t)
		var scheduled []*Record
		for i := 0; i < 6; i++ {
			scheduled = append(scheduled, e.seed(triggerScheduled, statusSuccess, day(i)))
		}
		others := []*Record{
			e.seed(triggerManual, statusSuccess, day(-30)),
			e.seed(triggerSafety, statusSuccess, day(-31)),
			e.seed(triggerImported, statusSuccess, day(-32)),
			e.seed(triggerScheduled, statusFailed, day(-33)),
			e.seed(triggerScheduled, statusCancelled, day(-34)),
			e.seed(triggerManual, statusSuccess, day(3)),
		}
		newest := scheduled[5]
		j := &Job{mgr: e.m.jobs}
		e.m.applyRetention(context.Background(), j, &Schedule{Slug: "fotolar", KeepLast: 3}, newest.ID)
		left := e.remaining()
		for i, r := range scheduled {
			if want := i >= 3; left[r.ID] != want {
				t.Errorf("scheduled backup of day %d: kept=%v, want %v", i, left[r.ID], want)
			}
		}
		for _, r := range others {
			if !left[r.ID] {
				t.Errorf("%s/%s backup was deleted", r.Trigger, r.Status)
			}
		}
		if n := len(e.files()); n != 3+4 {
			t.Errorf("%d files left: %q", n, e.files())
		}
		if a := e.auditOf("backup.delete"); len(a) != 3 || a[0].User != "zamanlayıcı" || !strings.Contains(a[0].Detail, "saklama kuralı") {
			t.Errorf("audit %+v", a)
		}
	})
	t.Run("manual backups only when configured", func(t *testing.T) {
		e := newEnv(t)
		m1 := e.seed(triggerManual, statusSuccess, day(0))
		s1 := e.seed(triggerScheduled, statusSuccess, day(1))
		m2 := e.seed(triggerManual, statusSuccess, day(2))
		safety := e.seed(triggerSafety, statusSuccess, day(-5))
		imported := e.seed(triggerImported, statusSuccess, day(-6))
		s2 := e.seed(triggerScheduled, statusSuccess, day(3))
		e.m.applyRetention(context.Background(), &Job{mgr: e.m.jobs}, &Schedule{Slug: "fotolar", KeepLast: 2, PruneManual: true}, s2.ID)
		left := e.remaining()
		if left[m1.ID] || left[s1.ID] || !left[m2.ID] || !left[s2.ID] {
			t.Errorf("left %v", left)
		}
		if !left[safety.ID] || !left[imported.ID] {
			t.Error("a safety or imported backup was deleted")
		}
	})
	t.Run("never the backup that was just made", func(t *testing.T) {
		// The clock of the server was wrong: older backups carry dates
		// in the future.
		e := newEnv(t)
		for i := 0; i < 4; i++ {
			e.seed(triggerScheduled, statusSuccess, day(100+i))
		}
		fresh := e.seed(triggerScheduled, statusSuccess, day(0))
		for _, keep := range []int{1, 2, 3} {
			e.m.applyRetention(context.Background(), &Job{mgr: e.m.jobs}, &Schedule{Slug: "fotolar", KeepLast: keep}, fresh.ID)
			if !e.remaining()[fresh.ID] {
				t.Fatalf("keep_last=%d: the backup that was just made was deleted", keep)
			}
		}
		e.m.applyRetention(context.Background(), &Job{mgr: e.m.jobs}, &Schedule{Slug: "fotolar", KeepLast: 1}, fresh.ID)
		if left := e.remaining(); len(left) != 1 || !left[fresh.ID] {
			t.Fatalf("left %v", left)
		}
	})
	t.Run("other applications and invalid settings", func(t *testing.T) {
		e := newEnv(t)
		a := e.seed(triggerScheduled, statusSuccess, day(0))
		b := e.seed(triggerScheduled, statusSuccess, day(1))
		other := &Record{Slug: "baska", CreatedAt: day(-9).Unix(), Status: statusSuccess, Consistency: consistencyStopped, Trigger: triggerScheduled}
		other.ID, _ = e.m.store.insert(context.Background(), other)
		for _, keep := range []int{0, -1} {
			e.m.applyRetention(context.Background(), &Job{mgr: e.m.jobs}, &Schedule{Slug: "fotolar", KeepLast: keep}, b.ID)
		}
		if left := e.remaining(); !left[a.ID] || !left[b.ID] {
			t.Fatal("keep_last below 1 deleted backups")
		}
		e.m.applyRetention(context.Background(), &Job{mgr: e.m.jobs}, &Schedule{Slug: "fotolar", KeepLast: 1}, b.ID)
		if got, _ := e.m.store.get(context.Background(), other.ID); got == nil {
			t.Fatal("a backup of another application was deleted")
		}
	})
	t.Run("through the scheduler", func(t *testing.T) {
		e := newEnv(t)
		e.standardApp(false)
		ist := zone(t, "Europe/Istanbul")
		clock := &fakeClock{now: at(ist, "2026-05-10 03:00:30")}
		e.m.clock = clock.Now
		e.schedule(Schedule{Frequency: freqDaily, Hour: 3, KeepLast: 2, UpdatedAt: 1})
		// Dates in the future, as after a wrong clock.
		old := []*Record{e.seed(triggerScheduled, statusSuccess, time.Now().Add(48*time.Hour)),
			e.seed(triggerScheduled, statusSuccess, time.Now().Add(72*time.Hour))}
		manual := e.seed(triggerManual, statusSuccess, time.Now().Add(-time.Hour))
		e.m.runSchedules(context.Background())
		left := e.remaining()
		var fresh *Record
		for _, r := range e.records("fotolar") {
			if r.ID != old[0].ID && r.ID != old[1].ID && r.ID != manual.ID {
				fresh = r
			}
		}
		if fresh == nil || fresh.Status != statusSuccess {
			t.Fatalf("the backup that was just made is gone; left %v", left)
		}
		if len(left) != 3 || !left[manual.ID] || left[old[0].ID] || !left[old[1].ID] {
			t.Fatalf("left %v (old %d %d manual %d)", left, old[0].ID, old[1].ID, manual.ID)
		}
	})
}
