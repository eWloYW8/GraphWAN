package mesh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/link"
	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/testutil"
	"github.com/graphwan/graphwan/internal/transport"
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

func TestTCPPunchCapacityGrowsWithNetworksAndPreservesRekey(t *testing.T) {
	ctx, meshes, state, _ := webMeshes(t)
	t.Cleanup(func() {
		if t.Failed() {
			for i, m := range meshes {
				t.Logf("mesh %d: %+v", i, m.Report())
			}
		}
	})
	for i, m := range meshes {
		m.linkOptions = link.Options{Heartbeat: 50 * time.Millisecond, Timeout: 2 * time.Second, RenewAfter: 1500 * time.Millisecond}
		state.Agents[i].Endpoints = []model.Endpoint{{ID: testutil.ID(200 + i), Source: model.Interface, Transport: model.TCP, URL: fmt.Sprintf("tcp://127.0.0.1:%d", m.Port())}}
	}
	state.Networks[0].Edges[0].Transports = []model.Transport{model.TCP}
	state.Networks[0].Edges[0].Methods = model.ConnectionMethods{HolePunch: true}
	applyWebState(t, state, meshes)
	waitWeb(t, ctx, func() bool { return commonWeb(meshes, "") })
	physical := make([]*transport.PunchMux, 2)
	for i, m := range meshes {
		m.mu.Lock()
		for _, session := range m.punches {
			physical[i] = session
		}
		m.mu.Unlock()
		if physical[i] == nil {
			t.Fatal("no initial physical session")
		}
	}
	template := state.Clone().Networks[0]
	for i := 1; i < 20; i++ {
		network := state.Clone().Networks[0]
		network.ID = testutil.ID(1000 + i*4)
		network.CIDR = netip.MustParsePrefix(fmt.Sprintf("10.%d.0.0/24", i))
		for j := range network.Nodes {
			network.Nodes[j].ID = testutil.ID(1001 + i*4 + j)
			network.Nodes[j].Address = netip.MustParseAddr(fmt.Sprintf("10.%d.0.%d", i, j+1))
		}
		network.Edges[0].ID = testutil.ID(1003 + i*4)
		network.Edges[0].A, network.Edges[0].B = network.Nodes[0].ID, network.Nodes[1].ID
		state.Networks = append(state.Networks, network)
	}
	applyWebState(t, state, meshes)
	ready := func(previous map[string]bool) bool {
		active := map[model.ID][]string{}
		for _, m := range meshes {
			candidates := map[string]bool{}
			for _, stat := range m.Report() {
				if stat.Healthy && !previous[stat.LinkID] {
					candidates[stat.CandidateID] = true
				}
				if stat.Active && stat.Healthy {
					active[stat.EdgeID] = append(active[stat.EdgeID], stat.LinkID)
				}
			}
			if len(candidates) != 2*len(state.Networks) {
				return false
			}
		}
		for _, network := range state.Networks {
			ids := active[network.Edges[0].ID]
			if len(ids) != 2 || ids[0] != ids[1] {
				return false
			}
		}
		return true
	}
	waitWeb(t, ctx, func() bool { return ready(nil) })
	old := map[string]bool{}
	for _, stat := range meshes[0].Report() {
		old[stat.LinkID] = true
	}
	waitWeb(t, ctx, func() bool { return ready(old) })
	for i, m := range meshes {
		m.mu.Lock()
		same := len(m.punches) == 1
		for _, session := range m.punches {
			same = same && session == physical[i]
		}
		m.mu.Unlock()
		if !same {
			t.Fatal("network growth/rekey replaced the shared physical session")
		}
	}
	state.Networks = []model.Network{template}
	applyWebState(t, state, meshes)
	waitWeb(t, ctx, func() bool { return ready(nil) })
}

