package trash

import (
	"context"
	"errors"
	"testing"

	"github.com/duxlabs-dev/dux-go/nativebridge"
)

func init() {
	// Existing tests deliberately exercise an isolated freedesktop trash.
	nativeEnabled = func() bool { return !testing.Testing() }
	nativeCall = func(ctx context.Context, method string, params, result any) error {
		if !nativebridge.Available() {
			return errors.New("system Trash requires the Dux native helper")
		}
		return nativebridge.Call(ctx, method, params, result)
	}
}
