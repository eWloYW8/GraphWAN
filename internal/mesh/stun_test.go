package mesh

import (
	"bytes"
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/link"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
)

func TestExpiredObservationsDoNotCloseHealthyLinks(t *testing.T) {
	for _, test := range []struct {
		name      string
		host      string
		transport model.Transport
		methods   model.ConnectionMethods
		method    link.Method
	}{
		{"udp-punch", "127.0.0.1", model.UDP, model.ConnectionMethods{HolePunch: true}, link.Punch},
		{"udp-direct-v4", "127.0.0.1", model.UDP, model.ConnectionMethods{IPv4Direct: true}, link.Direct},
		{"tcp-direct-v4", "127.0.0.1", model.TCP, model.ConnectionMethods{IPv4Direct: true}, link.Direct},
		{"udp-direct-v6", "::1", model.UDP, model.ConnectionMethods{IPv6Direct: true}, link.Direct},
		{"tcp-direct-v6", "::1", model.TCP, model.ConnectionMethods{IPv6Direct: true}, link.Direct},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, meshes, state, delivered := webMeshesOn(t, test.host)
			state.Networks[0].Edges[0].Transports = []model.Transport{test.transport}
			state.Networks[0].Edges[0].Methods = test.methods
			for i, m := range meshes {
				m.linkOptions = link.Options{Heartbeat: 10 * time.Millisecond, Timeout: 500 * time.Millisecond, RenewAfter: 150 * time.Millisecond}
				state.Agents[i].Endpoints = []model.Endpoint{{ID: testutil.ID(200 + i), Source: model.Observed, Transport: test.transport,
					URL: fmt.Sprintf("%s://%s", test.transport, net.JoinHostPort(test.host, strconv.Itoa(int(m.Port())))), ExpiresAt: time.Now().Add(time.Minute)}}
			}
			// Direct cases expose only the destination's observed endpoint: the
			// receiver has no endpoint to dial back, so it cannot assist by punching.
			if test.method == link.Direct {
				state.Agents[0].Endpoints = nil
			}
			applyWebState(t, state, meshes)
			waitWeb(t, ctx, func() bool { return commonWeb(meshes, "") })
			if test.method == link.Direct {
				family := 4
				if test.host == "::1" {
					family = 6
				}
				want := link.CandidateID(state.Networks[0].Edges[0].ID, state.Networks[0].Nodes[0].ID, state.Agents[1].Endpoints[0].ID, family, link.Direct)
				for _, m := range meshes {
					for _, stat := range m.Report() {
						if stat.CandidateID != want {
							t.Fatal("one-sided direct connection used another method", stat)
						}
					}
				}
			}
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
			// Revoking the actual method still tears down retained sessions.
			state.Networks[0].Edges[0].Methods = model.ConnectionMethods{IPv4Direct: true}
			if test.method == link.Direct {
				state.Networks[0].Edges[0].Methods = model.ConnectionMethods{HolePunch: true}
			}
			applyWebState(t, state, meshes)
			if len(meshes[0].Report()) != 0 || len(meshes[1].Report()) != 0 {
				t.Fatal("revoked observed sessions retained")
			}
		})
	}
}
