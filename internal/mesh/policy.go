package mesh

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/link"
	"github.com/eWloYW8/GraphWAN/internal/model"
)

// Candidate IDs describe a stable preference. Bind the introduction separately
// to the exact current endpoint URL/source so an old completed handshake cannot
// claim the replacement endpoint merely by reusing its unchanged ID.
func endpointFingerprint(endpoint model.Endpoint) string {
	sum := sha256.Sum256([]byte(string(endpoint.Source) + "\x00" + string(endpoint.Transport) + "\x00" + endpoint.URL))
	return hex.EncodeToString(sum[:16])
}

// A healthy observed path can outlive its discovery lease, but never the
// configured method. Endpoint provenance is independent of direct/punch policy.
func observedMethodAllowed(cfg *policy, candidate link.Candidate) bool {
	if candidate.Port != 0 && !cfg.peer.Edge.Methods.HolePunchExtension {
		return false
	}
	if candidate.Endpoint.Source != model.Observed || !slices.Contains(cfg.peer.Edge.Transports, candidate.Endpoint.Transport) {
		return false
	}
	switch candidate.Method {
	case link.Direct:
		return candidate.Family == 4 && cfg.peer.Edge.Methods.IPv4Direct || candidate.Family == 6 && cfg.peer.Edge.Methods.IPv6Direct
	case link.Punch:
		return cfg.peer.Edge.Methods.HolePunch && (candidate.Endpoint.Transport == model.UDP || candidate.Endpoint.Transport == model.TCP)
	default:
		return false
	}
}

// A check reuses the candidate lists for one policy update at a fixed time.
type candidateCheck struct {
	cfg    *policy
	now    time.Time
	bases  [2][]link.Candidate
	loaded [2]bool
}

func candidateConfigured(cfg *policy, candidate link.Candidate, healthy bool) bool {
	check := candidateCheck{cfg: cfg, now: time.Now()}
	return check.configured(candidate, healthy)
}

// A configured endpoint authorizes its own live paths, including DNS answers
// retained across refresh. A vanished STUN lease may renew only an already
// healthy session; disabling its method/transport still revokes it.
func (check *candidateCheck) configured(candidate link.Candidate, healthy bool) bool {
	cfg := check.cfg
	if !cfg.peer.Edge.Enabled || !slices.Contains(cfg.peer.Edge.Transports, candidate.Endpoint.Transport) {
		return false
	}
	if healthy && observedMethodAllowed(cfg, candidate) {
		for _, initiator := range []model.ID{cfg.self, cfg.peer.Node.ID} {
			if candidate.Identity(cfg.peer.Edge.ID, initiator) == candidate.ID {
				return true
			}
		}
	}
	hasTarget := candidate.Target.IsValid()
	scope := link.ScopeIdentity(candidate.Scope)
	for i, outgoing := range []bool{true, false} {
		initiator, peer, scopes := cfg.self, cfg.peer, cfg.endpoints
		if !outgoing {
			initiator, peer, scopes = cfg.peer.Node.ID, model.Peer{Edge: cfg.peer.Edge, Endpoints: cfg.endpoints}, cfg.peer.Endpoints
		}
		if !check.loaded[i] {
			check.bases[i] = link.Candidates(initiator, peer, check.now)
			check.loaded[i] = true
		}
		for _, base := range check.bases[i] {
			if !hasTarget && scope == "" && base.ID != candidate.ID {
				continue
			}
			if base.Endpoint.URL != candidate.Endpoint.URL || base.Endpoint.Source != candidate.Endpoint.Source {
				continue
			}
			if hasTarget {
				var err error
				base, err = link.ResolveCandidate(base, candidate.Target)
				if err != nil {
					continue
				}
			}
			base, ok := introducedScope(base, scope, scopes)
			if ok && base.ID == candidate.ID {
				return true
			}
		}
	}
	return false
}

// Called with g.mu held, including at handshake completion, to prevent a dial
// started under old policy from reinstalling a revoked endpoint.
func (g *group) allowsCandidateLocked(cfg *policy, candidate link.Candidate) bool {
	return candidateConfigured(cfg, candidate, g.candidateHealthyLocked(candidate.ID))
}

func (g *group) candidateHealthyLocked(id string) bool {
	for _, l := range g.links {
		if l.Info().CandidateID == id && l.Stats().Healthy {
			return true
		}
	}
	return false
}

func (g *group) updatePolicy(cfg *policy) []*link.Link {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.policy.Store(cfg)
	g.edge.SetPreferred(cfg.peer.Edge.PreferredCandidate)
	check := candidateCheck{cfg: cfg, now: time.Now()}
	var retired []*link.Link
	for id, l := range g.links {
		if check.configured(g.linkCandidates[id], l.Stats().Healthy) {
			continue
		}
		retired = append(retired, l)
		delete(g.links, id)
		delete(g.linkCandidates, id)
		g.edge.Remove(id)
	}
	for id, state := range g.attempts {
		if check.configured(state.candidate, g.candidateHealthyLocked(state.candidate.ID)) {
			continue
		}
		if state.cancel != nil {
			state.cancel()
		}
		delete(g.attempts, id)
	}
	return retired
}
