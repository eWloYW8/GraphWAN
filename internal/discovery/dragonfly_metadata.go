package discovery

import "fmt"

type dragonFlyProbe string

const (
	dragonFlyHardware dragonFlyProbe = "hardware address"
	dragonFlyVLAN     dragonFlyProbe = "VLAN configuration"
	dragonFlyBridge   dragonFlyProbe = "bridge cache size"
	dragonFlyLAGG     dragonFlyProbe = "aggregation flags"
)

// DragonFly does not export if_dname. Its software Ethernet drivers can share
// IFT_ETHER with hardware and can be renamed or have their groups removed.
// Instead use the read-only ioctl contracts: TAP and Netgraph Ethernet drivers
// reject SIOCGHWADDR; bridge, vlan and lagg implement their own query as well as
// the generic Ethernet hardware-address query. Ordinary NICs reject those three
// software-driver queries. Keep this policy executable in Linux tests.
func dragonFlyPhysical(kind int, probe func(dragonFlyProbe) (bool, error)) (bool, error) {
	switch kind {
	case 6, 7, 9, 15, 62, 69, 71, 117, 199, 237, 243, 244:
	default:
		return false, nil
	}
	for _, operation := range []dragonFlyProbe{dragonFlyHardware, dragonFlyVLAN, dragonFlyBridge, dragonFlyLAGG} {
		supported, err := probe(operation)
		if err != nil {
			return false, fmt.Errorf("%s: %w", operation, err)
		}
		if operation == dragonFlyHardware && !supported || operation != dragonFlyHardware && supported {
			return false, nil
		}
	}
	return true, nil
}
