// Package audit records who did what, when and from where.
package audit

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

type Entry struct {
	ID        int64  `json:"id"`
	CreatedAt int64  `json:"created_at"`
	Username  string `json:"username"`
	IP        string `json:"ip"`
	Action    string `json:"action"`
	Target    string `json:"target"`
	Detail    string `json:"detail"`
	Success   bool   `json:"success"`
}

// Actor identifies the caller of an audited action.
type Actor struct {
	Username string
	IP       string
}

type Logger struct{ db *sql.DB }

func New(db *sql.DB) *Logger { return &Logger{db: db} }

// Log stores an audit record. Never pass secrets in target or detail. A
// storage failure is logged but does not fail the audited operation.
func (l *Logger) Log(ctx context.Context, a Actor, action, target, detail string, success bool) {
	// The record must survive cancellation of the request that caused it.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err := l.db.ExecContext(ctx, `INSERT INTO audit_log
		(created_at, username, ip, action, target, detail, success) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		time.Now().Unix(), a.Username, a.IP, action, target, detail, success)
	if err != nil {
		slog.Error("denetim kaydı yazılamadı", "action", action, "error", err.Error())
		return
	}
	slog.Info("denetim", "user", a.Username, "ip", a.IP, "action", action, "target", target, "success", success)
}

// List returns records newest first.
func (l *Logger) List(ctx context.Context, limit, offset int) ([]Entry, int, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := l.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := l.db.QueryContext(ctx, `SELECT id, created_at, username, ip, action, target, detail, success
		FROM audit_log ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.CreatedAt, &e.Username, &e.IP, &e.Action, &e.Target, &e.Detail, &e.Success); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// Prune deletes records older than the given age.
func (l *Logger) Prune(ctx context.Context, olderThan time.Duration) error {
	_, err := l.db.ExecContext(ctx, `DELETE FROM audit_log WHERE created_at < ?`,
		time.Now().Add(-olderThan).Unix())
	return err
}
