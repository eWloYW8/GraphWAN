package mesh

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/link"
	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/testutil"
)

func TestExpiredObservationsDoNotCloseHealthyLinks(t *testing.T) {
	ctx, meshes, state, delivered := webMeshes(t)
	state.Networks[0].Edges[0].Transports = []model.Transport{model.UDP}
	state.Networks[0].Edges[0].Methods = model.ConnectionMethods{HolePunch: true}
	for i, m := range meshes {
		m.linkOptions = link.Options{Heartbeat: 10 * time.Millisecond, Timeout: 500 * time.Millisecond, RenewAfter: 150 * time.Millisecond}
		state.Agents[i].Endpoints = []model.Endpoint{{ID: testutil.ID(200 + i), Source: model.Observed, Transport: model.UDP,
			URL: fmt.Sprintf("udp://127.0.0.1:%d", m.Port()), ExpiresAt: time.Now().Add(time.Minute)}}
	}
	applyWebState(t, state, meshes)
	waitWeb(t, ctx, func() bool { return commonWeb(meshes, "") })
	before := meshes[0].groups[key{state.Networks[0].ID, state.Networks[0].Nodes[1].ID}]
	old := map[string]bool{}
	for _, stat := range meshes[0].Report() {
		old[stat.LinkID] = true
	}
	for i := range state.Agents {
		state.Agents[i].Endpoints = nil
	}
	applyWebState(t, state, meshes)
	if meshes[0].groups[key{state.Networks[0].ID, state.Networks[0].Nodes[1].ID}] != before {
		t.Fatal("observation removal rebuilt healthy edge")
	}
	waitWeb(t, ctx, func() bool {
		if !commonWeb(meshes, "") {
			return false
		}
		for _, stat := range meshes[0].Report() {
			if stat.Active && !old[stat.LinkID] {
				return true
			}
		}
		return false
	})
	frame := bytes.Repeat([]byte{42}, 128)
	if err := meshes[0].Send(ctx, state.Networks[0].ID, state.Networks[0].Nodes[1].ID, frame); err != nil {
		t.Fatal(err)
	}
	select {
	case raw := <-delivered:
		if !bytes.Equal(raw, frame) {
			t.Fatal("corrupted frame")
		}
	case <-ctx.Done():
		t.Fatal("observation expiry stopped forwarding")
	}
	// Revoking the punch method still tears down those sessions.
	state.Networks[0].Edges[0].Methods = model.ConnectionMethods{IPv4Direct: true}
	applyWebState(t, state, meshes)
	if len(meshes[0].Report()) != 0 || len(meshes[1].Report()) != 0 {
		t.Fatal("revoked punch sessions retained")
	}
}
