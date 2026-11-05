package helper

import (
	"errors"
	"io"

	"github.com/duxlabs-dev/dux-go/files"
)

func Run(_ []string, _ io.Reader, _ io.Writer) error {
	return &files.Error{Code: files.CodeNotSupported, Err: errors.New("privileged file operations are unavailable on macOS")}
}

func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	report(stderr, Run(args, stdin, stdout))
	return ExitRefused
}
