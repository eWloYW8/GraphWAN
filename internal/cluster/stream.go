package cluster

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/eWloYW8/GraphWAN/internal/controltransport"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/store"
	"github.com/hashicorp/raft"
)

type address string

func (a address) Network() string { return "graphwan-cluster" }
func (a address) String() string  { return string(a) }

type streamLayer struct {
	runtime  *Runtime
	incoming chan net.Conn
	done     chan struct{}
	once     sync.Once
}

func newStreamLayer(r *Runtime) *streamLayer {
	return &streamLayer{runtime: r, incoming: make(chan net.Conn), done: make(chan struct{})}
}
func (l *streamLayer) Addr() net.Addr { return address(l.runtime.Identity.ID) }
func (l *streamLayer) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *streamLayer) Accept() (net.Conn, error) {
	select {
	case c := <-l.incoming:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *streamLayer) Dial(id raft.ServerAddress, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(l.runtime.ctx, timeout)
	defer cancel()
	ws, err := l.runtime.dial(ctx, model.ID(id))
	if err != nil {
		return nil, err
	}
	conn, _ := controltransport.WebSocketStream(ws, l.Addr(), address(id))
	return conn, nil
}
func (r *Runtime) Authenticate(req *http.Request) bool {
	if req.TLS == nil || len(req.TLS.VerifiedChains) == 0 || len(req.TLS.PeerCertificates) == 0 {
		return false
	}
	cert := req.TLS.PeerCertificates[0]
	p, err := r.peer(model.ID(cert.Subject.CommonName))
	if err != nil {
		return false
	}
	key, ok := cert.PublicKey.(ed25519.PublicKey)
	return ok && key.Equal(ed25519.PublicKey(p.PublicKey))
}
func (r *Runtime) Register(mux *http.ServeMux) {
	r.registerStatus(mux)
	mux.HandleFunc("GET /api/v1/cluster/raft", func(w http.ResponseWriter, req *http.Request) {
		if !r.Authenticate(req) {
			http.Error(w, "server identity required", 403)
			return
		}
		rc := http.NewResponseController(w)
		if rc.SetReadDeadline(time.Time{}) != nil || rc.SetWriteDeadline(time.Time{}) != nil {
			http.Error(w, "stream deadlines unavailable", 500)
			return
		}
		ws, err := websocket.Accept(w, req, &websocket.AcceptOptions{})
		if err != nil {
			return
		}
		conn, done := controltransport.WebSocketStream(ws, r.layer.Addr(), address(req.TLS.PeerCertificates[0].Subject.CommonName))
		defer conn.Close()
		select {
		case r.layer.incoming <- conn:
		case <-r.ctx.Done():
			return
		case <-time.After(5 * time.Second):
			return
		}
		select {
		case <-done:
		case <-r.ctx.Done():
		}
	})
	mux.HandleFunc("POST /api/v1/cluster/commit", func(w http.ResponseWriter, req *http.Request) {
		if !r.Authenticate(req) {
			http.Error(w, "server identity required", 403)
			return
		}
		var command store.Command
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxStateBytes)).Decode(&command); err != nil {
			http.Error(w, "invalid command", 400)
			return
		}
		result := commitResult{}
		if err := r.Apply(command); err != nil {
			if errors.Is(err, store.ErrConflict) {
				result.Conflict = true
				result.Version, _ = r.DB.Version()
			} else {
				result.Error = err.Error()
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	})
}
