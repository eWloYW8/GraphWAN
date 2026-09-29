package transport

import (
	"context"
	"crypto/rand"
	"errors"
	"github.com/graphwan/graphwan/internal/packetbuf"
	"net"
	"net/netip"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

const (
	udpHeaderSize = 20
	maxPendingUDP = 512
	udpQueueSize  = 256
)

var udpMagic = [4]byte{'G', 'W', 'D', 1}

type udpKey struct {
	remote netip.AddrPort
	token  [16]byte
}

// UDP multiplexes independent message connections over one data socket per address
// family. The same socket is used for STUN and hole punching; a NAT mapping must
// never be discovered on a different socket than the one carrying peer traffic.
type UDP struct {
	sockets      []*net.UDPConn
	batchSockets map[*net.UDPConn]*udpBatchSocket
	mu           sync.Mutex
	writeMu      sync.Mutex
	peers        map[udpKey]*Datagram
	pending      int // Only peers that have not passed the configured Noise handshake.
	accept       chan *Datagram
	done         chan struct{}
	closeOnce    sync.Once
	wg           sync.WaitGroup
	closeQUIC    func() error
	stunMu       sync.Mutex
	stunPending  map[[12]byte]*stunTransaction
}

type Datagram struct {
	replyControl  []byte
	batchSend     *udpBatchSend
	hub           *UDP
	key           udpKey
	incoming      chan *packetbuf.Buffer
	done          chan struct{}
	closeOnce     sync.Once
	lastSeen      atomic.Int64
	authenticated atomic.Bool
}

func ListenUDP(address string) (*UDP, error) {
	sockets, err := listenUDPSockets(address)
	if err != nil {
		return nil, err
	}
	return newUDP(sockets, true)
}

// NewUDP takes ownership of socket, including cleanup if setup fails.
func NewUDP(socket *net.UDPConn) (*UDP, error) { return newUDP([]*net.UDPConn{socket}, true) }

func newUDP(sockets []*net.UDPConn, read bool) (*UDP, error) {
	for _, socket := range sockets {
		// BSD defaults may be smaller than one permitted GraphWAN message.
		var err error
		switch runtime.GOOS {
		case "darwin", "dragonfly", "freebsd", "netbsd", "openbsd":
			err = socket.SetWriteBuffer(64 << 10)
		}
		if err == nil {
			err = enableUDPPacketInfo(socket)
		}
		if err != nil {
			for _, owned := range sockets {
				owned.Close()
			}
			return nil, err
		}
	}
	hub := &UDP{sockets: sockets, peers: map[udpKey]*Datagram{}, accept: make(chan *Datagram, 64), done: make(chan struct{})}
	hub.batchSockets = make(map[*net.UDPConn]*udpBatchSocket, len(sockets))
	for _, socket := range sockets {
		hub.batchSockets[socket] = newUDPBatchSocket(socket)
	}
	hub.wg.Add(1)
	if read {
		hub.wg.Add(len(sockets))
		for _, socket := range sockets {
			go hub.readLoop(socket)
		}
	}
	go hub.expireLoop()
	return hub, nil
}
func (h *UDP) LocalAddr() net.Addr { return h.sockets[0].LocalAddr() }
func (h *UDP) Close() error {
	h.stop()
	if h.closeQUIC != nil {
		h.closeQUIC()
	}
	h.wg.Wait()
	return nil
}
func (h *UDP) stop() {
	h.closeOnce.Do(func() {
		close(h.done)
		for _, socket := range h.sockets {
			socket.Close()
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, peer := range h.peers {
			peer.closeOnce.Do(func() { close(peer.done); peer.drainIncoming() })
		}
		clear(h.peers)
		h.pending = 0
	})
}
func (h *UDP) newPeer(key udpKey, reply []byte) *Datagram {
	peer := &Datagram{replyControl: reply, hub: h, key: key, incoming: make(chan *packetbuf.Buffer, udpQueueSize), done: make(chan struct{})}
	peer.lastSeen.Store(time.Now().UnixNano())
	h.peers[key] = peer
	h.pending++
	return peer
}
func (h *UDP) Dial(remote netip.AddrPort) (*Datagram, error) {
	remote = netip.AddrPortFrom(remote.Addr().Unmap(), remote.Port())
	if !remote.IsValid() || remote.Port() == 0 || remote.Addr().IsUnspecified() || remote.Addr().IsMulticast() {
		return nil, errors.New("invalid UDP peer address")
	}
	key := udpKey{remote: remote}
	rand.Read(key.token[:])
	h.mu.Lock()
	defer h.mu.Unlock()
	select {
	case <-h.done:
		return nil, net.ErrClosed
	default:
	}
	if h.pending >= maxPendingUDP {
		return nil, errors.New("UDP pending peer limit reached")
	}
	return h.newPeer(key, nil), nil
}
func (h *UDP) Accept(ctx context.Context) (*Datagram, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-h.done:
			return nil, net.ErrClosed
		case peer := <-h.accept:
			select {
			case <-peer.done:
				continue
			default:
				return peer, nil
			}
		}
	}
}
func (h *UDP) readLoop(socket *net.UDPConn) {
	defer h.wg.Done()
	defer h.stop()
	reader := newUDPPacketReader(socket)
	for {
		raw, control, flags, remote, err := reader.read()
		if udpReadTruncated(flags, err) {
			continue
		}
		if err != nil {
			return
		}
		h.receivePacket(raw, remote, udpReplyControl(control, remote))
	}
}

