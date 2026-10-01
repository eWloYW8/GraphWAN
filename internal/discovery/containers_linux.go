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
	// A host bridge may carry its addresses above physical NICs, VLANs or
	// bonds (e.g. Proxmox vmbr). Keep those bridges while excluding isolated
	// container bridges. Use kernel relationships, never interface-name guesses.
	byIndex := map[int]netlink.Link{}
	members := map[int][]int{}
	for _, link := range links {
		a := link.Attrs()
		byIndex[a.Index] = link
		if a.MasterIndex != 0 {
			members[a.MasterIndex] = append(members[a.MasterIndex], a.Index)
		}
	}
	var physicalBacking func(int, map[int]bool) bool
	physicalBacking = func(index int, visited map[int]bool) bool {
		link := byIndex[index]
		if link == nil || visited[index] {
			return false
		}
		visited[index] = true
		switch link.Type() {
		case "device":
			return true
		case "vlan":
			return physicalBacking(link.Attrs().ParentIndex, visited)
		case "bridge", "bond":
			for _, child := range members[index] {
				if physicalBacking(child, visited) {
					return true
				}
			}
		}
		return false
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
		if containerExcluded(link.Type(), link.Attrs().MasterIndex, uplinks[index]) || link.Type() == "bridge" && !uplinks[index] && !physicalBacking(index, map[int]bool{}) {
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
