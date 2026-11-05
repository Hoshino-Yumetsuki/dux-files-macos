package main

import (
	"context"
	"encoding/json"

	"github.com/duxlabs-dev/dux-files/core/internal/colorscheme"

	"github.com/duxlabs-dev/dux-files/core/internal/devices"
	"github.com/duxlabs-dev/dux-files/core/internal/netmount"
	"github.com/duxlabs-dev/dux-go/log"
	"github.com/duxlabs-dev/dux-go/nativebridge"
)

func watchNativeServices(ctx context.Context, devices *devices.Service, network *netmount.Service, colors *colorscheme.Watcher) context.CancelFunc {
	ctx, cancel := context.WithCancel(ctx)
	if !nativebridge.Available() {
		return cancel
	}
	events, err := nativebridge.Subscribe(ctx)
	if err != nil {
		log.Warnf("native system notifications unavailable: %v", err)
		return cancel
	}
	go func() {
		for event := range events {
			switch event.Topic {
			case "volumes":
				devices.Refresh()
				network.Refresh()
			case "colorScheme":
				var state colorscheme.State
				if colors != nil && json.Unmarshal(event.Data, &state) == nil {
					colors.Update(state)
				}
			}
		}
		if ctx.Err() == nil {
			log.Warn("native system notification stream closed")
		}
	}()
	return cancel
}
