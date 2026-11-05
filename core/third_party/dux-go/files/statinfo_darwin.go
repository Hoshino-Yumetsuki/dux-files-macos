package files

import (
	"io/fs"
	"syscall"
)

type statExtra struct {
	ctimeMs int64
	atimeMs int64
	uid     uint32
	gid     uint32
}

// IsDataless reports File Provider placeholders without opening their contents.
func IsDataless(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Flags&0x40000000 != 0 // SF_DATALESS, <sys/stat.h>
}

func extraStat(info fs.FileInfo) (statExtra, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return statExtra{}, false
	}
	return statExtra{
		ctimeMs: st.Ctimespec.Sec*1000 + st.Ctimespec.Nsec/1e6,
		atimeMs: st.Atimespec.Sec*1000 + st.Atimespec.Nsec/1e6,
		uid:     st.Uid,
		gid:     st.Gid,
	}, true
}
