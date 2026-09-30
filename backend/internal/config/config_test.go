package config

import (
	"path/filepath"
	"testing"
)

var keys = []string{
	"MYSERVER_LISTEN", "MYSERVER_DATA_DIR", "MYSERVER_MANIFEST_DIR", "MYSERVER_HELPER",
	"MYSERVER_DOCKER_HOST", "MYSERVER_LOG_LEVEL", "MYSERVER_COOKIE_SECURE", "MYSERVER_TERMINAL_DEFAULT_USER",
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "")
	}
}

func TestDefaults(t *testing.T) {
	clearEnv(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != ":8080" {
		t.Errorf("ListenAddr %q", c.ListenAddr)
	}
	if filepath.ToSlash(c.DataDir) != filepath.ToSlash(mustAbs(t, "/var/lib/myserver")) {
		t.Errorf("DataDir %q", c.DataDir)
	}
	if c.HelperPath != "/usr/local/libexec/myserver-helper" {
		t.Errorf("HelperPath %q", c.HelperPath)
	}
	if c.DockerHost != "unix:///var/run/docker.sock" {
		t.Errorf("DockerHost %q", c.DockerHost)
	}
	if c.LogLevel != "info" {
		t.Errorf("LogLevel %q", c.LogLevel)
	}
	if c.CookieSecure {
		t.Error("CookieSecure defaults to true; the panel would not work over plain HTTP")
	}
	if c.TerminalDefaultUser != "" {
		t.Errorf("TerminalDefaultUser %q", c.TerminalDefaultUser)
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestEnvironmentOverrides(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	t.Setenv("MYSERVER_LISTEN", "127.0.0.1:9000")
	t.Setenv("MYSERVER_DATA_DIR", dir)
	t.Setenv("MYSERVER_MANIFEST_DIR", filepath.Join(dir, "share", "manifests"))
	t.Setenv("MYSERVER_HELPER", "/opt/helper")
	t.Setenv("MYSERVER_DOCKER_HOST", "unix:///run/user/1000/docker.sock")
	t.Setenv("MYSERVER_LOG_LEVEL", "debug")
	t.Setenv("MYSERVER_COOKIE_SECURE", "true")
	t.Setenv("MYSERVER_TERMINAL_DEFAULT_USER", "ali")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != "127.0.0.1:9000" || c.HelperPath != "/opt/helper" || c.DockerHost != "unix:///run/user/1000/docker.sock" ||
		c.LogLevel != "debug" || !c.CookieSecure || c.TerminalDefaultUser != "ali" {
		t.Fatalf("config %+v", c)
	}
	if c.DataDir != dir {
		t.Errorf("DataDir %q, want %q", c.DataDir, dir)
	}
	if c.DBPath() != filepath.Join(dir, "myserver.db") {
		t.Errorf("DBPath %q", c.DBPath())
	}
	if c.BackupDir() != filepath.Join(dir, "backups") || c.AppsDir() != filepath.Join(dir, "apps") || c.UploadTmp() != filepath.Join(dir, "tmp") {
		t.Errorf("derived directories: %q %q %q", c.BackupDir(), c.AppsDir(), c.UploadTmp())
	}
	if c.IconDir() != filepath.Join(dir, "share", "icons") {
		t.Errorf("IconDir %q", c.IconDir())
	}
}

func TestRelativeDataDirBecomesAbsolute(t *testing.T) {
	clearEnv(t)
	t.Setenv("MYSERVER_DATA_DIR", filepath.Join("relative", "..", "data"))
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(c.DataDir) {
		t.Fatalf("DataDir %q is not absolute", c.DataDir)
	}
	if c.DataDir != mustAbs(t, "data") {
		t.Fatalf("DataDir %q is not cleaned", c.DataDir)
	}
}

func TestCookieSecureValues(t *testing.T) {
	for v, want := range map[string]bool{"1": true, "true": true, "TRUE": true, "t": true, "0": false, "false": false, "False": false} {
		clearEnv(t)
		t.Setenv("MYSERVER_COOKIE_SECURE", v)
		c, err := Load()
		if err != nil {
			t.Errorf("%q: %v", v, err)
			continue
		}
		if c.CookieSecure != want {
			t.Errorf("%q: CookieSecure = %v", v, c.CookieSecure)
		}
	}
	// A typo must stop the start instead of silently disabling the flag.
	for _, v := range []string{"yes", "on", "evet", "2", " true"} {
		clearEnv(t)
		t.Setenv("MYSERVER_COOKIE_SECURE", v)
		if c, err := Load(); err == nil {
			t.Errorf("%q accepted as CookieSecure = %v", v, c.CookieSecure)
		}
	}
}
