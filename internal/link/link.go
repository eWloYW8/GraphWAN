package link

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/packet"
	"github.com/graphwan/graphwan/internal/packetbuf"
)

const (
	dataMessage    = 1
	pingMessage    = 2
	pongMessage    = 3
	prepareMessage = 4
	acceptMessage  = 5
	commitMessage  = 6
	confirmMessage = 7
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
	// OwnedPackets opts into explicit packet ownership. Consumers must release
	// every buffer received from OwnedPackets; the legacy Packets API is unused.
	OwnedPackets bool
}

func (o Options) defaults() Options {
	if o.Heartbeat <= 0 {
		o.Heartbeat = time.Second
	}
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Second
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
	channel   Channel
	info      Info
	options   Options
	ctx       context.Context
	cancel    context.CancelFunc
	stopOnce  sync.Once
	wg        sync.WaitGroup
	done      chan struct{}
	data      chan queuedPacket
	selection chan Selection
	// Odd generations permit data; changing generations invalidates queued data.
	dataGeneration  atomic.Uint64
	control         chan []byte
	packets         chan []byte
	ownedPackets    chan *packetbuf.Buffer
	queueMu         sync.Mutex
	mu              sync.Mutex
	pending         map[uint64]time.Time
	nextPing        uint64
	lastPong        time.Time
	rtt             time.Duration
	sent, lost      uint64
	rx, tx, dropped atomic.Uint64
}

