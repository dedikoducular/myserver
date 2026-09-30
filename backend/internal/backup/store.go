package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// Backup statuses.
const (
	statusRunning   = "running"
	statusSuccess   = "success"
	statusFailed    = "failed"
	statusCancelled = "cancelled"
)

// Consistency modes.
const (
	consistencyStopped = "stopped"
	consistencyLive    = "live"
)

// Triggers.
const (
	triggerManual    = "manual"
	triggerScheduled = "scheduled"
	triggerSafety    = "safety"
	triggerImported  = "imported"
)

// Record is one row of backup_backups.
type Record struct {
	ID            int64    `json:"id"`
	Slug          string   `json:"slug"`
	AppName       string   `json:"app_name"`
	AppVersion    string   `json:"app_version"`
	FileName      string   `json:"file_name"`
	Size          int64    `json:"size"`
	CreatedAt     int64    `json:"created_at"`
	Duration      int64    `json:"duration"`
	Status        string   `json:"status"`
	Consistency   string   `json:"consistency"`
	AppWasRunning bool     `json:"-"`
	Encrypted     bool     `json:"encrypted"`
	IncludesBinds bool     `json:"includes_binds"`
	Trigger       string   `json:"trigger"`
	Error         string   `json:"error"`
	Warnings      []string `json:"warnings"`
	VerifiedAt    int64    `json:"verified_at"`
	VerifyOK      bool     `json:"verify_ok"`
	// FileMissing is set when the archive is no longer on disk.
	FileMissing bool `json:"file_missing"`
}

// Schedule is one row of backup_schedules.
type Schedule struct {
	Slug         string `json:"slug"`
	Enabled      bool   `json:"enabled"`
	Frequency    string `json:"frequency"`
	Hour         int    `json:"hour"`
	Minute       int    `json:"minute"`
	Weekday      int    `json:"weekday"`
	Monthday     int    `json:"monthday"`
	KeepLast     int    `json:"keep_last"`
	PruneManual  bool   `json:"prune_manual"`
	Live         bool   `json:"live"`
	IncludeBinds bool   `json:"include_binds"`
	LastRunAt    int64  `json:"last_run_at"`
	UpdatedAt    int64  `json:"updated_at"`
	// NextRunAt is computed, 0 when the schedule is disabled.
	NextRunAt int64 `json:"next_run_at"`
	// Exists is false for the defaults of an application without a
	// stored schedule.
	Exists bool `json:"exists"`
}

type store struct{ db *sql.DB }

const recordCols = `id, slug, app_name, app_version, file_name, size, created_at, duration, status,
	consistency, app_was_running, encrypted, includes_binds, trigger_kind, error, warnings, verified_at, verify_ok`

func scanRecord(row interface{ Scan(...any) error }) (*Record, error) {
	var r Record
	var warnings string
	if err := row.Scan(&r.ID, &r.Slug, &r.AppName, &r.AppVersion, &r.FileName, &r.Size, &r.CreatedAt, &r.Duration,
		&r.Status, &r.Consistency, &r.AppWasRunning, &r.Encrypted, &r.IncludesBinds, &r.Trigger, &r.Error,
		&warnings, &r.VerifiedAt, &r.VerifyOK); err != nil {
		return nil, err
	}
	r.Warnings = []string{}
	_ = json.Unmarshal([]byte(warnings), &r.Warnings)
	if r.Warnings == nil {
		r.Warnings = []string{}
	}
	return &r, nil
}

func warningsJSON(w []string) string {
	if w == nil {
		w = []string{}
	}
	b, err := json.Marshal(w)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func (s *store) insert(ctx context.Context, r *Record) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO backup_backups
		(slug, app_name, app_version, file_name, size, created_at, duration, status, consistency,
		 app_was_running, encrypted, includes_binds, trigger_kind, error, warnings)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Slug, r.AppName, r.AppVersion, r.FileName, r.Size, r.CreatedAt, r.Duration, r.Status, r.Consistency,
		r.AppWasRunning, r.Encrypted, r.IncludesBinds, r.Trigger, r.Error, warningsJSON(r.Warnings))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// finish stores the outcome of a backup.
