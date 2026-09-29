// Package link manages an Edge's connection candidates and healthy transports.
package link

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/netip"
	"net/url"
	"slices"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

type Method string

const (
	Direct Method = "direct"
	Punch  Method = "punch"
)

type Candidate struct {
	ID       string         `json:"id"`
	Endpoint model.Endpoint `json:"endpoint"`
	Family   int            `json:"family"`
	Method   Method         `json:"method"`
	Priority int            `json:"priority"`
	Target   netip.Addr     `json:"target,omitempty"`
	Scope    model.Endpoint `json:"scope,omitempty"`
}

// ResolveCandidate preserves the configured URL (including its TLS hostname)
// while identifying one concrete DNS answer independently of answer order.
// Literal endpoints already have a unique target and keep their original ID.
func ResolveCandidate(base Candidate, address netip.Addr) (Candidate, error) {
	u, err := url.Parse(base.Endpoint.URL)
	if err != nil || base.Endpoint.Source != model.Manual || base.Target.IsValid() || base.Scope.ID != "" {
		return Candidate{}, errors.New("invalid DNS candidate")
	}
	if _, err := netip.ParseAddr(u.Hostname()); err == nil {
		return Candidate{}, errors.New("literal endpoint cannot override its address")
	}
	address = address.Unmap()
	if !address.IsValid() || address.Zone() != "" || address.IsUnspecified() || address.IsMulticast() ||
		(base.Family != 4 && base.Family != 6) || (address.Is4() != (base.Family == 4)) {
		return Candidate{}, errors.New("DNS answer does not match candidate family")
	}
	sum := sha256.Sum256([]byte(base.ID + "/" + address.String()))
	base.ID, base.Target = hex.EncodeToString(sum[:16]), address
	if address.IsPrivate() || address.IsLinkLocalUnicast() {
		base.Priority += 20
	}
	return base, nil
}

// CandidateID identifies a dialing direction and endpoint policy, independently
// of live sockets, RTT samples, temporary NAT ports and handshake session keys.
func CandidateID(edge, initiator model.ID, endpoint model.ID, family int, method Method) string {
	raw := []byte(string(edge) + "/" + string(initiator) + "/" + string(endpoint) + "/" + string(rune('0'+family)) + "/" + string(method))
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

// Candidates enumerates every allowed remote endpoint/family/method combination.
// DNS endpoints retain distinct v4/v6 templates. The Mesh expands each template
// with ResolveCandidate so every answer has its own retry/session lifecycle.
func Candidates(local model.ID, peer model.Peer, now time.Time) []Candidate {
	result := []Candidate{}
	if !peer.Edge.Enabled {
		return result
	}
	for _, endpoint := range peer.Endpoints {
		if endpoint.Validate() != nil || !slices.Contains(peer.Edge.Transports, endpoint.Transport) {
			continue
		}
		if endpoint.Source == model.Observed && !now.Before(endpoint.ExpiresAt) {
			continue
		}
		parsed, _ := url.Parse(endpoint.URL)
		ip, err := netip.ParseAddr(parsed.Hostname())
		families := []int{4, 6}
		if err == nil {
			if ip.Is4() {
				families = []int{4}
			} else {
				families = []int{6}
			}
		}
		for _, family := range families {
			direct := family == 4 && peer.Edge.Methods.IPv4Direct || family == 6 && peer.Edge.Methods.IPv6Direct
			methods := []Method{}
			// STUN describes where an endpoint was observed, not whether it
			// requires punching. Try ordinary dialing under the family policy.
			if direct {
				methods = append(methods, Direct)
			}
			if peer.Edge.Methods.HolePunch && (endpoint.Transport == model.UDP || endpoint.Transport == model.TCP) {
				methods = append(methods, Punch)
			}
			for _, method := range methods {
				priority := 20
				if family == 6 {
					priority = 10
				}
				if ip.IsPrivate() || ip.IsLinkLocalUnicast() {
					priority += 20
				}
				if method == Punch {
					priority += 30
				}
				if endpoint.Transport != model.UDP {
					priority += 50
				}
				if endpoint.Transport != model.UDP && endpoint.Transport != model.TCP {
					priority += 50
				}
				result = append(result, Candidate{ID: CandidateID(peer.Edge.ID, local, endpoint.ID, family, method), Endpoint: endpoint, Family: family, Method: method, Priority: priority})
			}
		}
	}
	slices.SortFunc(result, func(a, b Candidate) int {
		if n := cmp.Compare(a.Priority, b.Priority); n != 0 {
			return n
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return result
}
