//go:build darwin

package devices

import "github.com/duxlabs-dev/dux-go/nativebridge"

func (s *Service) startNative() error {
	if !nativebridge.Available() {
		s.setReason("native volume helper is not installed")
		return nil
	}
	s.native = nativebridge.Call
	return s.refreshNative()
}
