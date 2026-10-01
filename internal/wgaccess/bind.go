// Package wgaccess embeds the standard WireGuard protocol at GraphWAN's edge.
package wgaccess

import (
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/packetbuf"
	"golang.zx2c4.com/wireguard/conn"
)

type endpoint struct {
	remote  netip.AddrPort
	control []byte
}

func (e *endpoint) ClearSrc()           { e.control = nil }
func (e *endpoint) SrcToString() string { return "" }
func (e *endpoint) DstToString() string { return e.remote.String() }
func (e *endpoint) DstToBytes() []byte  { b, _ := e.remote.MarshalBinary(); return b }
func (e *endpoint) DstIP() netip.Addr   { return e.remote.Addr() }
func (e *endpoint) SrcIP() netip.Addr   { return netip.Addr{} }

type datagram struct {
	buffer *packetbuf.Buffer
	ep     *endpoint
}
type bind struct {
	mu       sync.Mutex
	hub      *Hub
	owner    *network
	incoming chan datagram
	done     chan struct{}
}

func (b *bind) Open(uint16) ([]conn.ReceiveFunc, uint16, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done != nil {
		return nil, 0, conn.ErrBindAlreadyOpen
	}
	b.done = make(chan struct{})
	b.incoming = make(chan datagram, 256)
	done, incoming := b.done, b.incoming
	receive := func(bufs [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		select {
		case <-done:
			return 0, net.ErrClosed
		case p := <-incoming:
			sizes[0] = copy(bufs[0], p.buffer.Data)
			eps[0] = p.ep
			p.buffer.Release()
			return 1, nil
		}
	}
	return []conn.ReceiveFunc{receive}, b.hub.port, nil
}
func (b *bind) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done != nil {
		close(b.done)
		b.done = nil
		for {
			select {
			case p := <-b.incoming:
				p.buffer.Release()
			default:
				return nil
			}
		}
	}
	return nil
}
func (b *bind) enqueue(raw []byte, remote netip.AddrPort, control []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done == nil {
		return
	}
	p := datagram{packetbuf.Get(len(raw)), &endpoint{remote, append([]byte(nil), control...)}}
	copy(p.buffer.Data, raw)
	select {
	case b.incoming <- p:
	default:
		p.buffer.Release()
	}
}
func (b *bind) SetMark(uint32) error { return nil }
func (b *bind) BatchSize() int       { return 1 }
func (b *bind) ParseEndpoint(s string) (conn.Endpoint, error) {
	ip, err := netip.ParseAddrPort(s)
	if err != nil {
		return nil, err
	}
	return &endpoint{remote: ip}, nil
}
func (b *bind) Send(bufs [][]byte, ep conn.Endpoint) error {
	e, ok := ep.(*endpoint)
	if !ok {
		return conn.ErrWrongEndpointType
	}
	for _, raw := range bufs {
		if len(raw) >= 8 && (raw[0] == 1 || raw[0] == 2) {
			b.hub.recordIndex(binary.LittleEndian.Uint32(raw[4:8]), b.owner)
		}
		ctx, cancel := context.WithTimeout(b.hub.ctx, 2*time.Second)
		err := b.hub.send(ctx, raw, e.remote, e.control)
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}
