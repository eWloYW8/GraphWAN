package testutil

import (
	"net"
	"net/netip"
	"runtime"
	"testing"
)

// BSD does not automatically make every 127/8 address locally reachable. Tests
// with multiple loopback destinations need explicitly configured aliases there.
// Never change the developer's host networking from an ordinary Go test.
func RequireLoopbackAliases(t *testing.T, addresses ...string) {
	t.Helper()
	switch runtime.GOOS {
	case "darwin", "dragonfly", "freebsd", "netbsd", "openbsd":
	default:
		return
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	present := map[netip.Addr]bool{}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback == 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			t.Fatal(err)
		}
		for _, addr := range addrs {
			if prefix, err := netip.ParsePrefix(addr.String()); err == nil {
				present[prefix.Addr().Unmap()] = true
			}
		}
	}
	for _, address := range addresses {
		if !present[netip.MustParseAddr(address)] {
			t.Skipf("requires explicit BSD loopback alias %s; see docs/freebsd-operation.md native transport checks", address)
		}
	}
}
