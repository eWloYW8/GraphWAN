// Package model defines desired configuration independently of runtime state.
package model

import (
	"net/netip"
	"time"
)

const (
	SchemaVersion = 1
	DefaultPort   = 24752
	DefaultMTU    = 1280
	MaxMTU        = 9000
	MaxNodes      = 10000
	MaxEdges      = 100000
	MaxEndpoints  = 64
)

type Transport string

const (
	UDP  Transport = "udp"
	TCP  Transport = "tcp"
	QUIC Transport = "quic"
	WS   Transport = "ws"
	WSS  Transport = "wss"
	GRPC Transport = "grpc"
)

func (t Transport) Valid() bool {
	switch t {
	case UDP, TCP, QUIC, WS, WSS, GRPC:
		return true
	}
	return false
}

type EndpointSource string

const (
	Interface EndpointSource = "interface"
	Observed  EndpointSource = "observed"
	Manual    EndpointSource = "manual"
)

type Endpoint struct {
	ID        ID        `json:"id"`
	Transport Transport `json:"transport"`
	// URL includes scheme, host, explicit port and (for WS/WSS/gRPC) optional path.
	URL    string         `json:"url"`
	Source EndpointSource `json:"source"`
	// Observed mappings are ephemeral, including their externally mapped port.
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

type Agent struct {
	ID          ID         `json:"id"`
	Name        string     `json:"name"`
	PublicKey   []byte     `json:"public_key"`
	Endpoints   []Endpoint `json:"endpoints"`
	ListenPort  uint16     `json:"listen_port"`
	Revoked     bool       `json:"revoked"`
	STUNServers []string   `json:"stun_servers,omitempty"`
}

type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type Node struct {
	ID       ID         `json:"id"`
	AgentID  ID         `json:"agent_id"`
	Name     string     `json:"name"`
	Address  netip.Addr `json:"address"`
	Position Position   `json:"position"`
}

type ConnectionMethods struct {
	IPv4Direct bool `json:"ipv4_direct"`
	IPv6Direct bool `json:"ipv6_direct"`
	HolePunch  bool `json:"hole_punch"`
}

type Edge struct {
	ID         ID                `json:"id"`
	A          ID                `json:"a"`
	B          ID                `json:"b"`
	Weight     uint32            `json:"weight"`
	Enabled    bool              `json:"enabled"`
	Transports []Transport       `json:"transports"`
	Methods    ConnectionMethods `json:"methods"`
	// PreferredCandidate survives reconnects; an unavailable preference falls back to auto.
	PreferredCandidate string `json:"preferred_candidate,omitempty"`
}

type CipherSuite string

const ChaCha20Poly1305 CipherSuite = "chacha20-poly1305"

type Network struct {
	ID     ID           `json:"id"`
	Name   string       `json:"name"`
	CIDR   netip.Prefix `json:"cidr"`
	MTU    int          `json:"mtu"`
	Cipher CipherSuite  `json:"cipher"`
	Nodes  []Node       `json:"nodes"`
	Edges  []Edge       `json:"edges"`
}

type State struct {
	Schema   int       `json:"schema"`
	Revision uint64    `json:"revision"`
	Agents   []Agent   `json:"agents"`
	Networks []Network `json:"networks"`
}

func EmptyState() State {
	return State{Schema: SchemaVersion, Agents: []Agent{}, Networks: []Network{}}
}

// Clone prevents callers from retaining references into shared mutable state.
func (s State) Clone() State {
	out := s
	out.Agents = append([]Agent{}, s.Agents...)
	for i := range out.Agents {
		out.Agents[i].PublicKey = append([]byte(nil), s.Agents[i].PublicKey...)
		out.Agents[i].Endpoints = append([]Endpoint{}, s.Agents[i].Endpoints...)
		out.Agents[i].STUNServers = append([]string(nil), s.Agents[i].STUNServers...)
	}
	out.Networks = append([]Network{}, s.Networks...)
	for i := range out.Networks {
		n := &out.Networks[i]
		n.Nodes = append([]Node{}, n.Nodes...)
		n.Edges = append([]Edge{}, n.Edges...)
		for j := range n.Edges {
			n.Edges[j].Transports = append([]Transport{}, n.Edges[j].Transports...)
		}
	}
	return out
}
