package trash

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/duxlabs-dev/dux-go/nativebridge"
	"github.com/stretchr/testify/require"
)

func TestNativeRestoreAndRemoveKeepIdentityAndMapConflicts(t *testing.T) {
	oldEnabled, oldCall := nativeEnabled, nativeCall
	t.Cleanup(func() { nativeEnabled, nativeCall = oldEnabled, oldCall })
	nativeEnabled = func() bool { return true }
	root := t.TempDir()
	entry := Entry{Name: "note.txt", TrashDir: filepath.Join(root, ".Trash"), Identity: "device:inode:birth"}
	var method string
	var params map[string]string
	code := "EXISTS"
	nativeCall = func(ctx context.Context, name string, input, output any) error {
		require.NoError(t, ctx.Err())
		method, params = name, input.(map[string]string)
		return &nativebridge.Error{Code: code, Message: "native operation failed"}
	}
	target := filepath.Join(root, "note.txt")
	err := RestoreTo(entry, target)
	require.ErrorIs(t, err, os.ErrExist)
	require.Equal(t, "trash.restore", method)
	require.Equal(t, entry.Identity, params["identity"])
	require.Equal(t, entry.TrashDir, params["trashDir"])
	require.Equal(t, target, params["target"])
	var nativeErr *nativebridge.Error
	require.ErrorAs(t, err, &nativeErr)
	for _, test := range []struct {
		code string
		want error
	}{
		{"EXISTS", os.ErrExist}, {"EEXIST", os.ErrExist},
		{"NOTFOUND", os.ErrNotExist}, {"ENOENT", os.ErrNotExist},
		{"ACCESS", os.ErrPermission}, {"EACCES", os.ErrPermission}, {"EPERM", os.ErrPermission},
	} {
		code = test.code
		require.ErrorIs(t, RestoreTo(entry, target), test.want, code)
	}

	nativeCall = func(_ context.Context, name string, input, output any) error {
		method, params = name, input.(map[string]string)
		return nil
	}
	require.NoError(t, Remove(entry))
	require.Equal(t, "trash.remove", method)
	require.Equal(t, entry.Identity, params["identity"])
	entry.Identity = ""
	require.Error(t, RestoreTo(entry, target))
	require.Error(t, Remove(entry))
}

func TestNativeLookupRefusesTraversalWithoutCallingHelper(t *testing.T) {
	oldEnabled, oldCall := nativeEnabled, nativeCall
	t.Cleanup(func() { nativeEnabled, nativeCall = oldEnabled, oldCall })
	nativeEnabled = func() bool { return true }
	called := false
	nativeCall = func(context.Context, string, any, any) error {
		called = true
		return errors.New("unexpected call")
	}
	for _, name := range []string{"", ".", "..", "../note.txt", "folder/note.txt"} {
		_, err := Lookup(name, t.TempDir())
		require.Error(t, err)
	}
	require.False(t, called)
}
