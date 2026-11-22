package mesh

import (
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/link"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
)

func TestUpdatePolicyRevalidatesAttempts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg, g := punchPolicyAttempts(time.Now())
		want := 2 * (2 + link.MaxExtensionCandidates)
		if len(g.attempts) != want {
			t.Fatalf("fixture has %d attempts, want %d", len(g.attempts), want)
		}
		g.updatePolicy(cfg)
		if got := len(g.attempts); got != want {
			t.Fatalf("retained %d of %d valid attempts", got, want)
		}

		canceled := 0
		for _, attempt := range g.attempts {
			attempt.cancel = func() { canceled++ }
		}
		time.Sleep(time.Minute)
		g.updatePolicy(cfg)
		if len(g.attempts) != 0 || canceled != want {
			t.Fatalf("after expiry: %d attempts left, %d of %d canceled", len(g.attempts), canceled, want)
		}
	})
}

func BenchmarkUpdatePolicyPunchAttempts(b *testing.B) {
	cfg, g := punchPolicyAttempts(time.Now())
	b.ReportAllocs()
	for b.Loop() {
		g.updatePolicy(cfg)
	}
}

func punchPolicyAttempts(now time.Time) (*policy, *group) {
	cfg := &policy{self: testutil.ID(1), peer: model.Peer{
		Node: model.Node{ID: testutil.ID(2)},
		Edge: model.Edge{ID: testutil.ID(3), Enabled: true, Transports: []model.Transport{model.UDP}, Methods: model.ConnectionMethods{HolePunch: true, HolePunchExtension: true}},
	}}
	for i := range 2 {
		cfg.peer.Endpoints = append(cfg.peer.Endpoints, model.Endpoint{
			ID: testutil.ID(10 + i), Source: model.Observed, Transport: model.UDP,
			URL: fmt.Sprintf("udp://11.0.0.%d:40000", 1+i), ExpiresAt: now.Add(time.Minute),
		})
		cfg.endpoints = append(cfg.endpoints, model.Endpoint{
			ID: testutil.ID(20 + i), Source: model.Observed, Transport: model.UDP,
			URL: fmt.Sprintf("udp://11.0.0.%d:40000", 3+i), ExpiresAt: now.Add(time.Minute),
		})
	}
	g := &group{edge: link.NewCoordinatedEdge(cfg.self, cfg.peer.Node.ID, ""), attempts: map[string]*attempt{}}
	reverse := model.Peer{Edge: cfg.peer.Edge, Endpoints: cfg.endpoints}
	candidates := append(link.Candidates(cfg.self, cfg.peer, now), link.Candidates(cfg.peer.Node.ID, reverse, now)...)
	for _, candidate := range candidates {
		g.attempts[candidate.ID] = &attempt{candidate: candidate}
	}
	return cfg, g
}
