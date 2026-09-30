package controltransport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/coder/websocket"
	"github.com/eWloYW8/GraphWAN/internal/transport"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func Validate(kind string) error {
	switch kind {
	case "", "tcp", "websocket", "grpc", "wss":
		return nil
	default:
		return fmt.Errorf("unsupported server transport %q: choose tcp, websocket, grpc or wss", kind)
	}
}

// DialContext returns an *unencrypted* byte-stream adapter. http.Transport then
// performs its normal server-verified TLS handshake over it, using the Agent
// certificate after enrollment. Outer WSS has independent server-only TLS.
func DialContext(kind string, roots *x509.CertPool, serverNames ...string) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if err := Validate(kind); err != nil {
			return nil, err
		}
		switch kind {
		case "", "tcp":
			return transport.DialTCP(ctx, &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}, network, addr)
		case "grpc":
			return dialGRPC(ctx, addr)
		default:
			name := ""
			if len(serverNames) > 0 {
				name = serverNames[0]
			}
			return dialWebSocket(ctx, kind, addr, roots, name)
		}
	}
}

func dialWebSocket(ctx context.Context, kind, addr string, roots *x509.CertPool, serverName string) (net.Conn, error) {
	scheme := "ws"
	if kind == "wss" {
		scheme = "wss"
	}
	u := url.URL{Scheme: scheme, Host: addr, Path: WebSocketPath}
	tr := &http.Transport{DialContext: DialContext("tcp", roots), TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: serverName, MinVersion: tls.VersionTLS13}, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 10 * time.Second}
	httpClient := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ws, resp, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPClient: httpClient, Subprotocols: []string{WebSocketProtocol}, CompressionMode: websocket.CompressionDisabled})
	tr.CloseIdleConnections()
	if err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		return nil, err
	}
	if ws.Subprotocol() != WebSocketProtocol {
		ws.CloseNow()
		return nil, errors.New("controller did not acknowledge WebSocket tunnel protocol")
	}
	return webSocketConn(ws, address("local"), address(addr)), nil
}

func webSocketConn(ws *websocket.Conn, local, remote net.Addr) *tunnelConn {
	ws.SetReadLimit(maxChunk)
	ctx, cancel := context.WithCancel(context.Background())
	return byteTunnel(func(raw []byte) error { return ws.Write(ctx, websocket.MessageBinary, raw) }, func() ([]byte, error) {
		kind, raw, err := ws.Read(ctx)
		if err == nil && kind != websocket.MessageBinary {
			err = errChunk
		}
		return raw, err
	}, func() { cancel(); ws.CloseNow() }, local, remote)
}

func grpcConn(stream messageStream, closeTransport func(), local, remote net.Addr) *tunnelConn {
	return byteTunnel(func(raw []byte) error { return stream.SendMsg(&wrapperspb.BytesValue{Value: raw}) }, func() ([]byte, error) {
		var value wrapperspb.BytesValue
		err := stream.RecvMsg(&value)
		return value.Value, err
	}, closeTransport, local, remote)
}

func dialGRPC(ctx context.Context, addr string) (net.Conn, error) {
	cc, err := grpc.NewClient("passthrough:///"+addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithNoProxy(), grpc.WithDisableServiceConfig(),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return transport.DialTCP(ctx, &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}, "tcp", addr)
		}),
		grpc.WithMaxHeaderListSize(8192),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxChunk+4), grpc.MaxCallSendMsgSize(maxChunk+4)))
	if err != nil {
		return nil, err
	}
	// The HTTP dial context expires after setup. Cancel it only during setup;
	// the established stream is instead owned by the returned net.Conn.
	lifetime, cancel := context.WithCancel(context.Background())
	stop := context.AfterFunc(ctx, cancel)
	stream, err := cc.NewStream(lifetime, &grpc.StreamDesc{ClientStreams: true, ServerStreams: true}, GRPCMethod, grpc.WaitForReady(true))
	if err == nil {
		header, headerErr := stream.Header()
		if headerErr != nil {
			err = headerErr
		} else if values := header.Get("graphwan-protocol"); len(values) != 1 || values[0] != GRPCProtocol {
			err = errors.New("controller did not acknowledge gRPC tunnel protocol")
		}
	}
	if !stop() && err == nil {
		err = ctx.Err()
	}
	if err != nil {
		cancel()
		cc.Close()
		return nil, err
	}
	return grpcConn(stream, func() { cancel(); cc.Close() }, address("local"), address(addr)), nil
}

// WebSocketStream adapts a binary WebSocket to a bounded stream with deadlines.
func WebSocketStream(ws *websocket.Conn, local, remote net.Addr) (net.Conn, <-chan struct{}) {
	c := webSocketConn(ws, local, remote)
	return c, c.done
}
