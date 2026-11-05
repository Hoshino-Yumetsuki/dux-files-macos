//go:build !linux && !darwin

package files

import "io/fs"

type statExtra struct {
	ctimeMs int64
	atimeMs int64
	uid     uint32
	gid     uint32
}

// IsDataless reports whether a file is a macOS File Provider placeholder.
func IsDataless(fs.FileInfo) bool { return false }

func extraStat(fs.FileInfo) (statExtra, bool) { return statExtra{}, false }
