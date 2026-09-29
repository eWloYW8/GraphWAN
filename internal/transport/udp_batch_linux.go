//go:build linux

package transport

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"unsafe"

	"golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"
)

const maxUDPSuperPacket = 65507 // Also safe for an IPv4 path.

type udpBatchWriter interface {
	WriteBatch([]ipv6.Message, int) (int, error)
}
type udpBatchSocket struct {
	conn udpBatchWriter
	gso  bool
}

func newUDPBatchSocket(socket *net.UDPConn) *udpBatchSocket {
	_ = setUDPReadBuffer(socket, 4<<20)
	b := &udpBatchSocket{conn: ipv6.NewPacketConn(socket)}
	if raw, err := socket.SyscallConn(); err == nil {
		_ = raw.Control(func(fd uintptr) {
			_, err := unix.GetsockoptInt(int(fd), unix.IPPROTO_UDP, unix.UDP_SEGMENT)
			b.gso = err == nil
		})
	}
	return b
}

// The shared QUIC wrapper deliberately hides SyscallConn. Preserve the receive
// buffer tuning quic-go normally performs on a raw UDP socket, without exposing
// a read path that could bypass native/STUN dispatch. CAP_NET_ADMIN is already
// used by native TUN agents; unprivileged sockets keep the ordinary capped size.
func setUDPReadBuffer(socket *net.UDPConn, size int) error {
	if err := socket.SetReadBuffer(size); err != nil {
		return err
	}
	if raw, err := socket.SyscallConn(); err == nil {
		_ = raw.Control(func(fd uintptr) {
			actual, err := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUF)
			// Linux reports twice the requested size for accounting overhead.
			if err == nil && actual/2 < size {
				_ = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUFFORCE, size)
			}
		})
	}
	return nil
}

type udpBatchSend struct {
	messages   [128]ipv6.Message
	buffers    [256][]byte
	controls   [128][128]byte
	starts     [128]int
	header     [udpHeaderSize]byte
	remote     *net.UDPAddr
	disableGSO bool // A route/device can reject GSO even when the kernel supports it.
}

// prepare preserves every original datagram, including its token. Scatter/gather
// avoids concatenating payloads in userspace. GSO allows only equal-size segments
// followed by at most one smaller final segment, at most 64 per super-packet.
func (b *udpBatchSend) prepare(payloads [][]byte, reply []byte, gso bool) []ipv6.Message {
	count := 0
	for start := 0; start < len(payloads); {
		end := start + 1
		size := udpHeaderSize + len(payloads[start])
		total := size
		if gso {
			for end < len(payloads) && end-start < 64 {
				next := udpHeaderSize + len(payloads[end])
				if next > size || total+next > maxUDPSuperPacket {
					break
				}
				total += next
				end++
				if next < size {
					break
				}
			}
		}
		for i := start; i < end; i++ {
			b.buffers[2*i], b.buffers[2*i+1] = b.header[:], payloads[i]
		}
		control := reply
		if end-start > 1 {
			control = append(b.controls[count][:0], reply...)
			control = appendUDPSegment(control, uint16(size))
		}
		b.starts[count] = start
		b.messages[count] = ipv6.Message{Buffers: b.buffers[2*start : 2*end], Addr: b.remote, OOB: control}
		count++
		start = end
	}
	return b.messages[:count]
}

func appendUDPSegment(control []byte, size uint16) []byte {
	offset := len(control)
	control = append(control, make([]byte, unix.CmsgSpace(2))...)
	header := (*unix.Cmsghdr)(unsafe.Pointer(&control[offset]))
	header.Level, header.Type = unix.SOL_UDP, unix.UDP_SEGMENT
	header.SetLen(unix.CmsgLen(2))
	binary.NativeEndian.PutUint16(control[offset+unix.CmsgLen(0):], size)
	return control
}
func udpGSOUnsupported(err error) bool {
	return errors.Is(err, unix.EIO) || errors.Is(err, unix.EINVAL) ||
		errors.Is(err, unix.ENOPROTOOPT) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EMSGSIZE)
}

