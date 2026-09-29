package discovery

import "testing"

func TestDarwinPhysicalInterfaceMetadata(t *testing.T) {
	for _, test := range []struct {
		name, driver string
		metadata     darwinInterfaceMetadata
		want         bool
	}{
		{"ethernet", "en0", darwinInterfaceMetadata{kind: 6, family: 2}, true},
		{"hardware name is not a prefix blacklist", "tundra0", darwinInterfaceMetadata{kind: 6, family: 2}, true},
		{"USB ethernet", "en4", darwinInterfaceMetadata{kind: 6, family: 2, subfamily: 1}, true},
		{"WiFi ethernet framing", "en1", darwinInterfaceMetadata{kind: 6, family: 2, subfamily: 3}, true},
		{"WiFi type", "en1", darwinInterfaceMetadata{kind: 71, family: 2, subfamily: 3}, true},
		{"thunderbolt", "en2", darwinInterfaceMetadata{kind: 6, family: 2, subfamily: 4}, true},
		{"firewire", "fw0", darwinInterfaceMetadata{kind: 144, family: 13}, true},
		{"cellular without a MAC", "pdp_ip0", darwinInterfaceMetadata{kind: 255, family: 15}, true},
		{"utun delegated to WiFi", "utun0", darwinInterfaceMetadata{kind: 1, family: 17, subfamily: 3}, false},
		{"cloned Ethernet", "feth0", darwinInterfaceMetadata{kind: 6, family: 2, extendedFlags: 0x10000}, false},
		{"clone with hardware looking name", "en42", darwinInterfaceMetadata{kind: 6, family: 2, extendedFlags: 0x10000}, false},
		{"legacy TAP", "tap3", darwinInterfaceMetadata{kind: 6, family: 2}, false},
		{"legacy TUN", "tun0", darwinInterfaceMetadata{kind: 6, family: 2}, false},
		{"bridge", "bridge0", darwinInterfaceMetadata{kind: 209, family: 2}, false},
		{"VLAN", "vlan0", darwinInterfaceMetadata{kind: 6, family: 5}, false},
		{"bond", "bond0", darwinInterfaceMetadata{kind: 6, family: 14}, false},
		{"AWDL", "awdl0", darwinInterfaceMetadata{kind: 6, family: 2, subfamily: 3, extendedFlags: 0x100000}, false},
		{"VMNET", "vmenet0", darwinInterfaceMetadata{kind: 6, family: 2, subfamily: 9}, false},
		{"simulated cellular", "en5", darwinInterfaceMetadata{kind: 6, family: 2, subfamily: 10}, false},
		{"coprocessor", "en5", darwinInterfaceMetadata{kind: 6, family: 2, subfamily: 6}, false},
		{"redirect", "en5", darwinInterfaceMetadata{kind: 6, family: 2, subfamily: 11}, false},
		{"unknown family", "en5", darwinInterfaceMetadata{kind: 6, family: 999}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.metadata.physical(test.driver); got != test.want {
				t.Fatalf("physical(%q, %+v) = %v, want %v", test.driver, test.metadata, got, test.want)
			}
		})
	}
}
