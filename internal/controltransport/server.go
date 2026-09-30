package controltransport

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Server multiplexes direct TLS, plaintext WebSocket and HTTP/2 gRPC on one
// socket. HTTPS also accepts outer WSS. Tunnels terminate at a separate inner
// TLS listener: their headers can never substitute for a verified client cert,
// and the inner handler cannot recursively open more tunnels.
type Server struct {
	listener                         net.Listener
	tlsConfig                        *tls.Config
	plain, secure, inner             *queueListener
	plainHTTP, secureHTTP, innerHTTP *http.Server
	grpc                             *grpc.Server
	pending                          chan struct{}
	mu                               sync.Mutex
	closed                           bool
	connections                      map[net.Conn]struct{}
	closeOnce                        sync.Once
	wg                               sync.WaitGroup
}

func NewServer(listener net.Listener, handler http.Handler, config *tls.Config) *Server {
	s := &Server{listener: listener, tlsConfig: config.Clone(), pending: make(chan struct{}, 128), connections: make(map[net.Conn]struct{})}
	s.plain, s.secure, s.inner = newListener(listener.Addr()), newListener(listener.Addr()), newListener(listener.Addr())
	s.grpc = grpc.NewServer(grpc.UnknownServiceHandler(s.acceptGRPC), grpc.MaxRecvMsgSize(maxChunk+4), grpc.MaxSendMsgSize(maxChunk+4), grpc.MaxConcurrentStreams(16), grpc.MaxHeaderListSize(8192))
	outer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == WebSocketPath:
			s.acceptWebSocket(w, r)
		case r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc"):
			// The outer gRPC is only a byte carrier; authentication happens
			// inside its TLS stream, even when reached through a proxy.
			rc := http.NewResponseController(w)
			if rc.SetReadDeadline(time.Time{}) != nil || rc.SetWriteDeadline(time.Time{}) != nil {
				http.Error(w, "stream deadlines unavailable", 500)
				return
			}
			s.grpc.ServeHTTP(w, r)
		case r.TLS != nil:
			handler.ServeHTTP(w, r)
		default:
			http.Error(w, "HTTPS required", http.StatusUpgradeRequired)
		}
	})
	newHTTP := func(h http.Handler) *http.Server {
		return &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10, HTTP2: &http.HTTP2Config{MaxConcurrentStreams: 16}}
	}
	s.plainHTTP, s.secureHTTP, s.innerHTTP = newHTTP(outer), newHTTP(outer), newHTTP(handler)
	s.secureHTTP.TLSConfig = config.Clone()
	s.plainHTTP.Protocols = new(http.Protocols)
	s.plainHTTP.Protocols.SetHTTP1(true)
	s.plainHTTP.Protocols.SetUnencryptedHTTP2(true)
	// WebSocket upgrades require HTTP/1.1 on the *inner* connection.
	s.innerHTTP.Protocols = new(http.Protocols)
	s.innerHTTP.Protocols.SetHTTP1(true)
	return s
}

func (s *Server) track(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		c.Close()
		return false
	}
	s.connections[c] = struct{}{}
	return true
}
func (s *Server) untrack(c net.Conn) { s.mu.Lock(); delete(s.connections, c); s.mu.Unlock() }

func (s *Server) Serve() error {
	defer s.Close()
	for _, serve := range []func() error{
		func() error { return s.plainHTTP.Serve(s.plain) },
		func() error { return s.secureHTTP.ServeTLS(s.secure, "", "") },
		func() error { return s.innerHTTP.Serve(s.inner) },
	} {
		s.wg.Add(1)
		go func() { defer s.wg.Done(); serve(); s.Close() }()
	}
	for {
		c, err := s.listener.Accept()
		if err != nil {
			return err
		}
		select {
		case s.pending <- struct{}{}:
		default:
			c.Close()
			continue
		}
		if !s.track(c) {
			<-s.pending
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { <-s.pending }()
			handed := false
			defer func() {
				if !handed {
					c.Close()
					s.untrack(c)
				}
			}()
			c.SetReadDeadline(time.Now().Add(5 * time.Second))
			r := bufio.NewReaderSize(c, 1024)
			prefix, err := r.Peek(1)
			if err != nil {
				c.Close()
				return
			}
			c.SetReadDeadline(time.Time{})
			target := s.plain
			if prefix[0] == 22 {
				target = s.secure
			} else if prefix[0] != 'G' && prefix[0] != 'P' {
				c.Close()
				return
			}
			handed = target.put(&bufferedConn{Conn: c, reader: r, release: sync.OnceFunc(func() { s.untrack(c) })})
		}()
	}
}

