//go:build (freebsd || darwin || openbsd || netbsd) && integration

package tunnel_test

import (
	"net"
	"net/netip"
	"testing"

	"github.com/graphwan/graphwan/internal/tunnel"
	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

func checkNativeRoute(t *testing.T, iface *net.Interface, config tunnel.Config) {
	t.Helper()
	if !hasNativeRoute(t, iface, config) {
		t.Fatalf("connected route missing for %s on %s", config.Address.Masked(), iface.Name)
	}
}

func hasNativeRoute(t *testing.T, iface *net.Interface, config tunnel.Config) bool {
	t.Helper()
	family := unix.AF_INET
	if config.Address.Addr().Is6() {
		family = unix.AF_INET6
	}
	rib, err := route.FetchRIB(family, route.RIBTypeRoute, 0)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := route.ParseRIB(route.RIBTypeRoute, rib)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		r, ok := message.(*route.RouteMessage)
		if !ok || r.Index != iface.Index || len(r.Addrs) <= unix.RTAX_NETMASK {
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
		ones, _ := mask.Size()
		if dst.IsValid() && netip.PrefixFrom(dst, ones) == config.Address.Masked() {
			return true
		}
	}
	return false
}
