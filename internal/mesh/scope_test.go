package mesh

import (
	"net"
	"net/netip"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/link"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
	"github.com/eWloYW8/GraphWAN/internal/transport"
)

func TestScopeAdmissionAndPolicyRevocation(t *testing.T) {
	a := model.Endpoint{ID: testutil.ID(90), Source: model.Interface, Transport: model.UDP, URL: "udp://[fe80::1%25alice-nic]:24752"}
	b := model.Endpoint{ID: testutil.ID(91), Source: model.Interface, Transport: model.UDP, URL: "udp://[fe80::2%25bob-nic]:24752"}
	other := a
	other.ID = testutil.ID(92)
	other.URL = "udp://[fe80::1%25alice-other]:24752"
	edge := testutil.Topology().Networks[0].Edges[0]
	edge.Enabled = true
	edge.Transports = []model.Transport{model.UDP}
	edge.Methods = model.ConnectionMethods{IPv6Direct: true}
	cfg := &policy{self: edge.A, endpoints: []model.Endpoint{a, other}, peer: model.Peer{Node: model.Node{ID: edge.B}, Edge: edge, Endpoints: []model.Endpoint{b}}}
	base := link.Candidates(cfg.self, cfg.peer, time.Now())[0]
	candidates := scopedCandidates([]link.Candidate{base}, cfg.endpoints)
	if len(candidates) != 2 || candidates[0].ID == candidates[1].ID {
		t.Fatal("not all local interfaces enumerated")
	}
	if len(scopedCandidates([]link.Candidate{base}, nil)) != 0 {
		t.Fatal("unscoped link-local dial admitted")
	}
	reverse := &policy{self: edge.B, endpoints: []model.Endpoint{b}, peer: model.Peer{Node: model.Node{ID: edge.A}, Edge: edge, Endpoints: []model.Endpoint{a, other}}}
	for _, c := range candidates {
		if !candidateConfigured(cfg, c, false) || !candidateConfigured(reverse, c, false) {
			t.Fatal("endpoints disagree about candidate policy")
		}
		intro, ok := introducedScope(base, link.ScopeIdentity(c.Scope), reverse.peer.Endpoints)
		if !ok || intro.ID != c.ID {
			t.Fatal("introduction changed identity")
		}
	}
	if _, ok := introducedScope(base, "", cfg.endpoints); ok {
		t.Fatal("missing scope accepted")
	}
	if _, ok := introducedScope(base, link.ScopeIdentity(b), cfg.endpoints); ok {
		t.Fatal("responder scope accepted as initiator scope")
	}
	cfg.endpoints = []model.Endpoint{other}
	reverse.peer.Endpoints = cfg.endpoints
	if candidateConfigured(cfg, candidates[0], true) || candidateConfigured(reverse, candidates[0], true) {
		t.Fatal("removed scope retained a healthy session")
	}
	if !candidateConfigured(cfg, candidates[1], true) {
		t.Fatal("unaffected scope revoked")
	}
	cfg.endpoints = slices.Clone(cfg.endpoints)
	cfg.endpoints[0].URL = a.URL
	if candidateConfigured(cfg, candidates[1], true) {
		t.Fatal("replaced scope accepted stale handshake")
	}
	// DNS withdrawal may retain an authenticated answer, but never a revoked scope.
	base.Endpoint.Source = model.Manual
	base.Endpoint.URL = "udp://peer.example.test:24752"
	dns, err := link.ResolveCandidate(base, netip.MustParseAddr("fe80::2"))
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := link.ScopeCandidate(dns, other)
	if err != nil {
		t.Fatal(err)
	}
	cfg.peer.Endpoints = []model.Endpoint{base.Endpoint}
	cfg.endpoints = []model.Endpoint{other}
	if !candidateConfigured(cfg, scoped, true) {
		t.Fatal("configured DNS scope rejected")
	}
	cfg.endpoints = nil
	if candidateConfigured(cfg, scoped, true) {
		t.Fatal("withdrawn DNS scope survived")
	}
	cfg.endpoints = []model.Endpoint{other}
	cfg.peer.Endpoints = []model.Endpoint{b}
	if !candidateConfigured(cfg, candidates[1], true) {
		t.Fatal("candidate unavailable before method revocation")
	}
	cfg.peer.Edge.Methods.IPv6Direct = false
	if candidateConfigured(cfg, candidates[1], true) {
		t.Fatal("disabled IPv6 method retained scope")
	}
}

type scopedIngressConn struct {
	transport.Conn
	address net.Addr
}

func (c scopedIngressConn) RemoteAddr() net.Addr { return c.address }

func TestScopedIngressUsesRecipientsInterface(t *testing.T) {
	endpoint := model.Endpoint{ID: testutil.ID(90), Source: model.Interface, Transport: model.UDP, URL: "udp://[fe80::2%25receiver]:24752"}
	c := link.Candidate{Endpoint: endpoint, Family: 6}
	for _, test := range []struct {
		remote  string
		allowed bool
	}{
		{"[fe80::1%receiver]:1234", true}, {"[fe80::1%other]:1234", false}, {"[fe80::1]:1234", false}, {"[2001:db8::1]:1234", false}, {"192.0.2.1:1234", false},
	} {
		conn := scopedIngressConn{address: net.UDPAddrFromAddrPort(netip.MustParseAddrPort(test.remote))}
		if candidateIngress(c, conn) != test.allowed {
			t.Fatalf("wrong ingress scope: %s", test.remote)
		}
	}
	interfaces, err := net.Interfaces()
	if err != nil || len(interfaces) == 0 {
		t.Fatalf("read interfaces: %v", err)
	}
	iface := interfaces[0]
	if !sameLocalZone(iface.Name, strconv.Itoa(iface.Index)) {
		t.Fatal("numeric and named scope disagree")
	}
	if sameLocalZone("", "") || sameLocalZone("missing-a", "missing-b") {
		t.Fatal("unknown zones compare equal")
	}
}
