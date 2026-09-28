package mesh

import (
	"context"
	"crypto/ed25519"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/link"
	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/routing"
	"github.com/graphwan/graphwan/internal/testutil"
)

func TestRealUDPFailureFallsBackToExistingTCP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	state := testutil.Topology()
	state.Agents = state.Agents[:2]
	state.Networks[0].Nodes = state.Networks[0].Nodes[:2]
	state.Networks[0].Edges = state.Networks[0].Edges[:1]
	state.Networks[0].Edges[0].Methods = model.ConnectionMethods{IPv4Direct: true}
	delivered := make(chan []byte, 4)
	meshes := []*Mesh{}
	defer func() {
		for _, m := range meshes {
			m.Close()
		}
	}()
	for i := range 2 {
		seed := make([]byte, ed25519.SeedSize)
		seed[0] = byte(i + 1)
		identity := ed25519.NewKeyFromSeed(seed)
		m, err := New(ctx, identity, "127.0.0.1", 0, func(ctx context.Context, remote model.ID, raw []byte) error { delivered <- raw; return nil })
		if err != nil {
			t.Fatal(err)
		}
		meshes = append(meshes, m)
		state.Agents[i].ListenPort = m.Port()
		address := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(m.Port())))
		state.Agents[i].Endpoints = []model.Endpoint{{ID: testutil.ID(100 + i*2), Transport: model.UDP, Source: model.Manual, URL: "udp://" + address}, {ID: testutil.ID(101 + i*2), Transport: model.TCP, Source: model.Manual, URL: "tcp://" + address}}
	}
	preferred := link.CandidateID(state.Networks[0].Edges[0].ID, state.Networks[0].Nodes[0].ID, state.Agents[1].Endpoints[0].ID, 4, link.Direct)
	state.Networks[0].Edges[0].PreferredCandidate = preferred
	for i, m := range meshes {
		snapshot, err := routing.Compile(state, state.Agents[i].ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Apply(snapshot); err != nil {
			t.Fatal(err)
		}
	}
	wait := func(fn func() bool) {
		t.Helper()
		for !fn() {
			select {
			case <-ctx.Done():
				t.Fatal("condition timed out")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	commonActive := func() string {
		id := ""
		for _, m := range meshes {
			current := ""
			for _, stat := range m.Report() {
				if stat.Active {
					current = stat.LinkID
				}
			}
			if current == "" || id != "" && id != current {
				return ""
			}
			id = current
		}
		return id
	}
	tcpBefore := map[string]bool{}
	wait(func() bool {
		for _, m := range meshes {
			selected, tcp := false, false
			for _, stat := range m.Report() {
				if stat.Healthy && stat.Transport == model.TCP {
					tcp = true
					tcpBefore[stat.LinkID] = true
				}
				if stat.Active && stat.CandidateID == preferred {
					selected = true
				}
			}
			if !selected || !tcp {
				return false
			}
		}
		return commonActive() != ""
	})
	// Close only the real UDP data socket. TCP standby sockets remain established.
	meshes[0].udp.Close()
	wait(func() bool {
		for _, m := range meshes {
			activeTCP := false
			for _, stat := range m.Report() {
				if stat.Active && stat.Transport == model.TCP && tcpBefore[stat.LinkID] {
					activeTCP = true
				}
			}
			if !activeTCP {
				return false
			}
		}
		return commonActive() != ""
	})
	frame := make([]byte, 100)
	if err := meshes[0].Send(ctx, state.Networks[0].ID, state.Networks[0].Nodes[1].ID, frame); err != nil {
		t.Fatal(err)
	}
	select {
	case raw := <-delivered:
		if len(raw) != len(frame) {
			t.Fatal("fallback corrupted frame")
		}
	case <-ctx.Done():
		t.Fatal("TCP standby did not forward after UDP failure")
	}
}

func TestSessionRotationKeepsTrafficOnHealthyReplacement(t *testing.T) {
	for _, kind := range []model.Transport{model.TCP, model.QUIC} {
		t.Run(string(kind), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			state := testutil.Topology()
			state.Agents = state.Agents[:2]
			state.Networks[0].Nodes = state.Networks[0].Nodes[:2]
			state.Networks[0].Edges = state.Networks[0].Edges[:1]
			state.Networks[0].Edges[0].Methods = model.ConnectionMethods{IPv4Direct: true}
			state.Networks[0].Edges[0].Transports = []model.Transport{kind}
			delivered := make(chan []byte, 8)
			meshes := []*Mesh{}
			defer func() {
				for _, m := range meshes {
					m.Close()
				}
			}()
			for i := range 2 {
				seed := make([]byte, ed25519.SeedSize)
				seed[0] = byte(i + 1)
				m, err := New(ctx, ed25519.NewKeyFromSeed(seed), "127.0.0.1", 0, func(ctx context.Context, remote model.ID, raw []byte) error { delivered <- raw; return nil })
				if err != nil {
					t.Fatal(err)
				}
				m.linkOptions = link.Options{Heartbeat: 10 * time.Millisecond, Timeout: 500 * time.Millisecond, RenewAfter: 150 * time.Millisecond}
				meshes = append(meshes, m)
				state.Agents[i].ListenPort = m.Port()
				state.Agents[i].Endpoints = []model.Endpoint{{ID: testutil.ID(100 + i), Transport: kind, Source: model.Manual, URL: string(kind) + "://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(int(m.Port())))}}
			}
			for i, m := range meshes {
				snapshot, err := routing.Compile(state, state.Agents[i].ID)
				if err != nil {
					t.Fatal(err)
				}
				if err := m.Apply(snapshot); err != nil {
					t.Fatal(err)
				}
			}
			old := map[string]bool{}
			for len(old) == 0 {
				for _, stat := range meshes[0].Report() {
					if stat.Active {
						old[stat.LinkID] = true
					}
				}
				select {
				case <-ctx.Done():
					t.Fatal("initial link unavailable")
				case <-time.After(10 * time.Millisecond):
				}
			}
			replaced := false
			for range 50 {
				frame := make([]byte, 100)
				if err := meshes[0].Send(ctx, state.Networks[0].ID, state.Networks[0].Nodes[1].ID, frame); err != nil {
					t.Fatalf("rotation interrupted traffic: %v", err)
				}
				select {
				case <-delivered:
				case <-ctx.Done():
					t.Fatal("rotation stalled traffic")
				}
				active := 0
				for _, stat := range meshes[0].Report() {
					if stat.Active {
						active++
						if !old[stat.LinkID] {
							replaced = true
						}
					}
				}
				if active != 1 {
					t.Fatalf("active links during rotation: %d", active)
				}
				time.Sleep(20 * time.Millisecond)
			}
			if !replaced {
				t.Fatal("session was not replaced before retirement")
			}

		})
	}
}
