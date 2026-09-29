package transport

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"net/netip"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

const (
	udpHeaderSize = 20
	maxUDPPeers   = 512
	udpQueueSize  = 64
)

var udpMagic = [4]byte{'G', 'W', 'D', 1}

type udpKey struct {
	remote netip.AddrPort
	token  [16]byte
}

// UDP multiplexes independent message connections over one data socket. The
// same socket is used for STUN and hole punching; a NAT mapping must
// never be discovered on a different socket than the one carrying peer traffic.
type UDP struct {
	socket      *net.UDPConn
	mu          sync.Mutex
	writeMu     sync.Mutex
	peers       map[udpKey]*Datagram
	accept      chan *Datagram
	done        chan struct{}
	closeOnce   sync.Once
	wg          sync.WaitGroup
	closeQUIC   func() error
	stunMu      sync.Mutex
	stunPending map[[12]byte]*stunTransaction
}

type Datagram struct {
	replyControl  []byte
	hub           *UDP
	key           udpKey
	incoming      chan []byte
	done          chan struct{}
	closeOnce     sync.Once
	lastSeen      atomic.Int64
	authenticated atomic.Bool
}

func ListenUDP(address string) (*UDP, error) {
	addr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, err
	}
	return NewUDP(conn)
}

// NewUDP takes ownership of socket, including cleanup if setup fails.
func NewUDP(socket *net.UDPConn) (*UDP, error) { return newUDP(socket, true) }

func newUDP(socket *net.UDPConn, read bool) (*UDP, error) {
	// BSD send-buffer defaults can be smaller than one permitted GraphWAN
	// message (FreeBSD commonly defaults to 9216 bytes). Reserve enough space
	// per owned socket; do not require changing the host's global UDP sysctls.
	switch runtime.GOOS {
	case "darwin", "dragonfly", "freebsd", "netbsd", "openbsd":
		if err := socket.SetWriteBuffer(64 << 10); err != nil {
			socket.Close()
			return nil, err
		}
	}
	if err := enableUDPPacketInfo(socket); err != nil {
		socket.Close()
		return nil, err
	}
	hub := &UDP{socket: socket, peers: map[udpKey]*Datagram{}, accept: make(chan *Datagram, 64), done: make(chan struct{})}
	hub.wg.Add(1)
	if read {
		hub.wg.Add(1)
		go hub.readLoop()
	}
	go hub.expireLoop()
	return hub, nil
}
func (h *UDP) LocalAddr() net.Addr { return h.socket.LocalAddr() }
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
		h.socket.Close()
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, peer := range h.peers {
			peer.closeOnce.Do(func() { close(peer.done) })
		}
		clear(h.peers)
	})
}
func (h *UDP) newPeer(key udpKey, reply []byte) *Datagram {
	peer := &Datagram{replyControl: reply, hub: h, key: key, incoming: make(chan []byte, udpQueueSize), done: make(chan struct{})}
	peer.lastSeen.Store(time.Now().UnixNano())
	h.peers[key] = peer
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
	if len(h.peers) >= maxUDPPeers {
		return nil, errors.New("UDP peer limit reached")
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
func (h *UDP) readLoop() {
	defer h.wg.Done()
	defer h.stop()
	buffer := make([]byte, MaxMessage+udpHeaderSize+1)
	oob := make([]byte, 256)
	for {
		n, control, flags, remote, err := h.socket.ReadMsgUDPAddrPort(buffer, oob)
		if udpReadTruncated(flags, err) {
			continue
		}
		if err != nil {
			return
		}
		h.receivePacket(buffer[:n], remote, udpReplyControl(oob[:control], remote))
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
		if len(h.peers) >= maxUDPPeers {
			h.mu.Unlock()
			return
		}
		peer = h.newPeer(key, reply)
		select {
		case h.accept <- peer:
		default:
			delete(h.peers, key)
			peer.closeOnce.Do(func() { close(peer.done) })
			h.mu.Unlock()
			return
		}
	}
	h.mu.Unlock()
	payload := append([]byte{}, raw[udpHeaderSize:]...)
	select {
	case <-peer.done:
	case peer.incoming <- payload:
	default:
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
func (d *Datagram) LocalAddr() net.Addr  { return d.hub.LocalAddr() }
func (d *Datagram) RemoteAddr() net.Addr { return net.UDPAddrFromAddrPort(d.key.remote) }

// Authenticated is called only after validating a peer handshake. Validated
// application packets call Alive; unauthenticated traffic cannot extend lifetime.
func (d *Datagram) Authenticated() { d.authenticated.Store(true); d.Alive() }
func (d *Datagram) Alive()         { d.lastSeen.Store(time.Now().UnixNano()) }
func (d *Datagram) Close() error {
	d.hub.mu.Lock()
	defer d.hub.mu.Unlock()
	d.closeOnce.Do(func() {
		close(d.done)
		if d.hub.peers[d.key] == d {
			delete(d.hub.peers, d.key)
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
	cleanup, err := deadline(ctx, h.socket.SetWriteDeadline)
	if err != nil {
		return err
	}
	defer cleanup()
	n, _, err := h.socket.WriteMsgUDPAddrPort(raw, control, remote)
	if err != nil {
		return ctxError(ctx, err)
	}
	if n != len(raw) {
		return errors.New("partial UDP datagram")
	}
	return nil
}
func (d *Datagram) Receive(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-d.done:
		return nil, net.ErrClosed
	case payload := <-d.incoming:
		return payload, nil
	}
}

func (d *Datagram) Unreliable() bool { return true }
