package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
)

// FullMeshGroup is a topology group, not an IP subnet. Internal edges are
// derived rather than persisted, so membership and shared policy cannot drift.
type FullMeshGroup struct {
	ID         ID                `json:"id"`
	Name       string            `json:"name"`
	Weight     *uint32           `json:"weight,omitempty"`
	Members    []ID              `json:"members"`
	Transports []Transport       `json:"transports"`
	Methods    ConnectionMethods `json:"methods"`
}

// RoutingWeight preserves weight 1 for groups saved before weight was configurable.
func (g FullMeshGroup) RoutingWeight() uint32 {
	if g.Weight == nil {
		return 1
	}
	return *g.Weight
}

// GroupLink expands one node-to-group connection into independently routed edges.
type GroupLink struct {
	ID         ID                `json:"id"`
	Node       ID                `json:"node"`
	Group      ID                `json:"group"`
	Weight     uint32            `json:"weight"`
	Enabled    bool              `json:"enabled"`
	Transports []Transport       `json:"transports"`
	Methods    ConnectionMethods `json:"methods"`
}

func GroupEdgeID(group, a, b ID) ID {
	if a > b {
		a, b = b, a
	}
	sum := sha256.Sum256([]byte("graphwan/full-mesh/" + string(group) + "/" + string(a) + "/" + string(b)))
	return ID(hex.EncodeToString(sum[:16]))
}

// EffectiveEdges must only be called on validated/bounded groups.
func (n Network) EffectiveEdges() []Edge {
	if len(n.Groups) == 0 && len(n.GroupLinks) == 0 {
		return n.Edges
	}
	edges := slices.Clone(n.Edges)
	for _, g := range n.Groups {
		members := slices.Clone(g.Members)
		slices.Sort(members)
		for i, a := range members {
			for _, b := range members[i+1:] {
				edges = append(edges, Edge{ID: GroupEdgeID(g.ID, a, b), A: a, B: b, Weight: g.RoutingWeight(), Enabled: true, Transports: slices.Clone(g.Transports), Methods: g.Methods})
			}
		}
	}
	for _, link := range n.GroupLinks {
		for _, g := range n.Groups {
			if g.ID != link.Group {
				continue
			}
			for _, member := range g.Members {
				edges = append(edges, Edge{ID: GroupEdgeID(link.ID, link.Node, member), A: link.Node, B: member, Weight: link.Weight, Enabled: link.Enabled, Transports: slices.Clone(link.Transports), Methods: link.Methods})
			}
		}
	}
	return edges
}

func (n Network) validateGroups(addID func(ID) error) error {
	nodes := make(map[ID]Node, len(n.Nodes))
	for _, node := range n.Nodes {
		nodes[node.ID] = node
	}
	used := map[ID]bool{}
	count := int64(len(n.Edges))
	for _, g := range n.Groups {
		if err := addID(g.ID); err != nil {
			return err
		}
		if err := validateName(g.Name); err != nil {
			return err
		}
		if len(g.Members) < 2 || len(g.Members) > MaxNodes {
			return fmt.Errorf("full mesh group requires 2–%d members", MaxNodes)
		}
		size := int64(len(g.Members))
		count += size * (size - 1) / 2
		if count > MaxEdges {
			return fmt.Errorf("full mesh topology exceeds %d edges", MaxEdges)
		}
		for _, id := range g.Members {
			node, ok := nodes[id]
			if !ok || node.WireGuard != nil {
				return fmt.Errorf("full mesh members must be regular nodes in this network")
			}
			if used[id] {
				return fmt.Errorf("node belongs to a full mesh group more than once")
			}
			used[id] = true
		}
	}
	groups := map[ID]FullMeshGroup{}
	for _, g := range n.Groups {
		groups[g.ID] = g
	}
	pairs := map[[2]ID]bool{}
	for _, link := range n.GroupLinks {
		if err := addID(link.ID); err != nil {
			return err
		}
		node, ok := nodes[link.Node]
		g, exists := groups[link.Group]
		if !ok || !exists || node.WireGuard != nil || slices.Contains(g.Members, link.Node) {
			return fmt.Errorf("group link requires a regular node outside its target group")
		}
		key := [2]ID{link.Node, link.Group}
		if pairs[key] {
			return fmt.Errorf("duplicate node-to-group link")
		}
		pairs[key] = true
		count += int64(len(g.Members))
		if count > MaxEdges {
			return fmt.Errorf("expanded topology exceeds %d edges", MaxEdges)
		}
	}
	return nil
}

// PruneGroups removes deleted nodes and groups which no longer have a pair.
func (n *Network) PruneGroups() {
	nodes := map[ID]bool{}
	for _, node := range n.Nodes {
		nodes[node.ID] = true
	}
	groups := []FullMeshGroup{}
	for _, g := range n.Groups {
		g.Members = slices.DeleteFunc(slices.Clone(g.Members), func(id ID) bool { return !nodes[id] })
		if len(g.Members) >= 2 {
			groups = append(groups, g)
		}
	}
	n.Groups = groups
	groupIDs := map[ID]bool{}
	for _, g := range groups {
		groupIDs[g.ID] = true
	}
	n.GroupLinks = slices.DeleteFunc(slices.Clone(n.GroupLinks), func(l GroupLink) bool { return !nodes[l.Node] || !groupIDs[l.Group] })
}
