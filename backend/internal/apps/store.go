package apps

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// ImageInfo identifies the image a service runs.
type ImageInfo struct {
	Image  string `json:"image"`
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

// Installed is one row of apps_installed.
type Installed struct {
	Slug        string
	Name        string
	Version     string
	Config      *Config
	Inputs      *Inputs
	Images      map[string]ImageInfo
	InstalledAt int64
	UpdatedAt   int64
}

type store struct{ db *sql.DB }

const installedCols = `slug, name, version, config, inputs, images, installed_at, updated_at`

func scanInstalled(row interface{ Scan(...any) error }) (*Installed, error) {
	var it Installed
	var cfg, in, img string
	if err := row.Scan(&it.Slug, &it.Name, &it.Version, &cfg, &in, &img, &it.InstalledAt, &it.UpdatedAt); err != nil {
		return nil, err
	}
	it.Config = &Config{}
	if err := json.Unmarshal([]byte(cfg), it.Config); err != nil {
		return nil, err
	}
	it.Inputs = &Inputs{}
	if err := json.Unmarshal([]byte(in), it.Inputs); err != nil {
		return nil, err
	}
	it.Inputs.fill()
	// Rows written before the bind address existed mean "all interfaces".
	NormalizeConfig(it.Config)
	if it.Inputs.BindAddress == "" {
		it.Inputs.BindAddress = it.Config.BindAddress
	}
	it.Images = map[string]ImageInfo{}
	if err := json.Unmarshal([]byte(img), &it.Images); err != nil {
		return nil, err
	}
	return &it, nil
}

func (s *store) list(ctx context.Context) ([]*Installed, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+installedCols+` FROM apps_installed ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Installed{}
	for rows.Next() {
		it, err := scanInstalled(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// get returns nil, nil when the application is not installed.
func (s *store) get(ctx context.Context, slug string) (*Installed, error) {
	it, err := scanInstalled(s.db.QueryRowContext(ctx,
		`SELECT `+installedCols+` FROM apps_installed WHERE slug = ?`, slug))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return it, err
}

func (s *store) save(ctx context.Context, cfg *Config, in *Inputs, images map[string]ImageInfo) error {
	c, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	i, err := json.Marshal(in)
	if err != nil {
		return err
	}
	if images == nil {
		images = map[string]ImageInfo{}
	}
	g, err := json.Marshal(images)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	_, err = s.db.ExecContext(ctx, `INSERT INTO apps_installed
		(slug, name, version, config, inputs, images, installed_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(slug) DO UPDATE SET name = excluded.name, version = excluded.version,
			config = excluded.config, inputs = excluded.inputs, images = excluded.images,
			updated_at = excluded.updated_at`,
		cfg.Slug, cfg.Name, cfg.Version, string(c), string(i), string(g), now, now)
	return err
}

func (s *store) remove(ctx context.Context, slug string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM apps_installed WHERE slug = ?`, slug)
	return err
}

func (s *store) retain(ctx context.Context, slug string, in *Inputs) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO apps_retained (slug, inputs, removed_at) VALUES (?, ?, ?)
		ON CONFLICT(slug) DO UPDATE SET inputs = excluded.inputs, removed_at = excluded.removed_at`,
		slug, string(b), time.Now().Unix())
	return err
}

func (s *store) retained(ctx context.Context, slug string) (*Inputs, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT inputs FROM apps_retained WHERE slug = ?`, slug).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	in := &Inputs{}
	if err := json.Unmarshal([]byte(raw), in); err != nil {
		return nil, err
	}
	in.fill()
	return in, nil
}

func (s *store) dropRetained(ctx context.Context, slug string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM apps_retained WHERE slug = ?`, slug)
	return err
}
