//go:build netbsd

package tunnel

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestNetBSDIOCTLLayout(t *testing.T) {
	if got, want := unsafe.Sizeof(netBSDIPv6Request{}), uintptr((netBSDGetIPv6Flags>>16)&0x1fff); got != want {
		t.Fatalf("IPv6 request: %d, kernel expects %d", got, want)
	}
	if got, want := unsafe.Sizeof(netBSDIfreq{}), uintptr((unix.SIOCIFCREATE>>16)&0x1fff); got != want {
		t.Fatalf("ifreq: %d, kernel expects %d", got, want)
	}
	if got, want := unsafe.Sizeof(netBSDDescriptionRequest{}), uintptr((netBSDGetDescription>>16)&0x1fff); got != want {
		t.Fatalf("description request: %d, kernel expects %d", got, want)
	}
	if offset := unsafe.Offsetof(netBSDDescriptionRequest{}.Buffer); offset != 16+unsafe.Sizeof(uintptr(0)) {
		t.Fatalf("description pointer offset %d", offset)
	}
}
