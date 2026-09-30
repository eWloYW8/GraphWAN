package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const RoutingSubprotocol = "graphwan.control.routes.v1"

// RouteUpdate replaces only forwarding tables for one immutable configuration.
// Peer admission and dialing remain enabled even when all routes are withdrawn.
type RouteUpdate struct {
	Revision uint64          `json:"revision"`
	Networks []NetworkRoutes `json:"networks"`
}
type NetworkRoutes struct {
	ID     ID      `json:"id"`
	Routes []Route `json:"routes"`
}

func (u RouteUpdate) Hash() string {
	raw, _ := json.Marshal(u)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (u RouteUpdate) Apply(snapshot Snapshot) (Snapshot, error) {
	if u.Revision != snapshot.Revision || len(u.Networks) != len(snapshot.Networks) {
		return Snapshot{}, fmt.Errorf("route update configuration mismatch")
	}
	next := snapshot.Clone()
	indexes := map[ID]int{}
	for i, n := range next.Networks {
		indexes[n.ID] = i
	}
	for _, n := range u.Networks {
		i, ok := indexes[n.ID]
		if !ok {
			return Snapshot{}, fmt.Errorf("unknown or duplicate route network")
		}
		delete(indexes, n.ID)
		next.Networks[i].Routes = append([]Route{}, n.Routes...)
	}
	if err := next.Validate(snapshot.AgentID); err != nil {
		return Snapshot{}, err
	}
	return next, nil
}