func (h *UDP) receivePacket(raw []byte, remote netip.AddrPort, reply []byte) {
	if h.receiveSTUN(raw, remote) {
		return
	}
	n := len(raw)
	if n <= udpHeaderSize || n > MaxMessage+udpHeaderSize || [4]byte(raw[:4]) != udpMagic {
		return
	}
	key := udpKey{remote: netip.AddrPortFrom(remote.Addr().Unmap(), remote.Port()), token: [16]byte(raw[4:20])}
	if key.token == [16]byte{} {
		return
	}
	h.mu.Lock()
	select {
	case <-h.done:
		h.mu.Unlock()
		return
	default:
	}
	peer := h.peers[key]
	if peer == nil {
		if h.pending >= maxPendingUDP {
			h.mu.Unlock()
			return
		}
		peer = h.newPeer(key, reply)
		select {
		case h.accept <- peer:
		default:
			delete(h.peers, key)
			h.pending--
			peer.closeOnce.Do(func() { close(peer.done); peer.drainIncoming() })
			h.mu.Unlock()
			return
		}
	}
	// Enqueue and close/drain share hub.mu, so ownership cannot arrive after
	// a peer has drained its last queued packet.
	defer h.mu.Unlock()
	payload := packetbuf.Get(len(raw) - udpHeaderSize)
	copy(payload.Data, raw[udpHeaderSize:])
	select {
	case <-peer.done:
		payload.Release()
	case peer.incoming <- payload:
	default:
		payload.Release()
	}
}

func (h *UDP) expireLoop() {
	defer h.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-h.done:
			return
		case now := <-ticker.C:
			h.mu.Lock()
			expired := []*Datagram{}
			for _, peer := range h.peers {
				limit := 10 * time.Second
				if peer.authenticated.Load() {
					limit = 2 * time.Minute
				}
				if now.Sub(time.Unix(0, peer.lastSeen.Load())) > limit {
					expired = append(expired, peer)
				}
			}
			h.mu.Unlock()
			for _, peer := range expired {
				peer.Close()
			}
		}
	}
}
func (d *Datagram) LocalAddr() net.Addr  { return d.hub.socketFor(d.key.remote.Addr()).LocalAddr() }
func (d *Datagram) RemoteAddr() net.Addr { return net.UDPAddrFromAddrPort(d.key.remote) }

