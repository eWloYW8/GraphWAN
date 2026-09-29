package transport

import (
	"crypto/rand"
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
	hub     *UDP
	readMu  sync.Mutex
	buffer  [MaxMessage + udpHeaderSize + 1]byte
	control [256]byte
}

func (s *quicSocket) ReadFrom(raw []byte) (int, net.Addr, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	for {
		n, control, _, remote, err := s.hub.socket.ReadMsgUDPAddrPort(s.buffer[:], s.control[:])
		if err != nil {
			s.hub.stop()
			return 0, nil, err
		}
		if n >= 4 && [4]byte(s.buffer[:4]) == udpMagic {
			s.hub.receivePacket(s.buffer[:n], remote, udpReplyControl(s.control[:control], remote))
			continue
		}
		if s.hub.receiveSTUN(s.buffer[:n], remote) {
			continue
		}
		if n > len(raw) {
			continue
		}
		return copy(raw, s.buffer[:n]), net.UDPAddrFromAddrPort(remote), nil
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
func (s *quicSocket) SetReadBuffer(size int) error  { return s.hub.socket.SetReadBuffer(size) }
func (s *quicSocket) SetWriteBuffer(size int) error { return s.hub.socket.SetWriteBuffer(size) }

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
