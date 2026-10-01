package model

import (
	"fmt"
	"net/netip"
)

const MaxAdvertisedSubnets = 64
const MaxNetworkSubnets = 1024

func validateAdvertised(overlay netip.Prefix, entries []AdvertisedSubnet, seen map[netip.Prefix]bool) error {
	if len(entries) > MaxAdvertisedSubnets {
		return fmt.Errorf("at most %d advertised subnets per node", MaxAdvertisedSubnets)
	}
	for _, entry := range entries {
		p := entry.Prefix
		if !p.IsValid() || p != p.Masked() || p.Addr().Is4In6() || p.Addr().Zone() != "" ||
			p.Addr().BitLen() != overlay.Addr().BitLen() || p.Addr().IsMulticast() || p.Addr().IsLoopback() || p.Addr().IsLinkLocalUnicast() {
			return fmt.Errorf("invalid advertised subnet %s (use a canonical prefix of the overlay address family)", p)
		}
		// Broad routes including /0 are permitted. Overlay addresses always take
		// precedence; a subnet wholly inside the overlay is not an external subnet.
		if p.Bits() >= overlay.Bits() && overlay.Contains(p.Addr()) {
			return fmt.Errorf("advertised subnet %s is inside the overlay", p)
		}
		switch entry.GatewayMode {
		case GatewayOff, GatewayRoute, GatewaySNAT:
		default:
			return fmt.Errorf("invalid automatic gateway mode %q", entry.GatewayMode)
		}
		if seen[p] {
			return fmt.Errorf("advertised subnet %s is declared more than once in the network", p)
		}
		seen[p] = true
		if len(seen) > MaxNetworkSubnets {
			return fmt.Errorf("too many advertised subnets in network")
		}
	}
	return nil
}
