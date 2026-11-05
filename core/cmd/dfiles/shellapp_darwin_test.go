package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeStop(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp")
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	socket := filepath.Join(dir, fmt.Sprintf("dfiles-%d.sock", os.Getpid()))
	t.Setenv("DFILES_SOCKET", "")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	t.Cleanup(func() { listener.Close() })
	require.NoError(t, stopNativeUI(), "a headless daemon is not stopped by the UI lifecycle")
	require.FileExists(t, socket)
	host := exec.Command("sleep", "30")
	require.NoError(t, host.Start())
	t.Cleanup(func() { _ = host.Process.Kill(); _ = host.Wait() })
	cleanup, err := shellApp.TrackExternalUI(host.Process.Pid)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	requests := make(chan string, 1)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			conn.Write([]byte("{}\n"))
			line, err := bufio.NewReader(conn).ReadBytes('\n')
			if err != nil {
				conn.Close()
				continue
			}
			var request struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}
			if json.Unmarshal(line, &request) != nil {
				conn.Close()
				continue
			}
			json.NewEncoder(conn).Encode(map[string]any{"id": request.ID, "result": map[string]any{"ok": true}})
			requests <- request.Method
			listener.Close()
			conn.Close()
			return
		}
	}()
	require.NoError(t, stopNativeUI())
	assert.Equal(t, "ui.quit", <-requests)
	_, err = os.Stat(socket)
	assert.True(t, os.IsNotExist(err))
	require.NoError(t, stopNativeUI(), "an already stopped backend is harmless")
}

func TestNativeUITracking(t *testing.T) {
	root := isolate(t)
	unrelated := exec.Command("/bin/sleep", "30")
	require.NoError(t, unrelated.Start())
	t.Cleanup(func() { _ = unrelated.Process.Kill(); _ = unrelated.Wait() })
	for _, raw := range []string{"", "not-a-pid", "0", "1", "-1", strconv.Itoa(os.Getpid()), strconv.Itoa(unrelated.Process.Pid), "999999999"} {
		t.Setenv("DFILES_UI_PID", raw)
		cleanup, err := trackNativeUI()
		if raw == "" {
			require.NoError(t, err)
			assert.Nil(t, cleanup)
			continue
		}
		require.Error(t, err)
		assert.Nil(t, cleanup)
	}
	assert.Empty(t, shellApp.PIDs())

	t.Setenv("DFILES_UI_PID", strconv.Itoa(os.Getppid()))
	cleanup, err := trackNativeUI()
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	t.Cleanup(cleanup)
	assert.Contains(t, shellApp.PIDs(), os.Getppid())
	assert.Contains(t, shellApp.PIDs(), os.Getpid())
	cleanup()
	assert.Empty(t, shellApp.PIDs())
	assert.NoFileExists(t, filepath.Join(root, "runtime", fmt.Sprintf("dfiles-%d.pid", os.Getpid())))

	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "missing"))
	cleanup, err = trackNativeUI()
	require.Error(t, err)
	assert.Nil(t, cleanup)
}

func TestNativeBootHelper(t *testing.T) {
	if os.Getenv("DFILES_HELPER_BOOT_FIXTURE") == "1" {
		executable, err := os.Executable()
		require.NoError(t, err)
		executable, err = filepath.EvalSymlinks(executable)
		require.NoError(t, err)
		helper := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(executable))), "builddir", "macos-dev", "Dux Files.app", "Contents", "MacOS", "dfiles-native")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		backend, err := bootBackend(ctx, shellApp)
		require.NoError(t, err)
		defer backend.Close()
		if _, overridden := os.LookupEnv("DFILES_HELPER_EXPECT_OVERRIDE"); overridden {
			assert.Equal(t, "", os.Getenv("DFILES_NATIVE_HELPER"))
			assert.NoFileExists(t, os.Getenv("DFILES_HELPER_CALLS"))
			return
		}
		assert.Equal(t, helper, os.Getenv("DFILES_NATIVE_HELPER"))
		assert.FileExists(t, os.Getenv("DFILES_HELPER_CALLS"), "native services invoke the helper before the UI starts")
		return
	}

	t.Setenv("TMPDIR", "/tmp")
	root, err := filepath.EvalSymlinks(isolate(t))
	require.NoError(t, err)
	macOS := filepath.Join(root, "builddir", "macos-dev", "Dux Files.app", "Contents", "MacOS")
	require.NoError(t, os.MkdirAll(macOS, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(macOS, "Dux Files"), nil, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(macOS, "dfiles-native"), []byte("#!/bin/sh\nprintf invoked >> \"$DFILES_HELPER_CALLS\"\nexit 1\n"), 0o755))
	executable, err := os.Executable()
	require.NoError(t, err)
	binary, err := os.ReadFile(executable)
	require.NoError(t, err)
	fixture := filepath.Join(root, "core", "bin", "dfiles")
	require.NoError(t, os.MkdirAll(filepath.Dir(fixture), 0o755))
	require.NoError(t, os.WriteFile(fixture, binary, 0o755))
	t.Setenv("PATH", t.TempDir())
	t.Setenv("DFILES_HELPER_BOOT_FIXTURE", "1")
	t.Setenv("DFILES_HELPER_EXPECT_OVERRIDE", "")
	require.NoError(t, os.Unsetenv("DFILES_HELPER_EXPECT_OVERRIDE"))
	t.Setenv("DFILES_NATIVE_HELPER", "")
	t.Setenv("DFILES_HELPER_CALLS", filepath.Join(root, "helper-calls"))
	for _, override := range []bool{false, true} {
		if override {
			t.Setenv("DFILES_HELPER_EXPECT_OVERRIDE", "1")
			t.Setenv("DFILES_HELPER_CALLS", filepath.Join(root, "overridden-helper-calls"))
		} else {
			require.NoError(t, os.Unsetenv("DFILES_NATIVE_HELPER"))
		}
		cmd := exec.Command(fixture, "-test.run=^TestNativeBootHelper$")
		cmd.Env = os.Environ()
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, string(output))
		t.Setenv("DFILES_NATIVE_HELPER", "")
	}
}
