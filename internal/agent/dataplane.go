package agent

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"

	"github.com/eWloYW8/GraphWAN/internal/forwarding"
	"github.com/eWloYW8/GraphWAN/internal/gateway"
	"github.com/eWloYW8/GraphWAN/internal/mesh"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/packet"
	"github.com/eWloYW8/GraphWAN/internal/tunnel"
)

type DataPlaneOptions struct {
	TunnelFactory tunnel.Factory
	BindHost      string
	Logger        *slog.Logger
	Endpoints     func([]model.Endpoint) error
}
type runtimeState struct {
	snapshot model.Snapshot
	router   *forwarding.Router
	mesh     *mesh.Mesh
	devices  map[model.ID]*runtimeTunnel
}

type DataPlane struct {
	gateway       gateway.Manager
	identity      ed25519.PrivateKey
	options       DataPlaneOptions
	ctx           context.Context
	cancel        context.CancelFunc
	applyMu       sync.Mutex
	state         atomic.Pointer[runtimeState]
	wg            sync.WaitGroup
	closed        bool
	lastRoutes    *model.RouteUpdate
	discoveryWake chan struct{}
	repairWake    chan struct{}
}

func NewDataPlane(parent context.Context, identity ed25519.PrivateKey, options DataPlaneOptions) (*DataPlane, error) {
	if len(identity) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid agent identity")
	}
	if options.TunnelFactory == nil {
		if err := tunnel.Recover(); err != nil {
			return nil, fmt.Errorf("recover owned TUN devices: %w", err)
		}
		options.TunnelFactory = tunnel.Open
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(parent)
	runtime := &DataPlane{identity: append(ed25519.PrivateKey{}, identity...), options: options, ctx: ctx, cancel: cancel, discoveryWake: make(chan struct{}, 1), repairWake: make(chan struct{}, 1)}
	runtime.wg.Add(1)
	go runtime.repairLoop()
	if options.Endpoints != nil {
		runtime.wg.Add(1)
		go runtime.discover()
	}
	return runtime, nil
}
func (r *DataPlane) Apply(ctx context.Context, snapshot model.Snapshot) error {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	if r.closed {
		return errors.New("agent runtime is closed")
	}
	if err := snapshot.Validate(snapshot.AgentID); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	previous := r.state.Load()
	if previous != nil && (previous.snapshot.AgentID != snapshot.AgentID || previous.snapshot.Revision > snapshot.Revision) {
		return errors.New("runtime identity change or revision rollback")
	}
	next := &runtimeState{snapshot: snapshot.Clone(), devices: map[model.ID]*runtimeTunnel{}}
	prepared := []tunnel.Device{}
	type tunnelUpdate struct {
		network       model.ID
		device        *runtimeTunnel
		apply         func(tunnel.Config) error
		before, after tunnel.Config
	}
	var tunnelUpdates []tunnelUpdate
	updatedTunnels := 0
	committed := false
	newMesh := false
	gatewayChanged := false
	defer func() {
		if !committed {
			if gatewayChanged {
				if err := r.gateway.Apply(context.Background(), snapshot.AgentID, gatewayEntries(previous)); err != nil {
					r.options.Logger.Error("gateway rollback failed", "error", err)
				}
			}
			for i := updatedTunnels - 1; i >= 0; i-- {
				update := tunnelUpdates[i]
				if err := update.apply(update.before); err != nil {
					r.failTunnel(update.network, update.device, fmt.Errorf("rollback TUN configuration: %w", err))
				}
			}
			for _, device := range prepared {
				device.Close()
			}
			if newMesh && next.mesh != nil {
				next.mesh.Close()
			}
		}
	}()
	for _, network := range snapshot.Networks {
		cfg := tunnel.Config{Address: netip.PrefixFrom(network.Self.Address, network.CIDR.Bits()), MTU: network.MTU}
		if previous != nil {
			if old := previous.devices[network.ID]; old != nil && old.failure.Load() == nil {
				before := old.Configuration()
				if before.Address == cfg.Address && before.MTU == cfg.MTU {
					next.devices[network.ID] = old
					continue
				}
				var apply func(tunnel.Config) error
				if updater, ok := old.Device.(tunnel.Reconfigurable); ok {
					apply = updater.Reconfigure
				} else if setter, ok := old.Device.(tunnel.MTUSetter); ok && before.Address == cfg.Address {
					apply = func(c tunnel.Config) error { return setter.SetMTU(c.MTU) }
				}
				if apply != nil {
					cfg.Name = before.Name
					tunnelUpdates = append(tunnelUpdates, tunnelUpdate{network.ID, old, apply, before, cfg})
					next.devices[network.ID] = old
					continue
				}
			}
		}
		device, err := r.options.TunnelFactory(cfg)
		if err != nil {
			return fmt.Errorf("network %s: %w", network.Name, err)
		}
		prepared = append(prepared, device)
		next.devices[network.ID] = newRuntimeTunnel(device)
	}
	if previous != nil && previous.snapshot.ListenPort == snapshot.ListenPort {
		next.mesh = previous.mesh
	} else {
		var err error
		next.mesh, err = mesh.New(r.ctx, r.identity, r.options.BindHost, snapshot.ListenPort, r.receive, r.receiveBatch)
		if err != nil {
			return fmt.Errorf("listen for peers: %w", err)
		}
		newMesh = true
	}
	forwardingSnapshot := carryLiveRoutes(snapshot, r.lastRoutes)
	router, err := forwarding.New(forwardingSnapshot, next.mesh.Send, r.deliver, forwarding.BatchOptions{Deliver: r.deliverBatch, SendOwned: next.mesh.SendOwnedBatch})
	if err != nil {
		return err
	}
	next.router = router
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, update := range tunnelUpdates {
		if err := update.apply(update.after); err != nil {
			if errors.Is(err, tunnel.ErrUnavailable) {
				r.failTunnel(update.network, update.device, err)
			}
			return fmt.Errorf("network %s: update TUN configuration: %w", update.network, err)
		}
		updatedTunnels++
	}
	if err := r.gateway.Apply(ctx, snapshot.AgentID, gatewayEntries(next)); err != nil {
		return fmt.Errorf("configure gateway: %w", err)
	}
	gatewayChanged = true
	if err := next.mesh.Apply(snapshot); err != nil {
		return err
	}
	r.state.Store(next)
	select {
	case r.discoveryWake <- struct{}{}:
	default:
	}
	committed = true
	r.options.Logger.Info("configuration applied", "revision", snapshot.Revision, "networks", len(snapshot.Networks), "listen_port", snapshot.ListenPort)
	for id, device := range next.devices {
		if previous == nil || previous.devices[id] != device {
			r.wg.Add(1)
			go r.readTunnel(id, device)
		}
	}
	if previous != nil {
		if previous.mesh != next.mesh {
			previous.mesh.Close()
		}
		for id, device := range previous.devices {
			if next.devices[id] != device {
				device.Close()
			}
		}
	}
	return nil
}
func (r *DataPlane) Report() []model.LinkStatus {
	state := r.state.Load()
	if state == nil {
		return nil
	}
	return state.mesh.Report()
}
func (r *DataPlane) Close() error {
	r.applyMu.Lock()
	if r.closed {
		r.applyMu.Unlock()
		return nil
	}
	r.closed = true
	r.cancel()
	state := r.state.Swap(nil)
	gatewayErr := r.gateway.Close()
	if gatewayErr != nil {
		r.options.Logger.Error("gateway cleanup failed", "error", gatewayErr)
	}
	r.applyMu.Unlock()
	if state != nil {
		for _, device := range state.devices {
			device.Close()
		}
		state.mesh.Close()
	}
	r.wg.Wait()
	return gatewayErr
}
func (r *DataPlane) readTunnel(network model.ID, device *runtimeTunnel) {
	defer r.wg.Done()
	if batch, ok := device.Device.(tunnel.BatchDevice); ok {
		r.readTunnelBatch(network, device, batch)
		return
	}
	buffer := make([]byte, packet.MaxPayload+1)
	for {
		n, err := device.Read(buffer)
		if err == nil && n == 0 {
			err = io.ErrNoProgress
		}
		if err != nil {
			r.failTunnel(network, device, err)
			return
		}
		state := r.state.Load()
		if state == nil || state.devices[network] != device {
			continue
		}
		// Invalid, unroutable or congested packets are dropped independently; a bad
		// packet cannot terminate the interface forwarding loop.
		state.router.FromTunnel(r.ctx, network, buffer[:n])
	}
}
func (r *DataPlane) receive(ctx context.Context, remote model.ID, frame []byte) error {
	state := r.state.Load()
	if state == nil {
		return errors.New("agent runtime is not configured")
	}
	return state.router.FromPeer(ctx, remote, frame)
}
func (r *DataPlane) deliver(ctx context.Context, network model.ID, raw []byte) error {
	state := r.state.Load()
	if state == nil {
		return errors.New("agent runtime is not configured")
	}
	device := state.devices[network]
	if device == nil {
		return forwarding.ErrNetwork
	}
	if device.failure.Load() != nil {
		return tunnel.ErrUnavailable
	}
	n, err := device.Write(raw)
	if errors.Is(err, tunnel.ErrUnavailable) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed) {
		r.failTunnel(network, device, err)
	}
	if err == nil && n != len(raw) {
		return io.ErrShortWrite
	}
	return err
}

