package link

import (
	"context"
	"encoding/binary"
	"errors"
	"github.com/eWloYW8/GraphWAN/internal/packetbuf"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/packet"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
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
	for i := range queueSize + 72 {
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

func TestOutgoingQueueBoundOwnershipAndRecovery(t *testing.T) {
	c := &gatedChannel{testChannel: &testChannel{id: "bounded", incoming: make(chan []byte, 256), done: make(chan struct{}), echo: true}, gate: make(chan struct{}), entered: make(chan struct{}, 1), delivered: make(chan []byte, queueSize+2)}
	l, err := New(t.Context(), c, Info{NetworkID: testutil.ID(1), EdgeID: testutil.ID(2), PeerID: testutil.ID(3), CandidateID: "candidate", Transport: model.TCP}, Options{Heartbeat: 50 * time.Millisecond, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	eventually(t, func() bool { return l.Stats().Healthy })
	frame := make([]byte, packet.MaxFrame)
	if err := l.Send(t.Context(), frame); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.entered:
	case <-time.After(time.Second):
		t.Fatal("writer did not enter blocked transport")
	}
	for i := 1; i <= queueSize; i++ {
		binary.BigEndian.PutUint32(frame[:4], uint32(i))
		if err := l.Send(t.Context(), frame); err != nil {
			t.Fatal("queue rejected a frame below its bound", err)
		}
	}
	clear(frame) // The forwarding loop reuses its input buffer after Send returns.
	if err := l.Send(t.Context(), frame); !errors.Is(err, ErrQueueFull) {
		t.Fatal("full outbound queue did not apply backpressure", err)
	}
	if l.dropped.Load() != 1 {
		t.Fatal("overflow drop was not counted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := l.Send(ctx, frame); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled send entered the congested queue", err)
	}
	close(c.gate)
	for i := 0; i <= queueSize; i++ {
		select {
		case raw := <-c.delivered:
			if len(raw) != packet.MaxFrame+1 || binary.BigEndian.Uint32(raw[1:5]) != uint32(i) {
				t.Fatal("queue lost ordering or retained the caller's reused buffer")
			}
		case <-time.After(time.Second):
			t.Fatal("queue failed to drain after transport recovered")
		}
	}
	if err := l.Send(t.Context(), frame); err != nil {
		t.Fatal("drained queue did not recover", err)
	}
	select {
	case <-c.delivered:
	case <-time.After(time.Second):
		t.Fatal("recovered queue did not transmit")
	}
	eventually(t, func() bool { return l.Stats().Healthy && l.Stats().TXBytes == uint64((queueSize+2)*packet.MaxFrame) })
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

type ownedTestChannel struct {
	*testChannel
	batches chan []*packetbuf.Buffer
}

func (c *ownedTestChannel) ReceiveOwnedBatch(ctx context.Context) ([]*packetbuf.Buffer, error) {
	select {
	case batch := <-c.batches:
		return batch, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, net.ErrClosed
	}
}
func TestOwnedQueueDropAndCloseReleaseBuffers(t *testing.T) {
	all := make([]*packetbuf.Buffer, queueSize+100)
	for i := range all {
		all[i] = packetbuf.Get(101)
		all[i].Data[0] = dataMessage
	}
	c := &ownedTestChannel{testChannel: &testChannel{id: "owned", incoming: make(chan []byte, 1), done: make(chan struct{})}, batches: make(chan []*packetbuf.Buffer, 32)}
	for i := 0; i < len(all); i += 32 {
		c.batches <- append([]*packetbuf.Buffer(nil), all[i:min(i+32, len(all))]...)
	}
	l, err := New(t.Context(), c, Info{NetworkID: testutil.ID(1), EdgeID: testutil.ID(2), PeerID: testutil.ID(3), CandidateID: "candidate", Transport: model.TCP}, Options{OwnedPackets: true})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	eventually(t, func() bool { return l.rx.Load() == uint64(len(all)*100) })
	l.Close()
	if l.dropped.Load() != 100 {
		t.Fatal("wrong overflow count", l.dropped.Load())
	}
	for _, b := range all {
		if b != nil && b.Data != nil {
			t.Fatal("buffer survived queue drop or close")
		}
	}
	// A double return would allow the same live storage to be acquired twice.
	held := make([]*packetbuf.Buffer, len(all))
	seen := map[*packetbuf.Buffer]bool{}
	for i := range held {
		held[i] = packetbuf.Get(101)
		if seen[held[i]] {
			t.Fatal("buffer returned twice")
		}
		seen[held[i]] = true
	}
	packetbuf.ReleaseAll(held)
}

type recordingBatchChannel struct {
	*testChannel
	batches chan [][]byte
}

func (c *recordingBatchChannel) SendBatch(ctx context.Context, frames [][]byte) error {
	var data [][]byte
	for _, raw := range frames {
		if err := c.Send(ctx, raw); err != nil {
			return err
		}
		if raw[0] == dataMessage {
			data = append(data, append([]byte(nil), raw...))
		}
	}
	if len(data) > 0 {
		select {
		case c.batches <- data:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func TestSendBatchPublishesWholeBurstAndWraps(t *testing.T) {
	c := &recordingBatchChannel{testChannel: &testChannel{id: "batch", incoming: make(chan []byte, 256), done: make(chan struct{}), echo: true}, batches: make(chan [][]byte, 1)}
	l, err := New(t.Context(), c, Info{NetworkID: testutil.ID(1), EdgeID: testutil.ID(2), PeerID: testutil.ID(3), CandidateID: "batch", Transport: model.UDP}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	frames := make([][]byte, 32)
	for round := 0; round < 40; round++ {
		for i := range frames {
			frames[i] = make([]byte, 100)
			frames[i][0] = byte(round)
			frames[i][1] = byte(i)
		}
		if err := l.SendBatch(t.Context(), frames); err != nil {
			t.Fatal(err)
		}
		for _, raw := range frames {
			clear(raw)
		}
		select {
		case got := <-c.batches:
			if len(got) != len(frames) {
				t.Fatal("producer burst fragmented", len(got))
			}
			for i, raw := range got {
				if raw[1] != byte(round) || raw[2] != byte(i) {
					t.Fatal("batch storage was not copied or order changed")
				}
			}
		case <-time.After(time.Second):
			t.Fatal("writer stalled")
		}
	}
}
func TestSendBatchOverflowBoundsAndClose(t *testing.T) {
	c := &gatedChannel{testChannel: &testChannel{id: "batch-bound", incoming: make(chan []byte, 256), done: make(chan struct{}), echo: true}, gate: make(chan struct{}), entered: make(chan struct{}, 1), delivered: make(chan []byte, queueSize+2)}
	l, err := New(t.Context(), c, Info{NetworkID: testutil.ID(1), EdgeID: testutil.ID(2), PeerID: testutil.ID(3), CandidateID: "batch", Transport: model.UDP}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	raw := make([]byte, 100)
	if err := l.Send(t.Context(), raw); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.entered:
	case <-time.After(time.Second):
		t.Fatal("writer did not block")
	}
	frames := make([][]byte, 100)
	for i := range frames {
		frames[i] = raw
	}
	for range 5 {
		if err := l.SendBatch(t.Context(), frames); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.SendBatch(t.Context(), frames); !errors.Is(err, ErrQueueFull) {
		t.Fatal(err)
	}
	if l.dropped.Load() != 88 {
		t.Fatal("partial overflow count", l.dropped.Load())
	}
	l.queueMu.Lock()
	count := l.dataCount
	l.queueMu.Unlock()
	if count != queueSize {
		t.Fatal("queue grew beyond bound", count)
	}
	l.Close()
	if err := l.SendBatch(t.Context(), frames); !errors.Is(err, net.ErrClosed) {
		t.Fatal("enqueue after close", err)
	}
	l.queueMu.Lock()
	defer l.queueMu.Unlock()
	for _, entry := range l.data {
		if entry.buffer != nil {
			t.Fatal("close retained queued storage")
		}
	}
}

func TestOwnedSendCancellationAndZeroCopyQueue(t *testing.T) {
	c := &gatedChannel{testChannel: &testChannel{id: "owned-send", incoming: make(chan []byte, 256), done: make(chan struct{}), echo: true}, gate: make(chan struct{}), entered: make(chan struct{}, 1), delivered: make(chan []byte, 4)}
	l, err := New(t.Context(), c, Info{NetworkID: testutil.ID(1), EdgeID: testutil.ID(2), PeerID: testutil.ID(3), CandidateID: "owned", Transport: model.UDP}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := l.Send(t.Context(), make([]byte, 100)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.entered:
	case <-time.After(time.Second):
		t.Fatal("writer did not block")
	}
	original := packetbuf.GetHeadroom(100, 1)
	original.Data[0] = 42
	payloadPointer := &original.Data[0]
	if err := l.SendOwnedBatch(t.Context(), []*packetbuf.Buffer{original}); err != nil {
		t.Fatal(err)
	}
	l.queueMu.Lock()
	queued := l.data[l.dataHead].buffer
	if queued != original || &queued.Data[1] != payloadPointer || queued.Data[0] != dataMessage || queued.Data[1] != 42 {
		t.Error("reserved frame was copied or corrupted")
	}
	l.queueMu.Unlock()
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	rejected := packetbuf.Wrap(make([]byte, 100))
	if err := l.SendOwnedBatch(canceled, []*packetbuf.Buffer{rejected}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if rejected.Data != nil {
		t.Fatal("canceled owned frame retained")
	}
	close(c.gate)
	for range 2 {
		select {
		case <-c.delivered:
		case <-time.After(time.Second):
			t.Fatal("writer stalled")
		}
	}
	l.Close()
	rejected = packetbuf.Wrap(make([]byte, 100))
	if err := l.SendOwnedBatch(t.Context(), []*packetbuf.Buffer{rejected}); !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	if rejected.Data != nil {
		t.Fatal("closed link retained owned frame")
	}
}