func New(parent context.Context, channel Channel, info Info, options Options) (*Link, error) {
	options = options.defaults()
	if options.Timeout < 2*options.Heartbeat || options.Timeout/options.Heartbeat > 64 {
		return nil, errors.New("link timeout must span 2–64 heartbeats")
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
	l := &Link{channel: channel, info: info, options: options, ctx: ctx, cancel: cancel, done: make(chan struct{}), data: make(chan queuedPacket, queueSize), selection: make(chan Selection, 8), control: make(chan []byte, 8), packets: make(chan []byte, queueSize), pending: map[uint64]time.Time{}}
	if _, ok := channel.(interface {
		ReceiveOwnedBatch(context.Context) ([]*packetbuf.Buffer, error)
	}); ok && options.OwnedPackets {
		l.ownedPackets = make(chan *packetbuf.Buffer, queueSize)
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
			close(l.ownedPackets)
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
	if l.ownedPackets != nil {
		for b := range l.ownedPackets {
			b.Release()
		}
	}
	return nil
}
func (l *Link) OwnedPackets() <-chan *packetbuf.Buffer { return l.ownedPackets }
func (l *Link) Send(ctx context.Context, frame []byte) error {
	l.queueMu.Lock()
	defer l.queueMu.Unlock()
	if len(frame) <= packet.HeaderSize || len(frame) > packet.MaxFrame {
		return errors.New("invalid overlay frame size")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.ctx.Done():
		return net.ErrClosed
	default:
	}
	generation := l.dataGeneration.Load()
	if generation%2 == 0 {
		return ErrUnavailable
	}
	buffer := packetbuf.Get(len(frame) + 1)
	message := buffer.Data
	message[0] = dataMessage
	copy(message[1:], frame)
	select {
	case l.data <- queuedPacket{generation: generation, raw: message, buffer: buffer}:
		return nil
	case <-l.ctx.Done():
		buffer.Release()
		return net.ErrClosed
	default:
		buffer.Release()
		l.dropped.Add(1)
		return ErrQueueFull
	}
}
func (l *Link) healthy() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ctx.Err() == nil && !l.lastPong.IsZero() && time.Since(l.lastPong) < l.options.Timeout
}
func (l *Link) Stats() model.LinkStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	healthy := l.ctx.Err() == nil && !l.lastPong.IsZero() && time.Since(l.lastPong) < l.options.Timeout
	loss := float64(0)
	if l.sent > 0 {
		loss = float64(l.lost) / float64(l.sent)
	}
	return model.LinkStatus{NetworkID: l.info.NetworkID, EdgeID: l.info.EdgeID, LinkID: l.ID(), CandidateID: l.info.CandidateID, Transport: l.info.Transport, Remote: l.channel.RemoteAddr().String(), Healthy: healthy, RTTMillis: float64(l.rtt) / float64(time.Millisecond), Loss: loss, RXBytes: l.rx.Load(), TXBytes: l.tx.Load()}
}
func (l *Link) readLoop() {
	owned, owning := l.channel.(interface {
		ReceiveOwnedBatch(context.Context) ([]*packetbuf.Buffer, error)
	})
	var ownedPending []*packetbuf.Buffer
	var current *packetbuf.Buffer
	owning = owning && l.ownedPackets != nil
	batch, batching := l.channel.(interface {
		ReceiveBatch(context.Context) ([][]byte, error)
	})
	var pending [][]byte
	defer l.wg.Done()
	defer func() { current.Release(); packetbuf.ReleaseAll(ownedPending) }()
	defer l.stop()
	for {
		var raw []byte
		var err error
		current.Release()
		current = nil
		if owning {
			if len(ownedPending) == 0 {
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
		case dataMessage:
			if len(raw) <= packet.HeaderSize+1 || len(raw) > packet.MaxFrame+1 {
				return
			}
			l.rx.Add(uint64(len(raw) - 1))
			if current != nil {
				current.Data = raw[1:]
				select {
				case l.ownedPackets <- current:
					current = nil
				default:
					l.dropped.Add(1)
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
			now := time.Now()
			l.mu.Lock()
			if sent, ok := l.pending[nonce]; ok {
				sample := now.Sub(sent)
				if l.rtt == 0 {
					l.rtt = sample
				} else {
					l.rtt = (l.rtt*4 + sample) / 5
				}
				l.lastPong = now
				delete(l.pending, nonce)
			}
			l.mu.Unlock()
		default:
			return
		}
	}
}
func (l *Link) writeLoop() {
	batch, batching := l.channel.(interface {
		SendBatch(context.Context, [][]byte) error
	})
	var messages [32][]byte
	var owners [32]*packetbuf.Buffer
	defer l.wg.Done()
	defer func() {
		l.stop()
		packetbuf.ReleaseAll(owners[:])
		for {
			select {
			case data := <-l.data:
				data.buffer.Release()
			default:
				return
			}
		}
	}()
	for {
		var message []byte
		var generation uint64
		// Heartbeats have priority over queued user traffic.
		select {
		case message = <-l.control:
		default:
		}
		if message == nil {
			select {
			case <-l.ctx.Done():
				return
			case message = <-l.control:
			case data := <-l.data:
				message, generation = data.raw, data.generation
				owners[0] = data.buffer
			}
		}
		if message[0] == dataMessage && (generation%2 == 0 || generation != l.dataGeneration.Load()) {
			l.dropped.Add(1)
			owners[0].Release()
			owners[0] = nil
			continue
		}
		messages[0] = message
		count := 1
		if batching && message[0] == dataMessage {
		drain:
			for count < len(messages) {
				select {
				case data := <-l.data:
					if data.generation%2 == 0 || data.generation != l.dataGeneration.Load() {
						l.dropped.Add(1)
						data.buffer.Release()
						continue
					}
					messages[count] = data.raw
					owners[count] = data.buffer
					count++
				default:
					break drain
				}
			}
		}
		ctx, cancel := context.WithTimeout(l.ctx, l.options.WriteTimeout)
		var err error
		if batching {
			err = batch.SendBatch(ctx, messages[:count])
		} else {
			err = l.channel.Send(ctx, message)
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
	start := time.Now()
	ticker := time.NewTicker(l.options.Heartbeat)
	defer ticker.Stop()
	for {
		now := time.Now()
		l.mu.Lock()
		for seq, sent := range l.pending {
			if now.Sub(sent) >= l.options.Timeout {
				delete(l.pending, seq)
				l.lost++
			}
		}
		since := l.lastPong
		if since.IsZero() {
			since = start
		}
		if now.Sub(since) >= l.options.Timeout {
			l.mu.Unlock()
			return
		}
		l.nextPing++
		nonce := l.nextPing
		raw := make([]byte, 9)
		raw[0] = pingMessage
		binary.BigEndian.PutUint64(raw[1:], nonce)
		select {
		case l.control <- raw:
			l.pending[nonce] = now
			l.sent++
		default:
		}
		l.mu.Unlock()
		if l.channel.NeedsRekey() {
			return
		}
		select {
		case <-l.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
