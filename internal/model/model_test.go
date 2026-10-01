package model_test

import (
	"net/netip"
	"testing"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
)

func TestValidateTopology(t *testing.T) {
	if err := testutil.Topology().Validate(); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*model.State){
		"extension without hole punch": func(s *model.State) {
			s.Networks[0].Edges[0].Methods.HolePunch = false
			s.Networks[0].Edges[0].Methods.HolePunchExtension = true
		},
		"invalid gateway mode": func(s *model.State) {
			s.Networks[0].Nodes[0].AdvertisedSubnets = []model.AdvertisedSubnet{{Prefix: netip.MustParsePrefix("192.168.0.0/24"), GatewayMode: "invalid"}}
		},
		"duplicate advertised subnet": func(s *model.State) {
			for i := 0; i < 2; i++ {
				s.Networks[0].Nodes[i].AdvertisedSubnets = []model.AdvertisedSubnet{{Prefix: netip.MustParsePrefix("192.168.0.0/24"), GatewayMode: model.GatewayOff}}
			}
		},
		"advertised overlay subnet": func(s *model.State) {
			s.Networks[0].Nodes[0].AdvertisedSubnets = []model.AdvertisedSubnet{{Prefix: netip.MustParsePrefix("10.42.0.0/25"), GatewayMode: model.GatewayOff}}
		},
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