// Authenticated is called only after validating a peer handshake. Validated
// application packets call Alive; unauthenticated traffic cannot extend lifetime.
// Established configured Links must not exhaust pending handshake admission.
func (d *Datagram) Authenticated() {
	d.hub.mu.Lock()
	if d.hub.peers[d.key] == d && !d.authenticated.Swap(true) {
		d.hub.pending--
	}
	d.hub.mu.Unlock()
	d.Alive()
}
func (d *Datagram) Alive() { d.lastSeen.Store(time.Now().UnixNano()) }
func (d *Datagram) Close() error {
	d.hub.mu.Lock()
	defer d.hub.mu.Unlock()
	d.closeOnce.Do(func() {
		close(d.done)
		d.drainIncoming()
		if d.hub.peers[d.key] == d {
			delete(d.hub.peers, d.key)
			if !d.authenticated.Load() {
				d.hub.pending--
			}
		}
	})
	return nil
}
func (d *Datagram) Send(ctx context.Context, payload []byte) error {
	if len(payload) == 0 || len(payload) > MaxMessage {
		return errors.New("invalid UDP message size")
	}
	select {
	case <-d.done:
		return net.ErrClosed
	default:
	}
	raw := make([]byte, udpHeaderSize+len(payload))
	copy(raw, udpMagic[:])
	copy(raw[4:20], d.key.token[:])
	copy(raw[20:], payload)
	return d.hub.writeDatagramControl(ctx, raw, d.key.remote, d.replyControl)
}

func (h *UDP) writeDatagram(ctx context.Context, raw []byte, remote netip.AddrPort) error {
	return h.writeDatagramControl(ctx, raw, remote, nil)
}

func (h *UDP) writeDatagramControl(ctx context.Context, raw []byte, remote netip.AddrPort, control []byte) error {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	socket := h.socketFor(remote.Addr())
	cleanup, err := deadline(ctx, socket.SetWriteDeadline)
	if err != nil {
		return err
	}
	defer cleanup()
	n, _, err := socket.WriteMsgUDPAddrPort(raw, control, remote)
	if err != nil {
		return ctxError(ctx, err)
	}
	if n != len(raw) {
		return errors.New("partial UDP datagram")
	}
	return nil
}
func (d *Datagram) drainIncoming() {
	for {
		select {
		case b := <-d.incoming:
			b.Release()
		default:
			return
		}
	}
}
func (d *Datagram) receiveOwned(ctx context.Context) (*packetbuf.Buffer, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-d.done:
		return nil, net.ErrClosed
	case payload := <-d.incoming:
		return payload, nil
	}
}
func (d *Datagram) Receive(ctx context.Context) ([]byte, error) {
	b, err := d.receiveOwned(ctx)
	if err != nil {
		return nil, err
	}
	defer b.Release()
	return append([]byte(nil), b.Data...), nil
}
func (d *Datagram) ReceiveBatch(ctx context.Context) ([][]byte, error) {
	batch, err := d.ReceiveOwnedBatch(ctx)
	if err != nil {
		return nil, err
	}
	defer packetbuf.ReleaseAll(batch)
	out := make([][]byte, len(batch))
	for i, b := range batch {
		out[i] = append([]byte(nil), b.Data...)
	}
	return out, nil
}
func (d *Datagram) ReceiveOwnedBatch(ctx context.Context) ([]*packetbuf.Buffer, error) {
	first, err := d.receiveOwned(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*packetbuf.Buffer, 1, 32)
	out[0] = first
	for len(out) < cap(out) {
		select {
		case raw := <-d.incoming:
			out = append(out, raw)
		default:
			return out, nil
		}
	}
	return out, nil
}

func (d *Datagram) Unreliable() bool { return true }

// A single externally supplied socket may support both families. With separate
// wildcard sockets, select the family before sending native, STUN or QUIC data.
func (h *UDP) socketFor(remote netip.Addr) *net.UDPConn {
	for _, socket := range h.sockets {
		if socket.LocalAddr().(*net.UDPAddr).AddrPort().Addr().Unmap().Is4() == remote.Unmap().Is4() {
			return socket
		}
	}
	return h.sockets[0]
}
