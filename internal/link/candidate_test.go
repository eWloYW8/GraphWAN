package link_test

import (
	"slices"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/link"
	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/routing"
	"github.com/graphwan/graphwan/internal/testutil"
)

func TestCandidatePolicyAndStableIdentity(t *testing.T) {
	snapshot, err := routing.Compile(testutil.Topology(), testutil.ID(10))
	if err != nil {
		t.Fatal(err)
	}
	peer := snapshot.Networks[0].Peers[0]
	peer.Endpoints = []model.Endpoint{
		{ID: testutil.ID(90), Transport: model.UDP, Source: model.Interface, URL: "udp://192.0.2.1:24752"},
		{ID: testutil.ID(91), Transport: model.UDP, Source: model.Interface, URL: "udp://[2001:db8::1]:24752"},
		{ID: testutil.ID(92), Transport: model.UDP, Source: model.Observed, URL: "udp://198.51.100.1:40000", ExpiresAt: time.Now().Add(time.Hour)},
		{ID: testutil.ID(93), Transport: model.WSS, Source: model.Manual, URL: "wss://example.com:443/overlay"},
	}
	peer.Edge.Transports = []model.Transport{model.UDP, model.WSS}
	peer.Edge.Methods = model.ConnectionMethods{IPv6Direct: true, HolePunch: true}
	got := link.Candidates(snapshot.Networks[0].Self.ID, peer, time.Now())
	if len(got) != 5 {
		t.Fatalf("unexpected candidate count %d: %+v", len(got), got)
	}
	for _, c := range got {
		if c.Method == link.Direct && c.Family != 6 {
			t.Fatal("disabled IPv4 direct candidate generated")
		}
		if c.Endpoint.Transport == model.WSS && c.Method == link.Punch {
			t.Fatal("automatic WSS punching generated")
		}
	}
	ids := map[string]bool{}
	for _, c := range got {
		if ids[c.ID] {
			t.Fatal("duplicate candidate ID")
		}
		ids[c.ID] = true
	}
	slices.Reverse(peer.Endpoints)
	repeated := link.Candidates(snapshot.Networks[0].Self.ID, peer, time.Now())
	for i := range got {
		if got[i].ID != repeated[i].ID {
			t.Fatal("endpoint order changed identity or scheduling order")
		}
	}
	for i := range peer.Endpoints {
		if peer.Endpoints[i].Source == model.Observed {
			peer.Endpoints[i].ExpiresAt = time.Now().Add(-time.Second)
		}
	}
	if len(link.Candidates(snapshot.Networks[0].Self.ID, peer, time.Now())) != len(got)-1 {
		t.Fatal("expired mapping remained dialable")
	}
}
