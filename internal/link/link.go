package link

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/packet"
	"github.com/eWloYW8/GraphWAN/internal/packetbuf"
)

const (
	dataMessage    = 1
	pingMessage    = 2
	pongMessage    = 3
	prepareMessage = 4
	acceptMessage  = 5
	commitMessage  = 6
	confirmMessage = 7
	pathMessage    = 8
	retireMessage  = 9
	retiredMessage = 10
	// Leave room for multiple kernel GSO bursts while writers process bounded
	// batches. The queue is still finite; control messages use a separate queue.
	queueSize = 512
)

var ErrQueueFull = errors.New("link packet queue is full")
var ErrUnavailable = errors.New("edge has no healthy link")

type Channel interface {
	ID() string
	Send(context.Context, []byte) error
	Receive(context.Context) ([]byte, error)
	Close() error
	RemoteAddr() net.Addr
	NeedsRekey() bool
	Created() time.Time
}
type Info struct {
	NetworkID, EdgeID, PeerID model.ID
	CandidateID               string
	Transport                 model.Transport
}
type Options struct {
	Heartbeat, Timeout, WriteTimeout, RenewAfter time.Duration
	// IdleHeartbeat spaces probes when no user data is moving. An unanswered
	// probe resumes Heartbeat cadence and retains the normal response timeout.
	IdleHeartbeat time.Duration
	// PathExchange is enabled by the authenticated introduction capability.
	PathExchange bool
	// OwnedPackets opts into explicit packet ownership. Consumers must release
	// every buffer received from ReadOwnedBatch; the legacy Packets API is unused.
	OwnedPackets bool
}

func (o Options) defaults() Options {
	if o.Heartbeat <= 0 {
		o.Heartbeat = time.Second
	}
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Second
	}
	if o.IdleHeartbeat <= 0 {
		o.IdleHeartbeat = max(10*time.Second, o.Heartbeat)
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = 2 * time.Second
	}
	if o.RenewAfter <= 0 {
		o.RenewAfter = 50 * time.Minute
	}
	return o
}

type Link struct {
	channel                       Channel
	info                          Info
	options                       Options
	ctx                           context.Context
	cancel                        context.CancelFunc
	stopOnce                      sync.Once
	wg                            sync.WaitGroup
	done                          chan struct{}
	data                          [queueSize]queuedPacket
	dataHead, dataCount           int
	dataReady                     chan struct{}
	selection                     chan Selection
	retirements                   chan Retirement
	pathEnabled, pathAcknowledged bool
	observedLocal                 string
	lastPath                      time.Time
	// Odd generations permit data; changing generations invalidates queued data.
	dataGeneration  atomic.Uint64
	control         chan []byte
	packets         chan []byte
	ownedPackets    *packetbuf.Queue
	queueMu         sync.Mutex
	mu              sync.Mutex
	pending         map[uint64]time.Time
	nextPing        uint64
	lastPong        time.Time
	lastPing        time.Time
	probeSince      time.Time
	lastTraffic     time.Time
	sampledRX       uint64
	sampledTX       uint64
	rtt             time.Duration
	sent, lost      uint64
	rx, tx, dropped atomic.Uint64
}

