package filemanager1

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/duxlabs-dev/dux-go/nativebridge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

type folderBridge struct {
	owner    string
	state    string
	sets     int
	getError error
	setError error
	changed  string
	ignored  bool
}

func (b *folderBridge) call(_ context.Context, method string, params, result any) error {
	switch method {
	case "apps.folderHandler.get":
		if b.getError != nil {
			return b.getError
		}
		data, _ := json.Marshal(map[string]any{"owner": b.owner})
		return json.Unmarshal(data, result)
	case "apps.folderHandler.set":
		b.sets++
		args := params.(map[string]any)
		target := args["owner"].(string)
		if target == nativeBundleID {
			data, err := os.ReadFile(b.state)
			if err != nil || string(data) != b.owner+"\n" {
				return errors.New("previous handler was not saved before changing LaunchServices")
			}
		}
		if b.changed != "" {
			b.owner = b.changed
		}
		if args["expectedOwner"] != b.owner {
			return &nativebridge.Error{Code: "EBUSY", Message: "folder handler changed"}
		}
		if b.setError != nil {
			return b.setError
		}
		if b.ignored {
			return &nativebridge.Error{Code: "EIO", Message: "LaunchServices did not select the requested folder handler"}
		}
		b.owner = target
		return nil
	default:
		return errors.New("unexpected native method: " + method)
	}
}

func nativeService(t *testing.T) (*Service, *folderBridge) {
	t.Helper()
	bridge := &folderBridge{owner: "com.apple.finder", state: filepath.Join(t.TempDir(), "dfiles", "folder-handler")}
	return &Service{native: bridge.call, state: bridge.state}, bridge
}

func TestNewNativeDoesNotInvokeHelperOrCreateState(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS native constructor")
	}
	dir := t.TempDir()
	helper := filepath.Join(dir, "helper")
	require.NoError(t, os.WriteFile(helper, []byte("#!/bin/sh\nexit 1\n"), 0o700))
	t.Setenv("DFILES_NATIVE_HELPER", helper)
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	svc, err := NewNative()
	require.NoError(t, err)
	svc.Close()
	assert.NoDirExists(t, filepath.Join(dir, "state"))
}

func TestNativeFolderDefaultIsOnlyChangedByExplicitSet(t *testing.T) {
	svc, bridge := nativeService(t)
	assert.Equal(t, Status{Available: true, Owner: "com.apple.finder"}, svc.Status())
	assert.NoFileExists(t, bridge.state)
	svc.Close()
	assert.Zero(t, bridge.sets)

	status := svc.SetEnabled(true)
	require.Empty(t, status.Reason)
	assert.True(t, status.Enabled)
	assert.True(t, status.Owned)
	assert.Equal(t, nativeBundleID, status.Owner)
	info, err := os.Stat(bridge.state)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	require.Empty(t, svc.SetEnabled(true).Reason)
	assert.Equal(t, 1, bridge.sets)
	svc.Close()
	assert.Equal(t, nativeBundleID, bridge.owner)

	restarted := &Service{native: bridge.call, state: bridge.state}
	status = restarted.SetEnabled(false)
	require.Empty(t, status.Reason)
	assert.False(t, status.Enabled)
	assert.False(t, status.Owned)
	assert.Equal(t, "com.apple.finder", bridge.owner)
}

func TestNativeRestoreNeverOverwritesAnotherApplicationsChoice(t *testing.T) {
	for _, changedDuringRequest := range []bool{false, true} {
		t.Run(map[bool]string{false: "before request", true: "during request"}[changedDuringRequest], func(t *testing.T) {
			svc, bridge := nativeService(t)
			require.True(t, svc.SetEnabled(true).Owned)
			if changedDuringRequest {
				bridge.changed = "com.example.other"
			} else {
				bridge.owner = "com.example.other"
			}
			status := svc.SetEnabled(false)
			assert.Equal(t, "com.example.other", bridge.owner)
			assert.False(t, status.Enabled)
			assert.False(t, status.Owned)
			if changedDuringRequest {
				assert.Contains(t, status.Reason, "EBUSY: folder handler changed")
			} else {
				assert.Equal(t, 1, bridge.sets)
				assert.Empty(t, status.Reason)
			}
		})
	}
}

