// Package settings is the key/value configuration store and its API.
package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"time"
)

// Well-known keys shared between modules.
const (
	KeySetupComplete   = "setup.complete"
	KeyHostname        = "general.hostname"
	KeyTimezone        = "general.timezone"
	KeyLanguage        = "general.language"
	KeyAllowedRoots    = "files.allowed_roots"
	KeyTerminalEnabled = "terminal.enabled"
	KeyTerminalUser    = "terminal.user"
	KeyTerminalTimeout = "terminal.idle_timeout_minutes"
	KeySessionHours    = "security.session_hours"
)

var defaults = map[string]string{
	KeySetupComplete:   "false",
	KeyLanguage:        "tr",
	KeyAllowedRoots:    `["/home","/data","/media","/mnt"]`,
	KeyTerminalEnabled: "true",
	KeyTerminalTimeout: "15",
	KeySessionHours:    "12",
}

// RegisterDefault lets a module declare the default of a key it owns. Call
// it from the module constructor, before the server starts.
func RegisterDefault(key, value string) { defaults[key] = value }

// Store is a cached view of the settings table.
type Store struct {
	db    *sql.DB
	mu    sync.RWMutex
	cache map[string]string
}

func NewStore(ctx context.Context, db *sql.DB) (*Store, error) {
	s := &Store{db: db, cache: map[string]string{}}
	rows, err := db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		s.cache[k] = v
	}
	return s, rows.Err()
}

// Get returns the stored value, or the registered default, or "".
func (s *Store) Get(key string) string {
	s.mu.RLock()
	v, ok := s.cache[key]
	s.mu.RUnlock()
	if ok {
		return v
	}
	return defaults[key]
}

func (s *Store) Bool(key string) bool {
	b, _ := strconv.ParseBool(s.Get(key))
	return b
}

func (s *Store) Int(key string, fallback int) int {
	n, err := strconv.Atoi(s.Get(key))
	if err != nil {
		return fallback
	}
	return n
}

// Strings decodes a JSON string array value.
func (s *Store) Strings(key string) []string {
	var out []string
	if err := json.Unmarshal([]byte(s.Get(key)), &out); err != nil {
		return nil
	}
	return out
}

func (s *Store) Set(ctx context.Context, key, value string) error {
	if key == "" {
		return errors.New("settings: empty key")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, time.Now().Unix())
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.cache[key] = value
	s.mu.Unlock()
	return nil
}

func (s *Store) SetStrings(ctx context.Context, key string, values []string) error {
	if values == nil {
		values = []string{}
	}
	b, err := json.Marshal(values)
	if err != nil {
		return err
	}
	return s.Set(ctx, key, string(b))
}

// All returns defaults overlaid with stored values.
func (s *Store) All() map[string]string {
	out := make(map[string]string, len(defaults))
	for k, v := range defaults {
		out[k] = v
	}
	s.mu.RLock()
	for k, v := range s.cache {
		out[k] = v
	}
	s.mu.RUnlock()
	return out
}
