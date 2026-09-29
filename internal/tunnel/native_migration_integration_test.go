//go:build (freebsd || darwin || openbsd || netbsd || windows) && integration

package tunnel_test

import (
	"net"
	"net/netip"
	"testing"

	"github.com/graphwan/graphwan/internal/tunnel"
)

func checkNativeMigration(t *testing.T, beforeAddress, afterAddress string) {
	t.Helper()
	checkNativeMigrationMTU(t, beforeAddress, afterAddress, 9000)
}
func checkNativeMigrationMTU(t *testing.T, beforeAddress, afterAddress string, mtu int) {
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
	after := tunnel.Config{Address: netip.MustParsePrefix(afterAddress), MTU: mtu}
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
