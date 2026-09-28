package link

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/packet"
	"github.com/graphwan/graphwan/internal/testutil"
)

type testChannel struct {
	id       string
	incoming chan []byte
	done     chan struct{}
	once     sync.Once
	echo     bool
	sent     atomic.Int64
}

func (c *testChannel) ID() string { return c.id }
func (c *testChannel) RemoteAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 24752}
}
func (c *testChannel) Created() time.Time { return time.Now() }
func (c *testChannel) NeedsRekey() bool   { return false }
func (c *testChannel) Close() error       { c.once.Do(func() { close(c.done) }); return nil }
func (c *testChannel) Receive(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, net.ErrClosed
	case data := <-c.incoming:
		return data, nil
	}
}
func (c *testChannel) Send(ctx context.Context, raw []byte) error {
	if raw[0] == pingMessage && c.echo {
		response := append([]byte{}, raw...)
		response[0] = pongMessage
		select {
		case c.incoming <- response:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if raw[0] == dataMessage {
		c.sent.Add(1)
	}
	return nil
}
func newTestLink(t *testing.T, id, candidate string, echo bool) *Link {
	t.Helper()
	channel := &testChannel{id: id, incoming: make(chan []byte, 256), done: make(chan struct{}), echo: echo}
	l, err := New(context.Background(), channel, Info{NetworkID: testutil.ID(1), EdgeID: testutil.ID(2), PeerID: testutil.ID(3), CandidateID: candidate, Transport: model.UDP}, Options{Heartbeat: 10 * time.Millisecond, Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}
func eventually(t *testing.T, fn func() bool) {
	t.Helper()
	end := time.Now().Add(time.Second)
	for !fn() {
		if time.Now().After(end) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestHealthAndStandbyFailover(t *testing.T) {
	first := newTestLink(t, "first", "preferred", true)
	second := newTestLink(t, "second", "backup", true)
	eventually(t, func() bool { return first.Stats().Healthy && second.Stats().Healthy })
	edge := newEdge("preferred")
	edge.Add(first)
	edge.Add(second)
	active := func() string {
		for _, s := range edge.Report() {
			if s.Active {
				return s.CandidateID
			}
		}
		return ""
	}
	if active() != "preferred" {
		t.Fatal("manual preference ignored")
	}
	frame := make([]byte, packet.HeaderSize+20)
	if err := edge.Send(context.Background(), frame); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return first.channel.(*testChannel).sent.Load() == 1 })
	if second.channel.(*testChannel).sent.Load() != 0 {
		t.Fatal("standby carried user data")
	}
	first.Close()
	if active() != "backup" {
		t.Fatal("failed preference did not fall back")
	}
	if err := edge.Send(context.Background(), frame); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return second.channel.(*testChannel).sent.Load() == 1 })
	replacement := newTestLink(t, "replacement", "preferred", true)
	edge.Add(replacement)
	eventually(t, func() bool { return active() == "preferred" })
	if len(edge.Report()) != 3 {
		t.Fatal("selection destroyed standby links")
	}
}
func TestUnresponsiveLinkExpires(t *testing.T) {
	l := newTestLink(t, "no-pong", "candidate", false)
	select {
	case <-l.Done():
	case <-time.After(time.Second):
		t.Fatal("failed heartbeat did not close link")
	}
	if l.Stats().Healthy {
		t.Fatal("dead link is healthy")
	}
}
func TestQueueBoundAndInvalidMessages(t *testing.T) {
	l := newTestLink(t, "link", "candidate", true)
	channel := l.channel.(*testChannel)
	eventually(t, func() bool { return l.Stats().Healthy })
	// The receiver is deliberately not drained. Flooding valid-size messages must
	// drop excess packets while leaving heartbeat control processing responsive.
	for i := range 200 {
		raw := make([]byte, packet.HeaderSize+21)
		raw[0] = dataMessage
		binary.BigEndian.PutUint32(raw[1:5], uint32(i))
		channel.incoming <- raw
	}
	eventually(t, func() bool { return l.rx.Load() > 0 && l.dropped.Load() > 0 })
	if !l.Stats().Healthy {
		t.Fatal("data queue saturation blocked heartbeats")
	}
	channel.incoming <- []byte{255}
	select {
	case <-l.Done():
	case <-time.After(time.Second):
		t.Fatal("invalid message did not close link")
	}
}
func TestAutomaticSelectionHysteresis(t *testing.T) {
	a := newTestLink(t, "a", "a", false)
	b := newTestLink(t, "b", "b", false)
	setRTT := func(l *Link, rtt time.Duration) { l.mu.Lock(); l.lastPong = time.Now(); l.rtt = rtt; l.mu.Unlock() }
	setRTT(a, 40*time.Millisecond)
	setRTT(b, 20*time.Millisecond)
	edge := newEdge("")
	edge.Add(a)
	edge.Add(b)
	active := func() string {
		for _, s := range edge.Report() {
			if s.Active {
				return s.LinkID
			}
		}
		return ""
	}
	if active() != "b" {
		t.Fatal("lowest RTT was not selected")
	}
	setRTT(a, 19*time.Millisecond)
	edge.mu.Lock()
	edge.switched = time.Now().Add(-time.Minute)
	edge.mu.Unlock()
	if active() != "b" {
		t.Fatal("small RTT noise caused a switch")
	}
	setRTT(a, 10*time.Millisecond)
	if active() != "a" {
		t.Fatal("meaningfully faster path was not selected")
	}
	edge.SetPreferred("b")
	if active() != "b" {
		t.Fatal("preference did not bypass hold time")
	}
	a.Close()
	b.Close()
	if err := edge.Send(context.Background(), make([]byte, 100)); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}
