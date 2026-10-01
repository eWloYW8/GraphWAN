package wgaccess

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"net/netip"
	"sync"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

const (
	probeInterval  = 30 * time.Second
	probeTimeout   = 3 * time.Second
	probeFreshness = 40 * time.Second
)

type pingPeer struct {
	address              netip.Addr
	next, sent, measured time.Time
	token                [16]byte
	rtt                  time.Duration
	valid                bool
}
type pinger struct {
	mu        sync.Mutex
	source    netip.Addr
	tag       [8]byte
	peers     map[model.ID]*pingPeer
	byAddress map[netip.Addr]*pingPeer
}

func newPinger(source netip.Addr, peers []model.Peer) *pinger {
	p := &pinger{source: source, peers: map[model.ID]*pingPeer{}, byAddress: map[netip.Addr]*pingPeer{}}
	rand.Read(p.tag[:])
	for _, peer := range peers {
		state := &pingPeer{address: peer.Node.Address}
		p.peers[peer.Node.ID] = state
		p.byAddress[state.address] = state
	}
	return p
}

// Report drives scheduling; no probe goroutine, raw socket or OS route is
// required. The decrypted reply may arrive concurrently with this call.
func (p *pinger) sample(id model.ID, ready bool, now time.Time) (request []byte, rtt float64, measured time.Time, valid bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	peer := p.peers[id]
	if peer == nil {
		return
	}
	if !peer.sent.IsZero() && now.Sub(peer.sent) >= probeTimeout {
		peer.sent = time.Time{}
		peer.valid = false
	}
	if !ready {
		peer.sent = time.Time{}
		peer.valid = false
	}
	if ready && !now.Before(peer.next) && p.source.IsValid() && p.source.Is4() == peer.address.Is4() {
		rand.Read(peer.token[:])
		request = echoRequest(p.source, peer.address, p.tag, peer.token)
		peer.sent = now
		peer.next = now.Add(probeInterval)
	}
	valid = peer.valid && now.Sub(peer.measured) < probeFreshness
	if valid {
		rtt = float64(peer.rtt) / float64(time.Millisecond)
		measured = peer.measured
	}
	return
}
func (p *pinger) failed(id model.ID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if peer := p.peers[id]; peer != nil {
		peer.sent = time.Time{}
		peer.valid = false
	}
}

// consume only intercepts our randomized probe tag. User ICMP continues through
// the normal forwarding path. WireGuard already authenticated the source /32
// or /128; nonce, source, destination, echo ID/sequence and checksum bind replies
// to exactly one outstanding measurement. Late/duplicate replies cannot refresh it.
func (p *pinger) consume(raw []byte) bool {
	source, destination, message, ok := echoReply(raw)
	if !ok || destination != p.source || !bytes.Equal(message[8:16], p.tag[:]) {
		return false
	}
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	peer := p.byAddress[source]
	if peer == nil {
		return false
	}
	if peer.sent.IsZero() || !bytes.Equal(message[16:32], peer.token[:]) || !bytes.Equal(message[4:8], peer.token[:4]) {
		return true
	}
	elapsed := now.Sub(peer.sent)
	peer.sent = time.Time{}
	if elapsed < 0 || elapsed >= probeTimeout {
		peer.valid = false
		return true
	}
	peer.rtt = elapsed
	peer.measured = now
	peer.valid = true
	return true
}

func echoRequest(source, destination netip.Addr, tag [8]byte, token [16]byte) []byte {
	header := 20
	if source.Is6() {
		header = 40
	}
	raw := make([]byte, header+32)
	msg := raw[header:]
	copy(msg[4:8], token[:4])
	copy(msg[8:16], tag[:])
	copy(msg[16:], token[:])
	if source.Is4() {
		raw[0] = 0x45
		raw[8] = 64
		raw[9] = 1
		binary.BigEndian.PutUint16(raw[2:4], uint16(len(raw)))
		s, d := source.As4(), destination.As4()
		copy(raw[12:16], s[:])
		copy(raw[16:20], d[:])
		binary.BigEndian.PutUint16(raw[10:12], checksum(raw[:20]))
		msg[0] = 8
		binary.BigEndian.PutUint16(msg[2:4], checksum(msg))
	} else {
		raw[0] = 0x60
		raw[6] = 58
		raw[7] = 64
		binary.BigEndian.PutUint16(raw[4:6], 32)
		s, d := source.As16(), destination.As16()
		copy(raw[8:24], s[:])
		copy(raw[24:40], d[:])
		msg[0] = 128
		binary.BigEndian.PutUint16(msg[2:4], icmpv6Checksum(source, destination, msg))
	}
	return raw
}
func echoReply(raw []byte) (source, destination netip.Addr, message []byte, ok bool) {
	if len(raw) < 20 {
		return
	}
	switch raw[0] >> 4 {
	case 4:
		if raw[9] != 1 {
			return
		}
		header := int(raw[0]&15) * 4
		if header < 20 || len(raw) != header+32 || int(binary.BigEndian.Uint16(raw[2:4])) != len(raw) || binary.BigEndian.Uint16(raw[6:8])&0x3fff != 0 {
			return
		}
		message = raw[header:]
		if message[0] != 0 || message[1] != 0 || checksum(raw[:header]) != 0 || checksum(message) != 0 {
			return
		}
		source = netip.AddrFrom4([4]byte(raw[12:16]))
		destination = netip.AddrFrom4([4]byte(raw[16:20]))
	case 6:
		// Our small probes require no extension headers or fragmentation.
		if len(raw) != 72 || raw[6] != 58 || binary.BigEndian.Uint16(raw[4:6]) != 32 {
			return
		}
		source = netip.AddrFrom16([16]byte(raw[8:24]))
		destination = netip.AddrFrom16([16]byte(raw[24:40]))
		message = raw[40:]
		if message[0] != 129 || message[1] != 0 || icmpv6Checksum(source, destination, message) != 0 {
			return
		}
	default:
		return
	}
	ok = true
	return
}
func checksum(data []byte) uint16 {
	var sum uint32
	for len(data) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(data))
		data = data[2:]
	}
	if len(data) > 0 {
		sum += uint32(data[0]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}
func icmpv6Checksum(source, destination netip.Addr, message []byte) uint16 {
	var data [72]byte // 40-byte pseudo-header + our fixed 32-byte echo message.
	s, d := source.As16(), destination.As16()
	copy(data[:16], s[:])
	copy(data[16:32], d[:])
	binary.BigEndian.PutUint32(data[32:36], uint32(len(message)))
	data[39] = 58
	copy(data[40:], message)
	return checksum(data[:])
}
