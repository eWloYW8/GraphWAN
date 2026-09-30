package model

import "time"

type LinkStatus struct {
	NetworkID     ID        `json:"network_id"`
	EdgeID        ID        `json:"edge_id"`
	LinkID        string    `json:"link_id"`
	CandidateID   string    `json:"candidate_id"`
	Transport     Transport `json:"transport"`
	Local         string    `json:"local,omitempty"`
	ObservedLocal string    `json:"observed_local,omitempty"`
	Remote        string    `json:"remote"`
	Healthy       bool      `json:"healthy"`
	Active        bool      `json:"active"`
	RTTMillis     float64   `json:"rtt_ms"`
	Loss          float64   `json:"loss"`
	RXBytes       uint64    `json:"rx_bytes"`
	TXBytes       uint64    `json:"tx_bytes"`
}
type AgentReport struct {
	Resources       *ResourceUsage `json:"resources,omitempty"`
	Version         string         `json:"version"`
	AppliedRevision uint64         `json:"applied_revision"`
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
// Type is config, heartbeat, ack, endpoints or error. Configuration is immutable
// desired state; ACK describes runtime application, not merely receipt.
type ControlMessage struct {
	Type      string       `json:"type"`
	Snapshot  *Snapshot    `json:"snapshot,omitempty"`
	Report    *AgentReport `json:"report,omitempty"`
	Endpoints []Endpoint   `json:"endpoints,omitempty"`
	Error     string       `json:"error,omitempty"`
}
