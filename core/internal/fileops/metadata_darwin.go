package fileops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/duxlabs-dev/dux-go/nativebridge"
	"golang.org/x/sys/unix"
)

const metadataStrict = true

var cloneSyscall = unix.Clonefile

func cloneFile(src, dst string) (bool, error) {
	var st unix.Stat_t
	if err := unix.Lstat(src, &st); err != nil {
		return false, err
	}
	if st.Flags&(unix.UF_IMMUTABLE|unix.SF_IMMUTABLE|unix.UF_APPEND|unix.SF_APPEND|unix.SF_NOUNLINK) != 0 {
		return false, fmt.Errorf("cannot safely transfer a file with immutable or append-only flags")
	}
	const cloneACL = 0x0004 // CLONE_ACL from sys/clonefile.h
	err := cloneSyscall(src, dst, unix.CLONE_NOFOLLOW|unix.CLONE_NOOWNERCOPY|cloneACL)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EXDEV) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM) {
		return false, nil
	}
	return false, &os.LinkError{Op: "clonefile", Old: src, New: dst, Err: err}
}

var preserveMetadata = nativePreserveMetadata

func nativePreserveMetadata(ctx context.Context, src, dst string, info os.FileInfo, through FS) error {
	if through != local {
		return fmt.Errorf("metadata preservation is unavailable through the elevated filesystem")
	}
	if err := nativebridge.Call(ctx, "fileops.preserve", map[string]string{"src": src, "dst": dst}, nil); err != nil {
		return err
	}
	return verifyXattrs(src, dst)
}

func verifyXattrs(src, dst string) error {
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
	for _, name := range strings.Split(strings.TrimRight(string(names[:n]), "\x00"), "\x00") {
		// macOS rewrites provenance for the creating process; it is not transferable metadata.
		if name == "com.apple.provenance" {
			continue
		}
		size, err := unix.Lgetxattr(src, name, nil)
		if err != nil {
			return err
		}
		original := make([]byte, size)
		n, err := unix.Lgetxattr(src, name, original)
		if err != nil {
			return err
		}
		original = original[:n]
		size, err = unix.Lgetxattr(dst, name, nil)
		if err != nil {
			return fmt.Errorf("preserving extended attribute %s: %w", name, err)
		}
		copied := make([]byte, size)
		n, err = unix.Lgetxattr(dst, name, copied)
		if err != nil {
			return err
		}
		if !bytes.Equal(original, copied[:n]) {
			return fmt.Errorf("extended attribute %s was not preserved", name)
		}
	}
	return nil
}
