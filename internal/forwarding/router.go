// Package forwarding routes validated IP packets between TUN devices and peers.
package forwarding

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"sync/atomic"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/packet"
	"github.com/eWloYW8/GraphWAN/internal/packetbuf"
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

type BatchOptions struct {
	Send    func(context.Context, model.ID, model.ID, [][]byte) error
	Deliver func(context.Context, model.ID, [][]byte) error
	// SendOwned takes ownership of every buffer even on error; it must not
	// retain the caller's slice. It takes precedence over Send.
	SendOwned func(context.Context, model.ID, model.ID, []*packetbuf.Buffer) error
}

type node struct {
	id      model.ID
	address netip.Addr
	wire    [16]byte
}

type network struct {
	id        model.ID
	header    [packet.HeaderSize]byte
	self      model.Node
	mtu       int
	byAddress map[netip.Addr]*node
	byNode    map[[16]byte]*node
	peers     map[model.ID]bool
	routes    map[model.ID]model.ID
}
type table struct {
	agentID  model.ID
	revision uint64
	networks map[model.ID]*network
	byWire   map[[16]byte]*network
}

// Router swaps immutable forwarding tables atomically. Send chooses the active
// Link of the requested Edge; that choice must never alter the graph edge weight.
// Callbacks must not retain packet buffers unless they copy them.
type Router struct {
	state          atomic.Pointer[table]
	send           Send
	deliver        Deliver
	deliverBatch   func(context.Context, model.ID, [][]byte) error
	sendBatch      func(context.Context, model.ID, model.ID, [][]byte) error
	sendOwnedBatch func(context.Context, model.ID, model.ID, []*packetbuf.Buffer) error
}

