package mesh

import (
	"bytes"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/link"
	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/testutil"
)

func TestTCPPunchSharesPhysicalConnectionAcrossNetworksAndRekeys(t *testing.T) {
	ctx, meshes, state, delivered := webMeshes(t)
	state.Networks[0].Edges[0].Transports = []model.Transport{model.TCP}
	state.Networks[0].Edges[0].Methods = model.ConnectionMethods{HolePunch: true}
	for i, m := range meshes {
		m.linkOptions = link.Options{Heartbeat: 10 * time.Millisecond, Timeout: 500 * time.Millisecond, RenewAfter: 150 * time.Millisecond}
		state.Agents[i].Endpoints = []model.Endpoint{{ID: testutil.ID(200 + i), Source: model.Observed, Transport: model.TCP,
			URL: fmt.Sprintf("tcp://127.0.0.1:%d", m.Port()), ExpiresAt: time.Now().Add(time.Minute)}}
	}
	next := state.Clone().Networks[0]
	next.ID, next.CIDR = testutil.ID(250), netip.MustParsePrefix("10.43.0.0/24")
	for i := range next.Nodes {
		next.Nodes[i].ID = testutil.ID(251 + i)
		next.Nodes[i].Address = netip.MustParseAddr(fmt.Sprintf("10.43.0.%d", i+1))
	}
	next.Edges[0].ID, next.Edges[0].A, next.Edges[0].B = testutil.ID(253), next.Nodes[0].ID, next.Nodes[1].ID
	state.Networks = append(state.Networks, next)
	applyWebState(t, state, meshes)
	common := func() bool {
		active := map[model.ID][]string{}
		for _, m := range meshes {
			for _, stat := range m.Report() {
				if stat.Active && stat.Healthy {
					active[stat.EdgeID] = append(active[stat.EdgeID], stat.LinkID)
				}
			}
		}
		if len(active) != 2 {
			return false
		}
		for _, ids := range active {
			if len(ids) != 2 || ids[0] != ids[1] {
				return false
			}
		}
		return true
	}
	waitWeb(t, ctx, common)
	old := map[string]bool{}
	for _, stat := range meshes[0].Report() {
		old[stat.LinkID] = true
	}
	for i := range state.Agents {
		state.Agents[i].Endpoints = nil
	}
	applyWebState(t, state, meshes)
	waitWeb(t, ctx, func() bool {
		if !common() {
			return false
		}
		for _, stat := range meshes[0].Report() {
			if stat.Active && old[stat.LinkID] {
				return false
			}
		}
		return true
	})
	for _, network := range state.Networks {
		frame := bytes.Repeat([]byte{43}, 9000)
		if err := meshes[0].Send(ctx, network.ID, network.Nodes[1].ID, frame); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-delivered:
			if !bytes.Equal(got, frame) {
				t.Fatal("corrupted punch frame")
			}
		case <-ctx.Done():
			t.Fatal("TCP punch forwarding stalled")
		}
	}
	for _, m := range meshes {
		m.mu.Lock()
		count := len(m.punches)
		m.mu.Unlock()
		if count != 1 {
			t.Fatal("expected one physical connection for two networks and rekey:", count)
		}
	}
	for i := range state.Networks {
		state.Networks[i].Edges[0].Methods = model.ConnectionMethods{IPv4Direct: true}
	}
	applyWebState(t, state, meshes)
	for _, m := range meshes {
		m.mu.Lock()
		count := len(m.punches)
		m.mu.Unlock()
		if count != 0 || len(m.Report()) != 0 {
			t.Fatal("revoked punch connection retained")
		}
	}
}
