package wgaccess

import (
	"encoding/binary"
	"net/netip"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
)

func TestICMPProbeValidationAndExpiry(t *testing.T) {
	for _, addresses := range [][2]string{{"10.42.0.1", "10.42.0.2"}, {"fd42::1", "fd42::2"}} {
		t.Run(addresses[0], func(t *testing.T) {
			id := testutil.ID(1)
			p := newPinger(netip.MustParseAddr(addresses[0]), []model.Peer{{Node: model.Node{ID: id, Address: netip.MustParseAddr(addresses[1])}}})
			now := time.Now().Add(-20 * time.Millisecond)
			if req, _, _, valid := p.sample(id, false, now); req != nil || valid {
				t.Fatal("unestablished peer probed")
			}
			req, _, _, _ := p.sample(id, true, now)
			reply := probeReply(req)
			bad := append([]byte(nil), reply...)
			bad[len(bad)-1] ^= 1
			if p.consume(bad) {
				t.Fatal("bad checksum accepted")
			}
			if !p.consume(reply) {
				t.Fatal("valid echo reply not consumed")
			}
			if request, rtt, measured, valid := p.sample(id, true, time.Now()); request != nil || !valid || rtt < 20 || measured.IsZero() {
				t.Fatal("missing RTT", rtt, valid)
			}
			measured := p.peers[id].measured
			if !p.consume(reply) || p.peers[id].measured != measured {
				t.Fatal("replay changed measurement")
			}
			if request, _, _, _ := p.sample(id, true, now.Add(probeInterval)); request == nil {
				t.Fatal("next probe not scheduled")
			}
			if !p.consume(reply) || p.peers[id].sent.IsZero() {
				t.Fatal("old nonce completed new probe")
			}
			if _, rtt, _, valid := p.sample(id, true, now.Add(probeInterval+probeTimeout)); valid || rtt != 0 {
				t.Fatal("timeout presented as valid RTT")
			}
			if request, _, _, _ := p.sample(id, true, now.Add(probeInterval+probeTimeout+time.Second)); request != nil {
				t.Fatal("timeout caused high-frequency retries")
			}
		})
	}
}

// Simulates a standard IP stack echo response, including both checksums.
func probeReply(request []byte) []byte {
	raw := append([]byte(nil), request...)
	if raw[0]>>4 == 4 {
		source := [4]byte(raw[12:16])
		copy(raw[12:16], raw[16:20])
		copy(raw[16:20], source[:])
		raw[20] = 0
		raw[22], raw[23] = 0, 0
		binary.BigEndian.PutUint16(raw[22:24], checksum(raw[20:]))
		raw[10], raw[11] = 0, 0
		binary.BigEndian.PutUint16(raw[10:12], checksum(raw[:20]))
	} else {
		source := [16]byte(raw[8:24])
		copy(raw[8:24], raw[24:40])
		copy(raw[24:40], source[:])
		raw[40] = 129
		raw[42], raw[43] = 0, 0
		binary.BigEndian.PutUint16(raw[42:44], icmpv6Checksum(netip.AddrFrom16([16]byte(raw[8:24])), netip.AddrFrom16([16]byte(raw[24:40])), raw[40:]))
	}
	return raw
}
