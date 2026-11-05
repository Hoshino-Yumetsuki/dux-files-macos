//go:build !darwin

package devices

func (s *Service) startNative() error { return nil }
