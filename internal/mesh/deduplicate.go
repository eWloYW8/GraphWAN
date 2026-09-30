package mesh

import (
	"time"

	"github.com/eWloYW8/GraphWAN/internal/link"
)

type retirement struct {
	winner string
	retry  time.Time
	acked  bool
}

func (g *group) candidateConnectedLocked(candidate string) bool {
	if replacement := g.links[g.suppressed[candidate]]; replacement != nil && replacement.Healthy() && !replacement.RenewalDue() {
		return true
	}
	delete(g.suppressed, candidate)
	for _, l := range g.links {
		select {
		case <-l.Done():
			continue
		default:
		}
		if l.Info().CandidateID == candidate && !l.RenewalDue() {
			return true
		}
	}
	return false
}

// Only the common-Link selector decides which session survives. Comparing both
// authenticated observations handles NAT and wildcard UDP sockets without
// guessing a local source address or merging distinct IP/protocol paths.
func (g *group) deduplicate() {
	g.mu.Lock()
	defer g.mu.Unlock()
	cfg := g.policy.Load()
	for candidate, id := range g.suppressed {
		if l := g.links[id]; l == nil || !l.Healthy() {
			delete(g.suppressed, candidate)
		}
	}
	for id, state := range g.retiring {
		// A failed/independently retired loser no longer needs requests. The
		// follower retains completed records to answer a lost ACK idempotently.
		if g.links[id] == nil && (!state.acked || cfg.self < cfg.peer.Node.ID) {
			delete(g.retiring, id)
			g.edge.ForgetReplacement(id)
			continue
		}
		if winner := g.links[state.winner]; winner == nil || !winner.Healthy() {
			delete(g.retiring, id)
			g.edge.ForgetReplacement(id)
		}
	}
	if cfg.self > cfg.peer.Node.ID {
		return
	}
	paths := map[link.Path][]*link.Link{}
	for id, l := range g.links {
		if g.retiring[id] != nil || !l.Healthy() {
			continue
		}
		if path, ready := l.Path(); ready {
			paths[path] = append(paths[path], l)
		}
	}
	for path := range g.keepers {
		if len(paths[path]) == 0 {
			delete(g.keepers, path)
		}
	}
	for path, links := range paths {
		winner := g.links[g.keepers[path]]
		if winner == nil || !winner.Healthy() || winner.RenewalDue() {
			winner = nil
			for _, l := range links {
				if winner == nil || winner.RenewalDue() && !l.RenewalDue() || winner.RenewalDue() == l.RenewalDue() && (l.Stats().RTTMillis < winner.Stats().RTTMillis || l.Stats().RTTMillis == winner.Stats().RTTMillis && l.ID() < winner.ID()) {
					winner = l
				}
			}
			g.keepers[path] = winner.ID()
		}
		for _, l := range links {
			if l.ID() == winner.ID() {
				continue
			}
			// While all sessions are renewing, keep them until a fresh healthy
			// replacement appears rather than suppressing rekey attempts.
			if winner.RenewalDue() {
				continue
			}
			g.replaceLocked(l, winner)
		}
	}
	now := time.Now()
	for id, state := range g.retiring {
		if state.acked || now.Before(state.retry) || !g.edge.CanRetire(id) {
			continue
		}
		if winner := g.links[state.winner]; winner != nil {
			winner.SendRetirement(id, false)
			state.retry = now.Add(time.Second)
		}
	}
}

func (g *group) replaceLocked(old, winner *link.Link) {
	for candidate, id := range g.suppressed {
		if id == old.ID() {
			g.suppressed[candidate] = winner.ID()
		}
	}
	for _, state := range g.retiring {
		if state.winner == old.ID() {
			state.winner, state.retry = winner.ID(), time.Time{}
		}
	}
	g.suppressed[old.Info().CandidateID] = winner.ID()
	g.retiring[old.ID()] = &retirement{winner: winner.ID()}
	g.edge.Replace(old.ID(), winner.ID())
}

func (g *group) handleRetirement(winner *link.Link, message link.Retirement) {
	g.mu.Lock()
	var closeLink *link.Link
	defer func() {
		g.mu.Unlock()
		if closeLink != nil {
			closeLink.Close()
		}
	}()
	if g.links[winner.ID()] != winner || !winner.Healthy() || winner.ID() == message.ID {
		return
	}
	cfg := g.policy.Load()
	leader := cfg.self < cfg.peer.Node.ID
	state := g.retiring[message.ID]
	if message.Ack {
		if leader && state != nil && state.winner == winner.ID() && g.edge.CanRetire(message.ID) {
			state.acked = true
			closeLink = g.links[message.ID]
		}
		return
	}
	if leader {
		return
	}
	if state != nil && state.acked && state.winner == winner.ID() {
		winner.SendRetirement(message.ID, true)
		return
	}
	old := g.links[message.ID]
	if old == nil || !g.edge.CanRetire(old.ID()) {
		return
	}
	a, aOK := old.Path()
	b, bOK := winner.Path()
	if !aOK || !bOK || a != b {
		return
	}
	g.replaceLocked(old, winner)
	g.retiring[old.ID()].acked = true
	// Record suppression before acknowledging. A lost ACK is retried on winner;
	// both sides remember the result even after the redundant UDP session closes.
	winner.SendRetirement(old.ID(), true)
	closeLink = old
}
