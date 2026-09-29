package discovery

import "testing"

func TestNamedBSDDriverClassification(t *testing.T) {
	cloners := map[string]bool{"tap": true, "tun": true, "bridge": true, "veb": true, "vport": true, "pair": true, "vlan": true, "aggr": true, "vxlan": true, "custom": true}
	for _, test := range []struct {
		name string
		kind int
		want bool
	}{
		{"vioif0", 6, true}, {"wm0", 6, true}, {"rtwn1", 71, true}, {"vio0", 6, true}, {"em12", 6, true}, {"iwm0", 71, true}, {"bwfm0", 6, true}, {"umb0", 250, true},
		{"tap0", 6, false}, {"tun2", 23, false}, {"bridge0", 6, false}, {"veb0", 6, false}, {"vport0", 6, false},
		{"pair0", 6, false}, {"vlan42", 6, false}, {"aggr1", 6, false}, {"vxlan0", 6, false}, {"custom8", 6, false},
		{"lo0", 24, false}, {"enc0", 244, false}, {"gif0", 131, false}, {"unknown0", 53, false},
		{"", 6, false}, {"vio0\x00suffix", 6, false}, {"0", 6, false}, {"vio", 6, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := namedBSDPhysical(test.kind, test.name, cloners); got != test.want {
				t.Fatalf("kind %d: got %t, want %t", test.kind, got, test.want)
			}
		})
	}
	// A freshly registered clone driver must be rejected without a code change.
	cloners["vio"] = true
	if namedBSDPhysical(6, "vio0", cloners) {
		t.Fatal("kernel cloner registration ignored")
	}
}
