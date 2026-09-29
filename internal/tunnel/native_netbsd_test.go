//go:build netbsd && integration

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

func requireTestNetBSD(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 || os.Getenv("GRAPHWAN_TEST_VM") != "1" {
		t.Skip("requires root in a disposable NetBSD VM with GRAPHWAN_TEST_VM=1")
	}
}
func TestNativeNetBSDTunnel(t *testing.T) {
	requireTestNetBSD(t)
	for _, address := range []string{"10.240.51.1/24", "fd42:6751::1/64"} {
		for _, mtu := range []int{1280, 1500} {
			t.Run(fmt.Sprintf("%s/MTU%d", address, mtu), func(t *testing.T) {
				prefix := netip.MustParsePrefix(address)
				checkNativeTunnel(t, tunnel.Config{Address: prefix, MTU: mtu}, prefix.Addr().Next())
			})
		}
	}
}
func TestNativeNetBSDConfiguration(t *testing.T) {
	requireTestNetBSD(t)
	for _, pair := range [][2]string{
		{"10.240.52.1/24", "10.240.52.1/25"}, {"10.240.52.1/25", "10.240.52.1/24"},
		{"fd42:6752::1/64", "fd42:6752::1/80"}, {"fd42:6752::1/80", "fd42:6752::1/64"},
		{"fd42:6752::1/64", "fd42:6753::1/80"}, {"10.240.52.1/24", "fd42:6752::1/64"}, {"fd42:6752::1/64", "10.240.52.1/24"},
	} {
		t.Run(pair[0]+"→"+pair[1], func(t *testing.T) { checkNativeMigrationMTU(t, pair[0], pair[1], 1500) })
	}
}
func TestNativeNetBSDMTULimit(t *testing.T) {
	requireTestNetBSD(t)
	config := tunnel.Config{Address: netip.MustParsePrefix("10.240.53.1/24"), MTU: 1500}
	device, err := tunnel.Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer device.Close()
	iface, err := net.InterfaceByName(device.Name())
	if err != nil {
		t.Fatal(err)
	}
	if err := device.(tunnel.MTUSetter).SetMTU(9000); err == nil {
		t.Fatal("kernel MTU limit ignored")
	}
	if device.Configuration().MTU != 1500 {
		t.Fatal("failed MTU update changed configuration")
	}
	current, err := net.InterfaceByName(device.Name())
	if err != nil || current.Index != iface.Index || current.MTU != 1500 {
		t.Fatal("failed MTU update changed kernel interface:", err)
	}
	checkConfiguredNativeTunnel(t, device, config, config.Address.Addr().Next())
}
func TestNativeNetBSDRouteConflict(t *testing.T) {
	requireTestNetBSD(t)
	for _, pair := range [][2]string{{"10.240.54.1/24", "10.240.55.1/24"}, {"fd42:6754::1/64", "fd42:6755::1/64"}} {
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
			after := tunnel.Config{Address: netip.PrefixFrom(foreignConfig.Address.Addr().Next(), foreignConfig.Address.Bits()), MTU: 1500}
			if err := device.(tunnel.Reconfigurable).Reconfigure(after); err == nil {
				t.Fatal("foreign subnet route replaced")
			}
			if got := device.Configuration(); got.Address != before.Address || got.MTU != before.MTU {
				t.Fatal("failed configuration not rolled back")
			}
			iface, err := net.InterfaceByName(foreign.Name())
			if err != nil {
				t.Fatal(err)
			}
			checkNativeRoute(t, iface, foreignConfig)
			if other, err := tunnel.Open(after); err == nil {
				other.Close()
				t.Fatal("Open replaced a foreign route")
			}
			checkConfiguredNativeTunnel(t, device, before, before.Address.Addr().Next())
			checkNativeRoute(t, iface, foreignConfig)
		})
	}
}

func TestNativeNetBSDOwnership(t *testing.T) {
	requireTestNetBSD(t)
	config := tunnel.Config{Address: netip.MustParsePrefix("10.240.56.1/24"), MTU: 1280}
	device, err := tunnel.Open(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { device.Close() })
	name := device.Name()
	if output, err := exec.Command("/sbin/ifconfig", name, "destroy").CombinedOutput(); err != nil {
		t.Fatalf("destroy: %v: %s", err, output)
	}
	if err := device.(tunnel.MTUSetter).SetMTU(1500); !errors.Is(err, tunnel.ErrUnavailable) {
		t.Fatalf("revoked descriptor update: %v", err)
	}
	if err := device.Close(); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("/sbin/ifconfig", name, "create").CombinedOutput(); err != nil {
		t.Fatalf("create: %v: %s", err, output)
	}
	t.Cleanup(func() { exec.Command("/sbin/ifconfig", name, "destroy").Run() })
	before, err := net.InterfaceByName(name)
	if err != nil {
		t.Fatal(err)
	}
	config.Name = name
	if other, err := tunnel.Open(config); err == nil {
		other.Close()
		t.Fatal("adopted a pre-existing idle TUN")
	}
	after, err := net.InterfaceByName(name)
	if err != nil || after.Index != before.Index || after.MTU != before.MTU {
		t.Fatal("failed Open modified foreign TUN:", err)
	}
}
