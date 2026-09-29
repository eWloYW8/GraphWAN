package tunnel

import (
	"errors"
	"net/netip"
	"slices"
	"testing"
)

func TestUtunUnit(t *testing.T) {
	for name, want := range map[string]uint32{"": 0, "utun0": 1, "utun9": 10, "utun4294967294": 4294967295} {
		if got, err := utunUnit(name); err != nil || got != want {
			t.Fatalf("%q: %d %v", name, got, err)
		}
	}
	for _, name := range []string{"utun", "gw1", "utun-1", "utun+1", "utun01", "utun0x1", "utun1suffix", "utun4294967295", "utun999999999999999", "utun1\x00"} {
		if _, err := utunUnit(name); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
}

type routeKernel struct {
	configKernel
	entries   []routeEntry
	mutations []string
}

func (k *routeKernel) routes() subnetRoutes {
	return subnetRoutes{index: 7,
		list: func(netip.Prefix) ([]routeEntry, error) {
			err := k.run("routes", func() error { return nil })
			return slices.Clone(k.entries), err
		},
		add: func(p netip.Prefix) error {
			k.mutations = append(k.mutations, "add "+p.String())
			return k.run("route-add "+p.String(), func() error {
				for _, e := range k.entries {
					if e.prefix == p && !e.scoped {
						return errors.New("route exists")
					}
				}
				k.entries = append(k.entries, routeEntry{prefix: p, index: 7, usable: true})
				return nil
			})
		},
		remove: func(p netip.Prefix) error {
			k.mutations = append(k.mutations, "remove "+p.String())
			return k.run("route-remove "+p.String(), func() error {
				for i, e := range k.entries {
					if e.prefix == p && !e.scoped {
						k.entries = slices.Delete(k.entries, i, i+1)
						return nil
					}
				}
				return errors.New("route missing")
			})
		},
	}
}

func TestSubnetRouteOwnership(t *testing.T) {
	p := netip.MustParsePrefix("10.42.0.0/24")
	for _, scenario := range []string{"owned", "foreign", "scoped", "reject", "add-timeout", "delete-timeout"} {
		t.Run(scenario, func(t *testing.T) {
			k := routeKernel{configKernel: configKernel{faults: map[string]string{}}}
			switch scenario {
			case "owned", "delete-timeout":
				k.entries = []routeEntry{{prefix: p, index: 7, usable: true}}
			case "foreign":
				k.entries = []routeEntry{{prefix: p, index: 8, usable: true}}
			case "scoped":
				k.entries = []routeEntry{{prefix: p, index: 8, usable: true, scoped: true}}
			case "reject":
				k.entries = []routeEntry{{prefix: p, index: 7}}
			case "add-timeout":
				k.faults["route-add "+p.String()] = "after"
			}
			err := k.routes().ensure(p)
			if scenario == "foreign" || scenario == "reject" {
				if err == nil || len(k.mutations) != 0 {
					t.Fatal("conflicting route overwritten")
				}
				if scenario == "foreign" {
					if err := k.routes().discard(p); err != nil || len(k.mutations) != 0 {
						t.Fatal("foreign route deleted")
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "owned" && len(k.mutations) != 0 {
				t.Fatal("existing owned route recreated")
			}
			if scenario == "delete-timeout" {
				k.faults["route-remove "+p.String()] = "after"
			}
			if err := k.routes().discard(p); err != nil {
				t.Fatal(err)
			}
			for _, e := range k.entries {
				if !e.scoped {
					t.Fatal("owned route survived removal")
				}
			}
			if scenario == "scoped" && (len(k.entries) != 1 || k.entries[0].index != 8) {
				t.Fatal("scoped foreign route removed")
			}
		})
	}
}

func TestRoutedConfigurationRollback(t *testing.T) {
	before := Config{Address: netip.MustParsePrefix("fd42:6777::1/64"), MTU: 1280}
	after := Config{Address: netip.MustParsePrefix("fd42:6777::1/80"), MTU: 9000}
	oldRoute := routeEntry{prefix: before.Address.Masked(), index: 7, usable: true}
	foreign := routeEntry{prefix: netip.MustParsePrefix("192.0.2.0/24"), index: 9, usable: true}
	for _, scenario := range []string{"success", "add-fails", "remove-fails", "foreign-conflict", "rollback-fails", "address-fails", "old-route-lost"} {
		t.Run(scenario, func(t *testing.T) {
			k := routeKernel{configKernel: configKernel{mtu: 1280, addresses: []netip.Prefix{before.Address}, faults: map[string]string{}}, entries: []routeEntry{oldRoute, foreign}}
			switch scenario {
			case "add-fails":
				k.faults["route-add "+after.Address.Masked().String()] = "before"
			case "remove-fails":
				k.faults["route-remove "+before.Address.Masked().String()] = "before"
			case "foreign-conflict":
				k.entries = append(k.entries, routeEntry{prefix: after.Address.Masked(), index: 11, usable: true})
			case "rollback-fails":
				k.faults["route-add "+after.Address.Masked().String()] = "before"
				k.faults["add "+before.Address.String()] = "before"
			case "address-fails", "old-route-lost":
				k.faults["add "+after.Address.String()] = "after"
				if scenario == "old-route-lost" {
					k.entries = []routeEntry{foreign}
				}
			}
			err := changeRoutedConfig(before, after, k.ops(), k.routes())
			if !slices.Contains(k.entries, foreign) {
				t.Fatal("foreign route changed")
			}
			if scenario == "success" {
				if err != nil || k.mtu != after.MTU || !slices.Contains(k.addresses, after.Address) || slices.Contains(k.entries, oldRoute) {
					t.Fatalf("migration: %+v %v", k, err)
				}
				if ok, err := k.routes().owned(after.Address.Masked(), true); !ok || err != nil {
					t.Fatal("new route missing")
				}
				return
			}
			if err == nil {
				t.Fatal("failure ignored")
			}
			if scenario == "rollback-fails" {
				if !errors.Is(err, ErrUnavailable) {
					t.Fatal("failed restoration did not retire device")
				}
				return
			}
			if errors.Is(err, ErrUnavailable) || k.mtu != before.MTU || len(k.addresses) != 1 || k.addresses[0] != before.Address || !slices.Contains(k.entries, oldRoute) {
				t.Fatalf("rollback: %+v %v", k, err)
			}
			for _, e := range k.entries {
				if e.prefix == after.Address.Masked() && e.index == 7 {
					t.Fatal("new route survived rollback")
				}
			}
			if scenario == "foreign-conflict" && len(k.entries) != 3 {
				t.Fatal("conflicting foreign route deleted")
			}
		})
	}
}
