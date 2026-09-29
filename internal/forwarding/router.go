// Package forwarding routes validated IP packets between TUN devices and peers.
package forwarding

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync/atomic"

	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/packet"
	"github.com/graphwan/graphwan/internal/packetbuf"
)

var (
	ErrNetwork     = errors.New("unknown virtual network")
	ErrSource      = errors.New("packet source is not admitted")
	ErrDestination = errors.New("packet destination is not admitted")
	ErrPeer        = errors.New("packet arrived from an unconfigured neighbor")
	ErrUnreachable = errors.New("no route to destination")
	ErrMTU         = errors.New("packet exceeds network MTU")
)

type Send func(context.Context, model.ID, model.ID, []byte) error
type Deliver func(context.Context, model.ID, []byte) error

type network struct {
	self      model.Node
	mtu       int
	byAddress map[netip.Addr]model.ID
	byNode    map[model.ID]netip.Addr
	peers     map[model.ID]bool
	routes    map[model.ID]model.ID
}
type table struct {
	agentID  model.ID
	revision uint64
	networks map[model.ID]*network
}

// Router swaps immutable forwarding tables atomically. Send chooses the active
// Link of the requested Edge; that choice must never alter the graph edge weight.
// Callbacks must not retain packet buffers unless they copy them.
type Router struct {
	state        atomic.Pointer[table]
	send         Send
	deliver      Deliver
	deliverBatch func(context.Context, model.ID, [][]byte) error
}

func New(snapshot model.Snapshot, send Send, deliver Deliver, batches ...func(context.Context, model.ID, [][]byte) error) (*Router, error) {
	if send == nil || deliver == nil {
		return nil, errors.New("forwarding callbacks are required")
	}
	router := &Router{send: send, deliver: deliver}
	if len(batches) > 0 {
		router.deliverBatch = batches[0]
	}
	if err := router.Configure(snapshot); err != nil {
		return nil, err
	}
	return router, nil
}
func (r *Router) Configure(snapshot model.Snapshot) error {
	if err := snapshot.Validate(snapshot.AgentID); err != nil {
		return err
	}
	previous := r.state.Load()
	if previous != nil && (previous.agentID != snapshot.AgentID || snapshot.Revision < previous.revision) {
		return errors.New("forwarding table identity change or revision rollback")
	}
	next := &table{agentID: snapshot.AgentID, revision: snapshot.Revision, networks: map[model.ID]*network{}}
	for _, config := range snapshot.Networks {
		n := &network{self: config.Self, mtu: config.MTU, byAddress: map[netip.Addr]model.ID{}, byNode: map[model.ID]netip.Addr{}, peers: map[model.ID]bool{}, routes: map[model.ID]model.ID{}}
		for _, dest := range config.Directory {
			n.byAddress[dest.Address] = dest.NodeID
			n.byNode[dest.NodeID] = dest.Address
		}
		for _, peer := range config.Peers {
			n.peers[peer.Node.ID] = true
		}
		for _, route := range config.Routes {
			n.routes[route.Destination] = route.NextHop
		}
		next.networks[config.ID] = n
	}
	// Configuration application has one owner. CAS detects accidental competing
	// reconcilers instead of silently publishing an older table after a newer one.
	if !r.state.CompareAndSwap(previous, next) {
		return errors.New("concurrent forwarding table update")
	}
	return nil
}

func (r *Router) FromTunnel(ctx context.Context, networkID model.ID, raw []byte) error {
	current := r.state.Load()
	n := current.networks[networkID]
	if n == nil {
		return ErrNetwork
	}
	if len(raw) > n.mtu {
		return ErrMTU
	}
	info, err := packet.InspectIP(raw)
	if err != nil {
		return err
	}
	if info.Source != n.self.Address {
		return ErrSource
	}
	destination, ok := n.byAddress[info.Destination]
	if !ok {
		return ErrDestination
	}
	if destination == n.self.ID {
		return r.deliver(ctx, networkID, raw)
	}
	nextHop, ok := n.routes[destination]
	if !ok {
		return ErrUnreachable
	}
	p := packet.Packet{Header: packet.Header{Network: networkID, Source: n.self.ID, Destination: destination, HopLimit: packet.DefaultHopLimit, Epoch: current.revision, Flow: info.Flow}, Payload: raw}
	return r.sendPacket(ctx, networkID, nextHop, p)
}

// FromPeer must receive the Node ID authenticated by the peer Channel, never an
// unauthenticated ID read from the packet itself. Transit Nodes are trusted hop
// forwarders; the IP/Node directory check is admission, not end-to-end signing.
func (r *Router) FromPeer(ctx context.Context, peerID model.ID, frame []byte) error {
	return r.fromPeer(ctx, peerID, frame, r.deliver)
}

// FromPeerBatch validates every frame independently. Only admitted local
// packets are batched; transit retains the normal hop-limit and routing checks.
func (r *Router) FromPeerBatch(ctx context.Context, peerID model.ID, frames [][]byte) error {
	var networkID model.ID
	var packets [32][]byte
	count := 0
	var result error
	flush := func() {
		if count == 0 {
			return
		}
		if r.deliverBatch != nil {
			result = errors.Join(result, r.deliverBatch(ctx, networkID, packets[:count]))
		} else {
			for _, raw := range packets[:count] {
				result = errors.Join(result, r.deliver(ctx, networkID, raw))
			}
		}
		clear(packets[:count])
		count = 0
	}
	deliver := func(_ context.Context, id model.ID, raw []byte) error {
		if id != networkID || count == len(packets) {
			flush()
		}
		networkID = id
		packets[count] = raw
		count++
		return nil
	}
	for _, frame := range frames {
		result = errors.Join(result, r.fromPeer(ctx, peerID, frame, deliver))
	}
	flush()
	return result
}
func (r *Router) fromPeer(ctx context.Context, peerID model.ID, frame []byte, deliver Deliver) error {
	p, err := packet.Parse(frame)
	if err != nil {
		return err
	}
	current := r.state.Load()
	n := current.networks[p.Header.Network]
	if n == nil {
		return ErrNetwork
	}
	if !n.peers[peerID] {
		return ErrPeer
	}
	if len(p.Payload) > n.mtu {
		return ErrMTU
	}
	source, ok := n.byNode[p.Header.Source]
	if !ok || p.Header.Source == n.self.ID {
		return ErrSource
	}
	destination, ok := n.byNode[p.Header.Destination]
	if !ok {
		return ErrDestination
	}
	info, err := packet.InspectIP(p.Payload)
	if err != nil {
		return err
	}
	if info.Source != source {
		return ErrSource
	}
	if info.Destination != destination {
		return ErrDestination
	}
	if p.Header.Destination == n.self.ID {
		return deliver(ctx, p.Header.Network, p.Payload)
	}
	if err := p.Forward(); err != nil {
		return err
	}
	nextHop, ok := n.routes[p.Header.Destination]
	if !ok {
		return ErrUnreachable
	}
	if nextHop == peerID {
		return fmt.Errorf("route would return packet to ingress neighbor")
	}
	return r.sendPacket(ctx, p.Header.Network, nextHop, p)
}

func (r *Router) sendPacket(ctx context.Context, network, next model.ID, p packet.Packet) error {
	buffer := packetbuf.Get(packet.HeaderSize + len(p.Payload))
	defer buffer.Release()
	frame, err := p.AppendBinary(buffer.Data[:0])
	if err != nil {
		return err
	}
	return r.send(ctx, network, next, frame)
}
