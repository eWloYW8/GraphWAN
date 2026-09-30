package agent

import (
	"github.com/eWloYW8/GraphWAN/internal/routing"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
	"reflect"
	"testing"
)

func TestDiscoveryRevisionKeepsLiveRoutes(t *testing.T) {
	s := testutil.Topology()
	snap, err := routing.Compile(s, s.Agents[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	up := map[routing.EdgeKey]bool{}
	for _, e := range s.Networks[0].Edges[1:] {
		up[routing.EdgeKey{Network: s.Networks[0].ID, Edge: e.ID}] = true
	}
	live := routing.LiveRoutes(s, snap, up)
	next := snap.Clone()
	next.Revision++
	next.Endpoints[0].URL = "udp://192.0.2.100:24752"
	got := carryLiveRoutes(next, &live)
	if err := got.Validate(snap.AgentID); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Networks[0].Routes, live.Networks[0].Routes) {
		t.Fatal("discovery update reinstated a failed static path")
	}
	if !reflect.DeepEqual(got.Networks[0].Peers, next.Networks[0].Peers) || got.Revision != next.Revision {
		t.Fatal("configuration update lost")
	}
	// All routes withdrawn must stay withdrawn, even at a later revision.
	empty := routing.LiveRoutes(s, snap, nil)
	if len(carryLiveRoutes(next, &empty).Networks[0].Routes) != 0 {
		t.Fatal("withdrawn routes resurrected")
	}
	// Removing an admitted next hop must not retain an invalid forwarding table.
	next.Networks[0].Peers = nil
	got = carryLiveRoutes(next, &live)
	if len(got.Networks[0].Routes) != 0 {
		t.Fatal("removed peers retained as next hops")
	}
	if err := got.Validate(snap.AgentID); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snap.Networks[0].Routes, next.Networks[0].Routes) {
		t.Fatal("mutated input routes")
	}
}
