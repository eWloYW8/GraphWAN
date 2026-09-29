package discovery

import (
	"errors"
	"fmt"
	"slices"
	"testing"
)

// Driver contracts from DragonFly d1f4fb943c73e2b61ddabff6aa48ce7551db72f4.
// Names are deliberately editable aliases: no decision may depend on them.
func TestDragonFlyDriverClassification(t *testing.T) {
	for _, tc := range []struct {
		name      string
		kind      int
		supported []dragonFlyProbe
		want      bool
	}{
		{"Ethernet renamed tap0", 6, []dragonFlyProbe{dragonFlyHardware}, true},
		{"WiFi VAP renamed bridge0", 6, []dragonFlyProbe{dragonFlyHardware}, true},
		{"FireWire without media query", 6, []dragonFlyProbe{dragonFlyHardware}, true},
		{"virtio virtual machine NIC", 6, []dragonFlyProbe{dragonFlyHardware}, true},
		{"legacy sn/sbsh NIC without media query", 6, []dragonFlyProbe{dragonFlyHardware}, true},
		{"TAP renamed em0 with groups removed", 6, nil, false},
		{"closed TAP with empty status text", 6, nil, false},
		{"Netgraph eiface renamed em0", 6, nil, false},
		{"Netgraph fec renamed em0", 6, nil, false},
		{"bridge renamed em0", 6, []dragonFlyProbe{dragonFlyHardware, dragonFlyBridge}, false},
		{"VLAN renamed em0", 6, []dragonFlyProbe{dragonFlyHardware, dragonFlyVLAN}, false},
		{"lagg renamed em0", 6, []dragonFlyProbe{dragonFlyHardware, dragonFlyLAGG}, false},
		{"TUN spoofing Ethernet type", 6, nil, false},
		{"WireGuard", 131, nil, false},
		{"CARP", 248, []dragonFlyProbe{dragonFlyHardware}, false},
		{"loopback", 24, nil, false},
		{"unknown", 0, []dragonFlyProbe{dragonFlyHardware}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := dragonFlyPhysical(tc.kind, func(operation dragonFlyProbe) (bool, error) {
				if tc.kind == 0 || tc.kind == 24 || tc.kind == 131 || tc.kind == 248 {
					t.Fatal("queried an unsupported interface type")
				}
				return slices.Contains(tc.supported, operation), nil
			})
			if err != nil || got != tc.want {
				t.Fatalf("physical = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

func TestDragonFlyDiscoveryProbeFailures(t *testing.T) {
	operations := []dragonFlyProbe{dragonFlyHardware, dragonFlyVLAN, dragonFlyBridge, dragonFlyLAGG}
	for _, failed := range operations {
		t.Run(string(failed), func(t *testing.T) {
			failure := fmt.Errorf("interface disappeared during %s", failed)
			var calls []dragonFlyProbe
			physical, err := dragonFlyPhysical(6, func(operation dragonFlyProbe) (bool, error) {
				calls = append(calls, operation)
				if operation == failed {
					return false, failure
				}
				return operation == dragonFlyHardware, nil
			})
			if physical || !errors.Is(err, failure) {
				t.Fatalf("probe failure became type evidence: %v, %v", physical, err)
			}
			if !slices.Equal(calls, operations[:slices.Index(operations, failed)+1]) {
				t.Fatalf("continued querying after failure: %v", calls)
			}
		})
	}
}
