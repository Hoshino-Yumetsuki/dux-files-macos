package netmount

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/duxlabs-dev/dux-files/core/internal/location"
	"github.com/duxlabs-dev/dux-go/files"
)

func (s *Service) Refresh() {
	if s.native == nil {
		return
	}
	_ = s.refreshNative()
	s.publish(s.Snapshot())
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
	if err := s.native(ctx, "network.list", nil, &snapshot); err != nil {
		s.setReason("native network mount service is unavailable")
		return nativeError(err)
	}
	s.mu.Lock()
	s.mounts = snapshot.Mounts
	s.schemes = snapshot.Schemes
	s.available = snapshot.Available
	s.reason = snapshot.Reason
	s.mu.Unlock()
	return nil
}

func (s *Service) connectNative(uri, password string) (Connected, error) {
	remote := location.SplitPassword(strings.TrimSpace(uri))
	if password == "" {
		password = remote.Password
	}
	parsed, err := url.Parse(remote.URI)
	if err != nil || parsed.Hostname() == "" || parsed.Fragment != "" || parsed.RawQuery != "" || parsed.ForceQuery {
		return Connected{}, fail(files.CodeInvalid, "network address needs a scheme, host and path")
	}
	for _, part := range strings.Split(parsed.Path, "/") {
		if part == "." || part == ".." || strings.ContainsRune(part, 0) {
			return Connected{}, fail(files.CodeInvalid, "invalid network path")
		}
	}
	scheme := strings.ToLower(parsed.Scheme)
	if !slices.Contains(s.Snapshot().Schemes, scheme) {
		return Connected{}, fail(files.CodeNotSupported, "no native backend handles %s://", scheme)
	}
	parsed.Scheme = scheme
	remote.URI = parsed.String()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var connected Connected
	s.nativeMu.Lock()
	err = s.native(ctx, "network.connect", map[string]any{"uri": remote.URI, "password": password}, &connected)
	if err == nil {
		_ = s.reloadNative()
	}
	s.nativeMu.Unlock()
	if err != nil {
		return Connected{}, nativeError(err)
	}
	if connected.Mount != nil {
		mount := connected.Mount
		s.remember(Server{URI: mount.URI, Name: mount.Name, Scheme: mount.Scheme})
	}
	if connected.Server != "" {
		s.remember(Server{URI: connected.Server, Name: parsed.Hostname(), Scheme: scheme})
	}
	s.publish(s.Snapshot())
	return connected, nil
}

func (s *Service) disconnectNative(id string) error {
	mount, found := s.find(id)
	if !found {
		return fail(files.CodeNotFound, "unknown mount: %s", id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s.nativeMu.Lock()
	err := s.native(ctx, "network.disconnect", map[string]any{"id": mount.ID}, nil)
	if err == nil {
		_ = s.reloadNative()
	}
	s.nativeMu.Unlock()
	if err != nil {
		return nativeError(err)
	}
	s.publish(s.Snapshot())
	return nil
}

func nativeError(err error) error {
	var coded *files.Error
	if errors.As(err, &coded) {
		return err
	}
	code, message, ok := strings.Cut(err.Error(), ": ")
	if ok {
		switch files.Code(code) {
		case "EPERM", files.CodeAccess, files.CodeBusy, files.CodeInvalid, files.CodeNotFound, files.CodeNotSupported, files.CodeExists, files.CodeIO, "ECANCELED":
			return fail(files.Code(code), "%s", message)
		}
	}
	return &files.Error{Code: files.CodeIO, Err: err}
}
