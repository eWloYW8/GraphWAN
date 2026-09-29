package mesh

import (
	"net"
	"strconv"

	"github.com/graphwan/graphwan/internal/link"
	"github.com/graphwan/graphwan/internal/model"
)

func scopedCandidates(bases []link.Candidate, endpoints []model.Endpoint) []link.Candidate {
	result := make([]link.Candidate, 0, len(bases))
	for _, base := range bases {
		if !base.NeedsScope() {
			result = append(result, base)
			continue
		}
		for _, endpoint := range endpoints {
			if scoped, err := link.ScopeCandidate(base, endpoint); err == nil {
				result = append(result, scoped)
			}
		}
	}
	return result
}

// Only configured initiator endpoints can introduce a scope. Never trust an
// arbitrary zone string from a remote handshake (or from the remote endpoint).
func introducedScope(base link.Candidate, identity string, endpoints []model.Endpoint) (link.Candidate, bool) {
	if !base.NeedsScope() {
		return base, identity == ""
	}
	for _, endpoint := range endpoints {
		if link.ScopeIdentity(endpoint) != identity {
			continue
		}
		scoped, err := link.ScopeCandidate(base, endpoint)
		if err == nil {
			return scoped, true
		}
	}
	return link.Candidate{}, false
}

// Manual endpoints may spell an owner's zone numerically while Go reports its
// interface name on accepted sockets. Resolve both only in the receiver's OS.
func sameLocalZone(a, b string) bool {
	if a == b {
		return a != ""
	}
	index := func(zone string) int {
		if iface, err := net.InterfaceByName(zone); err == nil {
			return iface.Index
		}
		value, _ := strconv.Atoi(zone)
		return value
	}
	first, second := index(a), index(b)
	return first > 0 && first == second
}
