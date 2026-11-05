package netmount

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/duxlabs-dev/dux-go/files"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeConnectPassesPasswordSeparatelyAndRemembersOnlyPublicURI(t *testing.T) {
	dir := t.TempDir()
	svc := NewService(nil, dir)
	svc.schemes = []string{"smb"}
	mounted := false
	svc.native = func(_ context.Context, method string, params any, result any) error {
		switch method {
		case "network.connect":
			assert.Equal(t, map[string]any{"uri": "smb://guest@server/Photos", "password": "secret"}, params)
			mounted = true
			return json.Unmarshal([]byte(`{"mount":{"id":"/Volumes/Photos","uri":"smb://guest@server/Photos","name":"Photos","path":"/Volumes/Photos","scheme":"smb","kind":"network"},"location":"/Volumes/Photos"}`), result)
		case "network.list":
			snapshot := Snapshot{Available: true, Schemes: []string{"smb"}}
			if mounted {
				snapshot.Mounts = []Mount{{ID: "/Volumes/Photos", URI: "smb://guest@server/Photos"}}
			}
			*result.(*Snapshot) = snapshot
			return nil
		default:
			t.Fatalf("unexpected native call: %s", method)
			return nil
		}
	}
	connected, err := svc.Connect("smb://guest:secret@server/Photos", "")
	require.NoError(t, err)
	assert.Equal(t, "/Volumes/Photos", connected.Location)
	require.Len(t, svc.Snapshot().Mounts, 1)
	require.Len(t, svc.Servers(), 1)
	assert.NotContains(t, svc.Servers()[0].URI, "secret")
	err = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		require.NoError(t, err)
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.NotContains(t, string(data), "secret")
		return nil
	})
	require.NoError(t, err)
}

func TestNativeConnectRejectsUnsupportedAndMalformedAddressesWithoutCallingHelper(t *testing.T) {
	svc := NewService(nil, t.TempDir())
	svc.schemes = []string{"smb", "dav", "davs"}
	svc.native = func(context.Context, string, any, any) error {
		t.Fatal("invalid address reached native helper")
		return nil
	}
	for _, test := range []struct {
		uri  string
		code files.Code
	}{
		{"sftp://host/folder", files.CodeNotSupported},
		{"smb:///folder", files.CodeInvalid},
		{"smb://host/folder?password=secret", files.CodeInvalid},
		{"smb://host/folder#fragment", files.CodeInvalid},
		{"smb://host/folder?", files.CodeInvalid},
		{"smb://host/folder/%2e%2e/private", files.CodeInvalid},
		{"smb://host/folder/%00", files.CodeInvalid},
	} {
		_, err := svc.Connect(test.uri, "")
		require.Error(t, err)
		assert.Equal(t, test.code, files.CodeOf(err), test.uri)
	}
}

func TestNativeDisconnectReportsBusyWithoutRemovingMount(t *testing.T) {
	svc := NewService(nil, t.TempDir())
	svc.mounts = []Mount{{ID: "/Volumes/Photos", Path: "/Volumes/Photos", URI: "smb://server/Photos"}}
	svc.native = func(_ context.Context, method string, params any, _ any) error {
		assert.Equal(t, "network.disconnect", method)
		assert.Equal(t, map[string]any{"id": "/Volumes/Photos"}, params)
		return errors.New("EBUSY: share is in use")
	}
	err := svc.Disconnect("smb://server/Photos")
	require.Error(t, err)
	assert.Equal(t, files.CodeBusy, files.CodeOf(err))
	assert.Len(t, svc.Snapshot().Mounts, 1)
}

func TestNativePortableMountReportsUnsupported(t *testing.T) {
	svc := NewService(nil, t.TempDir())
	svc.native = func(context.Context, string, any, any) error {
		t.Fatal("portable mount reached native helper")
		return nil
	}
	err := svc.MountPortable("mtp", "device")
	require.Error(t, err)
	assert.Equal(t, files.CodeNotSupported, files.CodeOf(err))
	assert.Contains(t, err.Error(), "MTP and camera devices")
}
