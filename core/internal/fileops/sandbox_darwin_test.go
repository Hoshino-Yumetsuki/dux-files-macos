package fileops

import (
	"context"
	"os"
)

// Generic file-operation tests exercise the Go pipeline without a running app.
// The bundled native helper is exercised separately by the app smoke tests.
func init() {
	coordinateTransfer = func(r *runner, src, dst string, move bool) (outcome, error) {
		return r.transfer(src, dst, move)
	}
	preserveMetadata = func(_ context.Context, src, dst string, info os.FileInfo, through FS) error {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if err := through.Chmod(dst, info.Mode().Perm()|0o600, false); err != nil {
			return err
		}
		if _, ok := through.(localFS); ok {
			if err := copyXattrs(src, dst); err != nil {
				return err
			}
		}
		if err := through.Chtimes(dst, info.ModTime()); err != nil {
			return err
		}
		return through.Chmod(dst, info.Mode().Perm(), false)
	}
}
