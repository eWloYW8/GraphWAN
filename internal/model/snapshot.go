package model

import "net/netip"

// Snapshot is the durable, least-privilege configuration sent to one Agent.
// An empty Networks list actively removes all previous memberships.
type Snapshot struct {
	Schema     int             `json:"schema"`
	Revision   uint64          `json:"revision"`
	AgentID    ID              `json:"agent_id"`
	ListenPort uint16          `json:"listen_port"`
	Networks   []NetworkConfig `json:"networks"`
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
