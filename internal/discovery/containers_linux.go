//go:build linux

package discovery

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// Keep real NICs and the default-route veth used by containerized Agents. Other
// veths are container attachment endpoints, not reachable host underlay NICs.
func containerExcluded(kind string, master int, defaultRoute bool) bool {
	return kind == "veth" && (master != 0 || !defaultRoute)
}

func excludeContainerInterfaces(interfaces []net.Interface, eligible map[int]bool) error {
	links, err := netlink.LinkList()
	if err != nil {
		return err
	}
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_ALL, &netlink.Route{Table: unix.RT_TABLE_UNSPEC}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return err
	}
	uplinks := map[int]bool{}
	for _, route := range routes {
		if route.Type != unix.RTN_UNICAST {
			continue
		}
		if route.Dst != nil {
			bits, _ := route.Dst.Mask.Size()
			if bits != 0 {
				continue
			}
		}
		uplinks[route.LinkIndex] = true
		for _, hop := range route.MultiPath {
			uplinks[hop.LinkIndex] = true
		}
	}
	names := map[int]string{}
	for _, iface := range interfaces {
		names[iface.Index] = iface.Name
	}
	seen := map[int]bool{}
	for _, link := range links {
		index := link.Attrs().Index
		if !eligible[index] {
			continue
		}
		if names[index] != link.Attrs().Name {
			return fmt.Errorf("interface %d changed during container filtering", index)
		}
		seen[index] = true
		if containerExcluded(link.Type(), link.Attrs().MasterIndex, uplinks[index]) {
			eligible[index] = false
		}
	}
	for index, allowed := range eligible {
		if allowed && !seen[index] {
			return fmt.Errorf("interface %d disappeared during container filtering", index)
		}
	}
	return nil
}
