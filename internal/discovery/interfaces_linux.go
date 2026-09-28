//go:build linux

package discovery

import (
	"github.com/vishvananda/netlink"
	"net"
)

func physical(iface net.Interface) bool {
	link, err := netlink.LinkByIndex(iface.Index)
	if err != nil {
		return false
	}
	// veth is the underlay NIC exposed inside containers/network namespaces.
	// TUN/TAP, bridge, WireGuard and other overlay devices are excluded by type,
	// not by a name prefix that could accidentally match a physical interface.
	return link.Type() == "device" || link.Type() == "veth"
}