func (s *store) finish(ctx context.Context, r *Record) error {
	_, err := s.db.ExecContext(ctx, `UPDATE backup_backups SET file_name = ?, size = ?, duration = ?, status = ?,
		consistency = ?, app_was_running = ?, encrypted = ?, error = ?, warnings = ? WHERE id = ?`,
		r.FileName, r.Size, r.Duration, r.Status, r.Consistency, r.AppWasRunning, r.Encrypted, r.Error,
		warningsJSON(r.Warnings), r.ID)
	return err
}

func (s *store) setAppWasRunning(ctx context.Context, id int64, v bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE backup_backups SET app_was_running = ? WHERE id = ?`, v, id)
	return err
}

func (s *store) setVerified(ctx context.Context, id int64, ok bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE backup_backups SET verified_at = ?, verify_ok = ? WHERE id = ?`,
		time.Now().Unix(), ok, id)
	return err
}

// get returns nil, nil when the record does not exist.
func (s *store) get(ctx context.Context, id int64) (*Record, error) {
	r, err := scanRecord(s.db.QueryRowContext(ctx, `SELECT `+recordCols+` FROM backup_backups WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

func (s *store) query(ctx context.Context, where string, args ...any) ([]*Record, error) {
	q := `SELECT ` + recordCols + ` FROM backup_backups`
	if where != "" {
		q += ` WHERE ` + where
	}
	q += ` ORDER BY created_at DESC, id DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Record{}
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *store) list(ctx context.Context, slug string) ([]*Record, error) {
	if slug != "" {
		return s.query(ctx, `slug = ?`, slug)
	}
	return s.query(ctx, ``)
}

func (s *store) remove(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM backup_backups WHERE id = ?`, id)
	return err
}

// fileNameTaken reports whether a record already uses the file name.
func (s *store) fileNameTaken(ctx context.Context, slug, name string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM backup_backups WHERE slug = ? AND file_name = ?`, slug, name).Scan(&n)
	return n > 0, err
}

// lastScheduled returns the most recent finished scheduled backup of every
// application.
func (s *store) lastScheduled(ctx context.Context) ([]*Record, error) {
	return s.query(ctx, `trigger_kind = 'scheduled' AND status != 'running' AND id IN
		(SELECT MAX(id) FROM backup_backups WHERE trigger_kind = 'scheduled' AND status != 'running' GROUP BY slug)`)
}

const scheduleCols = `slug, enabled, frequency, hour, minute, weekday, monthday, keep_last, prune_manual,
	live, include_binds, last_run_at, updated_at`

func scanSchedule(row interface{ Scan(...any) error }) (*Schedule, error) {
	var s Schedule
	if err := row.Scan(&s.Slug, &s.Enabled, &s.Frequency, &s.Hour, &s.Minute, &s.Weekday, &s.Monthday,
		&s.KeepLast, &s.PruneManual, &s.Live, &s.IncludeBinds, &s.LastRunAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	s.Exists = true
	return &s, nil
}

// schedule returns nil, nil when the application has no schedule.
func (s *store) schedule(ctx context.Context, slug string) (*Schedule, error) {
	sc, err := scanSchedule(s.db.QueryRowContext(ctx, `SELECT `+scheduleCols+` FROM backup_schedules WHERE slug = ?`, slug))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return sc, err
}

func (s *store) schedules(ctx context.Context) ([]*Schedule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+scheduleCols+` FROM backup_schedules ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Schedule{}
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *store) saveSchedule(ctx context.Context, sc *Schedule) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO backup_schedules
		(slug, enabled, frequency, hour, minute, weekday, monthday, keep_last, prune_manual, live,
		 include_binds, last_run_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)
		ON CONFLICT(slug) DO UPDATE SET enabled = excluded.enabled, frequency = excluded.frequency,
			hour = excluded.hour, minute = excluded.minute, weekday = excluded.weekday,
			monthday = excluded.monthday, keep_last = excluded.keep_last,
			prune_manual = excluded.prune_manual, live = excluded.live,
			include_binds = excluded.include_binds, updated_at = excluded.updated_at`,
		sc.Slug, sc.Enabled, sc.Frequency, sc.Hour, sc.Minute, sc.Weekday, sc.Monthday, sc.KeepLast,
		sc.PruneManual, sc.Live, sc.IncludeBinds, sc.UpdatedAt)
	return err
}

func (s *store) setLastRun(ctx context.Context, slug string, at int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE backup_schedules SET last_run_at = ? WHERE slug = ?`, at, slug)
	return err
}
