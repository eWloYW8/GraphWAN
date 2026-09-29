//go:build darwin || openbsd || netbsd || dragonfly

package tunnel

import (
	"net"
	"net/netip"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

func bsdInterfaceAddresses(name string) ([]netip.Prefix, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	addresses, err := iface.Addrs()
	if err != nil {
		return nil, err
	}
	var result []netip.Prefix
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address.String())
		if err != nil {
			return nil, err
		}
		result = append(result, prefix)
	}
	return result, nil
}

func bsdRoutes(prefix netip.Prefix, scopeFlag int) ([]routeEntry, error) {
	family := unix.AF_INET
	if prefix.Addr().Is6() {
		family = unix.AF_INET6
	}
	rib, err := route.FetchRIB(family, route.RIBTypeRoute, 0)
	if err != nil {
		return nil, err
	}
	messages, err := route.ParseRIB(route.RIBTypeRoute, rib)
	if err != nil {
		return nil, err
	}
	var entries []routeEntry
	for _, message := range messages {
		r, ok := message.(*route.RouteMessage)
		if !ok || len(r.Addrs) <= unix.RTAX_NETMASK {
			continue
		}
		var dst netip.Addr
		var mask net.IPMask
		switch a := r.Addrs[unix.RTAX_DST].(type) {
		case *route.Inet4Addr:
			dst = netip.AddrFrom4(a.IP)
		case *route.Inet6Addr:
			dst = netip.AddrFrom16(a.IP)
		}
		switch a := r.Addrs[unix.RTAX_NETMASK].(type) {
		case *route.Inet4Addr:
			mask = net.IPMask(a.IP[:])
		case *route.Inet6Addr:
			mask = net.IPMask(a.IP[:])
		}
		if !dst.IsValid() {
			continue
		}
		ones, bits := mask.Size()
		if r.Flags&unix.RTF_HOST != 0 {
			ones, bits = dst.BitLen(), dst.BitLen()
		}
		if bits != dst.BitLen() {
			continue
		}
		entries = append(entries, routeEntry{prefix: netip.PrefixFrom(dst, ones).Masked(), index: r.Index,
			scoped: r.Flags&scopeFlag != 0, usable: r.Flags&unix.RTF_UP != 0 && r.Flags&(unix.RTF_REJECT|unix.RTF_BLACKHOLE) == 0})
	}
	return entries, nil
}
