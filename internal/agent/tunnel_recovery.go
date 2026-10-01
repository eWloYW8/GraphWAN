package agent

import (
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/tunnel"
)

// A device keeps its failure separate from the immutable configuration snapshot.
// Only applyMu writes retry state; packet paths and reports read failure atomically.
type runtimeTunnel struct {
	tunnel.Device
	failure atomic.Pointer[tunnelFailure]
	created time.Time
	retryAt time.Time
	backoff time.Duration
}
type tunnelFailure struct {
	err error
	at  time.Time
}

func newRuntimeTunnel(device tunnel.Device) *runtimeTunnel {
	return &runtimeTunnel{Device: device, created: time.Now(), backoff: time.Second}
}

// Health reports current forwarding-device failures independently of the last
// applied revision. Restoring a device never rewrites durable desired state.
func (r *DataPlane) Health() error {
	state := r.state.Load()
	if state == nil {
		return nil
	}
	var failures []error
	for _, network := range state.snapshot.Networks {
		if failure := state.devices[network.ID].failure.Load(); failure != nil {
			failures = append(failures, fmt.Errorf("network %s: %w", network.Name, failure.err))
		}
	}
	return errors.Join(failures...)
}

func (r *DataPlane) failTunnel(network model.ID, device *runtimeTunnel, err error) {
	// Packet receivers must never wait for applyMu: Apply can hold it while
	// retiring a Mesh and waiting for those same receivers to finish.
	state := r.state.Load()
	if r.ctx.Err() != nil || state == nil || state.devices[network] != device {
		return
	}
	if !device.failure.CompareAndSwap(nil, &tunnelFailure{err: err, at: time.Now()}) {
		return
	}
	// Remove the failed interface/routes and interrupt any other pending I/O.
	device.Close()
	r.options.Logger.Error("TUN unavailable; scheduling local recovery", "network", network, "error", err)
	select {
	case r.repairWake <- struct{}{}:
	default:
	}
}

func (r *DataPlane) repairLoop() {
	defer r.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-r.repairWake:
		case <-ticker.C:
		}
		r.repairTunnels(time.Now())
	}
}

func (r *DataPlane) repairTunnels(now time.Time) {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	state := r.state.Load()
	if r.closed || r.ctx.Err() != nil || state == nil {
		return
	}
	for _, network := range state.snapshot.Networks {
		if r.ctx.Err() != nil {
			return
		}
		old := state.devices[network.ID]
		failure := old.failure.Load()
		if failure == nil {
			continue
		}
		if old.retryAt.IsZero() {
			if failure.at.Sub(old.created) >= time.Minute {
				old.backoff = time.Second
			}
			old.retryAt = failure.at.Add(old.backoff)
		}
		if now.Before(old.retryAt) {
			continue
		}
		// Recover the applied snapshot, even if an interrupted in-place update
		// left the failed device with a different kernel configuration.
		cfg := tunnel.Config{Address: netip.PrefixFrom(network.Self.Address, network.CIDR.Bits()), MTU: network.MTU}
		device, err := r.options.TunnelFactory(cfg)
		if err != nil {
			old.backoff = min(30*time.Second, 2*old.backoff)
			old.retryAt = time.Now().Add(old.backoff)
			old.failure.Store(&tunnelFailure{err: fmt.Errorf("restore TUN: %w", err)})
			r.options.Logger.Warn("TUN recovery failed", "network", network.ID, "retry_after", old.backoff, "error", err)
			continue
		}
		if r.ctx.Err() != nil {
			device.Close()
			return
		}
		replacement := newRuntimeTunnel(device)
		// Repeated immediate failures also back off, even if Open itself succeeds.
		replacement.backoff = min(30*time.Second, 2*old.backoff)
		next := *state
		next.devices = maps.Clone(state.devices)
		next.devices[network.ID] = replacement
		if err := r.gateway.Apply(r.ctx, state.snapshot.AgentID, gatewayEntries(&next)); err != nil {
			device.Close()
			old.retryAt = time.Now().Add(old.backoff)
			old.failure.Store(&tunnelFailure{err: fmt.Errorf("restore gateway: %w", err)})
			continue
		}
		r.state.Store(&next)
		state = &next
		r.wg.Add(1)
		go r.readTunnel(network.ID, replacement)
		r.options.Logger.Info("TUN recovered", "network", network.ID, "interface", device.Name())
	}
}
