package agent

import (
	"context"
	"errors"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/tunnel"
)

type configurableDevice struct {
	*recoveryDevice
	current atomic.Pointer[tunnel.Config]
	failure string
}

func (d *configurableDevice) Configuration() tunnel.Config { return *d.current.Load() }
func (d *configurableDevice) Reconfigure(cfg tunnel.Config) error {
	if d.failure == "rollback" && cfg.Address.Bits() == 24 {
		return errors.New("injected rollback failure")
	}
	if d.failure == "update" && cfg.Address.Bits() == 25 {
		return errors.New("injected update failure with local rollback")
	}
	d.current.Store(&cfg)
	if d.failure == "unavailable" && cfg.Address.Bits() == 25 {
		return tunnel.ErrUnavailable
	}
	return nil
}

func TestTunnelConfigurationTransaction(t *testing.T) {
	for _, scenario := range []string{"success", "update", "unavailable", "rollback"} {
		t.Run(scenario, func(t *testing.T) {
			created := 0
			r, config, _, calls := recoveryRuntimeWithDevice(t, func(base *recoveryDevice) tunnel.Device {
				created++
				d := &configurableDevice{recoveryDevice: base}
				d.current.Store(&base.config)
				if created == 1 && scenario == "rollback" {
					d.failure = "rollback"
				}
				if created == 2 {
					d.failure = scenario
					if scenario == "rollback" {
						d.failure = "update"
					}
				}
				return d
			})
			before := r.state.Load()
			updated := config.Clone()
			updated.Revision++
			for i := range updated.Networks {
				updated.Networks[i].CIDR = netip.PrefixFrom(updated.Networks[i].Self.Address, 25).Masked()
				updated.Networks[i].MTU = 9000
			}
			err := r.Apply(context.Background(), updated)
			if (err == nil) != (scenario == "success") {
				t.Fatalf("apply: %v", err)
			}
			after := r.state.Load()
			if calls.Load() != 2 || after.mesh != before.mesh {
				t.Fatal("configuration update replaced resources")
			}
			if scenario != "success" && after != before {
				t.Fatal("failed update published a new snapshot")
			}
			for i, network := range config.Networks {
				d := before.devices[network.ID]
				if after.devices[network.ID] != d {
					t.Fatal("configuration update replaced reader")
				}
				failed := (scenario == "rollback" && i == 0) || (scenario == "unavailable" && i == 1)
				if failed {
					if d.failure.Load() == nil || r.Health() == nil {
						t.Fatal("unrestored configuration was not retired")
					}
					select {
					case <-d.Device.(*configurableDevice).done:
					default:
						t.Fatal("unavailable device still open")
					}
					continue
				}
				want := tunnel.Config{Address: netip.PrefixFrom(network.Self.Address, 24), MTU: 1280}
				if scenario == "success" {
					want.Address = netip.PrefixFrom(network.Self.Address, 25)
					want.MTU = 9000
				}
				if d.Configuration() != want {
					t.Fatalf("config %v, want %v", d.Configuration(), want)
				}
				checkRecoveryDelivery(t, d.Device.(*configurableDevice).recoveryDevice)
			}
			if scenario == "rollback" || scenario == "unavailable" {
				r.repairTunnels(time.Now().Add(2 * time.Second))
				repaired := r.state.Load()
				if repaired.snapshot.Revision != config.Revision || r.Health() != nil {
					t.Fatal("recovery changed applied revision or left a failure")
				}
				for _, network := range config.Networks {
					d := repaired.devices[network.ID]
					if d.Configuration().MTU != 1280 || d.Configuration().Address != netip.PrefixFrom(network.Self.Address, 24) {
						t.Fatal("recovery used rejected configuration")
					}
					checkRecoveryDelivery(t, d.Device.(*configurableDevice).recoveryDevice)
				}
			}
		})
	}
}
