package mesh

import (
	"fmt"
	"net"
	"net/netip"
	"slices"
)

func (m *Mesh) overlaySource(address net.Addr) bool {
	if address == nil {
		return false
	}
	local, err := netip.ParseAddrPort(address.String())
	addresses, _ := m.overlayAddresses.Load().([]netip.Addr)
	return err == nil && slices.Contains(addresses, local.Addr().Unmap())
}

// checkUnderlayRoute rejects candidates whose default source is a local TUN IP.
// A connected UDP socket asks the OS to select a source address without sending
// any packets. This also covers transports whose shared socket reports an
// unspecified local address. Private endpoints remain usable on real LANs.
// Check all local networks: recursion can cross GraphWAN network boundaries.
// This is an admission check, not interface binding or a route-change watcher.
func (m *Mesh) checkUnderlayRoute(address string) error {
	addresses, _ := m.overlayAddresses.Load().([]netip.Addr)
	if len(addresses) == 0 {
		return nil
	}
	remote, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("underlay requires a resolved address: %w", err)
	}
	network := "udp6"
	if remote.Addr().Is4() {
		network = "udp4"
	}
	conn, err := net.DialUDP(network, nil, net.UDPAddrFromAddrPort(remote))
	if err != nil {
		return fmt.Errorf("underlay route lookup: %w", err)
	}
	defer conn.Close()
	if m.overlaySource(conn.LocalAddr()) {
		return fmt.Errorf("underlay route to %s uses a GraphWAN address", address)
	}
	return nil
}