// SendBatch uses sendmmsg and, when supported by the route, UDP segmentation.
// The receiver still sees the same independent datagrams and wire protocol.
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
	defer func() { clear(b.buffers[:]); clear(b.messages[:]) }()
	socket := h.socketFor(d.key.remote.Addr())
	cleanup, err := deadline(ctx, socket.SetWriteDeadline)
	if err != nil {
		return err
	}
	defer cleanup()
	batchSocket := h.batchSockets[socket]
	gso := batchSocket.gso && !b.disableGSO
	messages := b.prepare(payloads, d.replyControl, gso)
	sent := 0
	for sent < len(messages) {
		n, err := batchSocket.conn.WriteBatch(messages[sent:], 0)
		// x/net exposes sendmmsg's -1 result on syscall failure. It is not a
		// successfully submitted count and must not move the retry cursor back.
		if n > 0 {
			sent += n
		}
		if err != nil {
			if gso && sent < len(messages) && udpGSOUnsupported(err) {
				// Do not retransmit any successfully submitted prefix. Retrying only the
				// unsent messages also preserves legacy IP fragmentation for large MTUs.
				remaining := payloads[b.starts[sent]:]
				b.disableGSO, gso = true, false
				messages = b.prepare(remaining, d.replyControl, false)
				sent = 0
				continue
			}
			return ctxError(ctx, err)
		}
		if n <= 0 {
			return io.ErrNoProgress
		}
	}
	return nil
}

type udpPacketReader struct {
	conn        *ipv6.PacketConn
	messages    [32]ipv6.Message
	buffers     [32][]byte
	next, count int
	segment     []byte
	segmentSize int
	control     []byte
	remote      netip.AddrPort
}

func newUDPPacketReader(socket *net.UDPConn) *udpPacketReader {
	r := &udpPacketReader{conn: ipv6.NewPacketConn(socket)}
	size := MaxMessage + udpHeaderSize + 1
	if raw, err := socket.SyscallConn(); err == nil {
		_ = raw.Control(func(fd uintptr) {
			// Readback support excludes older kernels with incomplete UDP GRO support.
			if _, err := unix.GetsockoptInt(int(fd), unix.IPPROTO_UDP, unix.UDP_GRO); err == nil {
				if unix.SetsockoptInt(int(fd), unix.IPPROTO_UDP, unix.UDP_GRO, 1) == nil {
					size = 65535
				}
			}
		})
	}
	for i := range r.messages {
		r.buffers[i] = make([]byte, size)
		r.messages[i].Buffers = r.buffers[i : i+1]
		r.messages[i].OOB = make([]byte, 256)
	}
	return r
}

func udpGROSize(control []byte) (int, error) {
	for len(control) >= unix.SizeofCmsghdr {
		header, data, rest, err := unix.ParseOneSocketControlMessage(control)
		if err != nil {
			return 0, err
		}
		if header.Level == unix.SOL_UDP && header.Type == unix.UDP_GRO {
			// Unlike the uint16 UDP_SEGMENT send option, Linux publishes GRO
			// ancillary data as a native int (32 bits), including on big-endian
			// hosts. See include/linux/udp.h: udp_cmsg_recv.
			if len(data) < 4 {
				return 0, errors.New("short UDP GRO control message")
			}
			size := binary.NativeEndian.Uint32(data)
			if size == 0 || size > 65535 {
				return 0, errors.New("invalid UDP GRO segment size")
			}
			return int(size), nil
		}
		control = rest
	}
	return 0, nil
}
func (r *udpPacketReader) read() ([]byte, []byte, int, netip.AddrPort, error) {
	if len(r.segment) > 0 {
		packet := r.segment[:min(r.segmentSize, len(r.segment))]
		r.segment = r.segment[len(packet):]
		return packet, r.control, 0, r.remote, nil
	}
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
	raw := m.Buffers[0][:min(m.N, len(m.Buffers[0]))]
	control, remote := m.OOB[:m.NN], m.Addr.(*net.UDPAddr).AddrPort()
	// Never split a truncated super-packet or one with missing ancillary data.
	if udpReadTruncated(m.Flags, nil) {
		return raw, control, m.Flags, remote, nil
	}
	size, err := udpGROSize(control)
	if err != nil {
		return nil, nil, unix.MSG_CTRUNC, remote, nil
	}
	if size > 0 && size < len(raw) {
		r.segment, r.segmentSize = raw[size:], size
		r.control, r.remote = control, remote
		raw = raw[:size]
	}
	return raw, control, m.Flags, remote, nil
}

func (r *udpPacketReader) buffered() bool { return len(r.segment) > 0 || r.next < r.count }
