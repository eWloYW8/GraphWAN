//go:build linux

package mesh

import (
	"bytes"
	"context"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/link"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
)

// Nine Networks with sixty one-way endpoints each require 540 simultaneous
// authenticated connections. This exceeds the old shared 512-connection ceiling
// without changing model limits or bypassing real transport/Noise admission.
func TestAuthenticatedCapacityAcrossNetworks(t *testing.T) {
	testutil.RequireLoopbackAliases(t, "127.0.0.2", "127.0.0.60")
	for _, kind := range []model.Transport{model.UDP, model.QUIC, model.GRPC} {
		t.Run(string(kind), func(t *testing.T) {
			ctx, meshes, state, delivered := webMeshesWithTimeout(t, "0.0.0.0", 90*time.Second)
			state.Agents[0].Endpoints = nil
			state.Agents[1].Endpoints = nil
			for i := range 60 {
				state.Agents[1].Endpoints = append(state.Agents[1].Endpoints, model.Endpoint{
					ID: testutil.ID(1000 + i), Source: model.Manual, Transport: kind,
					URL: fmt.Sprintf("%s://127.0.0.%d:%d", kind, i+1, meshes[1].Port()),
				})
			}
			state.Networks[0].Edges[0].Transports = []model.Transport{kind}
			template := state.Clone().Networks[0]
			state.Networks = nil
			for i := range 9 {
				n := template
				n.Nodes = append([]model.Node{}, template.Nodes...)
				n.Edges = append([]model.Edge{}, template.Edges...)
				n.ID, n.Name = testutil.ID(2000+i*4), fmt.Sprintf("Capacity %d", i)
				n.CIDR = netip.MustParsePrefix(fmt.Sprintf("10.90.%d.0/24", i))
				for j := range n.Nodes {
					n.Nodes[j].ID = testutil.ID(2001 + i*4 + j)
					n.Nodes[j].Address = netip.MustParseAddr(fmt.Sprintf("10.90.%d.%d", i, j+1))
				}
				n.Edges[0].ID = testutil.ID(2003 + i*4)
				n.Edges[0].A, n.Edges[0].B = n.Nodes[0].ID, n.Nodes[1].ID
				n.Edges[0].PreferredCandidate = link.CandidateID(n.Edges[0].ID, n.Nodes[0].ID, state.Agents[1].Endpoints[59].ID, 4, link.Direct)
				state.Networks = append(state.Networks, n)
			}
			all := state.Clone()
			state.Networks = state.Networks[:1]
			applyWebState(t, state, meshes)
			healthyLinks := func(m *Mesh) map[string]bool {
				result := map[string]bool{}
				for _, stat := range m.Report() {
					if stat.Healthy {
						result[stat.LinkID] = true
					}
				}
				return result
			}
			waitCapacity := func(networks []model.Network) {
				t.Helper()
				deadline, cancel := context.WithTimeout(ctx, 40*time.Second)
				defer cancel()
				for {
					healthy := [2]map[string]bool{{}, {}}
					active := [2]map[model.ID]model.LinkStatus{{}, {}}
					for i, m := range meshes {
						for _, stat := range m.Report() {
							if stat.Healthy {
								healthy[i][stat.CandidateID] = true
								if stat.Active {
									active[i][stat.NetworkID] = stat
								}
							}
						}
					}
					ready := len(healthy[0]) == 60*len(networks) && len(healthy[1]) == 60*len(networks)
					for _, n := range networks {
						for _, endpoint := range state.Agents[1].Endpoints {
							id := link.CandidateID(n.Edges[0].ID, n.Nodes[0].ID, endpoint.ID, 4, link.Direct)
							ready = ready && healthy[0][id] && healthy[1][id]
						}
						a, b := active[0][n.ID], active[1][n.ID]
						ready = ready && a.LinkID != "" && a.LinkID == b.LinkID && a.CandidateID == n.Edges[0].PreferredCandidate
					}
					if ready {
						return
					}
					select {
					case <-deadline.Done():
						t.Fatalf("%s capacity: healthy candidates %d/%d, want %d on each peer", kind, len(healthy[0]), len(healthy[1]), 60*len(networks))
					case <-time.After(50 * time.Millisecond):
					}
				}
			}
			waitCapacity(state.Networks)
			initialLinks := healthyLinks(meshes[0])
			*state = all
			state.Revision++
			applyWebState(t, state, meshes)
			waitCapacity(state.Networks)
			grownLinks := healthyLinks(meshes[0])
			for id := range initialLinks {
				if !grownLinks[id] {
					t.Fatal("configuration expansion replaced an existing healthy Link")
				}
			}
			// Establish another key generation while every original candidate stays
			// connected. A fixed total ceiling also prevents this replacement.
			n := state.Networks[0]
			meshes[0].mu.Lock()
			g := meshes[0].groups[key{n.ID, n.Nodes[1].ID}]
			meshes[0].mu.Unlock()
			candidate := g.candidates(g.policy.Load(), true)[0]
			old := map[string]bool{}
			for _, stat := range meshes[0].Report() {
				old[stat.LinkID] = true
			}
			if err := g.dial(ctx, candidate); err != nil {
				t.Fatal("replacement handshake at capacity:", err)
			}
			waitWeb(t, ctx, func() bool {
				for _, a := range meshes[0].Report() {
					if a.CandidateID != candidate.ID || !a.Healthy || old[a.LinkID] {
						continue
					}
					for _, b := range meshes[1].Report() {
						if a.LinkID == b.LinkID && b.Healthy {
							return true
						}
					}
				}
				return false
			})
			waitCapacity(state.Networks)
			replacedLinks := healthyLinks(meshes[0])
			for id := range grownLinks {
				if !replacedLinks[id] {
					t.Fatal("replacement handshake displaced an existing healthy Link")
				}
			}
			for _, n := range state.Networks {
				payload := bytes.Repeat([]byte{byte(n.CIDR.Addr().As4()[2])}, 1280)
				if err := meshes[0].Send(ctx, n.ID, n.Nodes[1].ID, payload); err != nil {
					t.Fatal(err)
				}
				select {
				case got := <-delivered:
					if !bytes.Equal(got, payload) {
						t.Fatal("payload crossed Networks or was corrupted")
					}
				case <-ctx.Done():
					t.Fatal("delivery at capacity stalled")
				}
			}
			// Removing memberships must reclaim Links while preserving the retained
			// Network's complete endpoint set.
			state.Networks = state.Networks[:1]
			state.Revision++
			applyWebState(t, state, meshes)
			waitCapacity(state.Networks)
			state.Networks = nil
			state.Revision++
			applyWebState(t, state, meshes)
			waitWeb(t, ctx, func() bool {
				return len(meshes[0].Report()) == 0 && len(meshes[1].Report()) == 0
			})
		})
	}
}
