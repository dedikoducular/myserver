package updates

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"time"
)

const (
	areaApt    = "apt"
	areaDocker = "docker"
	areaSelf   = "self"

	keepJobs = 50
)

type store struct{ db *sql.DB }

func dbCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

// loadState fills v with the persisted state of an area, if any.
func (s *store) loadState(area string, v any) bool {
	ctx, cancel := dbCtx()
	defer cancel()
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM updates_state WHERE area = ?`, area).Scan(&payload)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("güncelleme durumu okunamadı", "area", area, "error", err.Error())
		}
		return false
	}
	if err := json.Unmarshal([]byte(payload), v); err != nil {
		slog.Warn("güncelleme durumu çözülemedi", "area", area, "error", err.Error())
		return false
	}
	return true
}

func (s *store) saveState(area string, checkedAt int64, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	ctx, cancel := dbCtx()
	defer cancel()
	_, err = s.db.ExecContext(ctx, `INSERT INTO updates_state (area, checked_at, payload) VALUES (?, ?, ?)
		ON CONFLICT(area) DO UPDATE SET checked_at = excluded.checked_at, payload = excluded.payload`,
		area, checkedAt, string(b))
	if err != nil {
		slog.Warn("güncelleme durumu kaydedilemedi", "area", area, "error", err.Error())
	}
}

func (s *store) saveJob(meta JobMeta, log string) {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := s.db.ExecContext(ctx, `INSERT INTO updates_jobs
		(id, kind, title, detail, username, started_at, finished_at, status, message, log)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET finished_at = excluded.finished_at, status = excluded.status,
			message = excluded.message, log = excluded.log`,
		meta.ID, meta.Kind, meta.Title, meta.Detail, meta.Username, meta.StartedAt, meta.FinishedAt,
		meta.Status, meta.Message, log)
	if err != nil {
		slog.Warn("güncelleme işi kaydedilemedi", "id", meta.ID, "error", err.Error())
		return
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM updates_jobs WHERE id NOT IN
		(SELECT id FROM updates_jobs ORDER BY started_at DESC LIMIT ?)`, keepJobs)
}

// closeInterrupted marks jobs left "running" by a previous panel process.
func (s *store) closeInterrupted(except string) {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := s.db.ExecContext(ctx, `UPDATE updates_jobs SET status = 'failed', finished_at = ?,
		message = 'Panel yeniden başlatıldığı için işlemin sonucu izlenemedi.' WHERE status = 'running' AND id <> ?`,
		time.Now().Unix(), except)
	if err != nil {
		slog.Warn("yarım kalan güncelleme işleri kapatılamadı", "error", err.Error())
	}
}

func (s *store) listJobs(ctx context.Context) ([]JobMeta, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, title, detail, username, started_at, finished_at, status, message
		FROM updates_jobs ORDER BY started_at DESC LIMIT ?`, keepJobs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []JobMeta{}
	for rows.Next() {
		var m JobMeta
		if err := rows.Scan(&m.ID, &m.Kind, &m.Title, &m.Detail, &m.Username, &m.StartedAt, &m.FinishedAt, &m.Status, &m.Message); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *store) getJob(ctx context.Context, id string) (JobMeta, string, error) {
	var m JobMeta
	var log string
	err := s.db.QueryRowContext(ctx, `SELECT id, kind, title, detail, username, started_at, finished_at, status, message, log
		FROM updates_jobs WHERE id = ?`, id).
		Scan(&m.ID, &m.Kind, &m.Title, &m.Detail, &m.Username, &m.StartedAt, &m.FinishedAt, &m.Status, &m.Message, &log)
	return m, log, err
}
