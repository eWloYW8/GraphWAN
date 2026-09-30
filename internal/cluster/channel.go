package cluster

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/hashicorp/yamux"
)

const channelProtocol = "graphwan.cluster.v1"

var errLegacyPeer = errors.New("server does not support bidirectional cluster channels")

type channel struct {
	session  *yamux.Session
	outbound bool
	slots    chan struct{}
}
type channelPeer struct {
	active  *channel
	dialing bool
	retry   time.Time
	err     error
	changed chan struct{}
}

// Both ends may open streams on an authenticated connection, regardless of
// which end could reach the other's listener. There is at most one dial in
// flight per peer; an incoming connection wakes callers waiting on that dial.
type channels struct {
	local  model.ID
	ctx    context.Context
	cancel context.CancelFunc
	dial   func(context.Context, model.ID) (net.Conn, error)
	accept func(model.ID, net.Conn)
	mu     sync.Mutex
	closed bool
	peers  map[model.ID]*channelPeer
	wg     sync.WaitGroup
}

func newChannels(ctx context.Context, local model.ID, dial func(context.Context, model.ID) (net.Conn, error), accept func(model.ID, net.Conn)) *channels {
	ctx, cancel := context.WithCancel(ctx)
	return &channels{local: local, ctx: ctx, cancel: cancel, dial: dial, accept: accept, peers: map[model.ID]*channelPeer{}}
}
func (p *channelPeer) notify() { close(p.changed); p.changed = make(chan struct{}) }
func (c *channels) peer(id model.ID) *channelPeer {
	p := c.peers[id]
	if p == nil {
		p = &channelPeer{changed: make(chan struct{})}
		c.peers[id] = p
	}
	return p
}
func (c *channels) attach(id model.ID, conn net.Conn, outbound bool) (*channel, error) {
	config := yamux.DefaultConfig()
	config.AcceptBacklog = 64
	config.KeepAliveInterval = 2 * time.Second
	config.ConnectionWriteTimeout = 3 * time.Second
	config.StreamOpenTimeout = 3 * time.Second
	config.StreamCloseTimeout = 3 * time.Second
	config.LogOutput = io.Discard
	var session *yamux.Session
	var err error
	if outbound {
		session, err = yamux.Client(conn, config)
	} else {
		session, err = yamux.Server(conn, config)
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	next := &channel{session: session, outbound: outbound, slots: make(chan struct{}, 64)}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		session.Close()
		return nil, net.ErrClosed
	}
	p := c.peer(id)
	old := p.active
	// If simultaneous dials succeed, both peers prefer the connection initiated
	// by the lower Server ID. A lone connection is usable in either direction.
	preferred := outbound == (c.local < id)
	if old != nil && !old.session.IsClosed() && (!preferred || old.outbound == outbound) {
		c.mu.Unlock()
		session.Close()
		return old, nil
	}
	p.active = next
	p.err = nil
	p.notify()
	c.wg.Add(1)
	c.mu.Unlock()
	go c.receive(id, p, next)
	if old != nil {
		old.session.Close()
	}
	return next, nil
}
func (c *channels) receive(id model.ID, p *channelPeer, ch *channel) {
	defer c.wg.Done()
	var handlers sync.WaitGroup
	defer func() {
		ch.session.Close()
		handlers.Wait()
		c.mu.Lock()
		if p.active == ch {
			p.active = nil
			p.notify()
		}
		c.mu.Unlock()
	}()
	for {
		stream, err := ch.session.AcceptStream()
		if err != nil {
			return
		}
		select {
		case ch.slots <- struct{}{}:
		default:
			stream.Close()
			continue
		}
		conn := &channelStream{Conn: stream, release: sync.OnceFunc(func() { <-ch.slots })}
		handlers.Add(1)
		go func() { defer handlers.Done(); c.accept(id, conn) }()
	}
}
func (c *channels) connect(id model.ID, p *channelPeer) {
	defer c.wg.Done()
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	conn, err := c.dial(ctx, id)
	if err == nil {
		_, err = c.attach(id, conn, true)
	}
	c.mu.Lock()
	p.dialing = false
	p.err = err
	p.retry = time.Now().Add(time.Second)
	p.notify()
	c.mu.Unlock()
}
func (c *channels) open(ctx context.Context, id model.ID) (net.Conn, error) {
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return nil, net.ErrClosed
		}
		if err := ctx.Err(); err != nil {
			c.mu.Unlock()
			return nil, err
		}
		p := c.peer(id)
		if p.active != nil && !p.active.session.IsClosed() {
			ch := p.active
			c.mu.Unlock()
			return ch.open(ctx)
		}
		if !p.dialing {
			if p.err != nil && time.Now().Before(p.retry) {
				err := p.err
				c.mu.Unlock()
				return nil, err
			}
			p.dialing = true
			c.wg.Add(1)
			go c.connect(id, p)
		}
		changed := p.changed
		c.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.ctx.Done():
			return nil, net.ErrClosed
		}
	}
}
func (ch *channel) open(ctx context.Context) (net.Conn, error) {
	select {
	case ch.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-ch.session.CloseChan():
		return nil, net.ErrClosed
	}
	type result struct {
		conn net.Conn
		err  error
	}
	ready := make(chan result)
	go func() {
		stream, err := ch.session.OpenStream()
		var conn net.Conn
		if err != nil {
			<-ch.slots
		} else {
			conn = &channelStream{Conn: stream, release: sync.OnceFunc(func() { <-ch.slots })}
		}
		select {
		case ready <- result{conn, err}:
		case <-ctx.Done():
			if conn != nil {
				conn.Close()
			}
		}
	}()
	select {
	case r := <-ready:
		return r.conn, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (c *channels) prune(allowed map[model.ID]bool) {
	c.mu.Lock()
	var close []*yamux.Session
	for id, p := range c.peers {
		if !allowed[id] && p.active != nil {
			close = append(close, p.active.session)
			p.active = nil
			p.notify()
		}
		if !allowed[id] && !p.dialing {
			delete(c.peers, id)
		}
	}
	c.mu.Unlock()
	for _, s := range close {
		s.Close()
	}
}
func (c *channels) close() {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		c.cancel()
		for _, p := range c.peers {
			if p.active != nil {
				p.active.session.Close()
			}
			p.notify()
		}
	}
	c.mu.Unlock()
	c.wg.Wait()
}

type channelStream struct {
	net.Conn
	release func()
}

func (c *channelStream) Close() error { err := c.Conn.Close(); c.release(); return err }
