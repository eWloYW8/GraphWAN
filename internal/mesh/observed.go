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
// DNS targets withdrawn by a successful refresh follow the same healthy-session
// retention rule, provided their configured endpoint and method remain allowed.
func (g *group) candidates(cfg *policy, outgoing bool) []link.Candidate {
	if !cfg.peer.Edge.Enabled {
		return nil
	}
	initiator, peer := cfg.self, cfg.peer
	if !outgoing {
		initiator, peer = cfg.peer.Node.ID, model.Peer{Edge: cfg.peer.Edge, Endpoints: cfg.endpoints}
	}
	bases := link.Candidates(initiator, peer, time.Now())
	result := bases
	if outgoing {
		result = scopedCandidates(g.resolveCandidates(bases), cfg.endpoints)
	}
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
	for id, candidate := range g.retained {
		if !existing[id] {
			delete(g.retained, id)
			continue
		}
		if !alive[id] || seen[id] {
			continue
		}
		if candidate.Target.IsValid() {
			// DNS withdrawal does not tear down a live authenticated session.
			// Only its dialing side retains the old target for session renewal.
			if outgoing {
				for _, base := range bases {
					resolved, err := link.ResolveCandidate(base, candidate.Target)
					if err == nil {
						for _, scoped := range scopedCandidates([]link.Candidate{resolved}, cfg.endpoints) {
							if scoped.ID == id {
								result = append(result, scoped)
							}
						}
					}
				}
			}
			continue
		}
		if !cfg.peer.Edge.Methods.HolePunch || candidate.Method != link.Punch || !slices.Contains(cfg.peer.Edge.Transports, candidate.Endpoint.Transport) {
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
