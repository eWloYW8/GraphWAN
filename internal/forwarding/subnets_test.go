package forwarding_test

import (
	"context"
	"encoding/binary"
	"errors"
	"net/netip"
	"testing"

	"github.com/eWloYW8/GraphWAN/internal/forwarding"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/packet"
	"github.com/eWloYW8/GraphWAN/internal/routing"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
)

func ipv4(src, dst string) []byte {
	b := make([]byte, 28)
	b[0], b[8], b[9] = 0x45, 64, 17
	binary.BigEndian.PutUint16(b[2:4], uint16(len(b)))
	s, d := netip.MustParseAddr(src).As4(), netip.MustParseAddr(dst).As4()
	copy(b[12:16], s[:])
	copy(b[16:20], d[:])
	return b
}

func TestAdvertisedSubnetForwarding(t *testing.T) {
	state := testutil.Topology()
	n := &state.Networks[0]
	n.Nodes[2].AdvertisedSubnets = []model.AdvertisedSubnet{{Prefix: netip.MustParsePrefix("192.168.10.0/24"), GatewayMode: model.GatewayOff}}
	n.Nodes[3].AdvertisedSubnets = []model.AdvertisedSubnet{{Prefix: netip.MustParsePrefix("192.168.0.0/16"), GatewayMode: model.GatewayRoute}}
	routers := map[model.ID]*forwarding.Router{}
	snapshots := map[model.ID]model.Snapshot{}
	var delivered model.ID
	var hops []model.ID
	for _, node := range n.Nodes {
		node := node
		snapshot, err := routing.Compile(state, node.AgentID)
		if err != nil {
			t.Fatal(err)
		}
		snapshots[node.ID] = snapshot
		r, err := forwarding.New(snapshot, func(ctx context.Context, network, next model.ID, frame []byte) error {
			hops = append(hops, next)
			return routers[next].FromPeer(ctx, node.ID, frame)
		}, func(_ context.Context, _ model.ID, _ []byte) error { delivered = node.ID; return nil })
		if err != nil {
			t.Fatal(err)
		}
		routers[node.ID] = r
	}
	ctx := context.Background()
	a, b, c := n.Nodes[0].ID, n.Nodes[1].ID, n.Nodes[2].ID
	if err := routers[a].FromTunnel(ctx, n.ID, ipv4("10.42.0.1", "192.168.10.7")); err != nil {
		t.Fatal(err)
	}
	if delivered != c || len(hops) != 2 || hops[0] != b {
		t.Fatalf("wrong external path: %v %s", hops, delivered)
	}
	if err := routers[c].FromTunnel(ctx, n.ID, ipv4("192.168.10.7", "10.42.0.1")); err != nil || delivered != a {
		t.Fatalf("return path: %s %v", delivered, err)
	}
	if err := routers[c].FromTunnel(ctx, n.ID, ipv4("192.168.11.7", "10.42.0.1")); !errors.Is(err, forwarding.ErrSource) {
		t.Fatalf("spoofed external source admitted: %v", err)
	}
	forged, err := (packet.Packet{Header: packet.Header{Network: n.ID, Source: c, Destination: a, HopLimit: 16, Epoch: state.Revision}, Payload: ipv4("192.168.11.7", "10.42.0.1")}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if err := routers[b].FromPeer(ctx, c, forged); !errors.Is(err, forwarding.ErrSource) {
		t.Fatalf("spoofed transit source admitted: %v", err)
	}
	snap := snapshots[a].Clone()
	var live []model.Route
	for _, route := range snap.Networks[0].Routes {
		if route.Destination != c {
			live = append(live, route)
		}
	}
	snap.Networks[0].Routes = live
	if err := routers[a].Configure(snap); err != nil {
		t.Fatal(err)
	}
	if err := routers[a].FromTunnel(ctx, n.ID, ipv4("10.42.0.1", "192.168.10.7")); !errors.Is(err, forwarding.ErrUnreachable) {
		t.Fatalf("down gateway fell back to broader prefix: %v", err)
	}
}
