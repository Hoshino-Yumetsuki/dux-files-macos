//go:build !darwin

package main

import (
	"context"

	"github.com/duxlabs-dev/dux-files/core/internal/colorscheme"
	"github.com/duxlabs-dev/dux-files/core/internal/devices"
	"github.com/duxlabs-dev/dux-files/core/internal/netmount"
)

func watchNativeServices(context.Context, *devices.Service, *netmount.Service, *colorscheme.Watcher) context.CancelFunc {
	return nil
}
