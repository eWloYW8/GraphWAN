//go:build freebsd && integration

package tunnel_test

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"testing"

	"github.com/graphwan/graphwan/internal/tunnel"
	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

func TestNativeFreeBSDTunnel(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("GRAPHWAN_TEST_VM") != "1" {
		t.Skip("run as root in a disposable FreeBSD VM with GRAPHWAN_TEST_VM=1")
	}
	for _, addresses := range [][2]string{{"10.240.31.1/24", "10.240.31.2"}, {"fd42:6777::1/64", "fd42:6777::2"}} {
		for _, mtu := range []int{1280, 9000} {
			t.Run(fmt.Sprintf("%s/MTU%d", addresses[0], mtu), func(t *testing.T) {
				checkNativeTunnel(t, tunnel.Config{Name: "gw-integration", Address: netip.MustParsePrefix(addresses[0]), MTU: mtu}, netip.MustParseAddr(addresses[1]))
			})
		}
	}
}

func TestFreeBSDReplacement(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("GRAPHWAN_TEST_VM") != "1" {
		t.Skip("requires disposable root VM")
	}
	for _, address := range []string{"10.240.32.1/24", "fd42:6778::1/64"} {
		t.Run(address, func(t *testing.T) {
			cfg := tunnel.Config{Address: netip.MustParsePrefix(address), MTU: 1280}
			old, err := tunnel.Open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer old.Close()
			setter := old.(tunnel.MTUSetter)
			if err := setter.SetMTU(9000); err != nil {
				t.Fatal(err)
			}
			iface, err := net.InterfaceByName(old.Name())
			if err != nil || iface.MTU != 9000 || old.Configuration().MTU != 9000 {
				t.Fatalf("MTU update: %v %v", iface, err)
			}
			if err := setter.SetMTU(9001); err == nil {
				t.Fatal("invalid MTU accepted")
			}
			if old.Configuration().MTU != 9000 {
				t.Fatal("invalid update changed MTU")
			}
			cfg.MTU = 9000
			cfg.Address = netip.PrefixFrom(cfg.Address.Addr().Next(), cfg.Address.Bits())
			next, err := tunnel.Open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer next.Close()
			if old.Name() == next.Name() {
				t.Fatal("replacement reused old device")
			}
			if err := old.Close(); err != nil {
				t.Fatal(err)
			}
			iface, err = net.InterfaceByName(next.Name())
			if err != nil {
				t.Fatal(err)
			}
			checkNativeRoute(t, iface, cfg)
		})
	}
}

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

func TestFreeBSDAddressReconfigure(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("GRAPHWAN_TEST_VM") != "1" {
		t.Skip("requires disposable root VM")
	}
	for _, pair := range [][2]string{
		{"10.240.35.1/24", "10.240.35.1/25"},
		{"10.240.35.1/25", "10.240.35.1/24"},
		{"fd42:6781::1/64", "fd42:6781::1/80"},
		{"fd42:6781::1/80", "fd42:6781::1/64"},
		{"fd42:6781::1/64", "fd42:6782::1/80"},
		{"10.240.35.1/24", "fd42:6781::1/64"},
		{"fd42:6781::1/64", "10.240.35.1/24"},
	} {
		t.Run(pair[0]+"→"+pair[1], func(t *testing.T) {
			before := tunnel.Config{Address: netip.MustParsePrefix(pair[0]), MTU: 1280}
			device, err := tunnel.Open(before)
			if err != nil {
				t.Fatal(err)
			}
			defer device.Close()
			original, err := net.InterfaceByName(device.Name())
			if err != nil {
				t.Fatal(err)
			}
			after := tunnel.Config{Address: netip.MustParsePrefix(pair[1]), MTU: 9000}
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
		})
	}
}
