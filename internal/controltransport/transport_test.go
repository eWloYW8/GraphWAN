package controltransport

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/eWloYW8/GraphWAN/internal/pki"
	"github.com/eWloYW8/GraphWAN/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func fixture(t *testing.T, handler http.Handler) (*Server, string, *x509.CertPool) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ca, err := pki.LoadOrCreate(db)
	if err != nil {
		t.Fatal(err)
	}
	config, err := ca.ServerTLS([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(listener, handler, config)
	// Reproduce request timeout expiry without a 30-second test. Upgraded
	// carrier streams must clear these deadlines independently of inner HTTP.
	s.plainHTTP.ReadTimeout = time.Second
	s.plainHTTP.WriteTimeout = time.Second
	s.secureHTTP.ReadTimeout = time.Second
	s.secureHTTP.WriteTimeout = time.Second
	done := make(chan error, 1)
	go func() { done <- s.Serve() }()
	t.Cleanup(func() {
		s.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("server failed to close")
		}
		s.Wait()
	})
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca.PEM)
	return s, listener.Addr().String(), roots
}

func TestNestedWebSocketSurvivesSetupContextAndHTTPDeadlines(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || r.TLS.Version != tls.VersionTLS13 {
			http.Error(w, "inner TLS required", 401)
			return
		}
		rc := http.NewResponseController(w)
		rc.SetReadDeadline(time.Time{})
		rc.SetWriteDeadline(time.Time{})
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		ws.SetReadLimit(2 << 20)
		for {
			kind, raw, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			if ws.Write(r.Context(), kind, raw) != nil {
				return
			}
		}
	})
	_, addr, roots := fixture(t, handler)
	var sockets []*websocket.Conn
	for _, kind := range []string{"tcp", "websocket", "grpc", "wss"} {
		tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}, DialContext: DialContext(kind, roots)}
		t.Cleanup(tr.CloseIdleConnections)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		ws, _, err := websocket.Dial(ctx, "https://"+addr+"/echo", &websocket.DialOptions{HTTPClient: &http.Client{Transport: tr}})
		cancel()
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		t.Cleanup(func() { ws.CloseNow() })
		ws.SetReadLimit(2 << 20)
		sockets = append(sockets, ws)
	}
	time.Sleep(1200 * time.Millisecond)
	for i, ws := range sockets {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		payload := bytes.Repeat([]byte{byte(i + 1)}, 1<<20)
		if err := ws.Write(ctx, websocket.MessageBinary, payload); err != nil {
			cancel()
			t.Fatal(i, err)
		}
		_, got, err := ws.Read(ctx)
		cancel()
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("carrier %d large echo: %v, size %d", i, err, len(got))
		}
	}
}

func TestPlainPortCannotReachManagementAndTunnelCannotRecurse(t *testing.T) {
	_, addr, roots := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == WebSocketPath {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("secure"))
	}))
	client := &http.Client{Timeout: 3 * time.Second}
	for _, path := range []string{"/", "/api/v1/login", "/api/v1/enroll", "/api/v1/agent/control"} {
		resp, err := client.Get("http://" + addr + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 426 {
			t.Fatalf("plaintext %s: %d", path, resp.StatusCode)
		}
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, DialContext: DialContext("websocket", roots)}
	defer tr.CloseIdleConnections()
	client.Transport = tr
	resp, err := client.Get("https://" + addr + WebSocketPath)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("nested carrier endpoint exposed: %d", resp.StatusCode)
	}
}

func TestCarrierDeadlinesAndServerClose(t *testing.T) {
	s, addr, roots := fixture(t, http.NotFoundHandler())
	for _, kind := range []string{"websocket", "grpc", "wss"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			c, err := DialContext(kind, roots)(ctx, "tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			c.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
			_, err = c.Read(make([]byte, 1))
			if e, ok := err.(net.Error); !ok || !e.Timeout() {
				t.Fatalf("read deadline: %v", err)
			}
			c.SetReadDeadline(time.Time{})
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := DialContext("grpc", roots)(ctx, "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s.Close()
	c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = c.Read(make([]byte, 1)); err == nil {
		t.Fatal("server close left carrier open")
	}
}

func TestMalformedOuterMessagesAndCanceledSetup(t *testing.T) {
	_, addr, roots := fixture(t, http.NotFoundHandler())
	for _, kind := range []string{"tcp", "websocket", "grpc", "wss"} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if c, err := DialContext(kind, roots)(ctx, "tcp", addr); err == nil {
			c.Close()
			t.Fatalf("%s ignored canceled dial", kind)
		}
	}
	for _, test := range []struct {
		kind    websocket.MessageType
		payload []byte
	}{{websocket.MessageText, []byte("bad")}, {websocket.MessageBinary, nil}, {websocket.MessageBinary, make([]byte, maxChunk+1)}} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		ws, _, err := websocket.Dial(ctx, "ws://"+addr+WebSocketPath, &websocket.DialOptions{Subprotocols: []string{WebSocketProtocol}})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		ws.Write(ctx, test.kind, test.payload)
		if _, _, err = ws.Read(ctx); err == nil || ctx.Err() != nil {
			t.Errorf("malformed WebSocket message not promptly closed: %v", err)
		}
		ws.CloseNow()
		cancel()
	}
	cc, err := grpc.NewClient("passthrough:///"+addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithNoProxy())
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	for _, payload := range [][]byte{nil, make([]byte, maxChunk+1)} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		stream, err := cc.NewStream(ctx, &grpc.StreamDesc{ClientStreams: true, ServerStreams: true}, GRPCMethod)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		stream.Header()
		stream.SendMsg(&wrapperspb.BytesValue{Value: payload})
		var value wrapperspb.BytesValue
		if err := stream.RecvMsg(&value); err == nil || ctx.Err() != nil {
			t.Errorf("malformed gRPC message not promptly closed: %v", err)
		}
		cancel()
	}
}

func TestByteTunnelWriteDeadlineUnderBackpressure(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	c := byteTunnel(func(raw []byte) error { _, err := b.Write(raw); return err }, func() ([]byte, error) { var buf [1]byte; _, err := b.Read(buf[:]); return nil, err }, func() { b.Close() }, a.LocalAddr(), a.RemoteAddr())
	defer c.Close()
	c.SetWriteDeadline(time.Now().Add(30 * time.Millisecond))
	_, err := io.Copy(c, bytes.NewReader(make([]byte, 3*maxChunk)))
	if e, ok := err.(net.Error); !ok || !e.Timeout() {
		t.Fatalf("write deadline: %v", err)
	}
}
