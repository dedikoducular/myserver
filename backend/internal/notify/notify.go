// Package notify is the notification center shared by all modules.
package notify

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"
)

type Severity string

const (
	Info     Severity = "INFO"
	Success  Severity = "SUCCESS"
	Warning  Severity = "WARNING"
	Error    Severity = "ERROR"
	Critical Severity = "CRITICAL"
)

type Notification struct {
	ID        int64    `json:"id"`
	CreatedAt int64    `json:"created_at"`
	Severity  Severity `json:"severity"`
	Source    string   `json:"source"`
	Title     string   `json:"title"`
	Message   string   `json:"message"`
	Read      bool     `json:"read"`
}

type Center struct {
	db   *sql.DB
	mu   sync.Mutex
	subs map[chan Notification]struct{}
	// writeMu makes the dedupe check and the insert one step, so concurrent
	// PublishOnce calls cannot both pass the check.
	writeMu sync.Mutex
}

func New(db *sql.DB) *Center {
	return &Center{db: db, subs: map[chan Notification]struct{}{}}
}

// Publish stores and broadcasts a notification.
func (c *Center) Publish(ctx context.Context, sev Severity, source, title, message string) {
	c.publish(ctx, sev, source, title, message, "", 0)
}

// PublishOnce is Publish for recurring conditions (disk full, high
// temperature): a notification with the same dedupeKey is suppressed while
// one was created within the window.
func (c *Center) PublishOnce(ctx context.Context, sev Severity, source, title, message, dedupeKey string, window time.Duration) {
	c.publish(ctx, sev, source, title, message, dedupeKey, window)
}

func (c *Center) publish(ctx context.Context, sev Severity, source, title, message, key string, window time.Duration) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	now := time.Now().Unix()
	if key != "" {
		var n int
		err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications
			WHERE dedupe_key = ? AND created_at >= ?`, key, now-int64(window.Seconds())).Scan(&n)
		if err == nil && n > 0 {
			return
		}
	}
	res, err := c.db.ExecContext(ctx, `INSERT INTO notifications
		(created_at, severity, source, title, message, dedupe_key) VALUES (?, ?, ?, ?, ?, ?)`,
		now, string(sev), source, title, message, key)
	if err != nil {
		slog.Error("bildirim kaydedilemedi", "error", err.Error())
		return
	}
	id, _ := res.LastInsertId()
	n := Notification{ID: id, CreatedAt: now, Severity: sev, Source: source, Title: title, Message: message}
	c.mu.Lock()
	for ch := range c.subs {
		select {
		case ch <- n:
		default: // slow subscriber: drop rather than block publishers
		}
	}
	c.mu.Unlock()
}

// Subscribe returns a channel of new notifications and a cancel function.
func (c *Center) Subscribe() (<-chan Notification, func()) {
	ch := make(chan Notification, 16)
	c.mu.Lock()
	c.subs[ch] = struct{}{}
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		delete(c.subs, ch)
		c.mu.Unlock()
	}
}

func (c *Center) List(ctx context.Context, limit int, unreadOnly bool) ([]Notification, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT id, created_at, severity, source, title, message, read_at IS NOT NULL FROM notifications`
	if unreadOnly {
		q += ` WHERE read_at IS NULL`
	}
	q += ` ORDER BY id DESC LIMIT ?`
	rows, err := c.db.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.ID, &n.CreatedAt, &n.Severity, &n.Source, &n.Title, &n.Message, &n.Read); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (c *Center) UnreadCount(ctx context.Context) (int, error) {
	var n int
	err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications WHERE read_at IS NULL`).Scan(&n)
	return n, err
}

func (c *Center) MarkRead(ctx context.Context, id int64) error {
	_, err := c.db.ExecContext(ctx, `UPDATE notifications SET read_at = ? WHERE id = ? AND read_at IS NULL`,
		time.Now().Unix(), id)
	return err
}

func (c *Center) MarkAllRead(ctx context.Context) error {
	_, err := c.db.ExecContext(ctx, `UPDATE notifications SET read_at = ? WHERE read_at IS NULL`, time.Now().Unix())
	return err
}

func (c *Center) Delete(ctx context.Context, id int64) error {
	_, err := c.db.ExecContext(ctx, `DELETE FROM notifications WHERE id = ?`, id)
	return err
}

func (c *Center) Clear(ctx context.Context) error {
	_, err := c.db.ExecContext(ctx, `DELETE FROM notifications`)
	return err
}

// Prune deletes notifications older than the given age.
func (c *Center) Prune(ctx context.Context, olderThan time.Duration) error {
	_, err := c.db.ExecContext(ctx, `DELETE FROM notifications WHERE created_at < ?`,
		time.Now().Add(-olderThan).Unix())
	return err
}
