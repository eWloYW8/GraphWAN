// Package testutil contains deterministic fixtures shared by core unit tests.
package testutil

import (
	"crypto/ed25519"
	"fmt"
	"net/netip"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

func ID(n int) model.ID { return model.ID(fmt.Sprintf("%032x", n)) }

// Topology contains A-B-C and A-D-C, with E isolated. A-B-C costs 20;
// A-D-C costs 31, so latency or adjacency order must not choose the latter.
func Topology() model.State {
	s := model.EmptyState()
	s.Revision = 7
	n := model.Network{ID: ID(1), Name: "Test network", CIDR: netip.MustParsePrefix("10.42.0.0/24"), MTU: model.DefaultMTU, Cipher: model.ChaCha20Poly1305}
	for i := 0; i < 5; i++ {
		seed := make([]byte, ed25519.SeedSize)
		seed[0] = byte(i + 1)
		key := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
		s.Agents = append(s.Agents, model.Agent{ID: ID(10 + i), Name: fmt.Sprintf("Agent %c", 'A'+i), PublicKey: key, ListenPort: model.DefaultPort, Endpoints: []model.Endpoint{{ID: ID(30 + i), Transport: model.UDP, URL: fmt.Sprintf("udp://192.0.2.%d:24752", i+1), Source: model.Manual}}})
		n.Nodes = append(n.Nodes, model.Node{ID: ID(20 + i), AgentID: ID(10 + i), Name: fmt.Sprintf("Node %c", 'A'+i), Address: netip.MustParseAddr(fmt.Sprintf("10.42.0.%d", i+1))})
	}
	for i, pair := range [][3]int{{0, 1, 10}, {1, 2, 10}, {0, 3, 1}, {3, 2, 30}} {
		n.Edges = append(n.Edges, model.Edge{ID: ID(40 + i), A: ID(20 + pair[0]), B: ID(20 + pair[1]), Weight: uint32(pair[2]), Enabled: true, Transports: []model.Transport{model.UDP, model.TCP}, Methods: model.ConnectionMethods{IPv4Direct: true, IPv6Direct: true, HolePunch: true}})
	}
	s.Networks = append(s.Networks, n)
	return s
}
