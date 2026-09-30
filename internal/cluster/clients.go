package cluster

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/eWloYW8/GraphWAN/internal/controltransport"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/store"
)

type clients struct {
	mu         sync.Mutex
	roots      *x509.CertPool
	cert       tls.Certificate
	transports map[string]*http.Transport
	next       map[model.ID]string
}

func newClients(db *store.Store, identity Identity) (*clients, error) {
	state, err := db.Read()
	if err != nil {
		return nil, err
	}
	cert, err := identity.TLSCertificate()
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(state.ClusterCA) {
		return nil, errors.New("invalid cluster trust")
	}
	return &clients{roots: roots, cert: cert, transports: map[string]*http.Transport{}, next: map[model.ID]string{}}, nil
}
func (c *clients) client(server model.Server, ep model.ServerEndpoint) *http.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := string(server.ID) + "/" + ep.Transport + "/" + ep.URL
	tr := c.transports[key]
	if tr == nil {
		if len(c.transports) >= 256 {
			for _, old := range c.transports {
				old.CloseIdleConnections()
			}
			clear(c.transports)
		}
		tr = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: c.roots, Certificates: []tls.Certificate{c.cert}, ServerName: server.TLSName(), MinVersion: tls.VersionTLS13}, DialContext: controltransport.DialContext(ep.Transport, c.roots, server.TLSName()), TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 6 * time.Second, MaxIdleConnsPerHost: 4, IdleConnTimeout: 30 * time.Second}
		c.transports[key] = tr
	}
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func (c *clients) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, tr := range c.transports {
		tr.CloseIdleConnections()
	}
}
func (r *Runtime) peer(id model.ID) (model.Server, error) {
	s, err := r.DB.Read()
	if err != nil {
		return model.Server{}, err
	}
	for _, p := range s.Servers {
		if p.ID == id && !p.Revoked {
			return p, nil
		}
	}
	return model.Server{}, errors.New("unknown server")
}
func (r *Runtime) request(ctx context.Context, id model.ID, path string, input, output any) error {
	p, err := r.peer(id)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	for _, ep := range r.clients.endpoints(p) {
		if ep.Source == model.Observed && time.Now().After(ep.ExpiresAt) {
			continue
		}
		attempt, cancel := context.WithTimeout(ctx, 4*time.Second)
		req, e := http.NewRequestWithContext(attempt, http.MethodPost, ep.Origin()+path, bytes.NewReader(raw))
		if e != nil {
			cancel()
			return e
		}
		req.Header.Set("Content-Type", "application/json")
		resp, e := r.clients.client(p, ep).Do(req)
		if e == nil {
			r.clients.remember(p, ep, true)
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxStateBytes+1))
			resp.Body.Close()
			cancel()
			if readErr != nil {
				return readErr
			}
			if resp.StatusCode != 200 {
				return fmt.Errorf("server returned %d", resp.StatusCode)
			}
			if len(body) > maxStateBytes {
				return errors.New("cluster response too large")
			}
			return json.Unmarshal(body, output)
		}
		cancel()
		r.clients.remember(p, ep, false)
		err = e
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if err == nil {
		err = errors.New("no reachable server endpoints")
	}
	return err
}
func (r *Runtime) dial(ctx context.Context, id model.ID) (*websocket.Conn, error) {
	p, err := r.peer(id)
	if err != nil {
		return nil, err
	}
	for _, ep := range r.clients.endpoints(p) {
		if ep.Source == model.Observed && time.Now().After(ep.ExpiresAt) {
			continue
		}
		attempt, cancel := context.WithTimeout(ctx, 3*time.Second)
		ws, resp, e := websocket.Dial(attempt, ep.Origin()+"/api/v1/cluster/raft", &websocket.DialOptions{HTTPClient: r.clients.client(p, ep), CompressionMode: websocket.CompressionDisabled})
		cancel()
		if e == nil {
			r.clients.remember(p, ep, true)
			return ws, nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		err = e
		r.clients.remember(p, ep, false)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if err == nil {
		err = errors.New("no reachable server endpoints")
	}
	return nil, err
}

// Keep the last working entrance first. Failed attempts advance the starting
// point even when a caller's deadline expires before trying the whole list.
func (c *clients) endpoints(p model.Server) []model.ServerEndpoint {
	c.mu.Lock()
	key := c.next[p.ID]
	c.mu.Unlock()
	start := 0
	for i, e := range p.Endpoints {
		if e.URL == key {
			start = i
			break
		}
	}
	result := append([]model.ServerEndpoint{}, p.Endpoints[start:]...)
	return append(result, p.Endpoints[:start]...)
}
func (c *clients) remember(p model.Server, e model.ServerEndpoint, success bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if success {
		c.next[p.ID] = e.URL
		return
	}
	for i, ep := range p.Endpoints {
		if ep.URL == e.URL {
			c.next[p.ID] = p.Endpoints[(i+1)%len(p.Endpoints)].URL
			return
		}
	}
}
