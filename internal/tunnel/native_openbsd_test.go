//go:build openbsd && integration

package tunnel_test

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"testing"

	"github.com/eWloYW8/GraphWAN/internal/tunnel"
)

func TestNativeOpenBSDOwnership(t *testing.T) {
	requireTestOpenBSD(t)
	config := tunnel.Config{Address: netip.MustParsePrefix("10.240.41.1/24"), MTU: 1280}
	device, err := tunnel.Open(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = device.Close() })
	name := device.Name()
	command := func(args ...string) {
		t.Helper()
		if output, err := exec.Command("/sbin/ifconfig", append([]string{name}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("ifconfig %v: %v: %s", args, err, output)
		}
	}
	command("destroy")
	command("create")
	t.Cleanup(func() { command("destroy") })
	command("mtu", "1400")
	replacement, err := net.InterfaceByName(name)
	if err != nil {
		t.Fatal(err)
	}
	// A kernel-created idle interface must never be adopted by a new Open.
	config.Name = name
	if other, err := tunnel.Open(config); err == nil {
		other.Close()
		t.Fatal("Open adopted a pre-existing idle TUN")
	}
	if err := device.(tunnel.MTUSetter).SetMTU(9000); !errors.Is(err, tunnel.ErrUnavailable) {
		t.Fatalf("replacement update: %v", err)
	}
	_ = device.Close() // The externally revoked descriptor may report an error.
	current, err := net.InterfaceByName(name)
	if err != nil || current.Index != replacement.Index || current.MTU != 1400 {
		t.Fatalf("retired device changed or removed its replacement: %v, %v", current, err)
	}
}

func requireTestOpenBSD(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 || os.Getenv("GRAPHWAN_TEST_VM") != "1" {
		t.Skip("requires root on a disposable OpenBSD VM with GRAPHWAN_TEST_VM=1")
	}
}

func TestNativeOpenBSDTunnel(t *testing.T) {
	requireTestOpenBSD(t)
	for _, address := range []string{"10.240.31.1/24", "fd42:6777::1/64"} {
		for _, mtu := range []int{1280, 9000} {
			t.Run(fmt.Sprintf("%s/MTU%d", address, mtu), func(t *testing.T) {
				prefix := netip.MustParsePrefix(address)
				checkNativeTunnel(t, tunnel.Config{Address: prefix, MTU: mtu}, prefix.Addr().Next())
			})
		}
	}
}

func TestNativeOpenBSDConfiguration(t *testing.T) {
	requireTestOpenBSD(t)
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

func TestNativeOpenBSDRouteConflict(t *testing.T) {
	requireTestOpenBSD(t)
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
