//go:build linux

package tunnel

import (
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func (d *native) routes() subnetRoutes {
	iface, err := net.InterfaceByName(d.Name())
	if err != nil {
		return subnetRoutes{list: func(netip.Prefix) ([]routeEntry, error) { return nil, err }}
	}
	index := iface.Index
	row := func(p netip.Prefix) *netlink.Route {
		return &netlink.Route{LinkIndex: index, Dst: &net.IPNet{IP: net.IP(p.Addr().AsSlice()), Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen())},
			Table: unix.RT_TABLE_MAIN, Scope: netlink.SCOPE_LINK, Protocol: unix.RTPROT_STATIC}
	}
	return subnetRoutes{index: index,
		list: func(p netip.Prefix) ([]routeEntry, error) {
			family := netlink.FAMILY_V6
			if p.Addr().Is4() {
				family = netlink.FAMILY_V4
			}
			rows, err := netlink.RouteListFiltered(family, row(p), netlink.RT_FILTER_TABLE|netlink.RT_FILTER_DST)
			if err != nil {
				return nil, err
			}
			var entries []routeEntry
			for _, r := range rows {
				entries = append(entries, routeEntry{prefix: p, index: r.LinkIndex, usable: r.Type == unix.RTN_UNICAST})
			}
			return entries, nil
		},
		add:    func(p netip.Prefix) error { return netlink.RouteAdd(row(p)) },
		remove: func(p netip.Prefix) error { return netlink.RouteDel(row(p)) },
	}
}
