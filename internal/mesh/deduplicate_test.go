package mesh

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/link"
	"github.com/eWloYW8/GraphWAN/internal/model"
)

type memoryChannel struct {
	id            string
	local, remote *net.UDPAddr
	created       time.Time
	ctx           context.Context
	cancel        context.CancelFunc
	in            chan []byte
	peer          *memoryChannel
	dropACK       *atomic.Bool
	delay         time.Duration
}

func (c *memoryChannel) ID() string           { return c.id }
func (c *memoryChannel) Created() time.Time   { return c.created }
func (c *memoryChannel) NeedsRekey() bool     { return false }
func (c *memoryChannel) LocalAddr() net.Addr  { return c.local }
func (c *memoryChannel) RemoteAddr() net.Addr { return c.remote }
func (c *memoryChannel) Close() error         { c.cancel(); return nil }
func (c *memoryChannel) Send(ctx context.Context, raw []byte) error {
	if raw[0] == 3 {
		time.Sleep(c.delay)
	}
	// Lose the first retirement ACK, after the follower has closed the loser.
	if raw[0] == 10 && c.dropACK.CompareAndSwap(false, true) {
		return nil
	}
	select {
	case <-c.peer.ctx.Done():
		return nil // UDP has no remote-close notification.
	case <-ctx.Done():
		return ctx.Err()
	case c.peer.in <- bytes.Clone(raw):
		return nil
	}
}
func (c *memoryChannel) Receive(ctx context.Context) ([]byte, error) {
	select {
	case <-c.ctx.Done():
		return nil, net.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	case raw := <-c.in:
		return raw, nil
	}
}

// Real Link workers and coordinated selection, but only in-memory datagrams and
// a virtual clock: no sockets, deployments, or wall-clock timeout waits.
func TestDuplicatePathLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		id := func(n int) model.ID { return model.ID(fmt.Sprintf("%032x", n)) }
		groups := [2]*group{}
		for i := range groups {
			g := &group{ctx: ctx, links: map[string]*link.Link{}, suppressed: map[string]string{}, retiring: map[string]*retirement{}, keepers: map[link.Path]string{}}
			g.policy.Store(&policy{self: id(i + 1), peer: model.Peer{Node: model.Node{ID: id(2 - i)}}})
			g.edge = link.NewCoordinatedEdge(id(i+1), id(2-i), "")
			groups[i] = g
		}
		var dropped atomic.Bool
		all := []*link.Link{}
		defer func() {
			for _, l := range all {
				l.Close()
			}
		}()
		add := func(n int, a, b string, transport model.Transport, exchange bool) [2]*memoryChannel {
			session := fmt.Sprintf("%064x", n)
			channels := [2]*memoryChannel{}
			for i, remote := range []string{b, a} {
				address, err := net.ResolveUDPAddr("udp", remote)
				if err != nil {
					t.Fatal(err)
				}
				cctx, stop := context.WithCancel(ctx)
				delay := 10 * time.Millisecond
				if n == 1 {
					delay = 6 * time.Millisecond
				}
				if n == 3 {
					delay = 5 * time.Millisecond
				}
				channels[i] = &memoryChannel{id: session, local: &net.UDPAddr{IP: net.IPv4zero, Port: 24752}, remote: address, created: time.Now(), ctx: cctx, cancel: stop, in: make(chan []byte, 256), dropACK: &dropped, delay: delay}
			}
			channels[0].peer, channels[1].peer = channels[1], channels[0]
			candidate := string(id(n + 100))
			if n == 7 {
				candidate = string(id(101))
			} // Rekey the original candidate.
			for i, c := range channels {
				l, err := link.New(ctx, c, link.Info{NetworkID: id(3), EdgeID: id(4), PeerID: id(2 - i), CandidateID: candidate, Transport: transport}, link.Options{PathExchange: exchange && i == 1, Heartbeat: 100 * time.Millisecond, IdleHeartbeat: 100 * time.Millisecond, Timeout: 500 * time.Millisecond})
				if err != nil {
					t.Fatal(err)
				}
				all = append(all, l)
				groups[i].links[session] = l
				groups[i].edge.Add(l)
			}
			return channels
		}
		run := func(ticks int) {
			for range ticks {
				synctest.Wait()
				for _, g := range groups {
					for id, l := range g.links {
						select {
						case <-l.Done():
							g.edge.Remove(id)
							delete(g.links, id)
							continue
						default:
						}
						drain := true
						for drain {
							select {
							case m := <-l.Selections():
								g.edge.HandleSelection(l, m)
							case m := <-l.Retirements():
								g.handleRetirement(l, m)
							default:
								drain = false
							}
						}
					}
					g.edge.Tick()
					g.deduplicate()
					g.retireSessions()
				}
				time.Sleep(50 * time.Millisecond)
			}
		}
		original := add(1, "192.0.2.1:10001", "198.51.100.1:24752", model.UDP, true)
		run(15)
		add(2, "192.0.2.1:10002", "198.51.100.1:24753", model.UDP, true)
		add(3, "192.0.2.2:10001", "198.51.100.1:24752", model.UDP, true)
		add(4, "192.0.2.1:10001", "198.51.100.2:24752", model.UDP, true)
		add(5, "192.0.2.1:10001", "198.51.100.1:24752", model.TCP, true)
		// Legacy peers without path exchange must remain usable, without guessing.
		add(6, "192.0.2.1:10003", "198.51.100.1:24754", model.UDP, false)
		run(80)
		if !dropped.Load() {
			t.Fatal("retirement protocol was not exercised")
		}
		for _, g := range groups {
			if len(g.links) != 5 {
				t.Fatalf("retained %d sessions, want 5", len(g.links))
			}
			if g.links[fmt.Sprintf("%064x", 2)] != nil {
				t.Fatal("port-only duplicate survived")
			}
			if !g.candidateConnectedLocked(string(id(102))) {
				t.Fatal("suppressed candidate would redial")
			}
			active := ""
			for _, report := range g.edge.Report() {
				if report.Active {
					active = report.LinkID
				}
			}
			if active != fmt.Sprintf("%064x", 3) {
				t.Fatal("did not select the lowest-RTT path after a 1 ms improvement")
			}
		}
		// Even an authenticated retirement request cannot remove a different path.
		groups[1].handleRetirement(groups[1].links[original[1].id], link.Retirement{ID: fmt.Sprintf("%064x", 3)})
		if groups[1].retiring[fmt.Sprintf("%064x", 3)] != nil {
			t.Fatal("different IP path retired")
		}
		// Rekey replaces the keeper and carries candidate suppression forward.
		for _, c := range original {
			c.created = time.Now().Add(-51 * time.Minute)
		}
		renewed := add(7, "192.0.2.1:10004", "198.51.100.1:24755", model.UDP, true)
		run(60)
		for _, g := range groups {
			if g.links[original[0].id] != nil || g.links[renewed[0].id] == nil || g.suppressed[string(id(102))] != renewed[0].id {
				t.Fatalf("renewal failed: node=%s old=%v new=%v alias=%s retiring=%v", g.policy.Load().self, g.links[original[0].id] != nil, g.links[renewed[0].id] != nil, g.suppressed[string(id(102))], g.retiring)
			}
		}
		// A failed keeper releases aliases, permitting both dialing directions again.
		for _, g := range groups {
			g.links[renewed[0].id].Close()
		}
		run(15)
		for _, g := range groups {
			if g.candidateConnectedLocked(string(id(102))) {
				t.Fatal("failed keeper suppressed reconnection")
			}
		}
	})
}
