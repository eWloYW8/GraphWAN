//go:build windows && integration

package tunnel_test

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/eWloYW8/GraphWAN/internal/tunnel"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

func requireTestWindows(t *testing.T) {
	t.Helper()
	if os.Getenv("GRAPHWAN_TEST_WINDOWS") != "1" {
		t.Skip("requires GRAPHWAN_TEST_WINDOWS=1 on a disposable elevated Windows host")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Fatal("native Windows tests require an elevated administrator token")
	}
}

func TestNativeWindowsTunnel(t *testing.T) {
	requireTestWindows(t)
	for _, address := range []string{"10.240.31.1/24", "fd42:6777::1/64"} {
		for _, mtu := range []int{1280, 9000} {
			t.Run(fmt.Sprintf("%s/MTU%d", address, mtu), func(t *testing.T) {
				prefix := netip.MustParsePrefix(address)
				checkNativeTunnel(t, tunnel.Config{Address: prefix, MTU: mtu}, prefix.Addr().Next())
			})
		}
	}
}

func TestNativeWindowsConfiguration(t *testing.T) {
	requireTestWindows(t)
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

func checkNativeMTU(t *testing.T, iface *net.Interface, config tunnel.Config) {
	t.Helper()
	luid, err := winipcfg.LUIDFromIndex(uint32(iface.Index))
	if err != nil {
		t.Fatal(err)
	}
	// NDIS reports the link MTU; IP interface NLMTU controls IPv4/IPv6 packets.
	for _, family := range []winipcfg.AddressFamily{windows.AF_INET, windows.AF_INET6} {
		row, err := luid.IPInterface(family)
		if err != nil || row.NLMTU != uint32(config.MTU) {
			t.Fatalf("IP interface %d MTU: %+v %v", family, row, err)
		}
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
	rows, err := winipcfg.GetIPForwardTable2(windows.AF_UNSPEC)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.InterfaceIndex == uint32(iface.Index) && row.DestinationPrefix.Prefix() == config.Address.Masked() && row.NextHop.Addr().IsUnspecified() {
			return true
		}
	}
	return false
}

func TestNativeWindowsNameOwnership(t *testing.T) {
	requireTestWindows(t)
	config := tunnel.Config{Name: "gw-test-name", Address: netip.MustParsePrefix("10.240.38.1/24"), MTU: 1280}
	// Simultaneous creators must not rename or reuse each other's adapter.
	start := make(chan struct{})
	created := make(chan tunnel.Device, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			<-start
			device, err := tunnel.Open(config)
			if err == nil {
				created <- device
			}
		})
	}
	close(start)
	wg.Wait()
	close(created)
	var winner tunnel.Device
	count := 0
	for device := range created {
		defer device.Close()
		winner = device
		count++
	}
	if count != 1 {
		t.Fatalf("concurrent name reservation created %d adapters", count)
	}
	iface, err := net.InterfaceByName(winner.Name())
	if err != nil {
		t.Fatal(err)
	}
	config.Name = strings.ToUpper(config.Name)
	if duplicate, err := tunnel.Open(config); err == nil {
		duplicate.Close()
		t.Fatal("case-insensitive duplicate alias accepted")
	}
	current, err := net.InterfaceByName(winner.Name())
	if err != nil || current.Index != iface.Index {
		t.Fatal("duplicate creation renamed or replaced the existing adapter")
	}
	checkConfiguredNativeTunnel(t, winner, winner.Configuration(), config.Address.Addr().Next())
}

func TestNativeWindowsRouteOwnership(t *testing.T) {
	requireTestWindows(t)
	// Windows permits the same prefix on different LUIDs. Updating/closing one
	// must preserve the other's route and address even when the prefix matches.
	config := tunnel.Config{Address: netip.MustParsePrefix("10.240.39.1/24"), MTU: 1280}
	foreign, err := tunnel.Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	iface, err := net.InterfaceByName(foreign.Name())
	if err != nil {
		t.Fatal(err)
	}
	config.Address = netip.MustParsePrefix("10.240.39.2/24")
	device, err := tunnel.Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer device.Close()
	config.Address = netip.MustParsePrefix("10.240.40.1/24")
	config.MTU = 9000
	if err := device.(tunnel.Reconfigurable).Reconfigure(config); err != nil {
		t.Fatal(err)
	}
	checkNativeRoute(t, iface, foreign.Configuration())
	checkConfiguredNativeTunnel(t, device, config, config.Address.Addr().Next())
	checkNativeRoute(t, iface, foreign.Configuration())
	checkConfiguredNativeTunnel(t, foreign, foreign.Configuration(), foreign.Configuration().Address.Addr().Next())
}
