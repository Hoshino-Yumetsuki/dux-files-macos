package shellapp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/duxlabs-dev/dux-go/ipc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDarwinNativeHostCommand(t *testing.T) {
	if os.Getenv("SHELLAPP_NATIVE_FIXTURE") == "1" {
		executable, err := os.Executable()
		require.NoError(t, err)
		executable, err = filepath.EvalSymlinks(executable)
		require.NoError(t, err)
		macOS := filepath.Dir(executable)
		shell := filepath.Clean(filepath.Join(macOS, "..", "Resources", "qml", "qs"))
		a := New(Config{ID: "danktest", NativeHost: "Dux Files", QSAppID: "dev.duxlabs.danktest"})
		require.NoError(t, a.ResolveConfig(nil, nil))
		assert.Equal(t, shell, a.ConfigPath())
		a.startHidden = true
		cmd := a.buildUICommand(context.Background(), "/test/backend.sock")
		assert.Equal(t, []string{filepath.Join(macOS, "Dux Files"), "--no-backend", "-p", shell}, cmd.Args)
		output, err := cmd.Output()
		require.NoError(t, err)
		assert.Equal(t, "--no-backend\n-p\n"+shell+"\n/test/backend.sock\n1\n\n\n\n", string(output))
		require.NoError(t, os.Remove(filepath.Join(shell, "qmldir")))
		assert.ErrorContains(t, a.ResolveConfig(nil, nil), "requires staged QML")
		return
	}

	root := t.TempDir()
	macOS := filepath.Join(root, "Dux Files.app", "Contents", "MacOS")
	shell := filepath.Join(root, "Dux Files.app", "Contents", "Resources", "qml", "qs")
	require.NoError(t, os.MkdirAll(macOS, 0o755))
	require.NoError(t, os.MkdirAll(shell, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(shell, "shell.qml"), []byte("import QtQuick\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(shell, "qmldir"), []byte("module qs\n"), 0o644))
	fake := "#!/bin/sh\nprintf '%s\\n' \"$@\" \"$DANKTEST_SOCKET\" \"$DANKTEST_START_HIDDEN\" \"$QT_QPA_PLATFORM\" \"$QT_QPA_PLATFORMTHEME\" \"$QT_QPA_PLATFORMTHEME_QT6\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(macOS, "Dux Files"), []byte(fake), 0o755))
	executable, err := os.Executable()
	require.NoError(t, err)
	binary, err := os.ReadFile(executable)
	require.NoError(t, err)
	fixture := filepath.Join(macOS, "dfiles")
	require.NoError(t, os.WriteFile(fixture, binary, 0o755))
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("DANKTEST_SHELL_DIR", "")
	for _, key := range []string{"QT_QPA_PLATFORM", "QT_QPA_PLATFORMTHEME", "QT_QPA_PLATFORMTHEME_QT6"} {
		t.Setenv(key, "")
		require.NoError(t, os.Unsetenv(key))
	}
	cmd := exec.Command(fixture, "-test.run=^TestDarwinNativeHostCommand$")
	cmd.Env = append(os.Environ(), "SHELLAPP_NATIVE_FIXTURE=1")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
}

func TestDarwinDaemonExecutable(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	original := os.Args[0]
	os.Args[0] = "not-a-binary-on-path"
	t.Cleanup(func() { os.Args[0] = original })
	t.Setenv("PATH", t.TempDir())
	cmd := platformDaemonCommand([]string{"run", "-d", "--daemon-child"})
	assert.Equal(t, executable, cmd.Path)
	assert.Equal(t, []string{executable, "run", "-d", "--daemon-child"}, cmd.Args)
	require.NoError(t, cmd.Err)
}

func TestDarwinNativeHostResolution(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	cli := filepath.Join(root, "core", "bin", "dfiles")
	require.NoError(t, os.MkdirAll(filepath.Dir(cli), 0o755))
	require.NoError(t, os.WriteFile(cli, nil, 0o755))
	t.Setenv("PATH", t.TempDir())
	_, err = nativeHostPath(cli, "Dux Files")
	require.ErrorContains(t, err, "not found alongside")

	development := filepath.Join(root, "builddir", "macos-dev", "Dux Files.app", "Contents", "MacOS", "Dux Files")
	require.NoError(t, os.MkdirAll(filepath.Dir(development), 0o755))
	require.NoError(t, os.WriteFile(development, nil, 0o755))
	path, err := nativeHostPath(cli, "Dux Files")
	require.NoError(t, err)
	assert.Equal(t, development, path)

	sibling := filepath.Join(filepath.Dir(cli), "Dux Files")
	require.NoError(t, os.WriteFile(sibling, nil, 0o644))
	path, err = nativeHostPath(cli, "Dux Files")
	require.NoError(t, err)
	assert.Equal(t, development, path)
	require.NoError(t, os.Chmod(sibling, 0o755))
	path, err = nativeHostPath(cli, "Dux Files")
	require.NoError(t, err)
	assert.Equal(t, sibling, path)

	link := filepath.Join(root, "linked-dfiles")
	require.NoError(t, os.Symlink(cli, link))
	path, err = nativeHostPath(link, "Dux Files")
	require.NoError(t, err)
	assert.Equal(t, sibling, path)
	_, err = nativeHostPath(cli, "../Dux Files")
	assert.ErrorContains(t, err, "invalid native UI executable")
}

func TestDarwinNativeUICallsRequireTrackedHost(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp")
	a := testApp(t)
	a.cfg.NativeHost = "Dux Files"
	t.Setenv("DANKTEST_SOCKET", "")
	requests := make(chan ipc.Request, 2)
	srv := ipc.NewServer(ipc.Config{AppName: "danktest", APIVersion: 1},
		func(_ context.Context, w *ipc.ConnWriter, req ipc.Request, _ *ipc.Subscriber) {
			requests <- req
			w.Respond(req.ID, map[string]any{"ok": true})
		})
	require.NoError(t, srv.Listen())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); _ = srv.Close() })
	go func() { _ = srv.Serve(ctx) }()

	require.Error(t, a.CallUI("ui.show", nil), "a headless daemon is not a UI")
	cleanup, err := a.TrackExternalUI(os.Getppid())
	require.NoError(t, err)
	t.Cleanup(cleanup)
	require.NoError(t, a.CallUI("ui.open", map[string]any{"path": "/tracked"}))
	request := <-requests
	assert.Equal(t, "ui.open", request.Method)
	assert.Equal(t, map[string]any{"path": "/tracked"}, request.Params)
	cleanup()
	require.Error(t, a.CallUI("ui.show", nil), "cleanup makes the daemon undiscoverable as a UI")

	t.Setenv("DANKTEST_SOCKET", srv.SocketPath())
	require.NoError(t, a.CallUI("ui.show", nil), "explicit socket selection bypasses UI discovery")
	assert.Equal(t, "ui.show", (<-requests).Method)
}
