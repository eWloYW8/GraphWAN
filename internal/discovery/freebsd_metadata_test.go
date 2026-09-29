package discovery

import "testing"

func TestFreeBSDDriverClassification(t *testing.T) {
	cloners := map[string]bool{"tap": true, "tun": true, "wlan": true, "epair": true, "bridge": true, "customvpn": true}
	for _, test := range []struct {
		name string
		kind int
		want bool
	}{
		{"vtnet0", 6, true}, {"em12", 6, true}, {"mlx5en0", 6, true},
		{"wlan0", 6, true}, {"wlan12", 71, true}, {"epair42", 6, true},
		{"ib0", 199, true}, {"cell0", 243, true},
		{"tap12", 6, false}, {"tun2", 23, false}, {"vmnet5", 6, false},
		{"ngeth3", 6, false}, {"vlan2", 6, false}, {"bridge1", 6, false},
		{"lagg1", 6, false}, {"customvpn123", 6, false},
		{"lo0", 24, false}, {"gif0", 131, false}, {"unknown0", 53, false},
		{"wlan0", 24, false}, {"", 6, false}, {"vtnet0\x00suffix", 6, false},
	} {
		if got := freeBSDPhysical(test.kind, test.name, cloners); got != test.want {
			t.Errorf("driver %s, type %d: got %t, want %t", test.name, test.kind, got, test.want)
		}
	}
}
