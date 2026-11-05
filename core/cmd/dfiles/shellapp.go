package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"

	"github.com/duxlabs-dev/dux-files/core/internal/shellembed"
	"github.com/duxlabs-dev/dux-go/shellapp"
)

var shellApp = newShellApp()

func newShellApp() *shellapp.App {
	var app *shellapp.App
	cfg := shellapp.Config{
		ID:       "dfiles",
		QSAppID:  "dev.duxlabs.files",
		Version:  Version,
		Embedded: embeddedShell{},
		Boot:     func(ctx context.Context) (shellapp.Backend, error) { return bootBackend(ctx, app) },
		ExtraEnv: uiEnv,
	}
	if runtime.GOOS == "darwin" {
		cfg.NativeHost = "Dux Files"
		cfg.ShowMethod = "ui.show"
	}
	app = shellapp.New(cfg)
	return app
}

var (
	dbusActivated   bool
	portalActivated bool
	keepRunning     bool
)

func uiEnv(string) []string {
	var env []string
	if dbusActivated || portalActivated {
		env = append(env, "DFILES_DBUS_ACTIVATED=1")
	}
	if keepRunning {
		env = append(env, "DFILES_KEEP_RUNNING=1")
	}
	return env
}

func bootBackend(ctx context.Context, app *shellapp.App) (shellapp.Backend, error) {
	if _, overridden := os.LookupEnv("DFILES_NATIVE_HELPER"); runtime.GOOS == "darwin" && !overridden {
		host, err := app.NativeHostPath()
		if err != nil {
			return nil, err
		}
		if err := os.Setenv("DFILES_NATIVE_HELPER", filepath.Join(filepath.Dir(host), "dfiles-native")); err != nil {
			return nil, err
		}
	}
	svc, err := bootDaemonServices(ctx, true)
	if err != nil {
		return nil, err
	}
	return svc, nil
}

type embeddedShell struct{}

func (embeddedShell) Available() bool { return shellembed.Available() }

func (embeddedShell) Extract(baseDir string) (string, error) { return shellembed.Extract(baseDir) }

func (embeddedShell) Prune(baseDir, keep string) { shellembed.Prune(baseDir, keep) }
