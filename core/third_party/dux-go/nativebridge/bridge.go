// Package nativebridge calls the bundled macOS platform helper without cgo.
package nativebridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const maxReply = 64 << 20

type Error struct {
	Code    string `json:"code"`
	Message string `json:"error"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func helperPath() (string, error) {
	if runtime.GOOS != "darwin" {
		return "", errors.New("native platform helper requires macOS")
	}
	path, overridden := os.LookupEnv("DFILES_NATIVE_HELPER")
	if !overridden {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		path = filepath.Join(filepath.Dir(exe), "dfiles-native")
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("native platform helper requires an absolute path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("native platform helper is not executable")
	}
	return path, nil
}

func Available() bool {
	_, err := helperPath()
	return err == nil
}

func Call(ctx context.Context, method string, params any, result any) error {
	path, err := helperPath()
	if err != nil {
		return &Error{Code: "NOTSUPPORTED", Message: err.Error()}
	}
	body, err := json.Marshal(struct {
		Method string `json:"method"`
		Params any    `json:"params"`
	}{method, params})
	if err != nil {
		return err
	}
	timeout := 2 * time.Minute
	associationChange := method == "apps.setDefault" || method == "apps.folderHandler.set"
	if associationChange {
		// Allow the helper's consent deadline to elapse and its error to be returned.
		timeout += 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path)
	cmd.Stdin = bytes.NewReader(body)
	stdout := &limitedBuffer{limit: maxReply, cancel: cancel}
	stderr := &limitedBuffer{limit: 8192}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	if stdout.exceeded {
		return &Error{Code: "EIO", Message: "native platform reply exceeds 64 MiB"}
	}
	if ctx.Err() != nil {
		if associationChange {
			return &Error{Code: "ETIMEDOUT", Message: "default application change interrupted; outcome is unknown: " + ctx.Err().Error()}
		}
		return ctx.Err()
	}
	if err != nil {
		var reported Error
		if json.Unmarshal(stdout.Bytes(), &reported) == nil && reported.Message != "" {
			if reported.Code == "" {
				reported.Code = "EIO"
			}
			return &reported
		}
		return fmt.Errorf("native platform helper: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if !json.Valid(stdout.Bytes()) {
		return &Error{Code: "EIO", Message: "native platform helper returned invalid JSON"}
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(stdout.Bytes(), result)
}

type Event struct {
	Topic string          `json:"topic"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// Subscribe keeps one notification-only helper alive until the context ends.
func Subscribe(ctx context.Context) (<-chan Event, error) {
	path, err := helperPath()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, path, "--watch")
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		stdout.Close()
		return nil, err
	}
	events := make(chan Event, 8)
	go func() {
		defer close(events)
		defer cancel()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			var event Event
			if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Topic == "" {
				continue
			}
			select {
			case events <- event:
			case <-ctx.Done():
				stdout.Close()
				_ = cmd.Wait()
				return
			}
		}
		cancel()
		_ = cmd.Wait()
	}()
	return events, nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
	cancel   context.CancelFunc
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.Len()
	if n <= remaining {
		return b.Buffer.Write(p)
	}
	b.Buffer.Write(p[:remaining])
	b.exceeded = true
	if b.cancel != nil {
		b.cancel()
	}
	return n, nil
}
