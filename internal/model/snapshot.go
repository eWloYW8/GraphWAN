package model

import "net/netip"

// Snapshot is the durable, least-privilege configuration sent to one Agent.
// An empty Networks list actively removes all previous memberships.
type Snapshot struct {
	Schema              int             `json:"schema"`
	Revision            uint64          `json:"revision"`
	AgentID             ID              `json:"agent_id"`
	ListenPort          uint16          `json:"listen_port"`
	Endpoints           []Endpoint      `json:"endpoints"`
	STUNServers         []string        `json:"stun_servers,omitempty"`
	ExcludeContainerIPs bool            `json:"exclude_container_ips,omitempty"`
	Networks            []NetworkConfig `json:"networks"`
}

type NetworkConfig struct {
	ID        ID            `json:"id"`
	Name      string        `json:"name"`
	CIDR      netip.Prefix  `json:"cidr"`
	MTU       int           `json:"mtu"`
	Cipher    CipherSuite   `json:"cipher"`
	Self      Node          `json:"self"`
	Directory []Destination `json:"directory"`
	Peers     []Peer        `json:"peers"`
	Routes    []Route       `json:"routes"`
}

type Destination struct {
	NodeID  ID         `json:"node_id"`
	Address netip.Addr `json:"address"`
}

type Peer struct {
	Node      Node       `json:"node"`
	PublicKey []byte     `json:"public_key"`
	Endpoints []Endpoint `json:"endpoints"`
	Edge      Edge       `json:"edge"`
}

type Route struct {
	Destination ID     `json:"destination"`
	NextHop     ID     `json:"next_hop"`
	Cost        uint64 `json:"cost"`
}

// Clone gives each runtime configuration its own backing slices.
func (s Snapshot) Clone() Snapshot {
	out := s
	out.Endpoints = append([]Endpoint{}, s.Endpoints...)
	out.STUNServers = append([]string(nil), s.STUNServers...)
	out.Networks = append([]NetworkConfig{}, s.Networks...)
	for i := range out.Networks {
		n := &out.Networks[i]
		n.Directory = append([]Destination{}, n.Directory...)
		n.Routes = append([]Route{}, n.Routes...)
		n.Peers = append([]Peer{}, n.Peers...)
		for j := range n.Peers {
			p := &n.Peers[j]
			p.PublicKey = append([]byte{}, p.PublicKey...)
			p.Endpoints = append([]Endpoint{}, p.Endpoints...)
			p.Edge.Transports = append([]Transport{}, p.Edge.Transports...)
		}
	}
	return out
}
