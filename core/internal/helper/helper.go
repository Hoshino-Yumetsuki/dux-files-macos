package helper

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/duxlabs-dev/dux-go/files"
)

const (
	OpLstat     = "lstat"
	OpReadDir   = "readdir"
	OpReadlink  = "readlink"
	OpRead      = "read"
	OpWrite     = "write"
	OpMkdir     = "mkdir"
	OpMkdirAll  = "mkdirall"
	OpSymlink   = "symlink"
	OpRename    = "rename"
	OpRemove    = "remove"
	OpRemoveAll = "removeall"
	OpChmod     = "chmod"
	OpChown     = "chown"
	OpUtimes    = "utimes"
)

const (
	FlagRecursive = "recursive"
	FlagNoReplace = "noreplace"
	Ready         = "ready"
)

type Info struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Mode    uint32 `json:"mode"`
	MtimeNs int64  `json:"mtimeNs"`
	UID     uint32 `json:"uid"`
	GID     uint32 `json:"gid"`
}

type Entry struct {
	Name string `json:"name"`
	Mode uint32 `json:"mode"`
}

type Failure struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

const (
	ExitFailed  = 1
	ExitRefused = 2
)

// (uid_t)-1 is the kernel's keep-unchanged sentinel; -1 is the only way to say keep
const MaxID = 1<<32 - 1

func one(args []string, fn func(path string) error) error {
	if len(args) != 1 {
		return invalid("expected exactly one path")
	}
	return fn(args[0])
}

func withMode(args []string, fn func(path string, mode uint32) error) error {
	if len(args) != 2 {
		return invalid("expected a path and an octal mode")
	}
	mode, err := parseMode(args[1])
	if err != nil {
		return err
	}
	return fn(args[0], mode)
}

func withModeRecursive(args []string, fn func(path string, mode uint32, recursive bool) error) error {
	if len(args) < 2 || len(args) > 3 {
		return invalid("expected a path, an octal mode and optionally recursive")
	}
	mode, err := parseMode(args[1])
	if err != nil {
		return err
	}
	recursive, err := parseFlag(args[2:], FlagRecursive)
	if err != nil {
		return err
	}
	return fn(args[0], mode, recursive)
}

func parseMode(raw string) (uint32, error) {
	mode, err := strconv.ParseUint(raw, 8, 32)
	if err != nil || mode&^0o777 != 0 {
		return 0, invalid("not an octal mode: " + raw)
	}
	return uint32(mode), nil
}

func parseID(raw string) (int, error) {
	id, err := strconv.Atoi(raw)
	if err != nil || id < -1 || id >= MaxID {
		return 0, invalid("not a uid or gid: " + raw)
	}
	return id, nil
}

func parseFlag(args []string, name string) (bool, error) {
	switch {
	case len(args) == 0:
		return false, nil
	case args[0] == name:
		return true, nil
	default:
		return false, invalid("unexpected argument: " + args[0])
	}
}

func invalid(msg string) error {
	return &files.Error{Code: files.CodeInvalid, Err: errors.New(msg)}
}

func report(stderr io.Writer, err error) {
	line, _ := json.Marshal(Failure{Code: string(CodeOf(err)), Error: err.Error()})
	fmt.Fprintln(stderr, string(line))
}

func CodeOf(err error) files.Code {
	var coded *files.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return files.CodeFromOS(err)
}
