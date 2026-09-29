//go:build linux && integration

package tunnel_test

import (
	"net"
	"net/netip"
	"os"
	"testing"

	"github.com/eWloYW8/GraphWAN/internal/tunnel"
	"github.com/vishvananda/netlink"
)

func TestNativeLinuxTunnel(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("GRAPHWAN_TEST_NETNS") != "1" {
		t.Skip("run as root in an isolated network namespace with GRAPHWAN_TEST_NETNS=1")
	}
	selfNS, _ := os.Readlink("/proc/self/ns/net")
	initNS, _ := os.Readlink("/proc/1/ns/net")
	if selfNS == initNS {
		t.Fatal("integration test requires an isolated network namespace")
	}
	for _, addresses := range [][2]string{{"10.240.31.1/24", "10.240.31.2"}, {"fd42:6777::1/64", "fd42:6777::2"}} {
		t.Run(addresses[0], func(t *testing.T) {
			checkNativeTunnel(t, tunnel.Config{Name: "gw-integration", Address: netip.MustParsePrefix(addresses[0]), MTU: 1280}, netip.MustParseAddr(addresses[1]))
		})
	}
}

func checkNativeRoute(t *testing.T, iface *net.Interface, config tunnel.Config) {
	t.Helper()
	family := netlink.FAMILY_V4
	if config.Address.Addr().Is6() {
		family = netlink.FAMILY_V6
	}
	routes, err := netlink.RouteList(nil, family)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, route := range routes {
		if route.LinkIndex == iface.Index && route.Dst != nil && route.Dst.String() == config.Address.Masked().String() {
			found = true
		}
	}
	if !found {
		t.Fatal("connected route missing")
	}
}
