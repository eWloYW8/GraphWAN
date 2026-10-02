package routing

import (
	"slices"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
)

func meshFixture() model.State {
	s := testutil.Topology()
	n := &s.Networks[0]
	n.Edges = nil
	n.Groups = []model.FullMeshGroup{{ID: testutil.ID(100), Name: "Mesh", Members: []model.ID{n.Nodes[0].ID, n.Nodes[1].ID, n.Nodes[2].ID}, Transports: []model.Transport{model.UDP}, Methods: model.ConnectionMethods{IPv4Direct: true}}}
	n.GroupLinks = []model.GroupLink{{ID: testutil.ID(101), Node: n.Nodes[3].ID, Group: n.Groups[0].ID, Weight: 7, Enabled: true, Transports: []model.Transport{model.TCP}, Methods: model.ConnectionMethods{IPv4Direct: true}}}
	return s
}

func TestGroupedTopologyRouting(t *testing.T) {
	s := meshFixture()
	n := s.Networks[0]
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(n.EffectiveEdges()) != 6 {
		t.Fatal("missing derived edges")
	}
	for _, agent := range s.Agents {
		snapshot, err := Compile(s, agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err = snapshot.Validate(agent.ID); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := Compile(s, s.Agents[3].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Networks[0].Peers) != 3 {
		t.Fatal("aggregate must connect to every member")
	}
	for _, p := range snap.Networks[0].Peers {
		if p.Edge.Weight != 7 || !slices.Equal(p.Edge.Transports, []model.Transport{model.TCP}) {
			t.Fatal("lost aggregate policy")
		}
	}
	// Only D-A and A-B are available. D-C must remain unreachable: neither a
	// complete group nor an aggregate may resurrect a down child while routing.
	da := model.GroupEdgeID(n.GroupLinks[0].ID, n.Nodes[3].ID, n.Nodes[0].ID)
	ab := model.GroupEdgeID(n.Groups[0].ID, n.Nodes[0].ID, n.Nodes[1].ID)
	now := time.Now()
	reports := []model.AgentStatus{}
	for _, node := range n.Nodes {
		report := model.AgentStatus{AgentID: node.AgentID, Connected: true, LastSeen: now}
		for _, e := range n.EffectiveEdges() {
			if (e.ID == da || e.ID == ab) && (e.A == node.ID || e.B == node.ID) {
				report.Links = append(report.Links, model.LinkStatus{NetworkID: n.ID, EdgeID: e.ID, LinkID: string(e.ID), Transport: e.Transports[0], Healthy: true})
			}
		}
		reports = append(reports, report)
	}
	up := Availability(s, reports, now)
	routes := LiveRoutes(s, snap, up).Networks[0].Routes
	if len(routes) != 2 {
		t.Fatalf("down children reintroduced: %+v", routes)
	}
	for _, r := range routes {
		if r.NextHop != n.Nodes[0].ID || (r.Destination == n.Nodes[1].ID && r.Cost != 8) {
			t.Fatalf("unexpected route: %+v", r)
		}
	}
	// Reordering membership cannot cause session IDs to churn.
	clone := s.Clone()
	slices.Reverse(clone.Networks[0].Groups[0].Members)
	for _, e := range n.EffectiveEdges() {
		if !slices.ContainsFunc(clone.Networks[0].EffectiveEdges(), func(v model.Edge) bool { return v.ID == e.ID }) {
			t.Fatal("unstable edge ID")
		}
	}
	clone.Networks[0].Groups[0].Transports[0] = model.QUIC
	clone.Networks[0].GroupLinks[0].Transports[0] = model.QUIC
	if n.Groups[0].Transports[0] != model.UDP || n.GroupLinks[0].Transports[0] != model.TCP {
		t.Fatal("clone aliases policy")
	}
	clone = s.Clone()
	clone.Networks[0].Nodes = clone.Networks[0].Nodes[2:]
	clone.Networks[0].PruneGroups()
	if len(clone.Networks[0].Groups) != 0 || len(clone.Networks[0].GroupLinks) != 0 {
		t.Fatal("orphaned group or aggregate after deletion")
	}
}

func TestGroupedTopologyRejectsConflicts(t *testing.T) {
	cases := map[string]func(*model.Network){
		"explicit internal edge":    func(n *model.Network) { n.Edges = []model.Edge{n.EffectiveEdges()[0]} },
		"individual with aggregate": func(n *model.Network) { n.Edges = []model.Edge{n.EffectiveEdges()[3]} },
		"duplicate member":          func(n *model.Network) { n.Groups[0].Members[1] = n.Groups[0].Members[0] },
		"unknown member":            func(n *model.Network) { n.Groups[0].Members[0] = testutil.ID(999) },
		"singleton":                 func(n *model.Network) { n.Groups[0].Members = n.Groups[0].Members[:1] },
		"self group":                func(n *model.Network) { n.GroupLinks[0].Node = n.Groups[0].Members[0] },
		"missing group":             func(n *model.Network) { n.GroupLinks[0].Group = testutil.ID(999) },
		"duplicate aggregate": func(n *model.Network) {
			l := n.GroupLinks[0]
			l.ID = testutil.ID(102)
			n.GroupLinks = append(n.GroupLinks, l)
		},
		"invalid group policy":     func(n *model.Network) { n.Groups[0].Transports = nil },
		"invalid aggregate policy": func(n *model.Network) { n.GroupLinks[0].Methods.HolePunchExtension = true },
		"oversized mesh":           func(n *model.Network) { n.Groups[0].Members = make([]model.ID, 10000) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s := meshFixture()
			change(&s.Networks[0])
			if s.Validate() == nil {
				t.Fatal("accepted invalid topology")
			}
		})
	}
}
