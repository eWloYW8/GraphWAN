package model_test

import (
	"net/netip"
	"testing"

	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/routing"
	"github.com/graphwan/graphwan/internal/testutil"
)

func TestCompiledSnapshotValidation(t *testing.T) {
	s := testutil.Topology()
	for _, a := range s.Agents {
		snapshot, err := routing.Compile(s, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := snapshot.Validate(a.ID); err != nil {
			t.Fatal(err)
		}
	}
	original, err := routing.Compile(s, s.Agents[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*model.Snapshot){
		"wrong agent":           func(s *model.Snapshot) { s.AgentID = testutil.ID(999) },
		"wrong schema":          func(s *model.Snapshot) { s.Schema++ },
		"wrong self membership": func(s *model.Snapshot) { s.Networks[0].Self.AgentID = testutil.ID(999) },
		"missing self":          func(s *model.Snapshot) { s.Networks[0].Directory = s.Networks[0].Directory[1:] },
		"conflicting address":   func(s *model.Snapshot) { s.Networks[0].Directory[1].Address = s.Networks[0].Self.Address },
		"outside subnet":        func(s *model.Snapshot) { s.Networks[0].Directory[1].Address = netip.MustParseAddr("192.0.2.1") },
		"unknown next hop":      func(s *model.Snapshot) { s.Networks[0].Routes[0].NextHop = testutil.ID(999) },
		"unknown destination":   func(s *model.Snapshot) { s.Networks[0].Routes[0].Destination = testutil.ID(999) },
		"duplicate route":       func(s *model.Snapshot) { s.Networks[0].Routes = append(s.Networks[0].Routes, s.Networks[0].Routes[0]) },
		"route below edge cost": func(s *model.Snapshot) { s.Networks[0].Routes[0].Cost = 1 },
		"disabled peer":         func(s *model.Snapshot) { s.Networks[0].Peers[0].Edge.Enabled = false },
		"unrelated edge":        func(s *model.Snapshot) { s.Networks[0].Peers[0].Edge.A = testutil.ID(999) },
		"bad peer key":          func(s *model.Snapshot) { s.Networks[0].Peers[0].PublicKey = nil },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			snapshot := original.Clone()
			change(&snapshot)
			if err := snapshot.Validate(original.AgentID); err == nil {
				t.Fatal("accepted invalid snapshot")
			}
		})
	}
	if err := original.Validate(original.AgentID); err != nil {
		t.Fatal("clone aliased original", err)
	}
}
