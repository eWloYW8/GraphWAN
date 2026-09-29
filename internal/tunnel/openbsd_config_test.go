package tunnel

import "testing"

func TestOpenBSDUnit(t *testing.T) {
	for name, want := range map[string]uint32{"tun0": 0, "tun3": 3, "tun255": 255, "tun65535": 65535} {
		got, err := openBSDUnit(name)
		if err != nil || got != want {
			t.Fatalf("%q: %d, %v", name, got, err)
		}
	}
	for _, name := range []string{"", "tun", "tap0", "gw0", "tun-1", "tun+1", "tun01", "tun65536", "tun4294967296", "tun1/../tun2", "tun1\x00", "tun１"} {
		if _, err := openBSDUnit(name); err == nil {
			t.Errorf("accepted invalid or ambiguous name %q", name)
		}
	}
}
