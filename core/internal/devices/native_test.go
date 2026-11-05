package devices

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/duxlabs-dev/dux-go/files"
	"github.com/duxlabs-dev/dux-go/ipc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeDetachRechecksSystemDiskBeforeActing(t *testing.T) {
	for _, method := range []string{"devices.unmount", "devices.eject", "devices.powerOff", "devices.lock"} {
		t.Run(method, func(t *testing.T) {
			svc := NewService(nil, testUID)
			svc.nativeDevices = []Device{{ID: "disk3s1", Drive: "disk0"}}
			var actions []string
			svc.native = func(_ context.Context, method string, _ any, result any) error {
				if method == "volumes.list" {
					*result.(*Snapshot) = Snapshot{Available: true, Devices: []Device{{ID: "disk3s1", Drive: "disk0", Protected: true}}}
					return nil
				}
				actions = append(actions, method)
				return nil
			}
			out := call(t, svc, ipc.Request{ID: 1, Method: method, Params: map[string]any{"id": "disk3s1", "force": true}})
			assert.Equal(t, "EPERM", out["code"])
			assert.Empty(t, actions)
		})
	}
}

func TestNativeDetachFailsClosedWhenRefreshFails(t *testing.T) {
	for _, method := range []string{"devices.unmount", "devices.eject", "devices.powerOff", "devices.lock"} {
		t.Run(method, func(t *testing.T) {
			svc := NewService(nil, testUID)
			svc.nativeDevices = []Device{{ID: "disk7s1", Drive: "disk7"}}
			svc.native = func(_ context.Context, method string, _ any, _ any) error {
				assert.Equal(t, "volumes.list", method)
				return errors.New("EIO: cannot identify all system APFS physical stores")
			}
			out := call(t, svc, ipc.Request{ID: 1, Method: method, Params: map[string]any{"id": "disk7s1", "force": true}})
			assert.Equal(t, "EIO", out["code"])
			assert.False(t, svc.Snapshot().Available)
		})
	}
}

func TestNativeEjectChecksProtectedWholeDriveSiblings(t *testing.T) {
	svc := NewService(nil, testUID)
	svc.native = func(_ context.Context, method string, _ any, result any) error {
		if !assert.Equal(t, "volumes.list", method) {
			return errors.New("unexpected native action")
		}
		*result.(*Snapshot) = Snapshot{Available: true, Devices: []Device{
			{ID: "disk0s1", Drive: "disk0"},
			{ID: "disk3s1", Drive: "disk0", Protected: true},
		}}
		return nil
	}
	out := call(t, svc, ipc.Request{ID: 1, Method: "devices.eject", Params: map[string]any{"id": "disk0s1"}})
	assert.Equal(t, "EPERM", out["code"])
}

func TestNativeUnlockCanMountTheReturnedAPFSVolume(t *testing.T) {
	svc := NewService(nil, testUID)
	unlocked := false
	var actions []string
	svc.native = func(_ context.Context, method string, params any, result any) error {
		switch method {
		case "volumes.list":
			*result.(*Snapshot) = Snapshot{Available: true, Devices: []Device{{ID: "disk7s1", Encrypted: true, Unlocked: unlocked}}}
			return nil
		case "volumes.unlock":
			assert.Equal(t, "secret", params.(map[string]any)["passphrase"])
			unlocked = true
			actions = append(actions, method)
			return json.Unmarshal([]byte(`{"cleartext":"disk7s1"}`), result)
		case "volumes.mount":
			assert.Equal(t, "disk7s1", params.(map[string]any)["id"])
			actions = append(actions, method)
			return json.Unmarshal([]byte(`{"mountPoint":"/Volumes/Vault"}`), result)
		default:
			t.Fatalf("unexpected native call: %s", method)
			return nil
		}
	}
	out := call(t, svc, ipc.Request{ID: 2, Method: "devices.unlock", Params: map[string]any{"id": "disk7s1", "passphrase": "secret", "mount": true}})
	require.NotContains(t, out, "error")
	assert.Equal(t, "/Volumes/Vault", out["result"].(map[string]any)["mountPoint"])
	assert.Equal(t, []string{"volumes.unlock", "volumes.mount"}, actions)
}

func TestNativeActionReportsBusyAndPreservesMountedSnapshot(t *testing.T) {
	svc := NewService(nil, testUID)
	svc.native = func(_ context.Context, method string, params any, result any) error {
		if method == "volumes.list" {
			*result.(*Snapshot) = Snapshot{Available: true, Devices: []Device{{ID: "disk7s1", Mounted: true, MountPoint: "/Volumes/Vault"}}}
			return nil
		}
		assert.Equal(t, "volumes.unmount", method)
		assert.Equal(t, true, params.(map[string]any)["force"])
		return errors.New("EBUSY: volume is in use")
	}
	err := svc.Unmount("disk7s1", true)
	require.Error(t, err)
	assert.Equal(t, files.CodeBusy, files.CodeOf(err))
	assert.True(t, svc.Snapshot().Devices[0].Mounted)
}
