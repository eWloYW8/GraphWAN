package model

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
	"net/netip"
)

// Validate checks a compiled snapshot before an Agent persists or applies it.
// It validates forwarding references without needing the controller's full graph.
func (s Snapshot) Validate(agentID ID) error {
	if s.Servers != nil {
		if err := s.Servers.Validate(); err != nil {
			return err
		}
		if s.Servers.Revision != s.Revision {
			return fmt.Errorf("server directory revision mismatch")
		}
	}
	if s.Schema != SchemaVersion {
		return fmt.Errorf("unsupported snapshot schema %d", s.Schema)
	}
	if s.AgentID != agentID {
		return fmt.Errorf("snapshot is for a different agent")
	}
	if err := s.AgentID.Validate(); err != nil {
		return err
	}
	if s.ListenPort == 0 {
		return fmt.Errorf("listen port is zero")
	}
	if err := ValidateSTUNServers(s.STUNServers); err != nil {
		return err
	}
	if err := validateEndpoints(s.Endpoints); err != nil {
		return err
	}
	ids := map[ID]bool{}
	addID := func(id ID) error {
		if err := id.Validate(); err != nil {
			return err
		}
		if ids[id] {
			return fmt.Errorf("duplicate topology ID %s", id)
		}
		ids[id] = true
		return nil
	}
	prefixes := []netip.Prefix{}
	identities := map[ID][]byte{}
	for _, n := range s.Networks {
		if err := addID(n.ID); err != nil {
			return err
		}
		if err := validateName(n.Name); err != nil {
			return err
		}
		if !n.CIDR.IsValid() || n.CIDR != n.CIDR.Masked() || n.CIDR.Addr().Is4In6() || n.CIDR.Addr().IsMulticast() || n.CIDR.Bits() == 0 {
			return fmt.Errorf("invalid network prefix")
		}
		for _, p := range prefixes {
			if p.Overlaps(n.CIDR) {
				return fmt.Errorf("overlapping network prefixes")
			}
		}
		prefixes = append(prefixes, n.CIDR)
		if n.MTU < DefaultMTU || n.MTU > MaxMTU || !n.Cipher.Valid() {
			return fmt.Errorf("invalid MTU or cipher")
		}
		if n.Self.AgentID != agentID {
			return fmt.Errorf("network self membership belongs to another agent")
		}
		if err := validateName(n.Self.Name); err != nil {
			return err
		}
		if len(n.Directory) > MaxNodes || len(n.Peers) > MaxNodes || len(n.Routes) > MaxNodes {
			return fmt.Errorf("compiled topology exceeds limits")
		}
		directory := map[ID]netip.Addr{}
		addresses := map[netip.Addr]bool{}
		for _, d := range n.Directory {
			if err := addID(d.NodeID); err != nil {
				return err
			}
			if err := validateVirtualAddress(n.CIDR, d.Address); err != nil {
				return err
			}
			if addresses[d.Address] {
				return fmt.Errorf("duplicate virtual address")
			}
			addresses[d.Address] = true
			directory[d.NodeID] = d.Address
		}
		if addr, ok := directory[n.Self.ID]; !ok || addr != n.Self.Address {
			return fmt.Errorf("self is absent from address directory")
		}
		peers := map[ID]Peer{}
		members := map[ID]bool{agentID: true}
		for _, p := range n.Peers {
			if p.Node.ID == n.Self.ID || peers[p.Node.ID].Node.ID != "" {
				return fmt.Errorf("duplicate or self peer")
			}
			if addr, ok := directory[p.Node.ID]; !ok || addr != p.Node.Address {
				return fmt.Errorf("peer is absent from address directory")
			}
			if err := p.Node.AgentID.Validate(); err != nil {
				return err
			}
			if members[p.Node.AgentID] {
				return fmt.Errorf("duplicate peer agent")
			}
			members[p.Node.AgentID] = true
			if err := validateName(p.Node.Name); err != nil {
				return err
			}
			if len(p.PublicKey) != ed25519.PublicKeySize {
				return fmt.Errorf("invalid peer identity")
			}
			if prior, ok := identities[p.Node.AgentID]; ok && !bytes.Equal(prior, p.PublicKey) {
				return fmt.Errorf("conflicting peer identities")
			}
			identities[p.Node.AgentID] = p.PublicKey
			if err := validateEndpoints(p.Endpoints); err != nil {
				return err
			}
			e := p.Edge
			if err := addID(e.ID); err != nil {
				return err
			}
			if !e.Enabled || e.Weight == 0 || !(e.A == n.Self.ID && e.B == p.Node.ID || e.B == n.Self.ID && e.A == p.Node.ID) {
				return fmt.Errorf("invalid adjacent edge")
			}
			if !e.Methods.IPv4Direct && !e.Methods.IPv6Direct && !e.Methods.HolePunch {
				return fmt.Errorf("edge has no connection methods")
			}
			if len(e.PreferredCandidate) > 256 || len(e.Transports) == 0 {
				return fmt.Errorf("invalid edge transport policy")
			}
			transports := map[Transport]bool{}
			for _, t := range e.Transports {
				if !t.Valid() || transports[t] {
					return fmt.Errorf("invalid edge transports")
				}
				transports[t] = true
			}
			peers[p.Node.ID] = p
		}
		routes := map[ID]bool{}
		for _, r := range n.Routes {
			if _, ok := directory[r.Destination]; !ok || r.Destination == n.Self.ID || routes[r.Destination] {
				return fmt.Errorf("invalid route destination")
			}
			peer, ok := peers[r.NextHop]
			if !ok || r.Cost < uint64(peer.Edge.Weight) || r.Cost > uint64(MaxNodes)*uint64(^uint32(0)) {
				return fmt.Errorf("invalid next hop or route cost")
			}
			if r.Destination == r.NextHop && r.Cost != uint64(peer.Edge.Weight) {
				return fmt.Errorf("invalid direct route cost")
			}
			routes[r.Destination] = true
		}
		for id := range peers {
			if !routes[id] {
				return fmt.Errorf("adjacent peer has no route")
			}
		}
	}
	return nil
}

func validateEndpoints(endpoints []Endpoint) error {
	if len(endpoints) > MaxEndpoints {
		return fmt.Errorf("too many endpoints")
	}
	ids := map[ID]bool{}
	urls := map[string]bool{}
	for _, e := range endpoints {
		if err := e.Validate(); err != nil {
			return err
		}
		if ids[e.ID] || urls[e.URL] {
			return fmt.Errorf("duplicate endpoint")
		}
		ids[e.ID] = true
		urls[e.URL] = true
	}
	return nil
}

func validateVirtualAddress(prefix netip.Prefix, a netip.Addr) error {
	if !a.IsValid() || a.Zone() != "" || a.Is4In6() || a.IsUnspecified() || a.IsMulticast() || !prefix.Contains(a) {
		return fmt.Errorf("virtual address must be unicast within network CIDR")
	}
	if a.Is4() && prefix.Bits() <= 30 {
		b := a.As4()
		base := prefix.Addr().As4()
		ip := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
		start := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
		if ip == start || ip == start|uint32((uint64(1)<<uint(32-prefix.Bits()))-1) {
			return fmt.Errorf("virtual address is network or broadcast address")
		}
	}
	return nil
}