// A physical-connection allowance must not impose a fixed limit on the number
// of authorized neighbors, or let one identity consume another's allowance.
func TestTCPPunchRetainsMoreThan64Neighbors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	t.Cleanup(cancel)
	state := model.EmptyState()
	network := model.Network{ID: testutil.ID(1), Name: "Star", CIDR: netip.MustParsePrefix("10.42.0.0/24"), MTU: model.DefaultMTU, Cipher: model.ChaCha20Poly1305}
	delivered := make(chan int, 65)
	payload := bytes.Repeat([]byte{42}, 1280)
	var meshes []*Mesh
	for i := range 66 {
		seed := make([]byte, ed25519.SeedSize)
		seed[0] = byte(i + 1)
		identity := ed25519.NewKeyFromSeed(seed)
		m, err := New(ctx, identity, "127.0.0.1", 0, func(ctx context.Context, _ model.ID, raw []byte) error {
			if !bytes.Equal(raw, payload) {
				return fmt.Errorf("unexpected payload: %q", raw)
			}
			select {
			case delivered <- i:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { m.Close() })
		meshes = append(meshes, m)
		state.Agents = append(state.Agents, model.Agent{ID: testutil.ID(100 + i), Name: fmt.Sprintf("Agent %d", i), PublicKey: identity.Public().(ed25519.PublicKey), ListenPort: m.Port(), Endpoints: []model.Endpoint{{ID: testutil.ID(200 + i), Source: model.Interface, Transport: model.TCP, URL: fmt.Sprintf("tcp://127.0.0.1:%d", m.Port())}}})
		network.Nodes = append(network.Nodes, model.Node{ID: testutil.ID(300 + i), AgentID: state.Agents[i].ID, Name: fmt.Sprintf("Node %d", i), Address: netip.MustParseAddr(fmt.Sprintf("10.42.0.%d", i+1))})
		if i != 0 {
			network.Edges = append(network.Edges, model.Edge{ID: testutil.ID(400 + i), A: network.Nodes[0].ID, B: network.Nodes[i].ID, Enabled: true, Weight: 1, Transports: []model.Transport{model.TCP}, Methods: model.ConnectionMethods{HolePunch: true}})
		}
	}
	state.Networks = []model.Network{network}
	applyWebState(t, &state, meshes)
	t.Cleanup(func() {
		if t.Failed() {
			meshes[0].mu.Lock()
			t.Logf("hub physical sessions: %d", len(meshes[0].punches))
			meshes[0].mu.Unlock()
		}
	})
	waitWeb(t, ctx, func() bool {
		active := map[model.ID]string{}
		for _, stat := range meshes[0].Report() {
			if stat.Active && stat.Healthy {
				active[stat.EdgeID] = stat.LinkID
			}
		}
		if len(active) != 65 {
			return false
		}
		for _, leaf := range meshes[1:] {
			common := false
			for _, stat := range leaf.Report() {
				if stat.Active && stat.Healthy && active[stat.EdgeID] == stat.LinkID {
					common = true
				}
			}
			if !common {
				return false
			}
		}
		return true
	})
	for _, node := range network.Nodes[1:] {
		if err := meshes[0].Send(ctx, network.ID, node.ID, payload); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[int]bool{}
	for len(seen) < 65 {
		select {
		case i := <-delivered:
			if i == 0 || seen[i] {
				t.Fatal("misrouted or duplicate traffic", i)
			}
			seen[i] = true
		case <-ctx.Done():
			t.Fatal("not every neighbor received data")
		}
	}
	meshes[0].mu.Lock()
	physical := make(map[punchKey]*transport.PunchMux, len(meshes[0].punches))
	for k, session := range meshes[0].punches {
		physical[k] = session
	}
	meshes[0].mu.Unlock()
	if len(physical) != 65 {
		t.Fatalf("physical sessions = %d, want 65", len(physical))
	}
	state.Networks[0].Edges[64].Enabled = false
	applyWebState(t, &state, meshes)
	meshes[0].mu.Lock()
	defer meshes[0].mu.Unlock()
	if len(meshes[0].punches) != 64 {
		t.Fatalf("physical sessions after revocation = %d, want 64", len(meshes[0].punches))
	}
	for k, session := range meshes[0].punches {
		if session != physical[k] || k.identity == [32]byte(state.Agents[65].PublicKey) {
			t.Fatal("revocation did not preserve only the unrelated sessions")
		}
	}
}

func TestTCPPunchConnectionReservationsArePerIdentity(t *testing.T) {
	m := &Mesh{punches: map[punchKey]*transport.PunchMux{}, punchDials: map[punchKey]*punchDial{}}
	identity := [32]byte{1}
	var keys []punchKey
	for i := range 64 {
		k := punchKey{netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(1000+i)), identity}
		keys = append(keys, k)
		// Completed dials may remain in the pending map until their initial
		// stream opens. Their physical connection must not be counted twice.
		if i < 32 {
			m.punches[k] = nil
		}
		m.punchDials[k] = nil
	}
	extra := punchKey{netip.MustParseAddrPort("127.0.0.1:2000"), identity}
	if m.punchConnectionAvailableLocked(extra) {
		t.Fatal("one peer exceeded its connection and dial allowance")
	}
	other := extra
	other.identity = [32]byte{2}
	if !m.punchConnectionAvailableLocked(other) {
		t.Fatal("busy identity consumed another peer's allowance")
	}
	if !m.punchConnectionAvailableLocked(keys[63]) {
		t.Fatal("a reserved dial cannot install its connection at capacity")
	}
	if !m.punchConnectionAvailableLocked(keys[0]) {
		t.Fatal("a closed session cannot be replaced at capacity")
	}
	delete(m.punchDials, keys[63])
	if !m.punchConnectionAvailableLocked(extra) {
		t.Fatal("canceled dial did not release its reservation")
	}
}

func TestTCPPunchPeerCannotExceedPhysicalAllowance(t *testing.T) {
	ctx, meshes, state, _ := webMeshes(t)
	for i := range state.Agents {
		state.Agents[i].Endpoints = nil // Admit authenticated sessions without automatic dials.
	}
	state.Networks[0].Edges[0].Transports = []model.Transport{model.TCP}
	state.Networks[0].Edges[0].Methods = model.ConnectionMethods{HolePunch: true}
	applyWebState(t, state, meshes)
	var sessions []*transport.PunchMux
	t.Cleanup(func() {
		for _, session := range sessions {
			session.Close()
		}
	})
	open := func() (*transport.PunchMux, error) {
		var dialer net.Dialer
		conn, err := dialer.DialContext(ctx, "tcp", meshes[0].listener.Addr().String())
		if err != nil {
			return nil, err
		}
		return transport.NewPunchMux(ctx, conn, meshes[1].identity.Public().(ed25519.PublicKey), meshes[1].tls, func(pub ed25519.PublicKey) bool {
			return pub.Equal(meshes[0].identity.Public())
		})
	}
	for range 64 {
		session, err := open()
		if err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, session)
	}
	waitWeb(t, ctx, func() bool {
		meshes[0].mu.Lock()
		defer meshes[0].mu.Unlock()
		return len(meshes[0].punches) == 64
	})
	if extra, err := open(); err == nil {
		defer extra.Close()
		select {
		case <-extra.Done():
		case <-ctx.Done():
			t.Fatal("peer exceeded its physical connection allowance")
		}
	}
	for _, session := range sessions {
		select {
		case <-session.Done():
			t.Fatal("rejected connection disturbed an existing physical session")
		default:
		}
	}
	state.Networks[0].Edges[0].Enabled = false
	applyWebState(t, state, meshes)
	for _, session := range sessions {
		select {
		case <-session.Done():
		case <-ctx.Done():
			t.Fatal("revoked identity retained a physical session")
		}
	}
}
