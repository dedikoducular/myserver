package backup

// The scheduler. It runs inside the module's Start loop; there is no cron
// job and no systemd timer. All times are in the server's local time zone.

import (
	"context"
	"log/slog"
	"os"
	"time"
)

// Frequencies.
const (
	freqDaily   = "daily"
	freqWeekly  = "weekly"
	freqMonthly = "monthly"
)

func daysIn(year int, month time.Month, loc *time.Location) int {
	return time.Date(year, month+1, 0, 12, 0, 0, 0, loc).Day()
}

// monthlyAt returns the run of the month that lies offset months from now.
func monthlyAt(sc *Schedule, now time.Time, offset int) time.Time {
	first := time.Date(now.Year(), now.Month()+time.Month(offset), 1, 12, 0, 0, 0, now.Location())
	day := sc.Monthday
	if n := daysIn(first.Year(), first.Month(), now.Location()); day > n {
		day = n
	}
	return time.Date(first.Year(), first.Month(), day, sc.Hour, sc.Minute, 0, 0, now.Location())
}

// dailyAt returns the run of the day that lies offset days from now.
func dailyAt(sc *Schedule, now time.Time, offset int) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day()+offset, sc.Hour, sc.Minute, 0, 0, now.Location())
}

// prevDue returns the latest scheduled time that is not after now.
func prevDue(sc *Schedule, now time.Time) time.Time {
	switch sc.Frequency {
	case freqMonthly:
		for off := 0; off >= -13; off-- {
			if t := monthlyAt(sc, now, off); !t.After(now) {
				return t
			}
		}
	case freqWeekly:
		for off := 0; off >= -8; off-- {
			if t := dailyAt(sc, now, off); int(t.Weekday()) == sc.Weekday && !t.After(now) {
				return t
			}
		}
	default:
		for off := 0; off >= -2; off-- {
			if t := dailyAt(sc, now, off); !t.After(now) {
				return t
			}
		}
	}
	return time.Time{}
}

// nextDue returns the earliest scheduled time after now.
func nextDue(sc *Schedule, now time.Time) time.Time {
	switch sc.Frequency {
	case freqMonthly:
		for off := 0; off <= 13; off++ {
			if t := monthlyAt(sc, now, off); t.After(now) {
				return t
			}
		}
	case freqWeekly:
		for off := 0; off <= 8; off++ {
			if t := dailyAt(sc, now, off); int(t.Weekday()) == sc.Weekday && t.After(now) {
				return t
			}
		}
	default:
		for off := 0; off <= 2; off++ {
			if t := dailyAt(sc, now, off); t.After(now) {
				return t
			}
		}
	}
	return time.Time{}
}

// isDue reports whether a run is outstanding. However many runs were missed
// while the server was off, only one is outstanding: the schedule is
// compared with the start of the last run, not replayed.
func isDue(sc *Schedule, now time.Time) bool {
	if !sc.Enabled {
		return false
	}
	due := prevDue(sc, now)
	if due.IsZero() {
		return false
	}
	anchor := sc.LastRunAt
	if sc.UpdatedAt > anchor {
		// A schedule that was just created or changed does not run for
		// a time that had already passed.
		anchor = sc.UpdatedAt
	}
	return due.Unix() > anchor
}

// runSchedules runs the outstanding scheduled backups one after another, so
// that several applications scheduled for the same time do not compete for
// the disk.
func (m *Module) runSchedules(ctx context.Context) {
	api, err := m.appsAPI()
	if err != nil || m.deps.DB == nil {
		return
	}
	list, err := m.store.schedules(ctx)
	if err != nil {
		slog.Error("yedekleme zamanlamaları okunamadı", "error", err.Error())
		return
	}
	for _, sc := range list {
		if ctx.Err() != nil {
			return
		}
		if !isDue(sc, m.now()) {
			continue
		}
		cfg, err := api.AppConfig(ctx, sc.Slug)
		if err != nil {
			if isNotFound(err) {
				// The application was removed; its schedule stays
				// for a later reinstall but does not run.
				_ = m.store.setLastRun(ctx, sc.Slug, m.now().Unix())
			}
			continue
		}
		j := &Job{Kind: jobBackup, Slug: sc.Slug, Name: cfg.Name, Trigger: triggerScheduled,
			Actor: schedulerActor, cancellable: true}
		jctx, release, ok := m.jobs.create(ctx, sc.Slug, j)
		if !ok {
			continue // busy; tried again on the next tick
		}
		if err := m.store.setLastRun(ctx, sc.Slug, m.now().Unix()); err != nil {
			slog.Error("zamanlama güncellenemedi", "slug", sc.Slug, "error", err.Error())
		}
		opts := backupOptions{Slug: sc.Slug, Live: sc.Live, IncludeBinds: sc.IncludeBinds,
			Trigger: triggerScheduled, Actor: schedulerActor}
		schedule := *sc
		m.runJob(jctx, release, j, func(ctx context.Context, j *Job) error {
			rec, err := m.runBackup(ctx, j, opts)
			if err != nil {
				return err
			}
			m.applyRetention(ctx, j, &schedule, rec.ID)
			return nil
		})
	}
}

// applyRetention deletes the oldest backups of an application beyond the
// newest keep_last. Only successful scheduled backups are counted and
// deleted, unless the schedule says that manual backups are included.
// Safety backups and imported backups are never deleted automatically, and
// neither is the backup that was just made (keepID), whatever the clock said
// when the older ones were made.
func (m *Module) applyRetention(ctx context.Context, j *Job, sc *Schedule, keepID int64) {
	if sc.KeepLast < 1 {
		return
	}
	where := `slug = ? AND status = 'success' AND trigger_kind = 'scheduled'`
	if sc.PruneManual {
		where = `slug = ? AND status = 'success' AND trigger_kind IN ('scheduled', 'manual')`
	}
	recs, err := m.store.query(ctx, where, sc.Slug)
	if err != nil {
		slog.Error("saklama kuralı uygulanamadı", "slug", sc.Slug, "error", err.Error())
		return
	}
	// The new backup takes one of the places.
	places := sc.KeepLast
	for _, r := range recs {
		if r.ID == keepID {
			places--
		}
	}
	for _, r := range recs {
		if r.ID == keepID {
			continue
		}
		if places > 0 {
			places--
			continue
		}
		err := m.deleteRecord(ctx, r)
		m.deps.Audit.Log(ctx, schedulerActor, "backup.delete", r.Slug,
			auditDetail(r.FileName+" (saklama kuralı)", err), err == nil)
		if err != nil {
			j.Warn("Eski yedek silinemedi: " + r.FileName)
			continue
		}
		j.Log("Saklama kuralı gereği eski yedek silindi: " + r.FileName)
	}
}

// deleteRecord removes the archive of a record and then the record.
func (m *Module) deleteRecord(ctx context.Context, r *Record) error {
	if r.FileName != "" {
		p, err := m.recordPath(r)
		if err == nil {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return userErr("Yedek dosyası silinemedi.", err)
			}
		}
	}
	return m.store.remove(ctx, r.ID)
}
