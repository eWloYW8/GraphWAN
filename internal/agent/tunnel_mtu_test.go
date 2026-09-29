package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/tunnel"
)

type mtuDevice struct {
	*recoveryDevice
	mtu                      atomic.Int32
	failUpdate, failRollback bool
}

func (d *mtuDevice) Configuration() tunnel.Config {
	cfg := d.recoveryDevice.Configuration()
	cfg.MTU = int(d.mtu.Load())
	return cfg
}
func (d *mtuDevice) SetMTU(mtu int) error {
	if (mtu == 9000 && d.failUpdate) || (mtu == 1280 && d.failRollback) {
		return errors.New("injected MTU failure")
	}
	d.mtu.Store(int32(mtu))
	return nil
}

func TestTunnelMTUTransaction(t *testing.T) {
	for _, scenario := range []string{"success", "rollback", "rollback-failure"} {
		t.Run(scenario, func(t *testing.T) {
			created := 0
			r, config, _, calls := recoveryRuntimeWithDevice(t, func(base *recoveryDevice) tunnel.Device {
				created++
				d := &mtuDevice{recoveryDevice: base, failUpdate: created == 2 && scenario != "success", failRollback: created == 1 && scenario == "rollback-failure"}
				d.mtu.Store(int32(base.config.MTU))
				return d
			})
			before := r.state.Load()
			updated := config.Clone()
			updated.Revision++
			for i := range updated.Networks {
				updated.Networks[i].MTU = 9000
			}
			err := r.Apply(context.Background(), updated)
			if (err == nil) != (scenario == "success") {
				t.Fatalf("apply: %v", err)
			}
			if calls.Load() != 2 {
				t.Fatal("MTU edit recreated a TUN")
			}
			after := r.state.Load()
			if after.mesh != before.mesh {
				t.Fatal("MTU edit replaced the peer mesh")
			}
			if scenario != "success" && after != before {
				t.Fatal("failed update published a new runtime")
			}
			for i, network := range config.Networks {
				d := before.devices[network.ID]
				if after.devices[network.ID] != d {
					t.Fatal("MTU edit replaced a reader")
				}
				if scenario == "rollback-failure" && i == 0 {
					if d.failure.Load() == nil || r.Health() == nil {
						t.Fatal("rollback failure was not marked for recovery")
					}
					continue
				}
				want := 1280
				if scenario == "success" {
					want = 9000
				}
				if d.Configuration().MTU != want {
					t.Fatalf("MTU %d, want %d", d.Configuration().MTU, want)
				}
				checkRecoveryDelivery(t, d.Device.(*mtuDevice).recoveryDevice)
			}
			if scenario == "rollback-failure" {
				r.repairTunnels(time.Now().Add(2 * time.Second))
				repaired := r.state.Load()
				first := config.Networks[0].ID
				if repaired.devices[first] == before.devices[first] || repaired.devices[first].Configuration().MTU != 1280 {
					t.Fatal("recovery did not restore the applied MTU")
				}
				if repaired.snapshot.Revision != config.Revision || r.Health() != nil {
					t.Fatal("recovery changed revision or left a failure")
				}
				checkRecoveryDelivery(t, repaired.devices[first].Device.(*mtuDevice).recoveryDevice)
			}
		})
	}
}
