// Package routing compiles controller topology into per-agent forwarding tables.
package routing

import (
	"cmp"
	"container/heap"
	"fmt"
	"slices"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

type arc struct {
	to     model.ID
	weight uint32
}
type item struct {
	node, first model.ID
	cost        uint64
}
type queue []item

func (q queue) Len() int { return len(q) }
func (q queue) Less(i, j int) bool {
	if q[i].cost != q[j].cost {
		return q[i].cost < q[j].cost
	}
	if q[i].first != q[j].first {
		return q[i].first < q[j].first
	}
	return q[i].node < q[j].node
}
func (q queue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *queue) Push(v any)   { *q = append(*q, v.(item)) }
func (q *queue) Pop() any     { a := *q; v := a[len(a)-1]; *q = a[:len(a)-1]; return v }

// shortest assumes a validated graph. Positive weights make equal-cost next-hop
// tie breaking deterministic and keep independently compiled routes loop free.
func shortest(n model.Network, source model.ID, excluded map[model.ID]bool) []model.Route {
	adjacency := map[model.ID][]arc{}
	for _, e := range n.Edges {
		if !e.Enabled || excluded[e.A] || excluded[e.B] {
			continue
		}
		adjacency[e.A] = append(adjacency[e.A], arc{e.B, e.Weight})
		adjacency[e.B] = append(adjacency[e.B], arc{e.A, e.Weight})
	}
	best := map[model.ID]item{source: {node: source}}
	q := &queue{{node: source}}
	for q.Len() > 0 {
		current := heap.Pop(q).(item)
		if best[current.node] != current {
			continue
		}
		for _, a := range adjacency[current.node] {
			first := current.first
			if current.node == source {
				first = a.to
			}
			next := item{node: a.to, first: first, cost: current.cost + uint64(a.weight)}
			prev, ok := best[a.to]
			if !ok || next.cost < prev.cost || next.cost == prev.cost && next.first < prev.first {
				best[a.to] = next
				heap.Push(q, next)
			}
		}
	}
	routes := make([]model.Route, 0, len(best)-1)
	for id, r := range best {
		if id != source {
			routes = append(routes, model.Route{Destination: id, NextHop: r.first, Cost: r.cost})
		}
	}
	slices.SortFunc(routes, func(a, b model.Route) int { return cmp.Compare(a.Destination, b.Destination) })
	return routes
}

func Compile(state model.State, agentID model.ID) (model.Snapshot, error) {
	if err := state.Validate(); err != nil {
		return model.Snapshot{}, err
	}
	agents := map[model.ID]model.Agent{}
	for _, a := range state.Agents {
		agents[a.ID] = a
	}
	self, ok := agents[agentID]
	if !ok {
		return model.Snapshot{}, fmt.Errorf("unknown agent %s", agentID)
	}
	if self.Revoked {
		return model.Snapshot{}, fmt.Errorf("agent is revoked")
	}
	out := model.Snapshot{Schema: model.SchemaVersion, Revision: state.Revision, AgentID: agentID, ListenPort: self.ListenPort, Endpoints: append([]model.Endpoint{}, self.Endpoints...), Networks: []model.NetworkConfig{}}
	out.STUNServers = append([]string(nil), self.STUNServers...)
	out.ExcludeContainerIPs = self.ExcludeContainerIPs
	for _, network := range state.Networks {
		var me model.Node
		nodes := map[model.ID]model.Node{}
		excluded := map[model.ID]bool{}
		for _, node := range network.Nodes {
			nodes[node.ID] = node
			if node.AgentID == agentID {
				me = node
			}
			if agents[node.AgentID].Revoked {
				excluded[node.ID] = true
			}
		}
		if me.ID == "" {
			continue
		}
		config := model.NetworkConfig{ID: network.ID, Name: network.Name, CIDR: network.CIDR, MTU: network.MTU, Cipher: network.Cipher, Self: me, Directory: []model.Destination{}, Peers: []model.Peer{}, Routes: shortest(network, me.ID, excluded)}
		for _, node := range network.Nodes {
			if !excluded[node.ID] {
				config.Directory = append(config.Directory, model.Destination{NodeID: node.ID, Address: node.Address})
			}
		}
		for _, edge := range network.Edges {
			if !edge.Enabled || excluded[edge.A] || excluded[edge.B] {
				continue
			}
			var peerID model.ID
			switch me.ID {
			case edge.A:
				peerID = edge.B
			case edge.B:
				peerID = edge.A
			default:
				continue
			}
			node := nodes[peerID]
			agent := agents[node.AgentID]
			edge.Transports = append([]model.Transport{}, edge.Transports...)
			config.Peers = append(config.Peers, model.Peer{Node: node, PublicKey: append([]byte{}, agent.PublicKey...), Endpoints: append([]model.Endpoint{}, agent.Endpoints...), Edge: edge})
		}
		slices.SortFunc(config.Directory, func(a, b model.Destination) int { return cmp.Compare(a.NodeID, b.NodeID) })
		slices.SortFunc(config.Peers, func(a, b model.Peer) int { return cmp.Compare(a.Node.ID, b.Node.ID) })
		out.Networks = append(out.Networks, config)
	}
	slices.SortFunc(out.Networks, func(a, b model.NetworkConfig) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}
