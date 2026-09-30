package cluster

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/pki"
	"github.com/eWloYW8/GraphWAN/internal/store"
	"github.com/hashicorp/raft"
)

// Two Raft runtimes, temporary stores and an in-memory wire. No listeners,
// deployed processes, network namespaces or administrator privileges.
func TestOneWayClusterChannel(t *testing.T) {
	var dbs [2]*store.Store
	var identities [2]Identity
	for i := range dbs {
		var err error
		dbs[i], err = store.Open(filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { dbs[i].Close() })
		identities[i], err = LoadIdentity(dbs[i])
		if err != nil {
			t.Fatal(err)
		}
	}
	ca, err := pki.LoadOrCreate(dbs[0])
	if err != nil {
		t.Fatal(err)
	}
	state := model.EmptyState()
	state.ClusterID = model.NewID()
	state.ClusterCA = ca.PEM
	for i := range identities {
		csr, err := identities[i].CSR()
		if err != nil {
			t.Fatal(err)
		}
		identities[i].Certificate, _, err = ca.IssueAgent(identities[i].ID, csr)
		if err != nil {
			t.Fatal(err)
		}
		identities[i].Joined = true
		state.Servers = append(state.Servers, model.Server{ID: identities[i].ID, Name: identities[i].Name, PublicKey: identities[i].Public()})
	}
	var nodes [2]*Runtime
	if _, err := dbs[0].Update(0, func(s *model.State) error { *s = state.Clone(); return nil }); err != nil {
		t.Fatal(err)
	}
	image, err := dbs[0].Export()
	if err != nil {
		t.Fatal(err)
	}
	if err := dbs[1].Import(image); err != nil {
		t.Fatal(err)
	}
	for i := range nodes {
		nodes[i], err = Start(dbs[i], identities[i], t.TempDir(), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(nodes[i].Close)
	}
	var forward, reverse atomic.Int32
	// Only Server 0 can initiate a physical connection to Server 1.
	nodes[0].layer.channels.dial = func(ctx context.Context, id model.ID) (net.Conn, error) {
		forward.Add(1)
		a, b := net.Pipe()
		if _, err := nodes[1].layer.channels.attach(identities[0].ID, b, false); err != nil {
			a.Close()
			return nil, err
		}
		return a, nil
	}
	nodes[1].layer.channels.dial = func(context.Context, model.ID) (net.Conn, error) {
		reverse.Add(1)
		return nil, errors.New("inbound access blocked")
	}
	config := raft.Configuration{}
	for _, id := range identities {
		config.Servers = append(config.Servers, raft.Server{ID: raft.ServerID(id.ID), Address: raft.ServerAddress(id.ID), Suffrage: raft.Voter})
	}
	for _, r := range nodes {
		if err := r.Raft.BootstrapCluster(config).Error(); err != nil {
			t.Fatal(err)
		}
		r.StartMembership(nil)
	}
	wait := func(condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for !condition() {
			if time.Now().After(deadline) {
				t.Fatal("cluster did not converge")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	wait(func() bool { return nodes[0].Raft.State() == raft.Leader || nodes[1].Raft.State() == raft.Leader })
	leader := 0
	if nodes[1].Raft.State() == raft.Leader {
		leader = 1
	}
	write := func(leader int, name string) {
		t.Helper()
		wait(func() bool {
			_, id := nodes[1-leader].Raft.LeaderWithID()
			return id == raft.ServerID(identities[leader].ID)
		})
		current, err := dbs[1-leader].Read()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := dbs[1-leader].Update(current.Revision, func(s *model.State) error { s.Servers[0].Name = name; return nil }); err != nil {
			t.Fatal(err)
		}
		for _, db := range dbs {
			s, err := db.Read()
			if err != nil || s.Servers[0].Name != name {
				t.Fatalf("write not replicated: %v", err)
			}
		}
	}
	write(leader, "before-transfer")
	if forward.Load() != 1 {
		t.Fatalf("opened %d physical connections", forward.Load())
	}
	blockedBefore := reverse.Load()
	next := 1 - leader
	if err := nodes[leader].Raft.LeadershipTransferToServer(raft.ServerID(identities[next].ID), raft.ServerAddress(identities[next].ID)).Error(); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return nodes[next].Raft.State() == raft.Leader })
	write(next, "after-transfer")
	if reverse.Load() != blockedBefore || forward.Load() != 1 {
		t.Fatal("reverse RPCs dialed instead of reusing the channel")
	}
	for _, r := range nodes {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		peer := identities[0].ID
		if r == nodes[0] {
			peer = identities[1].ID
		}
		var status Status
		err := r.request(ctx, peer, "/api/v1/cluster/status", struct{}{}, &status)
		cancel()
		if err != nil || status.ID != peer {
			t.Fatalf("reverse status: %v", err)
		}
	}
	// Drop the only wire and let the reachable direction reconnect it.
	nodes[0].layer.channels.mu.Lock()
	session := nodes[0].layer.channels.peers[identities[1].ID].active.session
	nodes[0].layer.channels.mu.Unlock()
	session.Close()
	wait(func() bool { return forward.Load() > 1 })
	wait(func() bool { return nodes[0].Raft.State() == raft.Leader || nodes[1].Raft.State() == raft.Leader })
	leader = 0
	if nodes[1].Raft.State() == raft.Leader {
		leader = 1
	}
	write(leader, "after-reconnect")
}

func TestChannelDuplicateAndPrune(t *testing.T) {
	aID, bID := model.ID("a"), model.ID("b")
	noDial := func(context.Context, model.ID) (net.Conn, error) { return nil, errors.New("no dial") }
	accept := func(_ model.ID, c net.Conn) { c.Close() }
	a := newChannels(context.Background(), aID, noDial, accept)
	defer a.close()
	b := newChannels(context.Background(), bID, noDial, accept)
	defer b.close()
	// First install the nonpreferred B -> A connection.
	x, y := net.Pipe()
	oldA, err := a.attach(bID, x, false)
	if err != nil {
		t.Fatal(err)
	}
	oldB, err := b.attach(aID, y, true)
	if err != nil {
		t.Fatal(err)
	}
	// A simultaneous A -> B dial wins on both ends.
	x, y = net.Pipe()
	if _, err = a.attach(bID, x, true); err != nil {
		t.Fatal(err)
	}
	if _, err = b.attach(aID, y, false); err != nil {
		t.Fatal(err)
	}
	if !oldA.session.IsClosed() || !oldB.session.IsClosed() {
		t.Fatal("duplicate wire retained")
	}
	a.mu.Lock()
	active := a.peers[bID].active
	a.mu.Unlock()
	if !active.outbound {
		t.Fatal("wrong connection selected")
	}
	a.prune(map[model.ID]bool{})
	if !active.session.IsClosed() {
		t.Fatal("revoked channel retained")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = a.open(ctx, bID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}
