package routing

import (
	"slices"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

type EdgeKey struct{ Network, Edge model.ID }

// Availability requires a common healthy authenticated session at both ends.
// Active-selection handshakes and RTT changes alone do not withdraw an edge.
func Availability(state model.State, reports []model.AgentStatus, now time.Time) map[EdgeKey]bool {
	type sessions map[string]model.Transport
	ready := map[model.ID]map[EdgeKey]sessions{}
	for _, report := range reports {
		if !report.Connected || report.LastSeen.IsZero() || now.Sub(report.LastSeen) > 45*time.Second || report.ConfigError != "" || report.RuntimeError != "" {
			continue
		}
		edges := map[EdgeKey]sessions{}
		for _, l := range report.Links {
			if !l.Healthy || l.LinkID == "" {
				continue
			}
			key := EdgeKey{l.NetworkID, l.EdgeID}
			if edges[key] == nil {
				edges[key] = sessions{}
			}
			edges[key][l.LinkID] = l.Transport
		}
		ready[report.AgentID] = edges
	}
	revoked := map[model.ID]bool{}
	for _, a := range state.Agents {
		revoked[a.ID] = a.Revoked
	}
	up := map[EdgeKey]bool{}
	for _, n := range state.Networks {
		agents := map[model.ID]model.ID{}
		for _, node := range n.Nodes {
			agents[node.ID] = node.AgentID
		}
		for _, edge := range n.EffectiveEdges() {
			a, b := agents[edge.A], agents[edge.B]
			if !edge.Enabled || revoked[a] || revoked[b] {
				continue
			}
			key := EdgeKey{n.ID, edge.ID}
			if slices.Contains(edge.Transports, model.WireGuard) {
				gateway := a
				if gateway == "" {
					gateway = b
				}
				for _, t := range ready[gateway][key] {
					if t == model.WireGuard {
						up[key] = true
					}
				}
				continue
			}
			for id, transport := range ready[a][key] {
				if ready[b][key][id] == transport && slices.Contains(edge.Transports, transport) {
					up[key] = true
					break
				}
			}
		}
	}
	return up
}

func LiveRoutes(state model.State, snapshot model.Snapshot, up map[EdgeKey]bool) model.RouteUpdate {
	out := model.RouteUpdate{Revision: snapshot.Revision, Networks: []model.NetworkRoutes{}}
	revoked := map[model.ID]bool{}
	for _, a := range state.Agents {
		revoked[a.ID] = a.Revoked
	}
	for _, config := range snapshot.Networks {
		for _, original := range state.Networks {
			if original.ID != config.ID {
				continue
			}
			n := original
			n.Edges = nil
			n.Groups = nil
			n.GroupLinks = nil
			for _, edge := range original.EffectiveEdges() {
				if up[EdgeKey{n.ID, edge.ID}] {
					n.Edges = append(n.Edges, edge)
				}
			}
			excluded := map[model.ID]bool{}
			for _, node := range n.Nodes {
				excluded[node.ID] = revoked[node.AgentID]
			}
			out.Networks = append(out.Networks, model.NetworkRoutes{ID: n.ID, Routes: shortest(n, config.Self.ID, excluded)})
			break
		}
	}
	return out
}
