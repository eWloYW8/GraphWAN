package agent

import (
	"github.com/eWloYW8/GraphWAN/internal/gateway"
)

func gatewayEntries(state *runtimeState) []gateway.Entry {
	if state == nil {
		return nil
	}
	var entries []gateway.Entry
	for _, n := range state.snapshot.Networks {
		device := state.devices[n.ID]
		if device == nil || device.failure.Load() != nil {
			continue
		}
		for _, s := range n.Self.AdvertisedSubnets {
			entries = append(entries, gateway.Entry{Interface: device.Name(), Overlay: n.CIDR, Subnet: s.Prefix, Mode: s.GatewayMode})
		}
	}
	return entries
}
