package packetbuf

import (
	"context"
	"errors"
	"net"
	"sync"
)

// Queue publishes a packet batch atomically. A consumer cannot
// drain the first packet while its producer is still queuing the rest, which
// would fragment batches before GRO. Capacity is in packets, not batch objects.
type Queue struct {
	mu          sync.Mutex
	ready       chan struct{}
	packets     []*Buffer
	head, count int
	closed      bool
}

func NewQueue(capacity int) *Queue {
	if capacity <= 0 {
		panic("invalid packet queue capacity")
	}
	return &Queue{ready: make(chan struct{}, 1), packets: make([]*Buffer, capacity)}
}
func (q *Queue) notify() {
	select {
	case q.ready <- struct{}{}:
	default:
	}
}

// Put takes ownership of all buffers, releasing the tail on overflow or close.
func (q *Queue) Put(buffers []*Buffer) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	accepted := 0
	if !q.closed {
		accepted = min(len(buffers), len(q.packets)-q.count)
		for _, b := range buffers[:accepted] {
			q.packets[(q.head+q.count)%len(q.packets)] = b
			q.count++
		}
		if q.count > 0 {
			q.notify()
		}
	}
	ReleaseAll(buffers[accepted:])
	return len(buffers) - accepted
}

// Read transfers ownership into dst. It never waits to fill a batch.
func (q *Queue) Read(ctx context.Context, dst []*Buffer) (int, error) {
	if len(dst) == 0 {
		return 0, errors.New("empty packet batch destination")
	}
	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-q.ready:
		}
		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			return 0, net.ErrClosed
		}
		count := min(len(dst), q.count)
		for i := range count {
			dst[i] = q.packets[q.head]
			q.packets[q.head] = nil
			q.head = (q.head + 1) % len(q.packets)
		}
		q.count -= count
		if q.count > 0 {
			q.notify()
		}
		q.mu.Unlock()
		if count > 0 {
			return count, nil
		}
	}
}
func (q *Queue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	for i := range q.packets {
		q.packets[i].Release()
		q.packets[i] = nil
	}
	q.count = 0
	close(q.ready)
}
