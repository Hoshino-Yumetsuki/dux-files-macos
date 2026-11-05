package fileops

import (
	"context"
	"errors"
	"os"
)

const metadataStrict = false

func cloneFile(src, dst string) (bool, error) { return false, nil }

func preserveMetadata(_ context.Context, src, dst string, info os.FileInfo, through FS) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	err := through.Chmod(dst, info.Mode().Perm(), false)
	err = errors.Join(err, through.Chtimes(dst, info.ModTime()))
	if through == local {
		err = errors.Join(err, copyXattrs(src, dst))
	}
	return err
}
