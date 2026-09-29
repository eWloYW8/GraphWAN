package tunnel

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"testing"
)

type configKernel struct {
	mtu       int
	addresses []netip.Prefix
	// Fail once, either before mutation or after mutation (ambiguous timeout).
	faults map[string]string
}

func (k *configKernel) run(op string, apply func() error) error {
	fault := k.faults[op]
	delete(k.faults, op)
	if fault == "before" {
		return fmt.Errorf("injected %s failure", op)
	}
	if err := apply(); err != nil {
		return err
	}
	if fault == "after" {
		return fmt.Errorf("injected %s timeout after mutation", op)
	}
	return nil
}
func (k *configKernel) ops() configOperations {
	return configOperations{
		setMTU: func(mtu int) error {
			return k.run(fmt.Sprintf("mtu %d", mtu), func() error { k.mtu = mtu; return nil })
		},
		addresses: func() ([]netip.Prefix, error) {
			err := k.run("addresses", func() error { return nil })
			return slices.Clone(k.addresses), err
		},
		add: func(p netip.Prefix) error {
			return k.run("add "+p.String(), func() error {
				for _, existing := range k.addresses {
					if existing.Addr() == p.Addr() {
						return errors.New("address already exists")
					}
				}
				k.addresses = append(k.addresses, p)
				return nil
			})
		},
		remove: func(p netip.Prefix) error {
			return k.run("remove "+p.String(), func() error {
				for i, existing := range k.addresses {
					if existing.Addr() == p.Addr() {
						k.addresses = slices.Delete(k.addresses, i, i+1)
						return nil
					}
				}
				return errors.New("address not assigned")
			})
		},
	}
}

func TestAddressConfigurationTransaction(t *testing.T) {
	for _, pair := range [][2]string{
		{"10.42.0.1/24", "10.42.0.1/25"},
		{"fd42:6777::1/64", "fd42:6777::1/80"},
		{"fd42:6777::1/64", "fd42:6778::2/80"},
		{"10.42.0.1/24", "fd42:6777::1/64"},
	} {
		t.Run(pair[0]+"→"+pair[1], func(t *testing.T) {
			before := Config{Address: netip.MustParsePrefix(pair[0]), MTU: 1280}
			after := Config{Address: netip.MustParsePrefix(pair[1]), MTU: 9000}
			foreign := netip.MustParsePrefix("192.0.2.1/24")
			for _, scenario := range []string{"success", "mtu-failure", "add-before", "add-after", "remove-before", "remove-after", "rollback-address-failure", "rollback-query-failure", "rollback-mtu-failure"} {
				t.Run(scenario, func(t *testing.T) {
					k := configKernel{mtu: before.MTU, addresses: []netip.Prefix{before.Address, foreign}, faults: map[string]string{}}
					switch scenario {
					case "mtu-failure":
						k.faults["mtu 9000"] = "before"
					case "add-before":
						k.faults["add "+pair[1]] = "before"
					case "add-after":
						k.faults["add "+pair[1]] = "after"
					case "remove-before":
						k.faults["remove "+pair[0]] = "before"
					case "remove-after":
						k.faults["remove "+pair[0]] = "after"
					case "rollback-address-failure":
						k.faults["remove "+pair[0]], k.faults["add "+pair[0]] = "after", "before"
					case "rollback-query-failure":
						k.faults["add "+pair[1]], k.faults["addresses"] = "before", "before"
					case "rollback-mtu-failure":
						k.faults["add "+pair[1]], k.faults["mtu 1280"] = "before", "before"
					}
					err := changeConfig(before, after, k.ops())
					if !slices.Contains(k.addresses, foreign) {
						t.Fatal("unrelated address was removed")
					}
					if scenario == "success" {
						if err != nil || k.mtu != after.MTU || len(k.addresses) != 2 || !slices.Contains(k.addresses, after.Address) {
							t.Fatalf("new configuration: %+v %v", k, err)
						}
						return
					}
					if err == nil {
						t.Fatal("injected failure was ignored")
					}
					if scenario == "rollback-address-failure" || scenario == "rollback-query-failure" || scenario == "rollback-mtu-failure" {
						if !errors.Is(err, ErrUnavailable) {
							t.Fatalf("restoration failure must retire the device: %v", err)
						}
						return
					}
					if errors.Is(err, ErrUnavailable) || k.mtu != before.MTU || len(k.addresses) != 2 || !slices.Contains(k.addresses, before.Address) {
						t.Fatalf("old configuration not restored: %+v %v", k, err)
					}
				})
			}
		})
	}
}
