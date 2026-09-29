//go:build linux

package transport

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"

	"golang.org/x/net/ipv6"
)

type udpBatchSocket struct{ conn *ipv6.PacketConn }

func newUDPBatchSocket(socket *net.UDPConn) *udpBatchSocket {
	return &udpBatchSocket{ipv6.NewPacketConn(socket)}
}

type udpBatchSend struct {
	messages [128]ipv6.Message
	buffers  [128][2][]byte
	header   [udpHeaderSize]byte
	remote   *net.UDPAddr
}

// SendBatch uses sendmmsg with scatter/gather buffers; each encrypted message
// remains a separate datagram with the original token and source-interface info.
func (d *Datagram) SendBatch(ctx context.Context, payloads [][]byte) error {
	if len(payloads) == 0 || len(payloads) > 128 {
		return errors.New("invalid UDP batch size")
	}
	for _, raw := range payloads {
		if len(raw) == 0 || len(raw) > MaxMessage {
			return errors.New("invalid UDP message size")
		}
	}
	select {
	case <-d.done:
		return net.ErrClosed
	default:
	}
	h := d.hub
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	if d.batchSend == nil {
		d.batchSend = &udpBatchSend{remote: net.UDPAddrFromAddrPort(d.key.remote)}
		copy(d.batchSend.header[:4], udpMagic[:])
		copy(d.batchSend.header[4:], d.key.token[:])
	}
	b := d.batchSend
	for i, raw := range payloads {
		b.buffers[i] = [2][]byte{b.header[:], raw}
		b.messages[i] = ipv6.Message{Buffers: b.buffers[i][:], Addr: b.remote, OOB: d.replyControl}
	}
	defer func() {
		for i := range payloads {
			b.buffers[i][1] = nil
		}
	}()
	socket := h.socketFor(d.key.remote.Addr())
	cleanup, err := deadline(ctx, socket.SetWriteDeadline)
	if err != nil {
		return err
	}
	defer cleanup()
	messages := b.messages[:len(payloads)]
	for len(messages) > 0 {
		n, err := h.batchSockets[socket].conn.WriteBatch(messages, 0)
		if err != nil {
			return ctxError(ctx, err)
		}
		if n <= 0 {
			return io.ErrNoProgress
		}
		messages = messages[n:]
	}
	return nil
}

type udpPacketReader struct {
	conn        *ipv6.PacketConn
	messages    [32]ipv6.Message
	buffers     [32][]byte
	next, count int
}

func newUDPPacketReader(socket *net.UDPConn) *udpPacketReader {
	r := &udpPacketReader{conn: ipv6.NewPacketConn(socket)}
	for i := range r.messages {
		r.buffers[i] = make([]byte, MaxMessage+udpHeaderSize+1)
		r.messages[i].Buffers = r.buffers[i : i+1]
		r.messages[i].OOB = make([]byte, 256)
	}
	return r
}
func (r *udpPacketReader) read() ([]byte, []byte, int, netip.AddrPort, error) {
	if r.next == r.count {
		n, err := r.conn.ReadBatch(r.messages[:], 0)
		if err != nil {
			return nil, nil, 0, netip.AddrPort{}, err
		}
		if n == 0 {
			return nil, nil, 0, netip.AddrPort{}, io.ErrNoProgress
		}
		r.next, r.count = 0, n
	}
	m := &r.messages[r.next]
	r.next++
	// Linux can report the original length for a truncated datagram. Bound the
	// slice before passing its flags to the existing truncation policy.
	return m.Buffers[0][:min(m.N, len(m.Buffers[0]))], m.OOB[:m.NN], m.Flags, m.Addr.(*net.UDPAddr).AddrPort(), nil
}
