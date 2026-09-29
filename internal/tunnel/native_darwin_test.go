//go:build darwin && integration

package tunnel_test

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"testing"

	"github.com/graphwan/graphwan/internal/tunnel"
)

func requireTestMac(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 || os.Getenv("GRAPHWAN_TEST_MACOS") != "1" {
		t.Skip("requires root on a disposable Mac with GRAPHWAN_TEST_MACOS=1")
	}
}

func TestNativeDarwinTunnel(t *testing.T) {
	requireTestMac(t)
	for _, address := range []string{"10.240.31.1/24", "fd42:6777::1/64"} {
		for _, mtu := range []int{1280, 9000} {
			t.Run(fmt.Sprintf("%s/MTU%d", address, mtu), func(t *testing.T) {
				prefix := netip.MustParsePrefix(address)
				checkNativeTunnel(t, tunnel.Config{Address: prefix, MTU: mtu}, prefix.Addr().Next())
			})
		}
	}
}

func TestNativeDarwinConfiguration(t *testing.T) {
	requireTestMac(t)
	for _, pair := range [][2]string{
		{"10.240.35.1/24", "10.240.35.1/25"},
		{"10.240.35.1/25", "10.240.35.1/24"},
		{"fd42:6781::1/64", "fd42:6781::1/80"},
		{"fd42:6781::1/80", "fd42:6781::1/64"},
		{"fd42:6781::1/64", "fd42:6782::1/80"},
		{"10.240.35.1/24", "fd42:6781::1/64"},
		{"fd42:6781::1/64", "10.240.35.1/24"},
	} {
		t.Run(pair[0]+"→"+pair[1], func(t *testing.T) { checkNativeMigration(t, pair[0], pair[1]) })
	}
}

func TestNativeDarwinRouteConflict(t *testing.T) {
	requireTestMac(t)
	for _, pair := range [][2]string{
		{"10.240.38.1/24", "10.240.39.1/24"},
		{"fd42:6788::1/64", "fd42:6789::1/64"},
	} {
		t.Run(pair[0], func(t *testing.T) {
			before := tunnel.Config{Address: netip.MustParsePrefix(pair[0]), MTU: 1280}
			foreignConfig := tunnel.Config{Address: netip.MustParsePrefix(pair[1]), MTU: 1280}
			foreign, err := tunnel.Open(foreignConfig)
			if err != nil {
				t.Fatal(err)
			}
			defer foreign.Close()
			device, err := tunnel.Open(before)
			if err != nil {
				t.Fatal(err)
			}
			defer device.Close()
			after := tunnel.Config{Address: netip.PrefixFrom(foreignConfig.Address.Addr().Next(), foreignConfig.Address.Bits()), MTU: 9000}
			if err := device.(tunnel.Reconfigurable).Reconfigure(after); err == nil {
				t.Fatal("foreign subnet route was replaced")
			}
			if got := device.Configuration(); got.Address != before.Address || got.MTU != before.MTU {
				t.Fatal("route conflict did not restore old configuration")
			}
			iface, err := net.InterfaceByName(foreign.Name())
			if err != nil {
				t.Fatal(err)
			}
			checkNativeRoute(t, iface, foreignConfig)
			// Conflicts during initial creation must also preserve the first route.
			if other, err := tunnel.Open(after); err == nil {
				other.Close()
				t.Fatal("Open accepted foreign route")
			}
			checkNativeRoute(t, iface, foreignConfig)
			checkConfiguredNativeTunnel(t, device, before, before.Address.Addr().Next())
			checkNativeRoute(t, iface, foreignConfig)
		})
	}
}