func New(parent context.Context, channel Channel, info Info, options Options) (*Link, error) {
	options = options.defaults()
	if options.Timeout < 2*options.Heartbeat || options.Timeout/options.Heartbeat > 64 {
		return nil, errors.New("link timeout must span 2–64 heartbeats")
	}
	if options.IdleHeartbeat < options.Heartbeat {
		return nil, errors.New("idle heartbeat must not be shorter than active heartbeat")
	}
	if channel == nil || channel.ID() == "" || info.CandidateID == "" || !info.Transport.Valid() {
		return nil, errors.New("invalid link identity")
	}
	for _, id := range []model.ID{info.NetworkID, info.EdgeID, info.PeerID} {
		if err := id.Validate(); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithCancel(parent)
	l := &Link{channel: channel, info: info, options: options, ctx: ctx, cancel: cancel, done: make(chan struct{}), dataReady: make(chan struct{}, 1), selection: make(chan Selection, 8), control: make(chan []byte, 8), packets: make(chan []byte, queueSize), pending: map[uint64]time.Time{}}
	l.pathEnabled = options.PathExchange
	l.retirements = make(chan Retirement, 8)
	if _, ok := channel.(interface {
		ReceiveOwnedBatch(context.Context) ([]*packetbuf.Buffer, error)
	}); ok && options.OwnedPackets {
		l.ownedPackets = packetbuf.NewQueue(queueSize)
	}
	l.dataGeneration.Store(1)
	l.wg.Add(3)
	go l.readLoop()
	go l.writeLoop()
	go l.healthLoop()
	go func() {
		l.wg.Wait()
		close(l.packets)
		if l.ownedPackets != nil {
			l.ownedPackets.Close()
		}
		close(l.done)
	}()
	return l, nil
}

type queuedPacket struct {
	generation uint64
	raw        []byte
	buffer     *packetbuf.Buffer
}

// Selection messages are delivered separately so overlay backpressure cannot
// block negotiation. The sender retries dropped control messages.
func (l *Link) Selections() <-chan Selection { return l.selection }
func (l *Link) setActive(active bool) {
	generation := l.dataGeneration.Load()
	if (generation%2 == 1) != active {
		l.dataGeneration.Add(1)
	}
}
func (l *Link) ID() string             { return l.channel.ID() }
func (l *Link) Info() Info             { return l.info }
func (l *Link) Packets() <-chan []byte { return l.packets }
func (l *Link) Done() <-chan struct{}  { return l.done }
func (l *Link) Created() time.Time     { return l.channel.Created() }
func (l *Link) RenewalDue() bool       { return time.Since(l.channel.Created()) >= l.options.RenewAfter }
func (l *Link) stop() {
	l.stopOnce.Do(func() { l.queueMu.Lock(); l.cancel(); l.queueMu.Unlock(); l.channel.Close() })
}
func (l *Link) Close() error {
	l.stop()
	<-l.done
	return nil
}
func (l *Link) ReadOwnedBatch(ctx context.Context, dst []*packetbuf.Buffer) (int, error) {
	if l.ownedPackets == nil {
		return 0, errors.New("owned packet receive is not enabled")
	}
	return l.ownedPackets.Read(ctx, dst)
}
func (l *Link) Send(ctx context.Context, frame []byte) error {
	return l.SendBatch(ctx, [][]byte{frame})
}

// SendBatch copies and publishes a burst under one lock, preventing the writer
// from breaking a producer's burst into tiny batches. Capacity remains packets.
func (l *Link) SendBatch(ctx context.Context, frames [][]byte) error {
	return l.enqueue(ctx, frames, nil)
}

// SendOwnedBatch consumes every buffer, including on cancellation or overflow.
func (l *Link) SendOwnedBatch(ctx context.Context, buffers []*packetbuf.Buffer) error {
	if len(buffers) > 128 {
		packetbuf.ReleaseAll(buffers)
		return errors.New("invalid overlay batch size")
	}
	var frames [128][]byte
	for i, b := range buffers {
		if b != nil {
			frames[i] = b.Data
		}
	}
	return l.enqueue(ctx, frames[:len(buffers)], buffers)
}

func (l *Link) enqueue(ctx context.Context, frames [][]byte, owners []*packetbuf.Buffer) error {
	accepted := 0
	defer func() {
		if owners != nil {
			packetbuf.ReleaseAll(owners[accepted:])
		}
	}()
	if len(frames) == 0 || len(frames) > 128 {
		return errors.New("invalid overlay batch size")
	}
	for _, frame := range frames {
		if len(frame) <= packet.HeaderSize || len(frame) > packet.MaxFrame {
			return errors.New("invalid overlay frame size")
		}
	}
	l.queueMu.Lock()
	defer l.queueMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.ctx.Err() != nil {
		return net.ErrClosed
	}
	generation := l.dataGeneration.Load()
	if generation%2 == 0 {
		return ErrUnavailable
	}
	accepted = min(len(frames), len(l.data)-l.dataCount)
	for i, frame := range frames[:accepted] {
		var buffer *packetbuf.Buffer
		if owners != nil && owners[i].PrependByte(dataMessage) {
			buffer = owners[i]
		} else {
			buffer = packetbuf.Get(len(frame) + 1)
			buffer.Data[0] = dataMessage
			copy(buffer.Data[1:], frame)
			if owners != nil {
				owners[i].Release()
			}
		}
		l.data[(l.dataHead+l.dataCount)%len(l.data)] = queuedPacket{generation: generation, raw: buffer.Data, buffer: buffer}
		l.dataCount++
	}
	if l.dataCount > 0 {
		l.notifyData()
	}
	if accepted != len(frames) {
		l.dropped.Add(uint64(len(frames) - accepted))
		return ErrQueueFull
	}
	return nil
}
func (l *Link) notifyData() {
	select {
	case l.dataReady <- struct{}{}:
	default:
	}
}

// takeData is called with queueMu held, which also fences shutdown and enqueue.
func (l *Link) takeData() queuedPacket {
	data := l.data[l.dataHead]
	l.data[l.dataHead] = queuedPacket{}
	l.dataHead = (l.dataHead + 1) % len(l.data)
	l.dataCount--
	return data
}
func (l *Link) healthy() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.healthyLocked(time.Now())
}

