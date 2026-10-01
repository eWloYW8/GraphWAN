//go:build linux

package discovery

import (
	"net"

	"github.com/vishvananda/netlink"
)

func physicalInterfaces(_ []net.Interface) (map[int]bool, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, err
	}
	result := make(map[int]bool, len(links))
	for _, link := range links {
		// A veth is the underlay NIC exposed inside a container/network namespace.
		// Query kernel types so aliases do not determine whether TUN/TAP is excluded.
		switch link.Type() {
		case "device", "veth", "bridge", "vlan", "bond":
			result[link.Attrs().Index] = true
		}
	}
	return result, nil
}
