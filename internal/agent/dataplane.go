package agent

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/graphwan/graphwan/internal/discovery"
	"github.com/graphwan/graphwan/internal/forwarding"
	"github.com/graphwan/graphwan/internal/mesh"
	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/packet"
	"github.com/graphwan/graphwan/internal/tunnel"
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
	devices  map[model.ID]tunnel.Device
}

type DataPlane struct {
	identity ed25519.PrivateKey
	options  DataPlaneOptions
	ctx      context.Context
	cancel   context.CancelFunc
	applyMu  sync.Mutex
	state    atomic.Pointer[runtimeState]
	wg       sync.WaitGroup
	closed   bool
}

func NewDataPlane(parent context.Context, identity ed25519.PrivateKey, options DataPlaneOptions) (*DataPlane, error) {
	if len(identity) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid agent identity")
	}
	if options.TunnelFactory == nil {
		options.TunnelFactory = tunnel.Open
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(parent)
	runtime := &DataPlane{identity: append(ed25519.PrivateKey{}, identity...), options: options, ctx: ctx, cancel: cancel}
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
	next := &runtimeState{snapshot: snapshot.Clone(), devices: map[model.ID]tunnel.Device{}}
	prepared := []tunnel.Device{}
	committed := false
	newMesh := false
	defer func() {
		if !committed {
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
			if old := previous.devices[network.ID]; old != nil && old.Configuration().Address == cfg.Address && old.Configuration().MTU == cfg.MTU {
				next.devices[network.ID] = old
				continue
			}
		}
		device, err := r.options.TunnelFactory(cfg)
		if err != nil {
			return fmt.Errorf("network %s: %w", network.Name, err)
		}
		prepared = append(prepared, device)
		next.devices[network.ID] = device
	}
	if previous != nil && previous.snapshot.ListenPort == snapshot.ListenPort {
		next.mesh = previous.mesh
	} else {
		var err error
		next.mesh, err = mesh.New(r.ctx, r.identity, r.options.BindHost, snapshot.ListenPort, r.receive)
		if err != nil {
			return fmt.Errorf("listen for peers: %w", err)
		}
		newMesh = true
	}
	router, err := forwarding.New(snapshot, next.mesh.Send, r.deliver)
	if err != nil {
		return err
	}
	next.router = router
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := next.mesh.Apply(snapshot); err != nil {
		return err
	}
	r.state.Store(next)
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
	r.applyMu.Unlock()
	if state != nil {
		for _, device := range state.devices {
			device.Close()
		}
		state.mesh.Close()
	}
	r.wg.Wait()
	return nil
}
func (r *DataPlane) readTunnel(network model.ID, device tunnel.Device) {
	defer r.wg.Done()
	buffer := make([]byte, packet.MaxPayload+1)
	for {
		n, err := device.Read(buffer)
		if err != nil {
			if r.ctx.Err() == nil {
				state := r.state.Load()
				if state != nil && state.devices[network] == device {
					r.options.Logger.Error("TUN read stopped", "network", network, "error", err)
				}
			}
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
	n, err := device.Write(raw)
	if err == nil && n != len(raw) {
		return io.ErrShortWrite
	}
	return err
}
func (r *DataPlane) discover() {
	defer r.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
		}
		state := r.state.Load()
		if state == nil {
			continue
		}
		endpoints, err := discovery.Interfaces(r.identity.Public().(ed25519.PublicKey), state.snapshot.ListenPort)
		if err == nil {
			err = r.options.Endpoints(endpoints)
		}
		if err != nil {
			r.options.Logger.Warn("endpoint discovery failed", "error", err)
		}
	}
}
