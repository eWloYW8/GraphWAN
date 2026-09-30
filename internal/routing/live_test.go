package routing_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/routing"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
)

func TestLiveRouteWithdrawalAndRecovery(t *testing.T) {
	s := testutil.Topology()
	snap, err := routing.Compile(s, s.Agents[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	reports := make([]model.AgentStatus, len(s.Agents))
	for i, a := range s.Agents {
		reports[i] = model.AgentStatus{AgentID: a.ID, Connected: true, LastSeen: now}
		for _, e := range s.Networks[0].Edges {
			if e.A == s.Networks[0].Nodes[i].ID || e.B == s.Networks[0].Nodes[i].ID {
				reports[i].Links = append(reports[i].Links, model.LinkStatus{NetworkID: s.Networks[0].ID, EdgeID: e.ID, LinkID: string(e.ID), Transport: model.UDP, Healthy: true})
			}
		}
	}
	check := func(cost uint64, hop model.ID) {
		t.Helper()
		update := routing.LiveRoutes(s, snap, routing.Availability(s, reports, now))
		next, err := update.Apply(snap)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(next.Networks[0].Peers, snap.Networks[0].Peers) {
			t.Fatal("withdrawal removed reconnect peers")
		}
		for _, r := range next.Networks[0].Routes {
			if r.Destination == testutil.ID(22) {
				if uint64(r.Cost) != cost || r.NextHop != hop {
					t.Fatalf("route=%+v", r)
				}
				return
			}
		}
		if cost != 0 {
			t.Fatal("missing route")
		}
	}
	check(20, testutil.ID(21))
	reports[1].Links[0].Healthy = false
	check(31, testutil.ID(23))
	reports[1].Links[0].Healthy = true
	check(20, testutil.ID(21))
	reports[1].Links[0].LinkID = "different session"
	check(31, testutil.ID(23))
	reports[1].Links[0].LinkID = string(s.Networks[0].Edges[0].ID)
	reports[1].LastSeen = now.Add(-46 * time.Second)
	check(31, testutil.ID(23))
	reports[3].Connected = false
	check(0, "")
	update := routing.LiveRoutes(s, snap, nil)
	update.Revision++
	if _, err := update.Apply(snap); err == nil {
		t.Fatal("accepted stale configuration")
	}
}
