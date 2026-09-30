// Package config loads process configuration from the environment.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Version is set at build time with -ldflags "-X myserver/internal/config.Version=...".
var Version = "1.0.0-dev"

type Config struct {
	ListenAddr   string // MYSERVER_LISTEN
	DataDir      string // MYSERVER_DATA_DIR
	ManifestDir  string // MYSERVER_MANIFEST_DIR
	HelperPath   string // MYSERVER_HELPER
	DockerHost   string // MYSERVER_DOCKER_HOST
	LogLevel     string // MYSERVER_LOG_LEVEL
	CookieSecure bool   // MYSERVER_COOKIE_SECURE: force Secure cookies (behind a TLS proxy)
	// TerminalDefaultUser seeds the web terminal's system user on first
	// start; the installer sets it to the user who ran the installation.
	TerminalDefaultUser string // MYSERVER_TERMINAL_DEFAULT_USER
}

func Load() (*Config, error) {
	c := &Config{
		ListenAddr:  env("MYSERVER_LISTEN", ":8080"),
		DataDir:     env("MYSERVER_DATA_DIR", "/var/lib/myserver"),
		ManifestDir: env("MYSERVER_MANIFEST_DIR", "/usr/local/share/myserver/apps/manifests"),
		HelperPath:  env("MYSERVER_HELPER", "/usr/local/libexec/myserver-helper"),
		DockerHost:  env("MYSERVER_DOCKER_HOST", "unix:///var/run/docker.sock"),
		LogLevel:    env("MYSERVER_LOG_LEVEL", "info"),

		TerminalDefaultUser: os.Getenv("MYSERVER_TERMINAL_DEFAULT_USER"),
	}
	if v := os.Getenv("MYSERVER_COOKIE_SECURE"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("MYSERVER_COOKIE_SECURE: %w", err)
		}
		c.CookieSecure = b
	}
	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return nil, fmt.Errorf("data dir: %w", err)
	}
	c.DataDir = abs
	return c, nil
}

func (c *Config) DBPath() string    { return filepath.Join(c.DataDir, "myserver.db") }
func (c *Config) BackupDir() string { return filepath.Join(c.DataDir, "backups") }
func (c *Config) AppsDir() string   { return filepath.Join(c.DataDir, "apps") }
func (c *Config) IconDir() string   { return filepath.Join(filepath.Dir(c.ManifestDir), "icons") }
func (c *Config) UploadTmp() string { return filepath.Join(c.DataDir, "tmp") }

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
