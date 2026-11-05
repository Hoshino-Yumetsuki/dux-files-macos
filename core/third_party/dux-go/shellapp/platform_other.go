//go:build !darwin

package shellapp

import (
	"context"
	"errors"
	"os"
	"os/exec"

	"github.com/duxlabs-dev/dux-go/ipc"
)

func (a *App) NativeHostPath() (string, error) {
	return "", errors.New("native UI host requires macOS")
}

func (a *App) UISocketPath() (string, error) {
	return ipc.FindRunningSocketAny(append([]string{a.cfg.ID}, a.cfg.LegacyIDs...)...)
}

func platformDaemonCommand(args []string) *exec.Cmd {
	return exec.Command(os.Args[0], args...)
}

func (a *App) platformShellDir() (string, error) { return "", nil }

func (a *App) platformUICommand(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "qs", "-p", a.configPath)
}

func (a *App) appendSessionEnv(env []string) []string {
	if !a.sessionManaged {
		return env
	}
	if _, err := exec.LookPath("systemd-run"); err == nil {
		env = append(env, a.cfg.EnvPrefix+"_DEFAULT_LAUNCH_PREFIX=systemd-run --user --scope")
	}
	return env
}

func appendQtEnv(env []string) []string {
	if os.Getenv("QT_QPA_PLATFORMTHEME") == "" {
		env = append(env, "QT_QPA_PLATFORMTHEME=gtk3")
	}
	if os.Getenv("QT_QPA_PLATFORMTHEME_QT6") == "" {
		env = append(env, "QT_QPA_PLATFORMTHEME_QT6=gtk3")
	}
	if os.Getenv("QT_QPA_PLATFORM") == "" {
		env = append(env, "QT_QPA_PLATFORM=wayland;xcb")
	}
	return env
}
