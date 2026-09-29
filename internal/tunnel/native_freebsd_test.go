//go:build freebsd && integration

package tunnel_test

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"testing"

	"github.com/eWloYW8/GraphWAN/internal/tunnel"
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
			checkNativeMigration(t, pair[0], pair[1])
		})
	}
}