// Healthy avoids allocating formatted telemetry during connection maintenance.
func (l *Link) Healthy() bool { return l.healthy() }
func (l *Link) healthyLocked(now time.Time) bool {
	return l.ctx.Err() == nil && !l.lastPong.IsZero() && (l.probeSince.IsZero() || now.Sub(l.probeSince) < l.options.Timeout)
}
func (l *Link) Stats() model.LinkStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	healthy := l.healthyLocked(time.Now())
	loss := float64(0)
	if l.sent > 0 {
		loss = float64(l.lost) / float64(l.sent)
	}
	local := ""
	if channel, ok := l.channel.(interface{ LocalAddr() net.Addr }); ok && channel.LocalAddr() != nil {
		local = channel.LocalAddr().String()
	}
	return model.LinkStatus{NetworkID: l.info.NetworkID, EdgeID: l.info.EdgeID, LinkID: l.ID(), CandidateID: l.info.CandidateID, Transport: l.info.Transport, Local: local, ObservedLocal: l.observedLocal, Remote: l.channel.RemoteAddr().String(), Healthy: healthy, RTTMillis: float64(l.rtt) / float64(time.Millisecond), Loss: loss, RXBytes: l.rx.Load(), TXBytes: l.tx.Load()}
}
func (l *Link) readLoop() {
	owned, owning := l.channel.(interface {
		ReceiveOwnedBatch(context.Context) ([]*packetbuf.Buffer, error)
	})
	var ownedPending []*packetbuf.Buffer
	var current *packetbuf.Buffer
	var ready [packetbuf.BatchSize]*packetbuf.Buffer
	count := 0
	flush := func() {
		if count == 0 {
			return
		}
		l.dropped.Add(uint64(l.ownedPackets.Put(ready[:count])))
		clear(ready[:count])
		count = 0
	}
	owning = owning && l.ownedPackets != nil
	batch, batching := l.channel.(interface {
		ReceiveBatch(context.Context) ([][]byte, error)
	})
	var pending [][]byte
	defer l.wg.Done()
	defer func() { current.Release(); packetbuf.ReleaseAll(ownedPending); packetbuf.ReleaseAll(ready[:count]) }()
	defer l.stop()
	for {
		var raw []byte
		var err error
		current.Release()
		current = nil
		if owning {
			if len(ownedPending) == 0 {
				flush()
				ownedPending, err = owned.ReceiveOwnedBatch(l.ctx)
			}
			if err != nil || len(ownedPending) == 0 {
				return
			}
			current, ownedPending[0] = ownedPending[0], nil
			ownedPending = ownedPending[1:]
			raw = current.Data
		} else if batching {
			if len(pending) == 0 {
				pending, err = batch.ReceiveBatch(l.ctx)
			}
			if err != nil || len(pending) == 0 {
				return
			}
			raw, pending[0] = pending[0], nil
			pending = pending[1:]
		} else {
			raw, err = l.channel.Receive(l.ctx)
		}
		if err != nil {
			return
		}
		if len(raw) == 0 {
			return
		}
		switch raw[0] {
		case pathMessage, retireMessage, retiredMessage:
			if !l.handlePathControl(raw) {
				return
			}
		case dataMessage:
			if len(raw) <= packet.HeaderSize+1 || len(raw) > packet.MaxFrame+1 {
				return
			}
			l.rx.Add(uint64(len(raw) - 1))
			if current != nil {
				current.Data = raw[1:]
				ready[count], current = current, nil
				count++
				if count == len(ready) {
					flush()
				}
			} else {
				select {
				case l.packets <- raw[1:]:
				default:
					l.dropped.Add(1)
				}
			}
		case pingMessage:
			if len(raw) != 9 {
				return
			}
			response := append([]byte{}, raw...)
			response[0] = pongMessage
			select {
			case l.control <- response:
			case <-l.ctx.Done():
				return
			default:
			}
		case prepareMessage, acceptMessage, commitMessage, confirmMessage:
			if len(raw) != 25 {
				return
			}
			message := Selection{kind: raw[0], sequence: binary.BigEndian.Uint64(raw[17:])}
			copy(message.term[:], raw[1:17])
			if message.sequence == 0 || message.term == [16]byte{} {
				return
			}
			select {
			case l.selection <- message:
			default:
			}
		case pongMessage:
			if len(raw) != 9 {
				return
			}
			nonce := binary.BigEndian.Uint64(raw[1:])
			l.recordPong(nonce, time.Now())
		default:
			return
		}
	}
}
func (l *Link) writeLoop() {
	batch, batching := l.channel.(interface {
		SendBatch(context.Context, [][]byte) error
	})
	var messages [packetbuf.BatchSize][]byte
	var owners [packetbuf.BatchSize]*packetbuf.Buffer
	defer l.wg.Done()
	defer func() {
		l.stop()
		packetbuf.ReleaseAll(owners[:])
		l.queueMu.Lock()
		defer l.queueMu.Unlock()
		for l.dataCount > 0 {
			l.takeData().buffer.Release()
		}
	}()
	for {
		count := 0
		// Control traffic has priority at every bounded batch boundary.
		select {
		case messages[0] = <-l.control:
			count = 1
		default:
		}
		if count == 0 {
			select {
			case <-l.ctx.Done():
				return
			case messages[0] = <-l.control:
				count = 1
			case <-l.dataReady:
				l.queueMu.Lock()
				limit := 1
				if batching {
					limit = len(messages)
				}
				for l.dataCount > 0 && count < limit {
					data := l.takeData()
					if data.generation%2 == 0 || data.generation != l.dataGeneration.Load() {
						l.dropped.Add(1)
						data.buffer.Release()
						continue
					}
					messages[count], owners[count] = data.raw, data.buffer
					count++
				}
				if l.dataCount > 0 {
					l.notifyData()
				}
				l.queueMu.Unlock()
			}
		}
		if count == 0 {
			continue
		}
		ctx, cancel := context.WithTimeout(l.ctx, l.options.WriteTimeout)
		var err error
		if batching {
			err = batch.SendBatch(ctx, messages[:count])
		} else {
			err = l.channel.Send(ctx, messages[0])
		}
		cancel()
		if err != nil {
			return
		}
		for i, raw := range messages[:count] {
			if raw[0] == dataMessage {
				l.tx.Add(uint64(len(raw) - 1))
			}
			messages[i] = nil
			owners[i].Release()
			owners[i] = nil
		}
	}
}
func (l *Link) healthLoop() {
	defer l.wg.Done()
	defer l.stop()
	now := time.Now()
	ticker := time.NewTicker(l.options.Heartbeat)
	defer ticker.Stop()
	for {
		if !l.heartbeat(now) || l.channel.NeedsRekey() {
			return
		}
		select {
		case <-l.ctx.Done():
			return
		case now = <-ticker.C:
		}
	}
}

