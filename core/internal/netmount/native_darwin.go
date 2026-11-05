//go:build darwin

package netmount

import "github.com/duxlabs-dev/dux-go/nativebridge"

func (s *Service) startNative() error {
	if !nativebridge.Available() {
		s.setReason("native network mount helper is not installed")
		return nil
	}
	s.native = nativebridge.Call
	return s.refreshNative()
}
