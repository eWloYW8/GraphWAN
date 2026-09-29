package transport

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"

	"github.com/coder/websocket"
	"github.com/graphwan/graphwan/internal/model"
)

type WebSocket struct {
	conn          *websocket.Conn
	local, remote net.Addr
	path          string
}

func NewWebSocket(conn *websocket.Conn, local, remote net.Addr, path string) *WebSocket {
	conn.SetReadLimit(MaxMessage)
	return &WebSocket{conn: conn, local: local, remote: remote, path: path}
}
func (w *WebSocket) LocalAddr() net.Addr  { return w.local }
func (w *WebSocket) RemoteAddr() net.Addr { return w.remote }
func (w *WebSocket) EndpointPath() string { return w.path }
func (w *WebSocket) Close() error         { return w.conn.CloseNow() }
func (w *WebSocket) Send(ctx context.Context, raw []byte) error {
	if len(raw) == 0 || len(raw) > MaxMessage {
		return errors.New("invalid WebSocket message size")
	}
	return w.conn.Write(ctx, websocket.MessageBinary, raw)
}
func (w *WebSocket) Receive(ctx context.Context) ([]byte, error) {
	kind, raw, err := w.conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	if kind != websocket.MessageBinary || len(raw) == 0 || len(raw) > MaxMessage {
		w.Close()
		return nil, errors.New("invalid WebSocket binary message")
	}
	return raw, nil
}
func WebSocketProtocol(kind model.Transport) string { return "graphwan." + string(kind) + ".v1" }
func WebSocketPath(u *url.URL) string {
	if u.EscapedPath() == "" {
		return "/"
	}
	return u.EscapedPath()
}

// DialWebSocket disables redirects and environment HTTP proxies, and fixes the
// configured IPv4/IPv6 candidate family at the actual socket dial. RootCAs nil
// uses system roots for public TLS proxies; direct Agents use the identity pin.
func DialWebSocket(ctx context.Context, endpoint model.Endpoint, family int, identity ed25519.PublicKey, roots *x509.CertPool) (*WebSocket, error) {
	return DialWebSocketAt(ctx, endpoint, family, identity, roots, netip.Addr{})
}

// DialWebSocketAt dials a particular DNS answer without changing TLS or HTTP identity.
func DialWebSocketAt(ctx context.Context, endpoint model.Endpoint, family int, identity ed25519.PublicKey, roots *x509.CertPool, target netip.Addr) (*WebSocket, error) {
	if err := endpoint.Validate(); err != nil {
		return nil, err
	}
	if (endpoint.Transport != model.WS && endpoint.Transport != model.WSS) || (family != 4 && family != 6) {
		return nil, errors.New("invalid WebSocket candidate")
	}
	parsed, _ := url.Parse(endpoint.URL)
	dialAddress, err := EndpointDialAddress(endpoint, family, target)
	if err != nil {
		return nil, err
	}
	var socket net.Conn
	dialer := &net.Dialer{}
	t := &http.Transport{TLSClientConfig: PeerClientTLS(parsed.Hostname(), identity, roots), ForceAttemptHTTP2: false,
		DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, "tcp"+strconv.Itoa(family), dialAddress)
			if err == nil {
				socket = conn
			}
			return conn, err
		},
	}
	defer t.CloseIdleConnections()
	client := &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("WebSocket endpoint redirects are not allowed")
	}}
	protocol := WebSocketProtocol(endpoint.Transport)
	conn, response, err := websocket.Dial(ctx, endpoint.URL, &websocket.DialOptions{HTTPClient: client, Subprotocols: []string{protocol}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		return nil, err
	}
	if socket == nil || conn.Subprotocol() != protocol {
		conn.CloseNow()
		return nil, errors.New("WebSocket subprotocol not negotiated")
	}
	return NewWebSocket(conn, socket.LocalAddr(), socket.RemoteAddr(), WebSocketPath(parsed)), nil
}
