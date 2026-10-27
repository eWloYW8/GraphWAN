package agent

import (
	"slices"

	"github.com/eWloYW8/GraphWAN/internal/tunnel"
)

func autoRouteEntries(state *runtimeState) []tunnel.AutoRoute {
	if state == nil {
		return nil
	}
	var entries []tunnel.AutoRoute
	for _, n := range state.snapshot.Networks {
		device := state.devices[n.ID]
		if device == nil || device.failure.Load() != nil {
			continue
		}
		for _, destination := range n.Directory {
			if destination.NodeID == n.Self.ID {
				continue
			}
			for _, subnet := range destination.AdvertisedSubnets {
				if slices.Contains(n.Self.AutoRoutes, subnet.Prefix) {
					entries = append(entries, tunnel.AutoRoute{Device: device.Device, Prefix: subnet.Prefix})
				}
			}
		}
	}
	return entries
}
