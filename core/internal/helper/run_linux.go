package helper

import (
	"fmt"
	"io"
	"log/syslog"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	callerUID, err := strconv.Atoi(os.Getenv("PKEXEC_UID"))
	if err != nil {
		report(stderr, invalid("dfiles helper only runs under pkexec"))
		return ExitRefused
	}
	if os.Getuid() != 0 || os.Geteuid() != 0 {
		report(stderr, invalid("dfiles helper must be started as root"))
		return ExitRefused
	}
	if err := confine(callerUID); err != nil {
		report(stderr, err)
		return ExitRefused
	}
	unix.Umask(0)

	err = Run(args, stdin, stdout)
	journal(callerUID, args, err)
	if err != nil {
		report(stderr, err)
		return ExitFailed
	}
	return 0
}

func Run(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return invalid("missing operation")
	}
	op, rest := args[0], args[1:]

	switch op {
	case OpLstat:
		return one(rest, func(path string) error { return lstat(path, stdout) })
	case OpReadDir:
		return one(rest, func(path string) error { return readDir(path, stdout) })
	case OpReadlink:
		return one(rest, func(path string) error { return readlink(path, stdout) })
	case OpRead:
		return one(rest, func(path string) error { return read(path, stdout) })
	case OpWrite:
		return withMode(rest, func(path string, mode uint32) error { return write(path, mode, stdin, stdout) })
	case OpMkdir:
		return withMode(rest, mkdir)
	case OpMkdirAll:
		return withMode(rest, mkdirAll)
	case OpSymlink:
		if len(rest) != 2 {
			return invalid("symlink takes a target and a path")
		}
		return symlink(rest[0], rest[1])
	case OpRename:
		if len(rest) < 2 || len(rest) > 3 {
			return invalid("rename takes two paths and optionally noreplace")
		}
		noReplace, err := parseFlag(rest[2:], FlagNoReplace)
		if err != nil {
			return err
		}
		return rename(rest[0], rest[1], noReplace)
	case OpRemove:
		return one(rest, remove)
	case OpRemoveAll:
		return one(rest, removeAll)
	case OpChmod:
		return withModeRecursive(rest, chmod)
	case OpChown:
		return chownArgs(rest)
	case OpUtimes:
		if len(rest) != 2 {
			return invalid("utimes takes a path and a time in nanoseconds")
		}
		mtimeNs, err := strconv.ParseInt(rest[1], 10, 64)
		if err != nil {
			return invalid("not a time: " + rest[1])
		}
		return utimes(rest[0], mtimeNs)
	default:
		return invalid("unknown operation: " + op)
	}
}

func chownArgs(args []string) error {
	if len(args) < 3 || len(args) > 4 {
		return invalid("expected a path, a uid, a gid and optionally recursive")
	}
	uid, err := parseID(args[1])
	if err != nil {
		return err
	}
	gid, err := parseID(args[2])
	if err != nil {
		return err
	}
	recursive, err := parseFlag(args[3:], FlagRecursive)
	if err != nil {
		return err
	}
	return chown(args[0], uid, gid, recursive)
}

func journal(uid int, args []string, err error) {
	writer, openErr := syslog.New(syslog.LOG_AUTHPRIV|syslog.LOG_INFO, "dfiles-helper")
	if openErr != nil {
		return
	}
	defer writer.Close()
	outcome := "ok"
	if err != nil {
		outcome = "failed: " + err.Error()
	}
	_ = writer.Info(fmt.Sprintf("uid=%d %s %s", uid, strings.Join(args, " "), outcome))
}
