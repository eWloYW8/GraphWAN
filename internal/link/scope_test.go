package link_test

import (
	"net/netip"
	"testing"

	"github.com/graphwan/graphwan/internal/link"
	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/testutil"
)

func TestLinkLocalCandidateScopes(t *testing.T) {
	remote := model.Endpoint{ID: testutil.ID(90), Source: model.Interface, Transport: model.TCP, URL: "tcp://[fe80::2%25remote-nic]:24752"}
	base := link.Candidate{ID: link.CandidateID(testutil.ID(1), testutil.ID(2), remote.ID, 6, link.Direct), Endpoint: remote, Family: 6, Method: link.Direct}
	if _, err := base.DialTarget(); err == nil {
		t.Fatal("remote interface name accepted as local scope")
	}
	local := model.Endpoint{ID: testutil.ID(91), Source: model.Interface, Transport: model.UDP, URL: "udp://[fe80::1%25local-nic]:24752"}
	scoped, err := link.ScopeCandidate(base, local)
	if err != nil {
		t.Fatal(err)
	}
	target, err := scoped.DialTarget()
	if err != nil || target != netip.MustParseAddr("fe80::2%local-nic") || scoped.Endpoint != remote || scoped.Target.IsValid() {
		t.Fatalf("scope translation: %+v %v", scoped, err)
	}
	repeated, err := link.ScopeCandidate(base, local)
	if err != nil || scoped.ID != repeated.ID || scoped.ID == base.ID {
		t.Fatal("unstable scoped identity")
	}
	local.URL = "udp://[fe80::1%25other-nic]:24752"
	other, err := link.ScopeCandidate(base, local)
	if err != nil || other.ID == scoped.ID {
		t.Fatal("different interface alias reused a candidate identity")
	}
	if _, err := link.ScopeCandidate(scoped, local); err == nil {
		t.Fatal("candidate scoped twice")
	}
	for _, raw := range []string{"udp://[fe80::1]:24752", "udp://[fd42::1%25local-nic]:24752", "udp://192.0.2.1:24752"} {
		local.URL = raw
		if _, err := link.ScopeCandidate(base, local); err == nil {
			t.Fatalf("invalid scope admitted: %s", raw)
		}
	}
	local = scoped.Scope
	base.Endpoint.URL = "tcp://[2001:db8::2]:24752"
	if _, err := link.ScopeCandidate(base, local); err == nil {
		t.Fatal("global candidate accepted scope")
	}
	base.Endpoint.Source = model.Manual
	base.Endpoint.URL = "tcp://peer.example.test:24752"
	dns, err := link.ResolveCandidate(base, netip.MustParseAddr("fe80::2"))
	if err != nil {
		t.Fatal(err)
	}
	scoped, err = link.ScopeCandidate(dns, local)
	if err != nil {
		t.Fatal(err)
	}
	target, err = scoped.DialTarget()
	if err != nil || target.String() != "fe80::2%local-nic" || scoped.Target.String() != "fe80::2" {
		t.Fatal("DNS answer was replaced by socket-local scope")
	}
	if _, err := link.ResolveCandidate(scoped, netip.MustParseAddr("fe80::3")); err == nil {
		t.Fatal("scoped DNS candidate accepted another answer")
	}
}
