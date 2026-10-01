package model

import (
	"crypto/ed25519"
	"fmt"
	"math"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

func validateName(s string) error {
	if strings.TrimSpace(s) == "" || len(s) > 128 {
		return fmt.Errorf("name must contain 1–128 bytes and not be blank")
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return fmt.Errorf("name contains control characters")
		}
	}
	return nil
}

func (e Endpoint) Validate() error {
	if err := e.ID.Validate(); err != nil {
		return err
	}
	if !e.Transport.Valid() || e.Transport == WireGuard {
		return fmt.Errorf("invalid transport %q", e.Transport)
	}
	if e.Source != Interface && e.Source != Observed && e.Source != Manual {
		return fmt.Errorf("invalid endpoint source %q", e.Source)
	}
	if e.Source != Manual && e.Transport != UDP && e.Transport != TCP {
		return fmt.Errorf("automatic endpoints only support UDP/TCP")
	}
	if len(e.URL) > 2048 {
		return fmt.Errorf("endpoint URL too long")
	}
	u, err := url.Parse(e.URL)
	if err != nil {
		return fmt.Errorf("invalid endpoint URL: %w", err)
	}
	if u.Scheme != string(e.Transport) || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery || u.Opaque != "" {
		return fmt.Errorf("endpoint must be a %s URL without credentials, query or fragment", e.Transport)
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil || port == 0 {
		return fmt.Errorf("endpoint requires an explicit port in 1–65535")
	}
	if e.Transport != WS && e.Transport != WSS && e.Transport != GRPC && u.Path != "" {
		return fmt.Errorf("%s endpoint cannot have a path", e.Transport)
	}
	addr, addrErr := netip.ParseAddr(u.Hostname())
	if addrErr == nil {
		if addr.IsUnspecified() || addr.IsMulticast() || addr.Is4In6() {
			return fmt.Errorf("endpoint must contain a unicast address")
		}
	} else if e.Source != Manual || !validHostname(u.Hostname()) {
		return fmt.Errorf("invalid endpoint hostname")
	}
	if e.Source == Observed && e.ExpiresAt.IsZero() {
		return fmt.Errorf("observed endpoint requires an expiration")
	}
	return nil
}

func validHostname(host string) bool {
	if len(host) > 253 {
		return false
	}
	host = strings.TrimSuffix(host, ".")
	for label := range strings.SplitSeq(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// STUN servers are UDP host:port pairs or explicit udp:// / tcp:// addresses.
// New enrollments receive public defaults; an explicit empty list disables discovery.
func ValidateSTUNServers(servers []string) error {
	if len(servers) > 4 {
		return fmt.Errorf("at most four STUN servers are allowed")
	}
	seen := map[string]bool{}
	for _, server := range servers {
		kind, address, err := ParseSTUNServer(server)
		if err != nil {
			return err
		}
		key := string(kind) + "/" + address
		if seen[key] {
			return fmt.Errorf("duplicate STUN server")
		}
		seen[key] = true
	}
	return nil
}

func ParseSTUNServer(server string) (Transport, string, error) {
	kind, address := UDP, server
	if strings.Contains(server, "://") {
		scheme, rest, _ := strings.Cut(server, "://")
		kind, address = Transport(scheme), rest
		if kind != UDP && kind != TCP {
			return "", "", fmt.Errorf("STUN service must use UDP or TCP")
		}
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || len(server) > 300 {
		return "", "", fmt.Errorf("STUN server requires host:port")
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		return "", "", fmt.Errorf("invalid STUN server port")
	}
	ip, err := netip.ParseAddr(host)
	if err == nil {
		if ip.IsUnspecified() || ip.IsMulticast() || ip.Is4In6() || ip.Zone() != "" || ip == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
			return "", "", fmt.Errorf("invalid STUN server address")
		}
		host = ip.String()
	} else if !validHostname(host) {
		return "", "", fmt.Errorf("invalid STUN server hostname")
	}
	return kind, strings.ToLower(net.JoinHostPort(host, strconv.Itoa(int(n)))), nil
}

// Validate rejects ambiguous topology before persistence or compilation.
func (s State) Validate() error {
	if s.ClusterID != "" {
		if err := s.ServerDirectory().Validate(); err != nil {
			return err
		}
	} else if len(s.Servers) != 0 || len(s.ClusterCA) != 0 {
		return fmt.Errorf("server directory without cluster identity")
	}
	if s.Schema != SchemaVersion {
		return fmt.Errorf("unsupported schema %d", s.Schema)
	}
	ids := map[ID]bool{}
	addID := func(id ID) error {
		if err := id.Validate(); err != nil {
			return err
		}
		if ids[id] {
			return fmt.Errorf("duplicate ID %s", id)
		}
		ids[id] = true
		return nil
	}
	agents := map[ID]Agent{}
	keys := map[string]bool{}
	for _, server := range s.Servers {
		if err := addID(server.ID); err != nil {
			return err
		}
		keys[string(server.PublicKey)] = true
		for _, ep := range server.Endpoints {
			if err := addID(ep.ID); err != nil {
				return err
			}
		}
	}
	for _, a := range s.Agents {
		if a.Update != nil {
			if err := a.Update.Validate(); err != nil {
				return err
			}
		}
		if err := addID(a.ID); err != nil {
			return err
		}
		if err := validateName(a.Name); err != nil {
			return fmt.Errorf("agent %s: %w", a.ID, err)
		}
		if len(a.PublicKey) != ed25519.PublicKeySize {
			return fmt.Errorf("agent %s: invalid Ed25519 public key", a.ID)
		}
		if keys[string(a.PublicKey)] {
			return fmt.Errorf("duplicate agent identity")
		}
		keys[string(a.PublicKey)] = true
		if a.ListenPort == 0 {
			return fmt.Errorf("agent %s: listen port is zero", a.ID)
		}
		if err := ValidateSTUNServers(a.STUNServers); err != nil {
			return err
		}
		if len(a.Endpoints) > MaxEndpoints {
			return fmt.Errorf("agent %s: too many endpoints", a.ID)
		}
		urls := map[string]bool{}
		for _, e := range a.Endpoints {
			if err := addID(e.ID); err != nil {
				return err
			}
			if err := e.Validate(); err != nil {
				return fmt.Errorf("agent %s endpoint: %w", a.ID, err)
			}
			if urls[e.URL] {
				return fmt.Errorf("duplicate endpoint URL %q", e.URL)
			}
			urls[e.URL] = true
		}
		agents[a.ID] = a
	}
	memberships := map[ID][]netip.Prefix{}
	wgKeys := map[string]bool{}
	for _, n := range s.Networks {
		if err := addID(n.ID); err != nil {
			return err
		}
		if err := validateName(n.Name); err != nil {
			return fmt.Errorf("network %s: %w", n.ID, err)
		}
		if !n.CIDR.IsValid() || n.CIDR != n.CIDR.Masked() || n.CIDR.Addr().Is4In6() || n.CIDR.Addr().IsMulticast() || n.CIDR.Bits() == 0 {
			return fmt.Errorf("network %s: CIDR must be canonical unicast and not a default route", n.ID)
		}
		if n.MTU < DefaultMTU || n.MTU > MaxMTU {
			return fmt.Errorf("network %s: MTU must be %d–%d", n.ID, DefaultMTU, MaxMTU)
		}
		if !n.Cipher.Valid() {
			return fmt.Errorf("network %s: unsupported cipher", n.ID)
		}
		if len(n.Nodes) > MaxNodes || len(n.Edges) > MaxEdges {
			return fmt.Errorf("network %s: topology exceeds limits", n.ID)
		}
		nodes := map[ID]bool{}
		members := map[ID]bool{}
		addresses := map[netip.Addr]bool{}
		advertised := map[netip.Prefix]bool{}
		for _, node := range n.Nodes {
			if err := validateAdvertised(n.CIDR, node.AdvertisedSubnets, advertised); err != nil {
				return fmt.Errorf("node %s: %w", node.ID, err)
			}
			if err := addID(node.ID); err != nil {
				return err
			}
			if err := validateName(node.Name); err != nil {
				return fmt.Errorf("node %s: %w", node.ID, err)
			}
			if node.WireGuard != nil {
				if node.AgentID != "" || len(node.AdvertisedSubnets) != 0 {
					return fmt.Errorf("WireGuard nodes cannot have an agent identity or advertised subnets")
				}
				if err := node.WireGuard.Validate(); err != nil {
					return err
				}
				if wgKeys[node.WireGuard.PublicKey] {
					return fmt.Errorf("duplicate WireGuard public key")
				}
				wgKeys[node.WireGuard.PublicKey] = true
			} else {
				if _, ok := agents[node.AgentID]; !ok {
					return fmt.Errorf("node %s: unknown agent", node.ID)
				}
				if members[node.AgentID] {
					return fmt.Errorf("agent belongs to a network more than once")
				}
				members[node.AgentID] = true
				for _, prefix := range memberships[node.AgentID] {
					if prefix.Overlaps(n.CIDR) {
						return fmt.Errorf("agent %s has overlapping networks", node.AgentID)
					}
				}
				memberships[node.AgentID] = append(memberships[node.AgentID], n.CIDR)
			}
			a := node.Address
			if err := validateVirtualAddress(n.CIDR, a); err != nil {
				return fmt.Errorf("node %s: %w", node.ID, err)
			}
			if addresses[a] {
				return fmt.Errorf("duplicate virtual address %s", a)
			}
			addresses[a] = true
			if math.IsNaN(node.Position.X) || math.IsNaN(node.Position.Y) || math.IsInf(node.Position.X, 0) || math.IsInf(node.Position.Y, 0) {
				return fmt.Errorf("invalid node position")
			}
			nodes[node.ID] = true
		}
		wgNodes := map[ID]bool{}
		degrees := map[ID]int{}
		for _, node := range n.Nodes {
			wgNodes[node.ID] = node.WireGuard != nil
		}
		pairs := map[[2]ID]bool{}
		for _, e := range n.Edges {
			if err := addID(e.ID); err != nil {
				return err
			}
			if e.A == e.B || !nodes[e.A] || !nodes[e.B] {
				return fmt.Errorf("edge %s: requires two distinct network nodes", e.ID)
			}
			a, b := e.A, e.B
			if a > b {
				a, b = b, a
			}
			pair := [2]ID{a, b}
			if pairs[pair] {
				return fmt.Errorf("duplicate edge between %s and %s", a, b)
			}
			pairs[pair] = true
			degrees[e.A]++
			degrees[e.B]++
			if wgNodes[e.A] || wgNodes[e.B] {
				if wgNodes[e.A] && wgNodes[e.B] {
					return fmt.Errorf("WireGuard nodes require a regular Agent neighbor")
				}
				if len(e.Transports) != 1 || e.Transports[0] != WireGuard || e.PreferredCandidate != "" {
					return fmt.Errorf("WireGuard edges require only the wireguard transport")
				}
			} else {
				for _, t := range e.Transports {
					if t == WireGuard {
						return fmt.Errorf("WireGuard transport requires a WireGuard node")
					}
				}
			}
			if e.Weight == 0 {
				return fmt.Errorf("edge %s: weight must be positive", e.ID)
			}
			if len(e.Transports) == 0 {
				return fmt.Errorf("edge %s: no transports enabled", e.ID)
			}
			transports := map[Transport]bool{}
			for _, t := range e.Transports {
				if !t.Valid() || transports[t] {
					return fmt.Errorf("edge %s: invalid or duplicate transport", e.ID)
				}
				transports[t] = true
			}
			if !e.Methods.IPv4Direct && !e.Methods.IPv6Direct && !e.Methods.HolePunch {
				return fmt.Errorf("edge %s: no connection methods enabled", e.ID)
			}
			if len(e.PreferredCandidate) > 256 {
				return fmt.Errorf("preferred candidate too long")
			}
		}
		for id, wg := range wgNodes {
			if wg && degrees[id] != 1 {
				return fmt.Errorf("WireGuard node must have exactly one Agent connection")
			}
		}
	}
	return nil
}
