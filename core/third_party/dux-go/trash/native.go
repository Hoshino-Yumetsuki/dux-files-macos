package trash

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/duxlabs-dev/dux-go/nativebridge"
)

var nativeEnabled = func() bool { return false }
var nativeCall func(context.Context, string, any, any) error

// Native reports whether operations use the system Trash instead of freedesktop paths.
func Native() bool { return nativeEnabled() }

func callNative(method string, params any, result any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	err := nativeCall(ctx, "trash."+method, params, result)
	var nativeErr *nativebridge.Error
	if !errors.As(err, &nativeErr) {
		return err
	}
	switch nativeErr.Code {
	case "EXISTS", "EEXIST":
		return errors.Join(err, os.ErrExist)
	case "NOTFOUND", "ENOENT":
		return errors.Join(err, os.ErrNotExist)
	case "ACCESS", "EACCES", "EPERM":
		return errors.Join(err, os.ErrPermission)
	default:
		return err
	}
}

// Lookup returns an entry with its actual system Trash path and recorded origin.
func Lookup(name, dir string) (Entry, error) {
	if !Native() {
		return Entry{}, errors.New("system Trash is not active")
	}
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return Entry{}, errors.New("invalid trash item name")
	}
	var entry Entry
	err := callNative("get", map[string]string{"name": name, "trashDir": dir}, &entry)
	return entry, err
}

// RestoreTo restores a system Trash entry without replacing an existing target.
// An empty target selects its recorded original location.
func RestoreTo(entry Entry, target string) error {
	if !Native() {
		return errors.New("system Trash is not active")
	}
	if entry.Identity == "" {
		return errors.New("missing trash item identity")
	}
	return callNative("restore", map[string]string{
		"name": entry.Name, "trashDir": entry.TrashDir, "target": target, "identity": entry.Identity,
	}, nil)
}

// Remove permanently deletes the listed system Trash item only if its identity still matches.
func Remove(entry Entry) error {
	if !Native() {
		return errors.New("system Trash is not active")
	}
	if entry.Identity == "" {
		return errors.New("missing trash item identity")
	}
	return callNative("remove", map[string]string{
		"name": entry.Name, "trashDir": entry.TrashDir, "identity": entry.Identity,
	}, nil)
}

// Forget removes Dux metadata after an item has been permanently deleted.
func Forget(path string) error {
	if !Native() {
		return nil
	}
	return callNative("forget", map[string]string{"path": path}, nil)
}
