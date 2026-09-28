package link

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/testutil"
)

type pairedChannel struct {
	id       string
	incoming chan []byte
	remote   *pairedChannel
	done     chan struct{}
	once     sync.Once
	created  time.Time
	drop     [8]atomic.Int64
}

func (c *pairedChannel) ID() string { return c.id }
func (c *pairedChannel) RemoteAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 24752}
}
func (c *pairedChannel) Created() time.Time { return c.created }
func (c *pairedChannel) NeedsRekey() bool   { return false }
func (c *pairedChannel) Close() error       { c.once.Do(func() { close(c.done) }); return nil }
func (c *pairedChannel) Send(ctx context.Context, raw []byte) error {
	if int(raw[0]) < len(c.drop) && c.drop[raw[0]].Add(-1) >= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return net.ErrClosed
	case c.remote.incoming <- append([]byte{}, raw...):
		return nil
	}
}
func (c *pairedChannel) Receive(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, net.ErrClosed
	case raw := <-c.incoming:
		return raw, nil
	}
}
func pairedLinks(t *testing.T, id, candidate string) [2]*Link {
	t.Helper()
	var channels [2]*pairedChannel
	for i := range channels {
		channels[i] = &pairedChannel{id: id, incoming: make(chan []byte, 256), done: make(chan struct{}), created: time.Now()}
	}
	channels[0].remote, channels[1].remote = channels[1], channels[0]
	var links [2]*Link
	for i := range links {
		l, err := New(context.Background(), channels[i], Info{NetworkID: testutil.ID(1), EdgeID: testutil.ID(2), PeerID: testutil.ID(3 + i), CandidateID: candidate, Transport: model.UDP}, Options{Heartbeat: 10 * time.Millisecond, Timeout: 500 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		links[i] = l
		t.Cleanup(func() { l.Close() })
	}
	return links
}

type edgePair struct {
	t     *testing.T
	edges [2]*Edge
	links [][2]*Link
}

func newEdgePair(t *testing.T) *edgePair {
	return &edgePair{t: t, edges: [2]*Edge{NewCoordinatedEdge(testutil.ID(3), testutil.ID(4), ""), NewCoordinatedEdge(testutil.ID(4), testutil.ID(3), "")}}
}
func (p *edgePair) add(id, candidate string) [2]*Link {
	links := pairedLinks(p.t, id, candidate)
	for i := range p.edges {
		p.edges[i].Add(links[i])
	}
	p.links = append(p.links, links)
	return links
}
func (p *edgePair) assertAgreement() {
	p.t.Helper()
	p.edges[0].mu.Lock()
	p.edges[1].mu.Lock()
	a, b := p.edges[0].active, p.edges[1].active
	p.edges[1].mu.Unlock()
	p.edges[0].mu.Unlock()
	if a != "" && b != "" && a != b {
		p.t.Fatalf("endpoints selected different links: %q and %q", a, b)
	}
}
func (p *edgePair) step() {
	p.t.Helper()
	for _, links := range p.links {
		for i, l := range links {
			for {
				select {
				case message := <-l.Selections():
					p.edges[i].HandleSelection(l, message)
					p.assertAgreement()
				default:
					goto drained
				}
			}
		drained:
		}
	}
	for _, e := range p.edges {
		e.Tick()
		p.assertAgreement()
	}
}
func (p *edgePair) wait(fn func() bool) {
	p.t.Helper()
	end := time.Now().Add(3 * time.Second)
	for {
		p.step()
		if fn() {
			return
		}
		if time.Now().After(end) {
			p.t.Fatal("selection did not converge")
		}
		time.Sleep(time.Millisecond)
	}
}
func (p *edgePair) settled(id string) bool {
	for _, e := range p.edges {
		e.mu.Lock()
		ok := e.active == id && e.selection.confirmed
		e.mu.Unlock()
		if !ok {
			return false
		}
	}
	return true
}

func TestCoordinatedSelectionLossPreferenceAndFailover(t *testing.T) {
	for _, lost := range []byte{prepareMessage, acceptMessage, commitMessage, confirmMessage} {
		t.Run(string(rune('0'+lost)), func(t *testing.T) {
			p := newEdgePair(t)
			a, b := p.add("a", "first"), p.add("b", "second")
			p.wait(func() bool {
				return a[0].Stats().Healthy && a[1].Stats().Healthy && b[0].Stats().Healthy && b[1].Stats().Healthy
			})
			// Contradictory local measurements must not create asymmetric senders.
			for i := range 2 {
				a[i].mu.Lock()
				a[i].rtt = time.Duration(10+i*90) * time.Millisecond
				a[i].mu.Unlock()
				b[i].mu.Lock()
				b[i].rtt = time.Duration(100-i*90) * time.Millisecond
				b[i].mu.Unlock()
			}
			p.edges[0].SetPreferred("first")
			p.wait(func() bool { return p.settled("a") })
			old := Selection{kind: prepareMessage, term: p.edges[0].selection.term, sequence: p.edges[0].selection.sequence}
			// Lose two control datagrams of each phase during a real path switch.
			for i := range 2 {
				b[i].channel.(*pairedChannel).drop[lost].Store(2)
			}
			p.edges[0].SetPreferred("second")
			p.wait(func() bool { return p.settled("b") })
			p.edges[1].HandleSelection(a[1], old)
			old.kind = commitMessage
			p.edges[1].HandleSelection(a[1], old)
			if !p.settled("b") {
				t.Fatal("delayed messages resurrected old selection")
			}
			// Standby data queues are gated on both endpoints.
			for i := range 2 {
				if err := a[i].Send(context.Background(), make([]byte, 100)); err != ErrUnavailable {
					t.Fatalf("standby accepted data: %v", err)
				}
				if err := p.edges[i].Send(context.Background(), make([]byte, 100)); err != nil {
					t.Fatal(err)
				}
			}
			for i := range 2 {
				select {
				case <-b[i].Packets():
				case <-time.After(time.Second):
					t.Fatal("active data did not arrive")
				}
			}
			b[0].Close()
			b[1].Close()
			p.wait(func() bool { return p.settled("a") })
			p.add("b-recovered", "second")
			p.wait(func() bool { return p.settled("b-recovered") })
		})
	}
}

func TestCoordinatedLeaderRestartAndRoleAdmission(t *testing.T) {
	p := newEdgePair(t)
	old := p.add("old", "first")
	p.wait(func() bool { return p.settled("old") })
	term := p.edges[0].selection.term
	// A follower cannot select a Link by impersonating a prepare, and a leader
	// cannot skip prepare with an unsolicited commit on a standby Link.
	standby := p.add("standby", "second")
	p.edges[0].SetPreferred("first")
	p.wait(func() bool { return standby[0].Stats().Healthy && standby[1].Stats().Healthy })
	p.edges[0].HandleSelection(standby[0], Selection{kind: prepareMessage, term: term, sequence: 99})
	p.edges[1].HandleSelection(standby[1], Selection{kind: commitMessage, term: term, sequence: 99})
	if !p.settled("old") {
		t.Fatal("invalid role/phase changed selection")
	}
	// Leave the follower's UDP links alive until heartbeat detects the restart.
	old[0].Close()
	standby[0].Close()
	p.edges[0] = NewCoordinatedEdge(testutil.ID(3), testutil.ID(4), "")
	fresh := p.add("restart", "first")
	p.wait(func() bool { return p.settled("restart") })
	if p.edges[1].selection.term == term {
		t.Fatal("leader incarnation was not replaced")
	}
	// Even on a healthy Link, changing its bound incarnation is forbidden.
	p.edges[1].HandleSelection(fresh[1], Selection{kind: prepareMessage, term: term, sequence: 999})
	if !p.settled("restart") {
		t.Fatal("old incarnation reset a fresh channel")
	}
}

func TestCoordinatedAutomaticSelectionUsesSingleRTTAuthority(t *testing.T) {
	p := newEdgePair(t)
	a, b := p.add("a", "first"), p.add("b", "second")
	eventually(t, func() bool {
		return a[0].Stats().Healthy && a[1].Stats().Healthy && b[0].Stats().Healthy && b[1].Stats().Healthy
	})
	for i := range 2 {
		a[i].mu.Lock()
		a[i].rtt = time.Duration(10+i*90) * time.Millisecond
		a[i].mu.Unlock()
		b[i].mu.Lock()
		b[i].rtt = time.Duration(100-i*90) * time.Millisecond
		b[i].mu.Unlock()
	}
	p.wait(func() bool { return p.settled("a") })
	if len(p.edges[0].Report()) != 2 || len(p.edges[1].Report()) != 2 {
		t.Fatal("automatic selection destroyed standby")
	}
}

type gatedChannel struct {
	*testChannel
	gate      chan struct{}
	entered   chan struct{}
	delivered chan []byte
}

func (c *gatedChannel) Send(ctx context.Context, raw []byte) error {
	if raw[0] == dataMessage {
		select {
		case c.entered <- struct{}{}:
		default:
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.gate:
		}
		c.delivered <- append([]byte{}, raw...)
	}
	return c.testChannel.Send(ctx, raw)
}
func TestSelectionInvalidatesQueuedDataAcrossReactivation(t *testing.T) {
	c := &gatedChannel{testChannel: &testChannel{id: "gate", incoming: make(chan []byte, 256), done: make(chan struct{}), echo: true}, gate: make(chan struct{}), entered: make(chan struct{}, 1), delivered: make(chan []byte, 8)}
	l, err := New(context.Background(), c, Info{NetworkID: testutil.ID(1), EdgeID: testutil.ID(2), PeerID: testutil.ID(3), CandidateID: "candidate", Transport: model.UDP}, Options{Heartbeat: 10 * time.Millisecond, Timeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	eventually(t, func() bool { return l.Stats().Healthy })
	send := func(value byte) {
		frame := make([]byte, 100)
		frame[0] = value
		if err := l.Send(context.Background(), frame); err != nil {
			t.Fatal(err)
		}
	}
	send(1)
	select {
	case <-c.entered:
	case <-time.After(time.Second):
		t.Fatal("writer did not block")
	}
	send(2)
	l.setActive(false)
	l.setActive(true)
	send(3)
	close(c.gate)
	for _, want := range []byte{1, 3} {
		select {
		case raw := <-c.delivered:
			if raw[1] != want {
				t.Fatalf("stale queued packet survived switch: got %d want %d", raw[1], want)
			}
		case <-time.After(time.Second):
			t.Fatal("writer stalled")
		}
	}
	if l.dropped.Load() != 1 {
		t.Fatal("stale queue drop was not counted")
	}
}
