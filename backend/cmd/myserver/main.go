// Command myserver is the MyServer management panel.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"myserver/internal/audit"
	"myserver/internal/auth"
	"myserver/internal/config"
	"myserver/internal/database"
	"myserver/internal/logbuf"
	"myserver/internal/module"
	"myserver/internal/notify"
	"myserver/internal/privileged"
	"myserver/internal/server"
	"myserver/internal/settings"
	"myserver/migrations"
)

func main() {
	showVersion := flag.Bool("version", false, "sürümü yazdır ve çık")
	migrateOnly := flag.Bool("migrate", false, "veritabanını hazırla ve çık")
	flag.Parse()
	if *showVersion {
		fmt.Println(config.Version)
		return
	}
	if err := run(*migrateOnly); err != nil {
		slog.Error("panel başlatılamadı", "error", err.Error())
		os.Exit(1)
	}
}

func run(migrateOnly bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logs := logbuf.NewBuffer(500)
	slog.SetDefault(logbuf.New(os.Stdout, cfg.LogLevel, logs))

	if os.Geteuid() == 0 {
		slog.Warn("panel root olarak çalışıyor; 'myserver' kullanıcısıyla çalıştırılması önerilir")
	}
	for _, dir := range []string{cfg.DataDir, cfg.BackupDir(), cfg.AppsDir(), cfg.UploadTmp()} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("dizin oluşturulamadı %s: %w", dir, err)
		}
	}

	db, err := database.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	mctx, cancel := context.WithTimeout(ctx, time.Minute)
	err = database.Migrate(mctx, db, migrations.FS)
	cancel()
	if err != nil {
		return err
	}
	if migrateOnly {
		slog.Info("veritabanı hazır", "path", cfg.DBPath())
		return nil
	}

	// The data directory holds the password database and app secrets.
	settings.ProtectDir(cfg.DataDir)
	settings.ProtectDir("/var/lib/myserver")
	settings.ProtectDir("/var/lib/myserver-updates")
	store, err := settings.NewStore(ctx, db)
	if err != nil {
		return err
	}
	// Seed the terminal user once. The terminal module and the helper both
	// validate the name before it is ever used.
	if cfg.TerminalDefaultUser != "" && store.Get(settings.KeyTerminalUser) == "" {
		if err := store.Set(ctx, settings.KeyTerminalUser, cfg.TerminalDefaultUser); err != nil {
			slog.Warn("terminal kullanıcısı kaydedilemedi", "error", err.Error())
		}
	}
	auditLog := audit.New(db)
	priv := privileged.New(cfg.HelperPath)
	authSvc, err := auth.NewService(db, cfg, store, auditLog, priv)
	if err != nil {
		return err
	}
	deps := module.Deps{
		Cfg: cfg, DB: db, Settings: store, Audit: auditLog,
		Notify: notify.New(db), Logs: logs, Priv: priv, Auth: authSvc,
	}
	settingsAPI := settings.NewAPI(store, auditLog, priv, auth.ActorFrom)

	slog.Info("MyServer başlatılıyor", "version", config.Version, "data_dir", cfg.DataDir)
	srv := server.New(deps, settingsAPI, buildModules(deps, settingsAPI))
	if err := srv.Run(ctx); err != nil {
		return err
	}
	slog.Info("MyServer durduruldu")
	return nil
}
