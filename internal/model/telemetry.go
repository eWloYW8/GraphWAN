package model

import "time"

type LinkStatus struct {
	RTTValid           bool      `json:"rtt_valid,omitempty"`
	RTTMeasuredAt      time.Time `json:"rtt_measured_at,omitempty"`
	WireGuardPublicKey string    `json:"wireguard_public_key,omitempty"`
	LastHandshake      time.Time `json:"last_handshake,omitempty"`
	NetworkID          ID        `json:"network_id"`
	EdgeID             ID        `json:"edge_id"`
	LinkID             string    `json:"link_id"`
	CandidateID        string    `json:"candidate_id"`
	Transport          Transport `json:"transport"`
	Local              string    `json:"local,omitempty"`
	ObservedLocal      string    `json:"observed_local,omitempty"`
	Remote             string    `json:"remote"`
	Healthy            bool      `json:"healthy"`
	Active             bool      `json:"active"`
	RTTMillis          float64   `json:"rtt_ms"`
	Loss               float64   `json:"loss"`
	RXBytes            uint64    `json:"rx_bytes"`
	TXBytes            uint64    `json:"tx_bytes"`
}
type AgentReport struct {
	Update          *UpdateStatus  `json:"update,omitempty"`
	Resources       *ResourceUsage `json:"resources,omitempty"`
	Version         string         `json:"version"`
	AppliedRevision uint64         `json:"applied_revision"`
	RoutingHash     string         `json:"routing_hash,omitempty"`
	ConfigError     string         `json:"config_error,omitempty"`
	RuntimeError    string         `json:"runtime_error,omitempty"`
	Links           []LinkStatus   `json:"links"`
}
type AgentStatus struct {
	AgentID   ID        `json:"agent_id"`
	Connected bool      `json:"connected"`
	LastSeen  time.Time `json:"last_seen"`
	AgentReport
}

// ControlMessage is a versioned envelope carried on an authenticated WebSocket.
// Type is config, routes, heartbeat, ack, endpoints, update or error. Configuration is immutable
// desired state; ACK describes runtime application, not merely receipt.
type ControlMessage struct {
	Update    *UpdateRequest `json:"update,omitempty"`
	Type      string         `json:"type"`
	Snapshot  *Snapshot      `json:"snapshot,omitempty"`
	Routes    *RouteUpdate   `json:"routes,omitempty"`
	Report    *AgentReport   `json:"report,omitempty"`
	Endpoints []Endpoint     `json:"endpoints,omitempty"`
	Error     string         `json:"error,omitempty"`
}