func (l *Link) recordPong(nonce uint64, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if sent, ok := l.pending[nonce]; ok {
		sample := now.Sub(sent)
		if l.rtt == 0 {
			l.rtt = sample
		} else {
			l.rtt = (l.rtt*4 + sample) / 5
		}
		l.lastPong, l.probeSince = now, time.Time{}
		delete(l.pending, nonce)
	}
}

// Only the health worker calls heartbeat. Sampling existing user-byte counters
// avoids adding clocks or synchronization to the packet forwarding hot path.
func (l *Link) heartbeat(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pathEnabled && !l.pathAcknowledged && now.Sub(l.lastPath) >= time.Second {
		l.sendPathLocked()
		l.lastPath = now
	}
	for seq, sent := range l.pending {
		if now.Sub(sent) >= l.options.Timeout {
			delete(l.pending, seq)
			l.lost++
		}
	}
	if !l.probeSince.IsZero() && now.Sub(l.probeSince) >= l.options.Timeout {
		return false
	}
	rx, tx := l.rx.Load(), l.tx.Load()
	if rx != l.sampledRX || tx != l.sampledTX {
		l.lastTraffic = now
		l.sampledRX, l.sampledTX = rx, tx
	}
	interval := l.options.IdleHeartbeat
	if l.lastPong.IsZero() || !l.probeSince.IsZero() || now.Sub(l.lastTraffic) < l.options.IdleHeartbeat {
		interval = l.options.Heartbeat
	}
	if !l.lastPing.IsZero() && now.Sub(l.lastPing) < interval {
		return true
	}
	l.nextPing++
	raw := make([]byte, 9)
	raw[0] = pingMessage
	binary.BigEndian.PutUint64(raw[1:], l.nextPing)
	select {
	case l.control <- raw:
		l.pending[l.nextPing] = now
		l.lastPing = now
		if l.probeSince.IsZero() {
			l.probeSince = now
		}
		l.sent++
	default:
	}
	return true
}
