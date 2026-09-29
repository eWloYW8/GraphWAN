package routing_test

import (
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/routing"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
)

func TestWeightedMultiHop(t *testing.T) {
	s := testutil.Topology()
	snapshot, err := routing.Compile(s, s.Agents[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	n := snapshot.Networks[0]
	want := []model.Route{{Destination: testutil.ID(21), NextHop: testutil.ID(21), Cost: 10}, {Destination: testutil.ID(22), NextHop: testutil.ID(21), Cost: 20}, {Destination: testutil.ID(23), NextHop: testutil.ID(23), Cost: 1}}
	if !reflect.DeepEqual(n.Routes, want) {
		t.Fatalf("routes=%+v", n.Routes)
	}
	if len(n.Peers) != 2 || len(n.Directory) != 5 {
		t.Fatalf("compiled config=%+v", n)
	}
	// The source configuration must not be aliased by compiled snapshots.
	n.Peers[0].PublicKey[0] ^= 255
	n.Peers[0].Endpoints[0].URL = "changed"
	n.Peers[0].Edge.Transports[0] = model.WSS
	if err := s.Validate(); err != nil {
		t.Fatalf("compiler mutated state: %v", err)
	}
}
func TestDisabledAndRevoked(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		s := testutil.Topology()
		if revoked {
			s.Agents[1].Revoked = true
		} else {
			s.Networks[0].Edges[0].Enabled = false
		}
		snap, err := routing.Compile(s, s.Agents[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range snap.Networks[0].Routes {
			if r.Destination == testutil.ID(22) && (r.Cost != 31 || r.NextHop != testutil.ID(23)) {
				t.Fatalf("route=%+v", r)
			}
		}
		if len(snap.Networks[0].Peers) != 1 {
			t.Fatal("disabled/revoked peer retained")
		}
		if revoked {
			if _, err := routing.Compile(s, s.Agents[1].ID); err == nil {
				t.Fatal("compiled revoked agent")
			}
		}
	}
}
func TestIsolatedAndUnknown(t *testing.T) {
	s := testutil.Topology()
	snap, err := routing.Compile(s, s.Agents[4].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Networks[0].Peers) != 0 || len(snap.Networks[0].Routes) != 0 {
		t.Fatal("isolated node has routes")
	}
	if _, err := routing.Compile(s, testutil.ID(999)); err == nil {
		t.Fatal("accepted unknown agent")
	}
}
func TestEqualCostIndependentOfInputOrder(t *testing.T) {
	s := testutil.Topology()
	s.Networks[0].Edges[3].Weight = 19
	baseline, err := routing.Compile(s, s.Agents[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		rand.Shuffle(len(s.Networks[0].Edges), func(i, j int) {
			s.Networks[0].Edges[i], s.Networks[0].Edges[j] = s.Networks[0].Edges[j], s.Networks[0].Edges[i]
		})
		got, err := routing.Compile(s, s.Agents[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, baseline) {
			t.Fatal("input order changes snapshot")
		}
	}
	if baseline.Networks[0].Routes[1].NextHop != testutil.ID(21) {
		t.Fatal("tie did not select lowest next-hop ID")
	}
}

// Follow the actual independently compiled tables, not just one source's costs.
func TestForwardingTablesTerminateAtDestination(t *testing.T) {
	s := testutil.Topology()
	tables := map[model.ID]map[model.ID]model.Route{}
	for _, a := range s.Agents {
		snap, err := routing.Compile(s, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		n := snap.Networks[0]
		tables[n.Self.ID] = map[model.ID]model.Route{}
		for _, r := range n.Routes {
			tables[n.Self.ID][r.Destination] = r
		}
	}
	for source, table := range tables {
		for dest := range table {
			at := source
			for hops := 0; at != dest; hops++ {
				if hops >= len(tables) {
					t.Fatal("forwarding loop")
				}
				r, ok := tables[at][dest]
				if !ok {
					t.Fatal("blackhole")
				}
				at = r.NextHop
			}
		}
	}
}
