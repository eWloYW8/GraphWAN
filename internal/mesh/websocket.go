package mesh

import (
	"bufio"
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/transport"
)

// httpIngress receives only already-classified HTTP connections from the main
// TCP listener. It has no independently bound port or unbounded accept queue.
type httpIngress struct {
	address net.Addr
	pending chan net.Conn
	done    chan struct{}
	once    sync.Once
}

func (l *httpIngress) Addr() net.Addr { return l.address }
func (l *httpIngress) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *httpIngress) Accept() (net.Conn, error) {
	select {
	case <-l.done:
		return nil, net.ErrClosed
	case conn := <-l.pending:
		return conn, nil
	}
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

// Hold a classifier slot until net/http has parsed the upgrade request. Merely
// sending 'G' must not let a slow client escape the pre-authentication limit.
type httpConn struct {
	net.Conn
	release func()
}

func (c *httpConn) Close() error {
	err := c.Conn.Close()
	c.release()
	return err
}

func (c *bufferedConn) Read(raw []byte) (int, error) { return c.reader.Read(raw) }

func (m *Mesh) startHTTP(tlsConfig *tls.Config) {
	m.webListener = &httpIngress{address: m.listener.Addr(), pending: make(chan net.Conn), done: make(chan struct{})}
	m.webServer = &http.Server{Handler: http.HandlerFunc(m.acceptWebSocket), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8192}
	m.webServer.SetKeepAlivesEnabled(false)
	m.webServer.ConnState = func(conn net.Conn, state http.ConnState) {
		if state == http.StateActive || state == http.StateClosed || state == http.StateHijacked {
			conn.(*httpConn).release()
		}
	}
	m.tls = tlsConfig
	m.wg.Add(1)
	go func() { defer m.wg.Done(); m.webServer.Serve(m.webListener) }()
}
func (m *Mesh) classify(conn net.Conn) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		conn.Close()
		return
	}
	select {
	case m.sniffSlots <- struct{}{}:
	default:
		m.mu.Unlock()
		conn.Close()
		return
	}
	m.pending[conn] = true
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer func() { m.mu.Lock(); delete(m.pending, conn); m.mu.Unlock() }()
		release := sync.OnceFunc(func() { <-m.sniffSlots })
		transferred := false
		defer func() {
			if !transferred {
				conn.Close()
				release()
			}
		}()
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		stop := context.AfterFunc(ctx, func() { conn.Close() })
		defer stop()
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		reader := bufio.NewReaderSize(conn, 4096)
		first, err := reader.Peek(1)
		if err != nil {
			return
		}
		var current net.Conn = &bufferedConn{Conn: conn, reader: reader}
		switch first[0] {
		case 0: // A length-framed GraphWAN message is bounded to 16 KiB.
			conn.SetReadDeadline(time.Time{})
			if !stop() {
				return
			}
			transferred = true
			release()
			m.accept(transport.NewStream(current), model.TCP)
		case 22: // TLS handshake record, followed by HTTP/1.1 WebSocket upgrade.
			secured := tls.Server(current, m.tls)
			if err := secured.HandshakeContext(ctx); err != nil {
				return
			}
			current = secured
			fallthrough
		case 'G':
			conn.SetReadDeadline(time.Time{})
			// Stop the classifier's cancellation callback before transferring ownership
			// to net/http; m.Close also closes net/http's tracked connections.
			if !stop() {
				return
			}
			select {
			case m.webListener.pending <- &httpConn{Conn: current, release: release}:
				transferred = true
			case <-ctx.Done():
			case <-m.webListener.done:
			}
		}
	}()
}

func (m *Mesh) acceptWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" || r.URL.RawQuery != "" || r.Header.Get("Origin") != "" {
		http.Error(w, "peer WebSocket upgrade required", http.StatusBadRequest)
		return
	}
	var kind model.Transport
	switch r.Header.Get("Sec-WebSocket-Protocol") {
	case transport.WebSocketProtocol(model.WS):
		kind = model.WS
	case transport.WebSocketProtocol(model.WSS):
		kind = model.WSS
	default:
		http.Error(w, "unsupported peer subprotocol", http.StatusBadRequest)
		return
	}
	path := transport.WebSocketPath(r.URL)
	allowed := false
	m.mu.Lock()
	if !m.closed {
		for _, g := range m.groups {
			for _, endpoint := range g.policy.Load().endpoints {
				if endpoint.Source != model.Manual || endpoint.Transport != kind {
					continue
				}
				parsed, _ := url.Parse(endpoint.URL)
				if transport.WebSocketPath(parsed) == path {
					allowed = true
					break
				}
			}
			if allowed {
				break
			}
		}
	}
	m.mu.Unlock()
	if !allowed {
		http.NotFound(w, r)
		return
	}
	rc := http.NewResponseController(w)
	if rc.SetReadDeadline(time.Time{}) != nil || rc.SetWriteDeadline(time.Time{}) != nil {
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{transport.WebSocketProtocol(kind)}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	remote, err := net.ResolveTCPAddr("tcp", r.RemoteAddr)
	if err != nil {
		conn.CloseNow()
		return
	}
	local, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if local == nil {
		conn.CloseNow()
		return
	}
	// WSS may terminate at a reverse proxy. The outer hop is not trusted as an
	// Agent identity; the inner Noise handshake still verifies graph membership.
	m.accept(transport.NewWebSocket(conn, local, remote, path), kind)
}
