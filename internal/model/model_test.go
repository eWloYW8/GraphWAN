package model_test

import (
	"net/netip"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/testutil"
)

func TestValidateTopology(t *testing.T) {
	if err := testutil.Topology().Validate(); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*model.State){
		"duplicate ID":        func(s *model.State) { s.Networks[0].ID = s.Agents[0].ID },
		"zero ID":             func(s *model.State) { s.Networks[0].ID = "00000000000000000000000000000000" },
		"duplicate identity":  func(s *model.State) { s.Agents[1].PublicKey = s.Agents[0].PublicKey },
		"bad key":             func(s *model.State) { s.Agents[0].PublicKey = nil },
		"missing agent":       func(s *model.State) { s.Networks[0].Nodes[0].AgentID = testutil.ID(999) },
		"repeated membership": func(s *model.State) { s.Networks[0].Nodes[1].AgentID = s.Networks[0].Nodes[0].AgentID },
		"duplicate IP":        func(s *model.State) { s.Networks[0].Nodes[1].Address = s.Networks[0].Nodes[0].Address },
		"out of subnet":       func(s *model.State) { s.Networks[0].Nodes[0].Address = netip.MustParseAddr("10.43.0.1") },
		"broadcast":           func(s *model.State) { s.Networks[0].Nodes[0].Address = netip.MustParseAddr("10.42.0.255") },
		"network address":     func(s *model.State) { s.Networks[0].Nodes[0].Address = netip.MustParseAddr("10.42.0.0") },
		"noncanonical prefix": func(s *model.State) { s.Networks[0].CIDR = netip.MustParsePrefix("10.42.0.1/24") },
		"duplicate edge": func(s *model.State) {
			e := s.Networks[0].Edges[0]
			e.ID = testutil.ID(99)
			e.A, e.B = e.B, e.A
			s.Networks[0].Edges = append(s.Networks[0].Edges, e)
		},
		"self edge":      func(s *model.State) { s.Networks[0].Edges[0].B = s.Networks[0].Edges[0].A },
		"dangling edge":  func(s *model.State) { s.Networks[0].Edges[0].B = testutil.ID(999) },
		"zero weight":    func(s *model.State) { s.Networks[0].Edges[0].Weight = 0 },
		"unknown cipher": func(s *model.State) { s.Networks[0].Cipher = "none" },
		"invalid MTU":    func(s *model.State) { s.Networks[0].MTU = 65535 },
		"automatic websocket": func(s *model.State) {
			s.Agents[0].Endpoints[0].Source = model.Interface
			s.Agents[0].Endpoints[0].Transport = model.WSS
		},
		"missing expiry": func(s *model.State) { s.Agents[0].Endpoints[0].Source = model.Observed },
		"overlapping networks": func(s *model.State) {
			n := s.Networks[0]
			n.ID = testutil.ID(99)
			n.Edges = nil
			n.Nodes = []model.Node{n.Nodes[0]}
			n.Nodes[0].ID = testutil.ID(98)
			s.Networks = append(s.Networks, n)
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			s := testutil.Topology()
			change(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("accepted invalid state")
			}
		})
	}
}

func TestEndpointValidation(t *testing.T) {
	tests := []struct {
		url       string
		transport model.Transport
		source    model.EndpointSource
		valid     bool
	}{
		{"wss://node.example.com:443/overlay", model.WSS, model.Manual, true},
		{"grpc://node.example.com:443/overlay", model.GRPC, model.Manual, true},
		{"udp://[2001:db8::1]:24752", model.UDP, model.Interface, true},
		{"tcp://[fe80::1%25eth0]:24752", model.TCP, model.Interface, true},
		{"udp://192.0.2.1:41234", model.UDP, model.Observed, true},
		{"udp://0.0.0.0:24752", model.UDP, model.Manual, false},
		{"udp://224.0.0.1:24752", model.UDP, model.Manual, false},
		{"tcp://node.example.com:0", model.TCP, model.Manual, false},
		{"tcp://node.example.com:65536", model.TCP, model.Manual, false},
		{"wss://node.example.com/overlay", model.WSS, model.Manual, false},
		{"udp://192.0.2.1:24752/path", model.UDP, model.Manual, false},
		{"wss://user:pass@example.com:443/", model.WSS, model.Manual, false},
		{"wss://example.com:443/?token=abc", model.WSS, model.Manual, false},
		{"udp://example.com:24752", model.UDP, model.Interface, false},
		{"udp://bad_host:24752", model.UDP, model.Manual, false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			e := model.Endpoint{ID: testutil.ID(1), URL: tt.url, Transport: tt.transport, Source: tt.source, ExpiresAt: time.Now().Add(time.Hour)}
			err := e.Validate()
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v error=%v", tt.valid, err)
			}
		})
	}
}

func TestCloneIsIndependent(t *testing.T) {
	s := testutil.Topology()
	c := s.Clone()
	c.Agents[0].PublicKey[0] ^= 255
	c.Agents[0].Endpoints[0].URL = "changed"
	c.Networks[0].Nodes[0].Name = "changed"
	c.Networks[0].Edges[0].Transports[0] = model.WSS
	if s.Agents[0].PublicKey[0] == c.Agents[0].PublicKey[0] || s.Agents[0].Endpoints[0].URL == "changed" || s.Networks[0].Nodes[0].Name == "changed" || s.Networks[0].Edges[0].Transports[0] == model.WSS {
		t.Fatal("clone aliases original")
	}
}
