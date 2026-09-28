// Package link manages an Edge's connection candidates and healthy transports.
package link

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"net/url"
	"slices"
	"time"

	"github.com/graphwan/graphwan/internal/model"
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
}

// CandidateID identifies a dialing direction and endpoint policy, independently
// of live sockets, RTT samples, temporary NAT ports and handshake session keys.
func CandidateID(edge, initiator model.ID, endpoint model.ID, family int, method Method) string {
	raw := []byte(string(edge) + "/" + string(initiator) + "/" + string(endpoint) + "/" + string(rune('0'+family)) + "/" + string(method))
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

// Candidates enumerates every allowed remote endpoint/family/method combination.
// DNS endpoints retain distinct v4/v6 candidates so family policy is enforced
// when resolving/dialing, instead of depending on the resolver's first answer.
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
			if direct && endpoint.Source != model.Observed {
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
