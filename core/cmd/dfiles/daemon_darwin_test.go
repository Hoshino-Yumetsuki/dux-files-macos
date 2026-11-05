package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/duxlabs-dev/dux-files/core/internal/sandbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDarwinDaemonRejectsCopyWithoutNativeHelper(t *testing.T) {
	root := isolate(t)
	t.Setenv("DFILES_NATIVE_HELPER", "")
	short, err := os.MkdirTemp("/tmp", "dfiles-ipc-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	runtimeDir := filepath.Join(short, "r")
	require.NoError(t, os.Symlink(filepath.Join(root, "runtime"), runtimeDir))
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	ctx, cancel := context.WithCancel(context.Background())
	svc, err := bootDaemonServices(ctx, true)
	require.NoError(t, err)
	t.Cleanup(func() { cancel(); svc.Close() })
	t.Setenv("DFILES_SOCKET", svc.SocketPath())
	s, err := dialDaemon()
	require.NoError(t, err)
	t.Cleanup(s.Close)
	require.NoError(t, s.conn.SetDeadline(time.Now().Add(10*time.Second)))

	var caps sandbox.Capabilities
	require.NoError(t, s.call("system.capabilities", nil, &caps))
	assert.False(t, caps.UDisks)
	assert.False(t, caps.Gvfs)
	assert.Equal(t, "NOROUTE", caps.Features["elevation"].Reason)
	assert.True(t, caps.Features["openWith"].Available)
	assert.True(t, caps.Features["openWith"].Degraded)
	assert.Equal(t, "APPBUNDLEPATH", caps.Features["openWith"].Reason)
	var portalStatus struct {
		Available bool   `json:"available"`
		Reason    string `json:"reason"`
	}
	require.NoError(t, s.call("system.portal.get", nil, &portalStatus))
	assert.False(t, portalStatus.Available)
	assert.Equal(t, "NOTSUPPORTED", portalStatus.Reason)

	dest := filepath.Join(root, "copied")
	require.NoError(t, s.call("files.mkdir", map[string]any{"path": dest}, nil))
	source := filepath.Join(root, "home", "source.txt")
	require.NoError(t, os.WriteFile(source, []byte("ipc round trip"), 0o600))
	result, err := runJobUnattended(s, "files.op.copy", map[string]any{"paths": []string{source}, "dest": dest}, "skip")
	require.NoError(t, err)
	require.Error(t, result.err())
	assert.NoFileExists(t, filepath.Join(dest, "source.txt"))
	body, err := os.ReadFile(source)
	require.NoError(t, err)
	assert.Equal(t, "ipc round trip", string(body))

	var listed struct {
		Entries []struct{ Name string } `json:"entries"`
	}
	require.NoError(t, s.call("files.list", map[string]any{"path": dest}, &listed))
	assert.Empty(t, listed.Entries)

	cancel()
	select {
	case err := <-svc.Done():
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("daemon IPC did not stop")
	}
	assert.NoFileExists(t, svc.SocketPath())
}
