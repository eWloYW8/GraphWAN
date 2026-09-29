package forwarding_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"slices"
	"testing"

	"github.com/eWloYW8/GraphWAN/internal/forwarding"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/packet"
	"github.com/eWloYW8/GraphWAN/internal/routing"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
)

func ipPacket(source, dest byte) []byte {
	raw := make([]byte, 28)
	raw[0] = 0x45
	raw[8] = 64
	raw[9] = 17
	binary.BigEndian.PutUint16(raw[2:4], uint16(len(raw)))
	copy(raw[12:20], []byte{10, 42, 0, source, 10, 42, 0, dest})
	binary.BigEndian.PutUint16(raw[20:22], 10000)
	binary.BigEndian.PutUint16(raw[22:24], 20000)
	binary.BigEndian.PutUint16(raw[24:26], 8)
	return raw
}
func TestGraphWeightedMultiHopForwarding(t *testing.T) {
	state := testutil.Topology()
	routers := map[model.ID]*forwarding.Router{}
	path := []model.ID{}
	var delivered []byte
	var recipient model.ID
	for _, agent := range state.Agents {
		config, err := routing.Compile(state, agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		self := config.Networks[0].Self.ID
		router, err := forwarding.New(config, func(ctx context.Context, network, next model.ID, raw []byte) error {
			path = append(path, next)
			return routers[next].FromPeer(ctx, self, raw)
		}, func(ctx context.Context, network model.ID, raw []byte) error {
			delivered = bytes.Clone(raw)
			recipient = self
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		routers[self] = router
	}
	raw := ipPacket(1, 3)
	if err := routers[testutil.ID(20)].FromTunnel(context.Background(), testutil.ID(1), raw); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(path, []model.ID{testutil.ID(21), testutil.ID(22)}) {
		t.Fatalf("selected path %v", path)
	}
	if recipient != testutil.ID(22) || !bytes.Equal(delivered, raw) {
		t.Fatal("destination received incorrect IP packet")
	}
	if err := routers[testutil.ID(20)].FromTunnel(context.Background(), testutil.ID(1), ipPacket(1, 5)); !errors.Is(err, forwarding.ErrUnreachable) {
		t.Fatal("isolated destination was routed", err)
	}
}
func TestForwardingAdmissionAndHopLimit(t *testing.T) {
	state := testutil.Topology()
	config, err := routing.Compile(state, testutil.ID(11))
	if err != nil {
		t.Fatal(err)
	}
	sent, delivered := 0, 0
	router, err := forwarding.New(config, func(context.Context, model.ID, model.ID, []byte) error { sent++; return nil }, func(context.Context, model.ID, []byte) error { delivered++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	original := packet.Packet{Header: packet.Header{Network: testutil.ID(1), Source: testutil.ID(20), Destination: testutil.ID(22), HopLimit: 1, Epoch: state.Revision}, Payload: ipPacket(1, 3)}
	frame, _ := original.MarshalBinary()
	if err := router.FromPeer(context.Background(), testutil.ID(20), frame); !errors.Is(err, packet.ErrHopLimit) {
		t.Fatalf("TTL: %v", err)
	}
	original.Header.HopLimit = 32
	for _, tt := range []struct {
		name   string
		change func(*packet.Packet)
		peer   model.ID
		want   error
	}{
		{"unknown network", func(p *packet.Packet) { p.Header.Network = testutil.ID(999) }, testutil.ID(20), forwarding.ErrNetwork},
		{"nonadjacent sender", func(p *packet.Packet) {}, testutil.ID(23), forwarding.ErrPeer},
		{"spoofed source", func(p *packet.Packet) { p.Payload = ipPacket(4, 3) }, testutil.ID(20), forwarding.ErrSource},
		{"spoofed destination", func(p *packet.Packet) { p.Payload = ipPacket(1, 4) }, testutil.ID(20), forwarding.ErrDestination},
		{"unknown source", func(p *packet.Packet) { p.Header.Source = testutil.ID(999) }, testutil.ID(20), forwarding.ErrSource},
		{"forged local source", func(p *packet.Packet) { p.Header.Source = testutil.ID(21); p.Payload = ipPacket(2, 3) }, testutil.ID(20), forwarding.ErrSource},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := original
			tt.change(&p)
			frame, err := p.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			if err := router.FromPeer(context.Background(), tt.peer, frame); !errors.Is(err, tt.want) {
				t.Fatalf("got %v want %v", err, tt.want)
			}
		})
	}
	if sent != 0 || delivered != 0 {
		t.Fatal("rejected packets escaped admission")
	}
	if err := router.FromTunnel(context.Background(), testutil.ID(1), ipPacket(1, 3)); !errors.Is(err, forwarding.ErrSource) {
		t.Fatal("local TUN source spoof accepted")
	}
}

func TestBatchPreservesAdmissionAndPacketOrder(t *testing.T) {
	state := testutil.Topology()
	config, err := routing.Compile(state, testutil.ID(11))
	if err != nil {
		t.Fatal(err)
	}
	var received [][]byte
	sent := 0
	router, err := forwarding.New(config,
		func(context.Context, model.ID, model.ID, []byte) error { sent++; return nil },
		func(context.Context, model.ID, []byte) error { t.Fatal("single delivery used"); return nil },
		func(_ context.Context, network model.ID, raw [][]byte) error {
			if network != testutil.ID(1) || len(raw) > 32 {
				t.Fatal("invalid delivery batch")
			}
			for _, p := range raw {
				received = append(received, bytes.Clone(p))
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	var frames [][]byte
	for i := range 70 {
		raw := ipPacket(1, 2)
		raw[1] = byte(i)
		p := packet.Packet{Header: packet.Header{Network: testutil.ID(1), Source: testutil.ID(20), Destination: testutil.ID(21), HopLimit: 32}, Payload: raw}
		if i == 17 {
			p.Payload = ipPacket(4, 2)
		}
		frame, err := p.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, frame)
	}
	if err := router.FromPeerBatch(context.Background(), testutil.ID(20), frames); !errors.Is(err, forwarding.ErrSource) {
		t.Fatal("spoof not rejected", err)
	}
	if sent != 0 || len(received) != 69 {
		t.Fatal("invalid packet reached delivery or valid packet lost", len(received))
	}
	index := 0
	for i := range 70 {
		if i == 17 {
			continue
		}
		if received[index][1] != byte(i) {
			t.Fatal("packet order changed")
		}
		index++
	}
	received = nil
	if err := router.FromPeerBatch(context.Background(), testutil.ID(23), frames); !errors.Is(err, forwarding.ErrPeer) || len(received) != 0 {
		t.Fatal("nonadjacent peer admitted", err)
	}
}

func TestCachedHeaderMatchesCanonicalEncoder(t *testing.T) {
	state := testutil.Topology()
	config, err := routing.Compile(state, testutil.ID(11))
	if err != nil {
		t.Fatal(err)
	}
	var sent []byte
	router, err := forwarding.New(config, func(_ context.Context, _ model.ID, _ model.ID, raw []byte) error { sent = bytes.Clone(raw); return nil }, func(context.Context, model.ID, []byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{28, 1280, 99} {
		config.Revision++
		if err := router.Configure(config); err != nil {
			t.Fatal(err)
		}
		raw := make([]byte, size)
		copy(raw, ipPacket(2, 3))
		binary.BigEndian.PutUint16(raw[2:4], uint16(size))
		if err := router.FromTunnel(t.Context(), testutil.ID(1), raw); err != nil {
			t.Fatal(err)
		}
		info, _ := packet.InspectIP(raw)
		want, err := (packet.Packet{Header: packet.Header{Network: testutil.ID(1), Source: testutil.ID(21), Destination: testutil.ID(22), HopLimit: packet.DefaultHopLimit, Epoch: config.Revision, Flow: info.Flow}, Payload: raw}).MarshalBinary()
		if err != nil || !bytes.Equal(sent, want) {
			t.Fatal("cached encoding differs from canonical wire format")
		}
	}
	original := packet.Packet{Header: packet.Header{Network: testutil.ID(1), Source: testutil.ID(20), Destination: testutil.ID(22), HopLimit: 17, Epoch: 21, Flow: 22, Sequence: 23}, Payload: ipPacket(1, 3)}
	frame, _ := original.MarshalBinary()
	before := bytes.Clone(frame)
	if err := router.FromPeer(t.Context(), testutil.ID(20), frame); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(frame, before) {
		t.Fatal("transit mutated caller storage")
	}
	original.Header.HopLimit--
	want, _ := original.MarshalBinary()
	if !bytes.Equal(sent, want) {
		t.Fatal("transit changed fields other than hop limit")
	}
}
