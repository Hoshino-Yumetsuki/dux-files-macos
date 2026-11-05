//go:build !darwin

package netmount

func (s *Service) startNative() error { return nil }