func (r *DataPlane) readTunnelBatch(network model.ID, device *runtimeTunnel, batch tunnel.BatchDevice) {
	buffers := make([][]byte, batch.BatchSize())
	sizes := make([]int, len(buffers))
	packets := make([][]byte, len(buffers))
	for i := range buffers {
		buffers[i] = make([]byte, packet.MaxPayload+1)
	}
	for {
		n, err := batch.ReadBatch(buffers, sizes)
		if err == nil && n == 0 {
			err = io.ErrNoProgress
		}
		if err != nil {
			r.failTunnel(network, device, err)
			return
		}
		state := r.state.Load()
		if state == nil || state.devices[network] != device {
			continue
		}
		for i := range n {
			packets[i] = buffers[i][:sizes[i]]
		}
		state.router.FromTunnelBatch(r.ctx, network, packets[:n])
		clear(packets[:n])
	}
}
func (r *DataPlane) receiveBatch(ctx context.Context, remote model.ID, frames [][]byte) error {
	state := r.state.Load()
	if state == nil {
		return errors.New("agent runtime is not configured")
	}
	return state.router.FromPeerBatch(ctx, remote, frames)
}
func (r *DataPlane) deliverBatch(ctx context.Context, network model.ID, packets [][]byte) error {
	state := r.state.Load()
	if state == nil {
		return errors.New("agent runtime is not configured")
	}
	device := state.devices[network]
	if device == nil {
		return forwarding.ErrNetwork
	}
	if device.failure.Load() != nil {
		return tunnel.ErrUnavailable
	}
	batch, ok := device.Device.(tunnel.BatchDevice)
	if !ok {
		var result error
		for _, raw := range packets {
			result = errors.Join(result, r.deliver(ctx, network, raw))
		}
		return result
	}
	err := batch.WriteBatch(packets)
	if errors.Is(err, tunnel.ErrUnavailable) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed) {
		r.failTunnel(network, device, err)
	}
	return err
}