func New(snapshot model.Snapshot, send Send, deliver Deliver, batches ...BatchOptions) (*Router, error) {
	if send == nil || deliver == nil {
		return nil, errors.New("forwarding callbacks are required")
	}
	router := &Router{send: send, deliver: deliver}
	if len(batches) > 0 {
		router.deliverBatch = batches[0].Deliver
		router.sendBatch = batches[0].Send
		router.sendOwnedBatch = batches[0].SendOwned
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
	next := &table{agentID: snapshot.AgentID, revision: snapshot.Revision, networks: map[model.ID]*network{}, byWire: map[[16]byte]*network{}}
	for _, config := range snapshot.Networks {
		n := &network{id: config.ID, self: config.Self, mtu: config.MTU, byAddress: map[netip.Addr]*node{}, byNode: map[[16]byte]*node{}, peers: map[model.ID]bool{}, routes: map[model.ID]model.ID{}}
		// Snapshot validation above is the sole place where IDs need checking.
		header, err := (packet.Packet{Header: packet.Header{Network: config.ID, Source: config.Self.ID, Destination: config.Self.ID, HopLimit: packet.DefaultHopLimit, Epoch: snapshot.Revision}, Payload: []byte{0}}).MarshalBinary()
		if err != nil {
			return err
		}
		copy(n.header[:], header)
		next.byWire[[16]byte(header[8:24])] = n
		for _, dest := range config.Directory {
			entry := &node{id: dest.NodeID, address: dest.Address}
			if _, err := hex.Decode(entry.wire[:], []byte(dest.NodeID)); err != nil {
				return err
			}
			n.byAddress[dest.Address] = entry
			n.byNode[entry.wire] = entry
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
	n := r.state.Load().networks[networkID]
	if n == nil {
		return ErrNetwork
	}
	buffer, nextHop, err := r.encapsulate(ctx, n, raw)
	if err != nil || buffer == nil {
		return err
	}
	defer buffer.Release()
	return r.send(ctx, networkID, nextHop, buffer.Data)
}

// FromTunnelBatch preserves immediately available TUN bursts across routing and
// enqueue. Only consecutive frames for the same next hop are grouped. Invalid
// packets are dropped independently, and callbacks borrow storage until return.
func (r *Router) FromTunnelBatch(ctx context.Context, networkID model.ID, packets [][]byte) error {
	n := r.state.Load().networks[networkID]
	if n == nil {
		return ErrNetwork
	}
	var owners [packetbuf.BatchSize]*packetbuf.Buffer
	var frames [packetbuf.BatchSize][]byte
	var nextHop model.ID
	count := 0
	var result error
	flush := func() {
		if count == 0 {
			return
		}
		if r.sendOwnedBatch != nil {
			result = errors.Join(result, r.sendOwnedBatch(ctx, networkID, nextHop, owners[:count]))
		} else if r.sendBatch != nil {
			result = errors.Join(result, r.sendBatch(ctx, networkID, nextHop, frames[:count]))
		} else {
			for _, frame := range frames[:count] {
				result = errors.Join(result, r.send(ctx, networkID, nextHop, frame))
			}
		}
		if r.sendOwnedBatch == nil {
			packetbuf.ReleaseAll(owners[:count])
		}
		clear(owners[:count])
		clear(frames[:count])
		count = 0
	}
	for _, raw := range packets {
		buffer, hop, err := r.encapsulate(ctx, n, raw)
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if buffer == nil {
			continue
		}
		if count == len(frames) || hop != nextHop {
			flush()
		}
		nextHop = hop
		owners[count], frames[count] = buffer, buffer.Data
		count++
	}
	flush()
	return result
}

func (r *Router) encapsulate(ctx context.Context, n *network, raw []byte) (*packetbuf.Buffer, model.ID, error) {
	if len(raw) > n.mtu {
		return nil, "", ErrMTU
	}
	info, err := packet.InspectIP(raw)
	if err != nil {
		return nil, "", err
	}
	if info.Source != n.self.Address {
		return nil, "", ErrSource
	}
	destination, ok := n.byAddress[info.Destination]
	if !ok {
		return nil, "", ErrDestination
	}
	if destination.id == n.self.ID {
		return nil, "", r.deliver(ctx, n.id, raw)
	}
	nextHop, ok := n.routes[destination.id]
	if !ok {
		return nil, "", ErrUnreachable
	}
	buffer := packetbuf.GetHeadroom(packet.HeaderSize+len(raw), 1)
	frame := buffer.Data
	copy(frame, n.header[:])
	binary.BigEndian.PutUint16(frame[4:6], uint16(len(raw)))
	copy(frame[40:56], destination.wire[:])
	binary.BigEndian.PutUint64(frame[64:72], info.Flow)
	copy(frame[packet.HeaderSize:], raw)
	return buffer, nextHop, nil
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
	var packets [packetbuf.BatchSize][]byte
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
	p, err := packet.ParseView(frame)
	if err != nil {
		return err
	}
	current := r.state.Load()
	n := current.byWire[p.Network]
	if n == nil {
		return ErrNetwork
	}
	if !n.peers[peerID] {
		return ErrPeer
	}
	if len(p.Payload) > n.mtu {
		return ErrMTU
	}
	source, ok := n.byNode[p.Source]
	if !ok || source.id == n.self.ID {
		return ErrSource
	}
	destination, ok := n.byNode[p.Destination]
	if !ok {
		return ErrDestination
	}
	info, err := packet.InspectAddresses(p.Payload)
	if err != nil {
		return err
	}
	if info.Source != source.address {
		return ErrSource
	}
	if info.Destination != destination.address {
		return ErrDestination
	}
	if destination.id == n.self.ID {
		return deliver(ctx, n.id, p.Payload)
	}
	if p.HopLimit <= 1 {
		return packet.ErrHopLimit
	}
	nextHop, ok := n.routes[destination.id]
	if !ok {
		return ErrUnreachable
	}
	if nextHop == peerID {
		return fmt.Errorf("route would return packet to ingress neighbor")
	}
	// Keep the caller's authenticated frame immutable. Transit changes only
	// the hop limit; copying its canonical wire header avoids decoding/reencoding.
	buffer := packetbuf.Get(len(frame))
	defer buffer.Release()
	copy(buffer.Data, frame)
	buffer.Data[3]--
	return r.send(ctx, n.id, nextHop, buffer.Data)
}
