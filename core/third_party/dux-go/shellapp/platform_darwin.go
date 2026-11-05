package shellapp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/duxlabs-dev/dux-go/ipc"
)

func platformDaemonCommand(args []string) *exec.Cmd {
	executable, err := os.Executable()
	if err != nil {
		return &exec.Cmd{Err: err}
	}
	return exec.Command(executable, args...)
}

func nativeHostPath(executable, name string) (string, error) {
	if name == "" || filepath.Base(name) != name {
		return "", fmt.Errorf("invalid native UI executable name %q", name)
	}
	executable, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(executable)
	candidates := []string{filepath.Join(dir, name)}
	if filepath.Base(dir) == "bin" && filepath.Base(filepath.Dir(dir)) == "core" {
		repo := filepath.Dir(filepath.Dir(dir))
		for _, build := range []string{"builddir/macos-dev", "build/macos-static"} {
			candidates = append(candidates, filepath.Join(repo, build, name+".app", "Contents", "MacOS", name))
		}
	}
	for _, path := range candidates {
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return path, nil
		}
	}
	return "", fmt.Errorf("native UI %q not found alongside %s; build or install its macOS app bundle", name, executable)
}

func (a *App) NativeHostPath() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return nativeHostPath(executable, a.cfg.NativeHost)
}

func (a *App) UISocketPath() (string, error) {
	if socket := os.Getenv(a.cfg.EnvPrefix + "_SOCKET"); socket != "" {
		return socket, nil
	}
	if a.cfg.NativeHost == "" {
		return ipc.FindRunningSocketAny(append([]string{a.cfg.ID}, a.cfg.LegacyIDs...)...)
	}
	for _, pid := range a.PIDs() {
		for _, id := range append([]string{a.cfg.ID}, a.cfg.LegacyIDs...) {
			base := filepath.Join(a.runtimeDir(), fmt.Sprintf("%s-%d", id, pid))
			if _, err := os.Stat(base + pidFileExtension); err != nil {
				continue
			}
			if info, err := os.Stat(base + ".sock"); err == nil && info.Mode()&os.ModeSocket != 0 {
				return base + ".sock", nil
			}
		}
	}
	return "", fmt.Errorf("no running %s UI found", a.cfg.ID)
}

func (a *App) platformShellDir() (string, error) {
	if a.cfg.NativeHost == "" {
		return "", nil
	}
	host, err := a.NativeHostPath()
	if err != nil {
		return "", err
	}
	dir := filepath.Clean(filepath.Join(filepath.Dir(host), "..", "Resources", "qml", "qs"))
	if err := validateShellDir(dir); err != nil {
		return "", fmt.Errorf("bundled native UI: %w", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "qmldir")); err != nil {
		return "", fmt.Errorf("native UI requires staged QML with generated qmldir files: %w", err)
	}
	return dir, nil
}

func (a *App) platformUICommand(ctx context.Context) *exec.Cmd {
	if a.cfg.NativeHost == "" {
		return exec.CommandContext(ctx, "qs", "-p", a.configPath)
	}
	host, err := a.NativeHostPath()
	if err != nil {
		return &exec.Cmd{Err: err}
	}
	return exec.CommandContext(ctx, host, "--no-backend", "-p", a.configPath)
}

func (a *App) appendSessionEnv(env []string) []string { return env }

func appendQtEnv(env []string) []string { return env }