func TestNativeRestoreRequiresSavedPreviousHandler(t *testing.T) {
	for _, saved := range []string{"", "not a bundle ID", nativeBundleID + "\n"} {
		t.Run(saved, func(t *testing.T) {
			svc, bridge := nativeService(t)
			bridge.owner = nativeBundleID
			if saved != "" {
				require.NoError(t, os.MkdirAll(filepath.Dir(bridge.state), 0o700))
				require.NoError(t, os.WriteFile(bridge.state, []byte(saved), 0o600))
			}
			status := svc.SetEnabled(false)
			assert.True(t, status.Owned)
			assert.True(t, status.Enabled)
			assert.Contains(t, status.Reason, "cannot restore previous folder handler")
			assert.Zero(t, bridge.sets)
		})
	}
}

func TestNativeDoesNotChangeDefaultWithoutSavedState(t *testing.T) {
	svc, bridge := nativeService(t)
	require.NoError(t, os.MkdirAll(bridge.state, 0o700))
	status := svc.SetEnabled(true)
	assert.Contains(t, status.Reason, "save previous folder handler")
	assert.Equal(t, "com.apple.finder", bridge.owner)
	assert.Zero(t, bridge.sets)
}

func TestNativeSerializesStateChangesAcrossInstances(t *testing.T) {
	svc, bridge := nativeService(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(bridge.state), 0o700))
	lock, err := os.OpenFile(bridge.state+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	require.NoError(t, err)
	defer lock.Close()
	require.NoError(t, unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB))
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)

	status := svc.SetEnabled(true)
	assert.Contains(t, status.Reason, "folder handler state is busy")
	assert.Equal(t, "com.apple.finder", bridge.owner)
	assert.Zero(t, bridge.sets)
}

func TestNativeLaunchServicesErrorsAndCurrentOwnershipReachSettings(t *testing.T) {
	svc, bridge := nativeService(t)
	bridge.getError = &nativebridge.Error{Code: "EACCES", Message: "LaunchServices query denied"}
	status := svc.Status()
	assert.False(t, status.Available)
	assert.False(t, status.Owned)
	assert.Empty(t, status.Owner)
	assert.Equal(t, "EACCES: LaunchServices query denied", status.Reason)

	bridge.getError = nil
	bridge.setError = &nativebridge.Error{Code: "EACCES", Message: "LaunchServices change denied"}
	status = svc.SetEnabled(true)
	assert.True(t, status.Available)
	assert.False(t, status.Enabled)
	assert.Equal(t, "com.apple.finder", status.Owner)
	assert.Equal(t, "EACCES: LaunchServices change denied", status.Reason)

	bridge.setError = nil
	require.True(t, svc.SetEnabled(true).Owned)
	bridge.setError = &nativebridge.Error{Code: "ENOENT", Message: "saved folder handler no longer installed"}
	status = svc.SetEnabled(false)
	assert.True(t, status.Owned)
	assert.True(t, status.Enabled)
	assert.Contains(t, status.Reason, "saved folder handler no longer installed")

	bridge.getError = errors.New("current query failed")
	status = svc.Status()
	assert.False(t, status.Available)
	assert.Contains(t, status.Reason, "saved folder handler no longer installed")
	assert.Contains(t, status.Reason, "current query failed")
}

func TestNativeDoesNotClaimIgnoredChangesSucceeded(t *testing.T) {
	svc, bridge := nativeService(t)
	bridge.ignored = true
	status := svc.SetEnabled(true)
	assert.False(t, status.Enabled)
	assert.Contains(t, status.Reason, "LaunchServices did not select")
}

func TestNativeTimedOutChangeRetainsPreviousHandlerForRecovery(t *testing.T) {
	svc, bridge := nativeService(t)
	bridge.setError = &nativebridge.Error{Code: "ETIMEDOUT", Message: "outcome is unknown"}
	status := svc.SetEnabled(true)
	assert.Contains(t, status.Reason, "outcome is unknown")
	data, err := os.ReadFile(bridge.state)
	require.NoError(t, err)
	assert.Equal(t, "com.apple.finder\n", string(data))

	// The system may finish the original request after the helper times out.
	bridge.owner = nativeBundleID
	bridge.setError = nil
	status = svc.SetEnabled(false)
	assert.Empty(t, status.Reason)
	assert.Equal(t, "com.apple.finder", bridge.owner)
}
