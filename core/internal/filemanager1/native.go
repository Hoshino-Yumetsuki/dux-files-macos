package filemanager1

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/duxlabs-dev/dux-files/core/internal/atomicfile"
	"github.com/duxlabs-dev/dux-go/nativebridge"
	"github.com/duxlabs-dev/dux-go/paths"
	"golang.org/x/sys/unix"
)

const nativeBundleID = "dev.duxlabs.files"

func NewNative() (*Service, error) {
	if runtime.GOOS != "darwin" || !nativebridge.Available() {
		return nil, errors.New("native folder handler unavailable")
	}
	state := filepath.Join(paths.XDGStateHome(), "dfiles", "folder-handler")
	if !filepath.IsAbs(state) {
		return nil, errors.New("folder handler state requires an absolute state directory")
	}
	return &Service{native: nativebridge.Call, state: state}, nil
}

func (s *Service) nativeOwner() (string, error) {
	var result struct {
		Owner string `json:"owner"`
	}
	if err := s.native(context.Background(), "apps.folderHandler.get", nil, &result); err != nil {
		return "", err
	}
	if !bundleID(result.Owner) {
		return "", errors.New("LaunchServices returned no valid default folder handler")
	}
	return result.Owner, nil
}

func nativeOwned(owner string) bool { return strings.EqualFold(owner, nativeBundleID) }

func bundleID(id string) bool {
	if id == "" {
		return false
	}
	for _, ch := range id {
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '.' || ch == '-' {
			continue
		}
		return false
	}
	return true
}

func (s *Service) nativeStatus() Status {
	owner, err := s.nativeOwner()
	if err != nil {
		reason := err.Error()
		if s.reason != "" {
			reason = s.reason + "; " + reason
		}
		return Status{Reason: reason}
	}
	return Status{Available: true, Enabled: nativeOwned(owner), Owned: nativeOwned(owner), Owner: owner, Reason: s.reason}
}

func (s *Service) setNative(enabled bool) Status {
	s.reason = ""
	if err := s.changeNative(enabled); err != nil {
		s.reason = err.Error()
	}
	return s.nativeStatus()
}

func (s *Service) changeNative(enabled bool) error {
	if err := os.MkdirAll(filepath.Dir(s.state), 0o700); err != nil {
		return fmt.Errorf("prepare folder handler state: %w", err)
	}
	lock, err := os.OpenFile(s.state+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("folder handler state is busy: %w", err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)

	owner, err := s.nativeOwner()
	if err != nil {
		return err
	}
	if enabled == nativeOwned(owner) {
		return nil
	}
	target := nativeBundleID
	switch enabled {
	case true:
		if err := atomicfile.WriteFile(s.state, []byte(owner+"\n"), 0o600); err != nil {
			return fmt.Errorf("save previous folder handler: %w", err)
		}
	case false:
		data, err := os.ReadFile(s.state)
		if err != nil {
			return fmt.Errorf("cannot restore previous folder handler: %w", err)
		}
		target = strings.TrimSpace(string(data))
		if !bundleID(target) || nativeOwned(target) {
			return errors.New("cannot restore previous folder handler: saved handler is invalid")
		}
	}
	return s.native(context.Background(), "apps.folderHandler.set", map[string]any{"owner": target, "expectedOwner": owner}, nil)
}
