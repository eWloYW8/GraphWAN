package link_test

import (
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/link"
	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/routing"
	"github.com/graphwan/graphwan/internal/testutil"
)

func TestResolvedCandidateIdentityAndAdmission(t *testing.T) {
	endpoint := model.Endpoint{ID: testutil.ID(90), Source: model.Manual, Transport: model.WSS, URL: "wss://peer.example.test:443/path"}
	base := link.Candidate{ID: link.CandidateID(testutil.ID(1), testutil.ID(2), endpoint.ID, 4, link.Direct), Endpoint: endpoint, Family: 4, Method: link.Direct}
	one, err := link.ResolveCandidate(base, netip.MustParseAddr("192.0.2.1"))
	if err != nil {
		t.Fatal(err)
	}
	two, err := link.ResolveCandidate(base, netip.MustParseAddr("192.0.2.2"))
	if err != nil || one.ID == two.ID || one.ID == base.ID || one.Endpoint != endpoint {
		t.Fatal("DNS target identity or original endpoint lost")
	}
	repeated, err := link.ResolveCandidate(base, netip.MustParseAddr("::ffff:192.0.2.1"))
	if err != nil || repeated.ID != one.ID || repeated.Target != one.Target {
		t.Fatal("equivalent DNS answer changed identity")
	}
	for _, raw := range []string{"::1", "0.0.0.0", "224.0.0.1", "fe80::1%lo"} {
		if _, err := link.ResolveCandidate(base, netip.MustParseAddr(raw)); err == nil {
			t.Fatalf("invalid target admitted: %s", raw)
		}
	}
	if _, err := link.ResolveCandidate(one, two.Target); err == nil {
		t.Fatal("resolved candidate accepted a second override")
	}
	base.Endpoint.URL = "wss://192.0.2.1:443/path"
	if _, err := link.ResolveCandidate(base, one.Target); err == nil {
		t.Fatal("literal candidate accepted DNS introduction")
	}
}

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
