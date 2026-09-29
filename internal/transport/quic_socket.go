package transport

import (
	"crypto/rand"
	"github.com/eWloYW8/GraphWAN/internal/packetbuf"
	"net"
	"sync"
	"time"

	quic "github.com/quic-go/quic-go"
)

// quicSocket gives the QUIC engine sole ownership of socket reads and dispatches
// native GWD packets to the existing UDP hub. Hiding SyscallConn is intentional:
// raw/batched reads would bypass dispatch, and QUIC's socket-wide DF setting
// would change native UDP fragmentation behavior. QUIC uses 1200-byte packets.
type quicSocket struct {
	net.PacketConn
	hub         *UDP
	socket      *net.UDPConn
	readMu      sync.Mutex
	reader      *udpPacketReader
	packets     [packetbuf.BatchSize]udpPacket
	next, count int
}

func (s *quicSocket) ReadFrom(raw []byte) (int, net.Addr, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if s.reader == nil {
		s.reader = newUDPPacketReader(s.socket)
	}
	for {
		if s.next == s.count {
			n, err := s.reader.readBatch(s.packets[:])
			if udpReadTruncated(0, err) {
				continue
			}
			if err != nil {
				s.hub.stop()
				return 0, nil, err
			}
			var native [packetbuf.BatchSize]udpPacket
			nativeCount, quicCount := 0, 0
			for _, p := range s.packets[:n] {
				if udpReadTruncated(p.flags, nil) {
					continue
				}
				if len(p.raw) >= 4 && [4]byte(p.raw[:4]) == udpMagic {
					native[nativeCount] = p
					nativeCount++
				} else if !s.hub.receiveSTUN(p.raw, p.remote) {
					s.packets[quicCount] = p
					quicCount++
				}
			}
			if nativeCount > 0 {
				s.hub.receivePackets(native[:nativeCount])
			}
			s.next, s.count = 0, quicCount
			if quicCount == 0 {
				continue
			}
		}
		p := s.packets[s.next]
		s.next++
		if len(p.raw) > len(raw) {
			continue
		}
		return copy(raw, p.raw), net.UDPAddrFromAddrPort(p.remote), nil
	}
}

func (s *quicSocket) WriteTo(raw []byte, remote net.Addr) (int, error) {
	s.hub.writeMu.Lock()
	defer s.hub.writeMu.Unlock()
	if err := s.PacketConn.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return 0, err
	}
	defer s.PacketConn.SetWriteDeadline(time.Time{})
	return s.PacketConn.WriteTo(raw, remote)
}
func (s *quicSocket) SetReadBuffer(size int) error  { return setUDPReadBuffer(s.socket, size) }
func (s *quicSocket) SetWriteBuffer(size int) error { return s.socket.SetWriteBuffer(size) }

// Every local CID starts with Q, so normal incoming short headers cannot start GWD\x01.
// Long headers always have their high bit set. The remaining 120 bits are random.
type quicIDs struct{}

func (quicIDs) ConnectionIDLen() int { return 16 }
func (quicIDs) GenerateConnectionID() (quic.ConnectionID, error) {
	var raw [16]byte
	raw[0] = 'Q'
	if _, err := rand.Read(raw[1:]); err != nil {
		return quic.ConnectionID{}, err
	}
	return quic.ConnectionIDFromBytes(raw[:]), nil
}
