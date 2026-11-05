package fileops

import (
	"errors"
	"strings"

	"golang.org/x/sys/unix"
)

func copyXattrs(src, dst string) error {
	size, err := unix.Llistxattr(src, nil)
	if errors.Is(err, unix.ENOTSUP) {
		return nil
	}
	if err != nil || size == 0 {
		return err
	}
	names := make([]byte, size)
	n, err := unix.Llistxattr(src, names)
	if err != nil {
		return err
	}
	var failed error
	for _, name := range strings.Split(strings.TrimRight(string(names[:n]), "\x00"), "\x00") {
		size, err := unix.Lgetxattr(src, name, nil)
		if err != nil {
			failed = err
			continue
		}
		value := make([]byte, size)
		n, err := unix.Lgetxattr(src, name, value)
		if err != nil {
			failed = err
			continue
		}
		if err := unix.Lsetxattr(dst, name, value[:n], 0); err != nil {
			failed = err
		}
	}
	return failed
}
