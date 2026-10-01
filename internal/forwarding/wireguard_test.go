package forwarding_test

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/forwarding"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/routing"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
)

func TestWireGuardLeafRouting(t *testing.T) {
	s := testutil.Topology()
	n := &s.Networks[0]
	key, _ := ecdh.X25519().GenerateKey(rand.Reader)
	leaf := model.Node{ID: testutil.ID(80), Name: "Phone", Address: netip.MustParseAddr("10.42.0.10"), WireGuard: &model.WireGuardNode{PublicKey: base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), Endpoint: "vpn.example.com:24752"}}
	edge := model.Edge{ID: testutil.ID(81), A: n.Nodes[0].ID, B: leaf.ID, Weight: 10, Enabled: true, Transports: []model.Transport{model.WireGuard}, Methods: model.ConnectionMethods{IPv4Direct: true}}
	n.Nodes = append(n.Nodes, leaf)
	n.Edges = append(n.Edges, edge)
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := s.Clone()
	invalid.Networks[0].Edges = invalid.Networks[0].Edges[:len(n.Edges)-1]
	if invalid.Validate() == nil {
		t.Fatal("unattached WireGuard node admitted")
	}
	invalid = s.Clone()
	extra := edge
	extra.ID = testutil.ID(82)
	extra.A = n.Nodes[1].ID
	invalid.Networks[0].Edges = append(invalid.Networks[0].Edges, extra)
	if invalid.Validate() == nil {
		t.Fatal("multiply attached WireGuard node admitted")
	}
	routers := map[model.ID]*forwarding.Router{}
	var delivered model.ID
	for _, node := range n.Nodes {
		if node.WireGuard != nil {
			continue
		}
		snapshot, err := routing.Compile(s, node.AgentID)
		if err != nil {
			t.Fatal(err)
		}
		r, err := forwarding.New(snapshot, func(ctx context.Context, network, next model.ID, frame []byte) error {
			if next == leaf.ID {
				delivered = next
				return nil
			}
			return routers[next].FromPeer(ctx, node.ID, frame)
		}, func(context.Context, model.ID, []byte) error { delivered = node.ID; return nil })
		if err != nil {
			t.Fatal(err)
		}
		routers[node.ID] = r
	}
	ctx := context.Background()
	gateway := n.Nodes[0].ID
	remote := n.Nodes[2].ID
	if err := routers[gateway].FromWireGuard(ctx, n.ID, ipv4("10.42.0.10", "10.42.0.3")); err != nil || delivered != remote {
		t.Fatal("WireGuard multi-hop ingress", delivered, err)
	}
	if err := routers[remote].FromTunnel(ctx, n.ID, ipv4("10.42.0.3", "10.42.0.10")); err != nil || delivered != leaf.ID {
		t.Fatal("WireGuard multi-hop return", delivered, err)
	}
	if err := routers[gateway].FromWireGuard(ctx, n.ID, ipv4("10.42.0.2", "10.42.0.3")); !errors.Is(err, forwarding.ErrSource) {
		t.Fatal("spoofed WireGuard ingress", err)
	}
	report := model.AgentStatus{AgentID: n.Nodes[0].AgentID, Connected: true, LastSeen: time.Now(), AgentReport: model.AgentReport{Links: []model.LinkStatus{{NetworkID: n.ID, EdgeID: edge.ID, LinkID: "wg", Transport: model.WireGuard, Healthy: true}}}}
	if !routing.Availability(s, []model.AgentStatus{report}, time.Now())[routing.EdgeKey{Network: n.ID, Edge: edge.ID}] {
		t.Fatal("WireGuard edge incorrectly requires two Agent reports")
	}
	n.Edges[len(n.Edges)-1].Enabled = false
	snapshot, err := routing.Compile(s, n.Nodes[0].AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if err := routers[gateway].Configure(snapshot); err != nil {
		t.Fatal(err)
	}
	if err := routers[gateway].FromWireGuard(ctx, n.ID, ipv4("10.42.0.10", "10.42.0.3")); !errors.Is(err, forwarding.ErrSource) {
		t.Fatal("disabled WireGuard ingress", err)
	}
}
