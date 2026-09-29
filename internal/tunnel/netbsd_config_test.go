package tunnel

import (
	"net/netip"
	"testing"
)

func TestNetBSDConfigBounds(t *testing.T) {
	for _, mtu := range []int{1280, 1500, 1501, 9000} {
		err := validateNetBSDConfig(Config{Address: netip.MustParsePrefix("10.240.51.1/24"), MTU: mtu})
		if (err == nil) != (mtu <= 1500) {
			t.Fatalf("MTU %d: %v", mtu, err)
		}
	}
	if err := validateNetBSDConfig(Config{Name: "foreign", Address: netip.MustParsePrefix("fd42:6751::1/64"), MTU: 1280}); err == nil {
		t.Fatal("non-driver name accepted")
	}
}
