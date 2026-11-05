package fileops

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/duxlabs-dev/dux-go/trash"
	"github.com/stretchr/testify/require"
)

func nativeTrashFixture(t *testing.T) (string, map[string]trash.Entry) {
	t.Helper()
	oldNative, oldPut, oldLookup := nativeTrash, putTrash, lookupTrash
	oldRestore, oldList, oldRemove := restoreTrash, listTrash, removeTrash
	t.Cleanup(func() {
		nativeTrash, putTrash, lookupTrash = oldNative, oldPut, oldLookup
		restoreTrash, listTrash, removeTrash = oldRestore, oldList, oldRemove
	})
	dir := filepath.Join(t.TempDir(), ".Trash")
	require.NoError(t, os.Mkdir(dir, 0o700))
	entries := map[string]trash.Entry{}
	nativeTrash = func() bool { return true }
	putTrash = func(path string) (trash.Entry, error) {
		info, err := os.Lstat(path)
		if err != nil {
			return trash.Entry{}, err
		}
		name := filepath.Base(path)
		entry := trash.Entry{Name: name, OriginalPath: path, TrashDir: dir, FilesPath: filepath.Join(dir, name), Identity: name + "-identity", IsDir: info.IsDir()}
		if err := renameNoReplace(path, entry.FilesPath); err != nil {
			return trash.Entry{}, err
		}
		entries[name] = entry
		return entry, nil
	}
	lookupTrash = func(name, directory string) (trash.Entry, error) {
		entry, ok := entries[name]
		if !ok || directory != dir {
			return trash.Entry{}, os.ErrNotExist
		}
		return entry, nil
	}
	restoreTrash = func(entry trash.Entry, target string) error {
		if current := entries[entry.Name]; current.Identity != entry.Identity {
			return errors.New("changed trash item")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := renameNoReplace(entry.FilesPath, target); err != nil {
			return err
		}
		delete(entries, entry.Name)
		return nil
	}
	listTrash = func() ([]trash.Entry, error) {
		var result []trash.Entry
		for _, entry := range entries {
			result = append(result, entry)
		}
		return result, nil
	}
	removeTrash = func(entry trash.Entry) error {
		if err := os.RemoveAll(entry.FilesPath); err != nil {
			return err
		}
		delete(entries, entry.Name)
		return nil
	}
	return dir, entries
}

func TestNativeTrashUndoRedo(t *testing.T) {
	dir, entries := nativeTrashFixture(t)
	path := write(t, filepath.Join(t.TempDir(), "folder", "note.txt"), "body")
	manager, bus := newHarness(t)
	view := run(t, manager, bus, spec{kind: KindTrash, scope: "w1", sources: []string{path}})
	require.Equal(t, StateDone, view.State)
	require.NoFileExists(t, path)
	require.Equal(t, "body", read(t, filepath.Join(dir, "note.txt")))
	require.NoError(t, os.Remove(filepath.Dir(path)))
	require.Equal(t, StateDone, undo(t, manager, bus, "w1").State)
	require.Equal(t, "body", read(t, path))
	require.Empty(t, entries)
	require.Equal(t, StateDone, redo(t, manager, bus, "w1").State)
	require.NoFileExists(t, path)
	require.Len(t, entries, 1)
}

func TestNativeRestoreConflictHandlesExclusiveRenameRace(t *testing.T) {
	dir, entries := nativeTrashFixture(t)
	path := write(t, filepath.Join(t.TempDir(), "note.txt"), "trashed")
	manager, bus := newHarness(t)
	run(t, manager, bus, spec{kind: KindTrash, sources: []string{path}})
	restore := restoreTrash
	attempts := 0
	restoreTrash = func(entry trash.Entry, target string) error {
		attempts++
		if attempts == 1 {
			if err := os.WriteFile(target, []byte("racer"), 0o600); err != nil {
				return err
			}
			return os.ErrExist
		}
		return restore(entry, target)
	}
	bus.onConflict = func(c Conflict) Answer {
		require.NotContains(t, c.Actions, ActionReplace)
		require.NotContains(t, c.Actions, ActionMerge)
		return Answer{ID: c.ID, Action: ActionKeepBoth}
	}
	view := run(t, manager, bus, spec{kind: KindRestore, sources: []string{"note.txt"}, trashDir: dir})
	require.Equal(t, StateDone, view.State)
	require.Equal(t, 2, attempts)
	require.Equal(t, "racer", read(t, path))
	require.Equal(t, "trashed", read(t, filepath.Join(filepath.Dir(path), "note (copy).txt")))
	require.Empty(t, entries)
}

func TestNativeRestoreRejectsForeignAndChangedItems(t *testing.T) {
	dir, entries := nativeTrashFixture(t)
	path := write(t, filepath.Join(t.TempDir(), "note.txt"), "body")
	manager, bus := newHarness(t)
	run(t, manager, bus, spec{kind: KindTrash, scope: "w1", sources: []string{path}})
	entry := entries["note.txt"]
	entry.Identity = "replacement"
	entries[entry.Name] = entry
	bus.onFailure = func(f Failure) Answer { return Answer{ID: f.ID, Action: ActionSkip} }
	view := undo(t, manager, bus, "w1")
	require.Equal(t, 1, view.Skipped)
	require.NoFileExists(t, path)
	require.FileExists(t, entry.FilesPath)
	entry.OriginalPath = ""
	entries[entry.Name] = entry
	view = run(t, manager, bus, spec{kind: KindRestore, sources: []string{entry.Name}, trashDir: dir})
	require.Equal(t, 1, view.Skipped)
	require.NoFileExists(t, path)
	require.FileExists(t, entry.FilesPath)
}

func TestNativeEmptyUsesValidatedRemovalWithoutElevation(t *testing.T) {
	dir, entries := nativeTrashFixture(t)
	path := write(t, filepath.Join(t.TempDir(), "note.txt"), "body")
	manager, bus := newHarness(t)
	run(t, manager, bus, spec{kind: KindTrash, sources: []string{path}})
	var removed trash.Entry
	removeTrash = func(entry trash.Entry) error {
		removed = entry
		return os.ErrPermission
	}
	bus.onFailure = func(f Failure) Answer {
		require.False(t, f.Escalatable)
		return Answer{ID: f.ID, Action: ActionSkip}
	}
	view := run(t, manager, bus, spec{kind: KindEmptyTrash})
	require.Equal(t, StateDone, view.State)
	require.Equal(t, 1, view.Skipped)
	require.Equal(t, entries["note.txt"].Identity, removed.Identity)
	require.FileExists(t, filepath.Join(dir, "note.txt"))
	removeTrash = func(entry trash.Entry) error { return os.Remove(entry.FilesPath) }
	view = run(t, manager, bus, spec{kind: KindEmptyTrash})
	require.Equal(t, StateDone, view.State)
	require.Equal(t, 1, view.Progress.FilesDone)
	require.NoFileExists(t, filepath.Join(dir, "note.txt"))
}
