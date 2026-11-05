package fileops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestDarwinCopyPreservesExtendedAttributes(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "source")
	dst := filepath.Join(root, "destination")
	require.NoError(t, os.WriteFile(src, []byte("body"), 0o600))
	require.NoError(t, os.WriteFile(dst, []byte("body"), 0o600))
	const attr = "com.duxlabs.files.test"
	require.NoError(t, unix.Lsetxattr(src, attr, []byte("metadata"), 0))
	require.NoError(t, copyXattrs(src, dst))
	value := make([]byte, 64)
	n, err := unix.Lgetxattr(dst, attr, value)
	require.NoError(t, err)
	assert.Equal(t, "metadata", string(value[:n]))
}

func TestDarwinVerifyXattrsRejectsUserMetadataLoss(t *testing.T) {
	root := t.TempDir()
	src := write(t, filepath.Join(root, "source"), "body")
	dst := write(t, filepath.Join(root, "destination"), "body")
	const attr = "com.duxlabs.files.test"
	require.NoError(t, unix.Lsetxattr(src, attr, []byte("metadata"), 0))
	require.ErrorContains(t, verifyXattrs(src, dst), attr)
	require.NoError(t, unix.Lsetxattr(dst, attr, []byte("corrupted"), 0))
	require.ErrorContains(t, verifyXattrs(src, dst), attr)
	require.NoError(t, unix.Lsetxattr(dst, attr, []byte("metadata"), 0))
	require.NoError(t, verifyXattrs(src, dst))
}

func TestDarwinRenameDoesNotOverwrite(t *testing.T) {
	root := t.TempDir()
	src, dst := filepath.Join(root, "source"), filepath.Join(root, "destination")
	require.NoError(t, os.WriteFile(src, []byte("new"), 0o600))
	require.NoError(t, os.WriteFile(dst, []byte("old"), 0o600))
	require.ErrorIs(t, renameNoReplace(src, dst), unix.EEXIST)
	body, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "old", string(body))
	require.NoError(t, os.Remove(dst))
	require.NoError(t, renameNoReplace(src, dst))
	assert.NoFileExists(t, src)
}

func TestDarwinClonePropagatesExistingDestination(t *testing.T) {
	root := t.TempDir()
	src := write(t, filepath.Join(root, "src"), "source")
	dst := write(t, filepath.Join(root, "dst"), "destination")
	cloned, err := cloneFile(src, dst)
	require.False(t, cloned)
	require.ErrorIs(t, err, unix.EEXIST)
	require.Equal(t, "destination", read(t, dst))
}

func TestDarwinCloneFallsBackAcrossDevices(t *testing.T) {
	root := t.TempDir()
	src := write(t, filepath.Join(root, "src"), "source")
	previous := cloneSyscall
	cloneSyscall = func(string, string, int) error { return unix.EXDEV }
	t.Cleanup(func() { cloneSyscall = previous })
	cloned, err := cloneFile(src, filepath.Join(root, "dst"))
	require.NoError(t, err)
	require.False(t, cloned)
}

func TestDarwinMoveRetainsSourceWhenMetadataFails(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "dest")
	require.NoError(t, os.Mkdir(dest, 0o755))
	src := write(t, filepath.Join(root, "src"), "source")
	previous := preserveMetadata
	preserveMetadata = func(context.Context, string, string, os.FileInfo, FS) error {
		return errors.New("metadata unavailable")
	}
	t.Cleanup(func() { preserveMetadata = previous })
	manager, _ := newHarness(t)
	j := &job{id: "test", mgr: manager, ctx: context.Background(), state: StateRunning}
	r := &runner{job: j, errorAll: ActionSkip}
	info, err := os.Lstat(src)
	require.NoError(t, err)
	err = r.transferFile(src, filepath.Join(dest, "src"), info, true, true)
	require.ErrorIs(t, err, errSkipped)
	require.Equal(t, "source", read(t, src))
	require.NoFileExists(t, filepath.Join(dest, "src"))
	require.Empty(t, tempsIn(t, dest))
}

func TestDarwinTreeMoveRetainsAllSourcesWhenDirectoryMetadataFails(t *testing.T) {
	root := t.TempDir()
	src, dst := filepath.Join(root, "src"), filepath.Join(root, "dest")
	write(t, filepath.Join(src, "a"), "a")
	write(t, filepath.Join(src, "sub", "b"), "b")
	require.NoError(t, os.Mkdir(dst, 0o755))
	previous := preserveMetadata
	preserveMetadata = func(ctx context.Context, from, to string, info os.FileInfo, through FS) error {
		if from == src {
			return errors.New("directory metadata unavailable")
		}
		return previous(ctx, from, to, info, through)
	}
	t.Cleanup(func() { preserveMetadata = previous })
	manager, _ := newHarness(t)
	j := &job{id: "test", mgr: manager, ctx: context.Background(), state: StateRunning}
	r := &runner{job: j, errorAll: ActionSkip, conflictAll: ActionMerge}
	_, err := r.transfer(src, dst, true)
	require.ErrorIs(t, err, errSkipped)
	require.Equal(t, "a", read(t, filepath.Join(src, "a")))
	require.Equal(t, "b", read(t, filepath.Join(src, "sub", "b")))
	require.Equal(t, "a", read(t, filepath.Join(dst, "a")))
	require.Equal(t, "b", read(t, filepath.Join(dst, "sub", "b")))
}
