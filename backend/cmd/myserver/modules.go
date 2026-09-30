package main

import (
	"log/slog"

	"myserver/internal/apps"
	"myserver/internal/backup"
	"myserver/internal/docker"
	"myserver/internal/files"
	"myserver/internal/module"
	"myserver/internal/network"
	"myserver/internal/security"
	"myserver/internal/services"
	"myserver/internal/settings"
	"myserver/internal/storage"
	"myserver/internal/system"
	"myserver/internal/terminal"
	"myserver/internal/updates"
)

// constructor builds one module. A constructor that fails or panics
// disables only its own module.
type constructor struct {
	name string
	new  func(module.Deps, *settings.API) (module.Module, error)
}

// constructors lists the feature modules in load order.
var constructors = []constructor{
	{name: "system", new: system.New},
	{name: "docker", new: docker.New},
	{name: "apps", new: apps.New},
	{name: "files", new: files.New},
	{name: "storage", new: storage.New},
	{name: "services", new: services.New},
	{name: "terminal", new: terminal.New},
	{name: "backup", new: backup.New},
	{name: "updates", new: updates.New},
	{name: "network", new: network.New},
	{name: "firewall", new: security.New},
}

func buildModules(deps module.Deps, api *settings.API) []module.Module {
	var out []module.Module
	for _, c := range constructors {
		m, err := safeNew(c, deps, api)
		if err != nil {
			slog.Error("modül başlatılamadı", "module", c.name, "error", err.Error())
			continue
		}
		out = append(out, m)
	}
	connect(out)
	return out
}

func safeNew(c constructor, deps module.Deps, api *settings.API) (m module.Module, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = panicError{v}
		}
	}()
	return c.new(deps, api)
}

type panicError struct{ v any }

func (p panicError) Error() string { return "panic during module construction" }

// connect wires modules that depend on each other. A missing dependency
// (its module failed to load) is left unset; the dependent module then
// answers with a clear error instead of failing to start.
func connect(mods []module.Module) {
	var appsMod *apps.Module
	var backupMod *backup.Module
	for _, m := range mods {
		switch v := m.(type) {
		case *apps.Module:
			appsMod = v
		case *backup.Module:
			backupMod = v
		}
	}
	if backupMod != nil && appsMod != nil {
		backupMod.SetApps(appsMod)
	}
}
