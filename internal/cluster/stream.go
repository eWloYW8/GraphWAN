package cluster

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
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
	channels *channels
}

func newStreamLayer(r *Runtime) *streamLayer {
	l := &streamLayer{runtime: r, incoming: make(chan net.Conn), done: make(chan struct{})}
	l.channels = newChannels(r.ctx, r.Identity.ID, func(ctx context.Context, id model.ID) (net.Conn, error) {
		ws, err := r.dial(ctx, id, true)
		if err != nil {
			return nil, err
		}
		if _, err = r.peer(id); err != nil {
			ws.CloseNow()
			return nil, err
		}
		conn, _ := controltransport.WebSocketStream(ws, l.Addr(), address(id))
		return conn, nil
	}, l.acceptStream)
	return l
}
func (l *streamLayer) Addr() net.Addr { return address(l.runtime.Identity.ID) }
func (l *streamLayer) Close() error {
	l.once.Do(func() { close(l.done); l.channels.close() })
	return nil
}
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
	conn, err := l.open(ctx, model.ID(id), raftStream)
	if !errors.Is(err, errLegacyPeer) {
		return conn, err
	}
	ws, err := l.runtime.dial(ctx, model.ID(id), false)
	if err != nil {
		return nil, err
	}
	conn, _ = controltransport.WebSocketStream(ws, l.Addr(), address(id))
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
		ws, err := websocket.Accept(w, req, &websocket.AcceptOptions{Subprotocols: []string{channelProtocol}, CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return
		}
		conn, done := controltransport.WebSocketStream(ws, r.layer.Addr(), address(req.TLS.PeerCertificates[0].Subject.CommonName))
		defer conn.Close()
		if ws.Subprotocol() == channelProtocol {
			if _, err := r.peer(model.ID(req.TLS.PeerCertificates[0].Subject.CommonName)); err != nil {
				return
			}
			ch, err := r.layer.channels.attach(model.ID(req.TLS.PeerCertificates[0].Subject.CommonName), conn, false)
			if err != nil {
				return
			}
			select {
			case <-ch.session.CloseChan():
			case <-done:
			case <-r.ctx.Done():
			}
			return
		}
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
		result := r.applyResult(command)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	})
}

const (
	raftStream    byte = 1
	requestStream byte = 2
)

type channelRequest struct {
	Path    string         `json:"path"`
	Command *store.Command `json:"command,omitempty"`
}

func (l *streamLayer) open(ctx context.Context, id model.ID, kind byte) (net.Conn, error) {
	if _, err := l.runtime.peer(id); err != nil {
		return nil, err
	}
	conn, err := l.channels.open(ctx, id)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	deadline, _ := ctx.Deadline()
	conn.SetWriteDeadline(deadline)
	_, err = conn.Write([]byte{kind})
	if !stop() {
		conn.Close()
		return nil, ctx.Err()
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetWriteDeadline(time.Time{})
	return conn, nil
}
func (l *streamLayer) acceptStream(id model.ID, conn net.Conn) {
	if _, err := l.runtime.peer(id); err != nil {
		conn.Close()
		return
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var kind [1]byte
	if _, err := io.ReadFull(conn, kind[:]); err != nil {
		conn.Close()
		return
	}
	conn.SetReadDeadline(time.Time{})
	if kind[0] == raftStream {
		select {
		case l.incoming <- conn:
			return
		case <-l.done:
		case <-time.After(5 * time.Second):
		}
	} else if kind[0] == requestStream {
		l.runtime.serveRequest(conn)
	}
	conn.Close()
}
func (r *Runtime) applyResult(command store.Command) commitResult {
	result := commitResult{}
	if err := r.Apply(command); err != nil {
		if errors.Is(err, store.ErrConflict) {
			result.Conflict = true
			result.Version, _ = r.DB.Version()
		} else {
			result.Error = err.Error()
		}
	}
	return result
}
func (r *Runtime) serveRequest(conn net.Conn) {
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	var request channelRequest
	if err := json.NewDecoder(io.LimitReader(conn, maxStateBytes+1)).Decode(&request); err != nil {
		return
	}
	switch request.Path {
	case "/api/v1/cluster/status":
		json.NewEncoder(conn).Encode(r.Status())
	case "/api/v1/cluster/commit":
		if request.Command != nil {
			json.NewEncoder(conn).Encode(r.applyResult(*request.Command))
		}
	}
}