func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		connections := make([]net.Conn, 0, len(s.connections))
		for c := range s.connections {
			connections = append(connections, c)
		}
		s.mu.Unlock()
		s.listener.Close()
		s.plain.Close()
		s.secure.Close()
		s.inner.Close()
		for _, c := range connections {
			c.Close()
		}
		s.plainHTTP.Close()
		s.secureHTTP.Close()
		s.innerHTTP.Close()
		s.grpc.Stop()
	})
	return nil
}

// Wait is called after Serve returns and Close has interrupted pending I/O.
func (s *Server) Wait() { s.wg.Wait() }

func (s *Server) acquire() bool {
	select {
	case s.pending <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Server) acceptWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" || r.Header.Get("Origin") != "" || r.URL.RawQuery != "" || r.URL.ForceQuery || r.Header.Get("Sec-WebSocket-Protocol") != WebSocketProtocol {
		http.Error(w, "control tunnel required", http.StatusBadRequest)
		return
	}
	if !s.acquire() {
		http.Error(w, "too many pending tunnels", http.StatusServiceUnavailable)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{WebSocketProtocol}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		<-s.pending
		return
	}
	local, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	c := webSocketConn(ws, local, address(r.RemoteAddr))
	s.acceptTunnel(c)
}

func (s *Server) acceptGRPC(_ any, stream grpc.ServerStream) error {
	method, _ := grpc.MethodFromServerStream(stream)
	md, _ := metadata.FromIncomingContext(stream.Context())
	if method != GRPCMethod || len(md.Get("origin")) != 0 {
		return status.Error(codes.PermissionDenied, "control tunnel required")
	}
	if !s.acquire() {
		return status.Error(codes.ResourceExhausted, "too many pending tunnels")
	}
	if err := stream.SendHeader(metadata.Pairs("graphwan-protocol", GRPCProtocol)); err != nil {
		<-s.pending
		return err
	}
	p, ok := peer.FromContext(stream.Context())
	if !ok || p.Addr == nil {
		<-s.pending
		return status.Error(codes.Internal, "missing peer address")
	}
	c := grpcConn(stream, func() {}, s.listener.Addr(), p.Addr)
	s.acceptTunnel(c)
	return nil
}

// The caller holds a pending slot. A verified inner TLS handshake releases it;
// enrollment can then run without a certificate, while /agent/control still
// enforces the controller's normal mTLS identity and revocation checks.
func (s *Server) acceptTunnel(c *tunnelConn) {
	if !s.track(c) {
		<-s.pending
		return
	}
	defer s.untrack(c)
	defer c.Close()
	config := s.tlsConfig.Clone()
	config.NextProtos = []string{"http/1.1"}
	inner := tls.Server(c, config)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := inner.HandshakeContext(ctx)
	cancel()
	<-s.pending
	if err != nil {
		return
	}
	if !s.inner.put(inner) {
		return
	}
	<-c.done
}

type bufferedConn struct {
	net.Conn
	reader  *bufio.Reader
	release func()
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
func (c *bufferedConn) Close() error               { err := c.Conn.Close(); c.release(); return err }

type queueListener struct {
	addr     net.Addr
	incoming chan net.Conn
	done     chan struct{}
	once     sync.Once
}

func newListener(addr net.Addr) *queueListener {
	return &queueListener{addr: addr, incoming: make(chan net.Conn), done: make(chan struct{})}
}
func (l *queueListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.incoming:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *queueListener) put(c net.Conn) bool {
	select {
	case l.incoming <- c:
		return true
	case <-l.done:
		return false
	}
}
func (l *queueListener) Addr() net.Addr { return l.addr }
func (l *queueListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }

// Closed is useful to callers sharing shutdown handling with net/http.Server.
func Closed(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, http.ErrServerClosed)
}
