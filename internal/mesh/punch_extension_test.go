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

func TestPunchExtensionPolicy(t *testing.T) {
	now := time.Now()
	cfg := &policy{self: testutil.ID(1), peer: model.Peer{
		Node: model.Node{ID: testutil.ID(2)},
		Edge: model.Edge{ID: testutil.ID(3), Enabled: true, Transports: []model.Transport{model.UDP, model.TCP}, Methods: model.ConnectionMethods{HolePunch: true, HolePunchExtension: true}},
	}}
	for i, port := range []int{40000, 40008} {
		for j, transport := range []model.Transport{model.UDP, model.TCP} {
			cfg.peer.Endpoints = append(cfg.peer.Endpoints, model.Endpoint{ID: testutil.ID(10 + i*2 + j), Source: model.Observed, Transport: transport, URL: fmt.Sprintf("%s://11.0.0.1:%d", transport, port), ExpiresAt: now.Add(time.Minute)})
		}
	}
	candidates := link.Candidates(cfg.self, cfg.peer, now)
	count := 0
	predicted := map[model.Transport]bool{}
	unique := map[string]bool{}
	var retained link.Candidate
	for _, c := range candidates {
		if c.Port == 0 {
			continue
		}
		count++
		if c.Port < 1024 || c.Port > 41032 || c.Port < 38976 {
			t.Fatalf("unbounded port: %d", c.Port)
		}
		if c.Port == 40016 {
			predicted[c.Endpoint.Transport] = true
		}
		key := fmt.Sprintf("%s/%d", c.Endpoint.Transport, c.Port)
		if unique[key] {
			t.Fatalf("duplicate probe %s", key)
		}
		unique[key] = true
		if c.ID != c.Identity(cfg.peer.Edge.ID, cfg.self) || !candidateConfigured(cfg, c, false) {
			t.Fatal("outgoing candidate rejected")
		}
		receiver := &policy{self: cfg.peer.Node.ID, endpoints: cfg.peer.Endpoints, peer: model.Peer{Node: model.Node{ID: cfg.self}, Edge: cfg.peer.Edge}}
		if !candidateConfigured(receiver, c, false) {
			t.Fatal("receiver rejected predicted port")
		}
		retained = c
	}
	if count != link.MaxExtensionCandidates || !predicted[model.UDP] || !predicted[model.TCP] {
		t.Fatalf("missing bounded predictions: %d %v", count, predicted)
	}
	// A successful extended path can renew while STUN is unavailable.
	for i := range cfg.peer.Endpoints {
		cfg.peer.Endpoints[i].ExpiresAt = now.Add(-time.Minute)
	}
	if candidateConfigured(cfg, retained, false) || !candidateConfigured(cfg, retained, true) {
		t.Fatal("observation expiry/healthy retention broken")
	}
	cfg.peer.Edge.Methods.HolePunchExtension = false
	if candidateConfigured(cfg, retained, true) {
		t.Fatal("disabled extension retained a session")
	}
	// No scans of private/shared/reserved addresses or IPv6.
	for _, address := range []string{"10.1.2.3", "127.0.0.1", "100.64.0.1", "203.0.113.1", "[2001:4860:4860::8888]"} {
		cfg.peer.Edge.Methods.HolePunchExtension = true
		cfg.peer.Endpoints = cfg.peer.Endpoints[:1]
		cfg.peer.Endpoints[0].URL = "udp://" + address + ":40000"
		cfg.peer.Endpoints[0].ExpiresAt = now.Add(time.Minute)
		for _, c := range link.Candidates(cfg.self, cfg.peer, now) {
			if c.Port != 0 {
				t.Fatal("scanning ineligible address", address)
			}
		}
	}
}

func TestExtensionAttemptRate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := &Mesh{}
		if !m.allowExtensionAttempt() || m.allowExtensionAttempt() {
			t.Fatal("unbounded burst")
		}
		time.Sleep(499 * time.Millisecond)
		if m.allowExtensionAttempt() {
			t.Fatal("rate exceeded")
		}
		time.Sleep(time.Millisecond)
		if !m.allowExtensionAttempt() {
			t.Fatal("limiter did not recover")
		}
	})
}
