package forwarding_test

import (
	"context"
	"testing"

	"github.com/eWloYW8/GraphWAN/internal/forwarding"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/packet"
	"github.com/eWloYW8/GraphWAN/internal/routing"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
)

func BenchmarkForwarding(b *testing.B) {
	state := testutil.Topology()
	config, err := routing.Compile(state, testutil.ID(11))
	if err != nil {
		b.Fatal(err)
	}
	router, err := forwarding.New(config, func(context.Context, model.ID, model.ID, []byte) error { return nil }, func(context.Context, model.ID, []byte) error { return nil })
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	network, peer := testutil.ID(1), testutil.ID(20)
	incoming, err := (packet.Packet{Header: packet.Header{Network: network, Source: peer, Destination: testutil.ID(21), HopLimit: 32}, Payload: ipPacket(1, 2)}).MarshalBinary()
	if err != nil {
		b.Fatal(err)
	}
	outgoing := ipPacket(2, 3)
	b.Run("receive", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if err := router.FromPeer(ctx, peer, incoming); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("send", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if err := router.FromTunnel(ctx, network, outgoing); err != nil {
				b.Fatal(err)
			}
		}
	})
}
