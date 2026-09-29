package mesh

import (
	"net"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/transport"
)

func (m *Mesh) acceptWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" || r.URL.RawQuery != "" || r.URL.ForceQuery || r.Header.Get("Origin") != "" {
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
	if !m.allowsEndpoint(kind, path) {
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
