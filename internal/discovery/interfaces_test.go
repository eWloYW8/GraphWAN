package discovery

import (
	"errors"
	"net"
	"net/netip"
	"slices"
	"testing"

	"github.com/graphwan/graphwan/internal/model"
)

type addressText string

func (a addressText) Network() string { return "ip" }
func (a addressText) String() string  { return string(a) }

func TestInterfaceEndpointSnapshot(t *testing.T) {
	interfaces := []net.Interface{
		{Index: 1, Name: "tun-named-hardware", Flags: net.FlagUp},
		{Index: 2, Name: "eth-named-tunnel", Flags: net.FlagUp},
		{Index: 3, Name: "down", Flags: 0},
		{Index: 4, Name: "loop", Flags: net.FlagUp | net.FlagLoopback},
		{Index: 5, Name: "duplicate-address", Flags: net.FlagUp},
	}
	addresses := func(iface net.Interface) ([]net.Addr, error) {
		if iface.Index != 1 && iface.Index != 5 {
			t.Fatal("read addresses of excluded interface")
		}
		return []net.Addr{addressText("192.0.2.1/24"), addressText("fd42::1/64"), addressText("169.254.33.1/16"), addressText("fe80::1/64"), addressText("127.0.0.1/8"), addressText("224.0.0.1/4"), addressText("0.0.0.0/32"), addressText("255.255.255.255/32"), addressText("broken"), addressText("192.0.2.1/32")}, nil
	}
	physical := map[int]bool{1: true, 2: false, 3: true, 4: true, 5: true}
	before, err := interfaceEndpoints([]byte("identity"), 24752, interfaces, physical, addresses)
	if err != nil || len(before) != 10 {
		t.Fatalf("endpoint snapshot: %v %v", before, err)
	}
	for _, endpoint := range before {
		if endpoint.Source != model.Interface || endpoint.Transport != model.UDP && endpoint.Transport != model.TCP || endpoint.Validate() != nil {
			t.Fatal("invalid automatic endpoint")
		}
	}
	slices.Reverse(interfaces)
	after, err := interfaceEndpoints([]byte("identity"), 24752, interfaces, physical, addresses)
	if err != nil || !slices.Equal(before, after) {
		t.Fatal("enumeration order changed endpoint identity")
	}
	if _, err := interfaceEndpoints(nil, 0, interfaces, physical, addresses); err == nil {
		t.Fatal("zero port accepted")
	}
	broken := errors.New("interface vanished during address enumeration")
	if partial, err := interfaceEndpoints(nil, 24752, interfaces, physical, func(iface net.Interface) ([]net.Addr, error) {
		if iface.Index == 1 {
			return nil, broken
		}
		return addresses(iface)
	}); !errors.Is(err, broken) || partial != nil {
		t.Fatal("incomplete discovery snapshot published")
	}
}

func TestHardwareMetadataClassification(t *testing.T) {
	for _, test := range []struct {
		metadata interfaceMetadata
		allowed  bool
	}{
		{interfaceMetadata{kind: 6, hardware: true}, true},   // Ethernet, including guest NICs reported as hardware.
		{interfaceMetadata{kind: 71, hardware: true}, true},  // Wi-Fi.
		{interfaceMetadata{kind: 243, hardware: true}, true}, // Cellular may have no Ethernet MAC.
		{interfaceMetadata{kind: 6}, false},                  // TAP/virtual Ethernet.
		{interfaceMetadata{kind: 53}, false},                 // Wintun.
		{interfaceMetadata{kind: 6, hardware: true, filter: true}, false},
		{interfaceMetadata{kind: 6, hardware: true, endpoint: true}, false},
		{interfaceMetadata{kind: 24, hardware: true}, false},
		{interfaceMetadata{kind: 53, hardware: true}, false},
		{interfaceMetadata{kind: 131, hardware: true}, false},
		{interfaceMetadata{kind: 209, hardware: true}, false},
	} {
		if got := test.metadata.physical(); got != test.allowed {
			t.Fatalf("classification %+v: %t", test.metadata, got)
		}
	}
}

// Native BSD fixtures can acquire OS-generated link-local addresses when raised.
// Include those addresses only for the explicitly allowed fixture NICs; unexpected
// addresses on excluded clones still fail the native snapshot comparison.
func fixtureLinkLocalAddresses(t *testing.T, names ...string) []string {
	t.Helper()
	var result []string
	for _, name := range names {
		iface, err := net.InterfaceByName(name)
		if err != nil {
			t.Fatal(err)
		}
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range addresses {
			prefix, err := netip.ParsePrefix(raw.String())
			if err == nil && prefix.Addr().Is6() && prefix.Addr().IsLinkLocalUnicast() {
				result = append(result, prefix.Addr().WithZone(name).String())
			}
		}
	}
	return result
}
