//go:build (freebsd || darwin) && integration

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

func checkNativeMigration(t *testing.T, beforeAddress, afterAddress string) {
	t.Helper()
	before := tunnel.Config{Address: netip.MustParsePrefix(beforeAddress), MTU: 1280}
	device, err := tunnel.Open(before)
	if err != nil {
		t.Fatal(err)
	}
	defer device.Close()
	original, err := net.InterfaceByName(device.Name())
	if err != nil {
		t.Fatal(err)
	}
	after := tunnel.Config{Address: netip.MustParsePrefix(afterAddress), MTU: 9000}
	if err := device.(tunnel.Reconfigurable).Reconfigure(after); err != nil {
		t.Fatal(err)
	}
	current, err := net.InterfaceByName(device.Name())
	if err != nil || current.Index != original.Index {
		t.Fatalf("reconfiguration replaced interface: %v", err)
	}
	if hasNativeRoute(t, current, before) {
		t.Fatal("old subnet route survived update")
	}
	addresses, err := current.Addrs()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, address := range addresses {
		if address.String() == before.Address.String() {
			t.Fatal("old address or prefix survived update")
		}
		found = found || address.String() == after.Address.String()
	}
	if !found {
		t.Fatal("new address missing")
	}
	reported := device.Configuration()
	if reported.Address != after.Address || reported.MTU != after.MTU || reported.Name != device.Name() {
		t.Fatal("device reports stale configuration")
	}
	checkConfiguredNativeTunnel(t, device, after, after.Address.Addr().Next())
}
