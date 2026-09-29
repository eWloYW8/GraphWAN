//go:build linux

package discovery

import "testing"

func TestContainerInterfacePolicyPreservesAgentUplink(t *testing.T) {
	for _, test := range []struct {
		kind            string
		master          int
		uplink, exclude bool
	}{
		{"device", 0, false, false}, {"device", 4, false, false},
		{"veth", 0, true, false}, {"veth", 0, false, true},
		{"veth", 4, false, true}, {"veth", 4, true, true},
	} {
		if got := containerExcluded(test.kind, test.master, test.uplink); got != test.exclude {
			t.Fatalf("%+v: excluded=%t", test, got)
		}
	}
}
