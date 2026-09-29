//go:build (linux || freebsd || darwin || openbsd || netbsd) && integration

package tunnel_test

import (
	"net"
	"testing"

	"github.com/eWloYW8/GraphWAN/internal/tunnel"
)

func checkNativeMTU(t *testing.T, iface *net.Interface, config tunnel.Config) {
	t.Helper()
	if iface.MTU != config.MTU {
		t.Fatalf("interface MTU = %d, want %d", iface.MTU, config.MTU)
	}
}
