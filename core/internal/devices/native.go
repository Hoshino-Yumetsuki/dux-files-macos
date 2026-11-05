package devices

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/duxlabs-dev/dux-go/files"
)

func (s *Service) Refresh() {
	if s.native == nil {
		return
	}
	_ = s.refreshNative()
	s.publish()
}

func (s *Service) refreshNative() error {
	s.nativeMu.Lock()
	defer s.nativeMu.Unlock()
	return s.reloadNative()
}

func (s *Service) reloadNative() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var snapshot Snapshot
	if err := s.native(ctx, "volumes.list", nil, &snapshot); err != nil {
		s.setReason("native volume service is unavailable")
		return nativeError(err)
	}
	s.mu.Lock()
	s.nativeDevices = snapshot.Devices
	s.available = snapshot.Available
	s.reason = snapshot.Reason
	s.mu.Unlock()
	return nil
}

func (s *Service) nativeAction(action, id string, params map[string]any) (string, error) {
	s.nativeMu.Lock()
	defer s.nativeMu.Unlock()
	if err := s.reloadNative(); err != nil {
		return "", err
	}
	device, found := s.find(id)
	if !found {
		return "", fail(files.CodeNotFound, "unknown device: %s", id)
	}
	switch action {
	case "unmount", "lock":
		if err := s.checkDetach(device, false); err != nil {
			return "", err
		}
	case "eject", "powerOff":
		if err := s.checkDetach(device, true); err != nil {
			return "", err
		}
	}
	if params == nil {
		params = map[string]any{}
	}
	params["id"] = id
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var result struct {
		MountPoint string `json:"mountPoint"`
		Cleartext  string `json:"cleartext"`
	}
	if err := s.native(ctx, "volumes."+action, params, &result); err != nil {
		return "", nativeError(err)
	}
	for _, point := range device.MountPoints {
		s.usage.forget(point)
	}
	s.usage.forget(result.MountPoint)
	_ = s.reloadNative()
	s.publish()
	if action == "unlock" {
		return result.Cleartext, nil
	}
	return result.MountPoint, nil
}

func nativeError(err error) error {
	var coded *files.Error
	if errors.As(err, &coded) {
		return err
	}
	code, message, ok := strings.Cut(err.Error(), ": ")
	if ok {
		switch files.Code(code) {
		case CodePerm, files.CodeAccess, files.CodeBusy, files.CodeInvalid, files.CodeNotFound, files.CodeNotSupported, files.CodeExists, files.CodeIO, "ECANCELED":
			return fail(files.Code(code), "%s", message)
		}
	}
	return &files.Error{Code: files.CodeIO, Err: err}
}
