package mesh

import (
	"cmp"
	"slices"
	"time"

	"github.com/graphwan/graphwan/internal/link"
	"github.com/graphwan/graphwan/internal/model"
)

// Fresh observations authorize new punching. A healthy authenticated session is
// stronger evidence than a STUN lease and lets that SAME candidate renew keys
// during controller/STUN outages. Once all sessions for it fail, the retained
// candidate is discarded; an expired mapping alone never authorizes a new dial.
func (g *group) candidates(cfg *policy, outgoing bool) []link.Candidate {
	initiator, peer := cfg.self, cfg.peer
	if !outgoing {
		initiator, peer = cfg.peer.Node.ID, model.Peer{Edge: cfg.peer.Edge, Endpoints: cfg.endpoints}
	}
	result := link.Candidates(initiator, peer, time.Now())
	seen := map[string]bool{}
	for _, candidate := range result {
		seen[candidate.ID] = true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	alive := map[string]bool{}
	existing := map[string]bool{}
	for _, l := range g.links {
		select {
		case <-l.Done():
			continue
		default:
		}
		existing[l.Info().CandidateID] = true
		if l.Stats().Healthy {
			alive[l.Info().CandidateID] = true
		}
	}
	for id, candidate := range g.observed {
		if !existing[id] {
			delete(g.observed, id)
			continue
		}
		if !alive[id] || seen[id] || !cfg.peer.Edge.Methods.HolePunch || candidate.Method != link.Punch || !slices.Contains(cfg.peer.Edge.Transports, candidate.Endpoint.Transport) {
			continue
		}
		if link.CandidateID(cfg.peer.Edge.ID, initiator, candidate.Endpoint.ID, candidate.Family, candidate.Method) == id {
			result = append(result, candidate)
		}
	}
	slices.SortFunc(result, func(a, b link.Candidate) int {
		if n := cmp.Compare(a.Priority, b.Priority); n != 0 {
			return n
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return result
}
