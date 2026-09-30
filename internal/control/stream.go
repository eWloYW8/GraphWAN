package control

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/http"
	"reflect"
	"slices"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/eWloYW8/GraphWAN/internal/discovery"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/routing"
	"github.com/eWloYW8/GraphWAN/internal/store"
)

func (s *Server) notify() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.watchers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (s *Server) authenticatedAgent(r *http.Request) (model.ID, error) {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		return "", errors.New("client certificate required")
	}
	cert := r.TLS.PeerCertificates[0]
	id := model.ID(cert.Subject.CommonName)
	if err := id.Validate(); err != nil {
		return "", err
	}
	pub, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok {
		return "", errors.New("invalid certificate key")
	}
	state, err := s.db.Read()
	if err != nil {
		return "", err
	}
	for _, a := range state.Agents {
		if a.ID == id && !a.Revoked && pub.Equal(ed25519.PublicKey(a.PublicKey)) {
			return id, nil
		}
	}
	return "", errors.New("unknown or revoked agent")
}

func (s *Server) agentControl(w http.ResponseWriter, r *http.Request) {
	id, err := s.authenticatedAgent(r)
	if err != nil {
		fail(w, 401, "valid agent certificate required")
		return
	}
	// The upgraded connection has its own per-message deadlines. Clear the
	// HTTP server's request deadlines before handing it to the WebSocket layer.
	rc := http.NewResponseController(w)
	if err := rc.SetReadDeadline(time.Time{}); err != nil {
		s.internal(w, err)
		return
	}
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		s.internal(w, err)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{model.RoutingSubprotocol}, CompressionMode: websocket.CompressionNoContextTakeover})
	if err != nil {
		return
	}
	conn.SetReadLimit(maxBody)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	changed := make(chan struct{}, 1)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		conn.CloseNow()
		return
	}
	s.streamWG.Add(1)
	old := s.streams[id]
	s.streams[id] = conn
	s.watchers[changed] = true
	s.statuses[id] = model.AgentStatus{AgentID: id, Connected: true, LastSeen: time.Now()}
	s.mu.Unlock()
	defer s.streamWG.Done()
	if old != nil {
		old.CloseNow()
	}
	defer func() {
		conn.CloseNow()
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.watchers, changed)
		if s.streams[id] == conn {
			delete(s.streams, id)
			status := s.statuses[id]
			status.Connected = false
			s.statuses[id] = status
		}
	}()
	done := make(chan error, 1)
	go func() { done <- s.readAgent(ctx, conn, id) }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var last uint64
	var snapshot model.Snapshot
	var routeHash string
	var lastUp map[routing.EdgeKey]bool
	var lastWrite time.Time
	first := true
	for {
		state, err := s.db.Read()
		if err != nil {
			conn.CloseNow()
			<-done
			return
		}
		message := model.ControlMessage{Type: "heartbeat"}
		if first || state.Revision != last {
			snapshot, err = routing.Compile(state, id)
			if err != nil {
				conn.CloseNow()
				<-done
				return
			}
			message.Type = "config"
			message.Snapshot = &snapshot
			last = snapshot.Revision
			first = false
			routeHash = ""
		} else if conn.Subprotocol() == model.RoutingSubprotocol {
			s.mu.Lock()
			ack := s.statuses[id].AppliedRevision
			s.mu.Unlock()
			if ack == state.Revision {
				up := routing.Availability(state, s.telemetry(state), time.Now())
				if routeHash == "" || !maps.Equal(up, lastUp) {
					lastUp = up
					routes := routing.LiveRoutes(state, snapshot, up)
					if hash := routes.Hash(); hash != routeHash {
						message.Type, message.Routes = "routes", &routes
						routeHash = hash
					}
				}
			}
		}
		if message.Type != "heartbeat" || time.Since(lastWrite) >= 15*time.Second {
			writeCtx, stop := context.WithTimeout(ctx, 10*time.Second)
			err = wsjson.Write(writeCtx, conn, message)
			stop()
			if err != nil {
				conn.CloseNow()
				<-done
				return
			}
			lastWrite = time.Now()
		}
		select {
		case <-done:
			return
		case <-ctx.Done():
			conn.CloseNow()
			<-done
			return
		case <-changed:
		case <-ticker.C:
		}
	}
}

func (s *Server) readAgent(ctx context.Context, conn *websocket.Conn, id model.ID) error {
	for {
		var message model.ControlMessage
		readCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		err := wsjson.Read(readCtx, conn, &message)
		cancel()
		if err != nil {
			return err
		}
		switch message.Type {
		case "ack":
			if message.Report == nil {
				return errors.New("missing agent report")
			}
			state, err := s.db.Read()
			if err != nil {
				return err
			}
			snapshot, err := routing.Compile(state, id)
			if err != nil {
				return err
			}
			// A previous revision may still be running while the new snapshot is
			// being reconciled. Preserve its ACK/error, but discard obsolete
			// link telemetry so removed edges cannot remain visible.
			if message.Report.AppliedRevision < snapshot.Revision {
				message.Report.Links = nil
			}
			if err := validateReport(snapshot, *message.Report); err != nil {
				return err
			}
			s.mu.Lock()
			if s.streams[id] == conn {
				s.statuses[id] = model.AgentStatus{AgentID: id, Connected: true, LastSeen: time.Now(), AgentReport: *message.Report}
			}
			s.mu.Unlock()
		case "endpoints":
			if err := s.updateEndpoints(id, message.Endpoints); err != nil && !errors.Is(err, store.ErrUnavailable) && !errors.Is(err, store.ErrConflict) {
				return err
			}
		default:
			return fmt.Errorf("unknown control message %q", message.Type)
		}
	}
}

func validateReport(snapshot model.Snapshot, report model.AgentReport) error {
	if report.AppliedRevision > snapshot.Revision || len(report.RoutingHash) > 64 || len(report.Version) > 128 || len(report.ConfigError) > 4096 || len(report.RuntimeError) > 4096 || len(report.Links) > 4096 {
		return errors.New("invalid agent report")
	}
	if report.Resources != nil {
		if err := report.Resources.Validate(); err != nil {
			return err
		}
	}
	edges := map[[2]model.ID]model.Edge{}
	for _, n := range snapshot.Networks {
		for _, peer := range n.Peers {
			edges[[2]model.ID{n.ID, peer.Edge.ID}] = peer.Edge
		}
	}
	active := map[[2]model.ID]bool{}
	links := map[string]bool{}
	for _, link := range report.Links {
		key := [2]model.ID{link.NetworkID, link.EdgeID}
		edge, ok := edges[key]
		if !ok || !slices.Contains(edge.Transports, link.Transport) || len(link.LinkID) == 0 || len(link.LinkID) > 128 || len(link.CandidateID) > 256 || len(link.Remote) > 2048 || len(link.Local) > 2048 || len(link.ObservedLocal) > 2048 || links[link.LinkID] {
			return errors.New("invalid link identity")
		}
		links[link.LinkID] = true
		if math.IsNaN(link.RTTMillis) || math.IsInf(link.RTTMillis, 0) || link.RTTMillis < 0 || math.IsNaN(link.Loss) || link.Loss < 0 || link.Loss > 1 {
			return errors.New("invalid link metrics")
		}
		if link.Active {
			if active[key] || !link.Healthy {
				return errors.New("invalid active link")
			}
			active[key] = true
		}
	}
	return nil
}

func (s *Server) updateEndpoints(id model.ID, endpoints []model.Endpoint) error {
	if len(endpoints) > model.MaxEndpoints {
		return errors.New("too many endpoints")
	}
	for _, e := range endpoints {
		if e.Source != model.Interface && e.Source != model.Observed {
			return errors.New("agent cannot change manual endpoints")
		}
		if err := e.Validate(); err != nil {
			return err
		}
	}
	for range 8 {
		state, err := s.db.Read()
		if err != nil {
			return err
		}
		_, err = s.db.Update(state.Revision, func(state *model.State) error {
			for i := range state.Agents {
				a := &state.Agents[i]
				if a.ID != id {
					continue
				}
				if a.Revoked {
					return errors.New("agent revoked")
				}
				combined := discovery.CoalesceEndpoints(a.Endpoints, endpoints, time.Now())
				for _, e := range a.Endpoints {
					if e.Source == model.Manual {
						combined = append(combined, e)
					}
				}
				if reflect.DeepEqual(a.Endpoints, combined) {
					return errUnchanged
				}
				a.Endpoints = combined
				return nil
			}
			return errNotFound
		})
		if errors.Is(err, errUnchanged) {
			return nil
		}
		if errors.Is(err, store.ErrConflict) {
			continue
		}
		if err == nil {
			s.notify()
		}
		return err
	}
	return store.ErrConflict
}

var errUnchanged = errors.New("unchanged endpoints")

func (s *Server) getTelemetry(w http.ResponseWriter, r *http.Request) {
	state, err := s.db.Read()
	if err != nil {
		s.internal(w, err)
		return
	}
	respond(w, 200, s.telemetry(state))
}

// telemetry copies shared reports before HTTP writes and suppresses runtime
// paths that no longer belong to desired topology. Offline metrics are historical.
func (s *Server) telemetry(state model.State) []model.AgentStatus {
	allowed := map[model.ID]map[[2]model.ID]bool{}
	for _, n := range state.Networks {
		agents := map[model.ID]model.ID{}
		for _, node := range n.Nodes {
			agents[node.ID] = node.AgentID
		}
		for _, edge := range n.Edges {
			if !edge.Enabled {
				continue
			}
			for _, node := range []model.ID{edge.A, edge.B} {
				id := agents[node]
				if allowed[id] == nil {
					allowed[id] = map[[2]model.ID]bool{}
				}
				allowed[id][[2]model.ID{n.ID, edge.ID}] = true
			}
		}
	}
	remote := map[model.ID]model.AgentStatus{}
	if s.cluster != nil {
		for _, status := range s.cluster.RemoteAgents() {
			if status.LastSeen.After(remote[status.AgentID].LastSeen) {
				remote[status.AgentID] = status
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	statuses := make([]model.AgentStatus, 0, len(state.Agents))
	for _, a := range state.Agents {
		status := s.statuses[a.ID]
		if candidate := remote[a.ID]; candidate.LastSeen.After(status.LastSeen) {
			status = candidate
		}
		status.AgentID = a.ID
		status.Resources = status.Resources.Clone()
		if a.Revoked || time.Since(status.LastSeen) > 45*time.Second {
			status.Connected = false
		}
		links := []model.LinkStatus{}
		for _, link := range status.Links {
			if !allowed[a.ID][[2]model.ID{link.NetworkID, link.EdgeID}] {
				continue
			}
			if !status.Connected {
				link.Healthy, link.Active = false, false
			}
			links = append(links, link)
		}
		status.Links = links
		statuses = append(statuses, status)
	}
	return statuses
}

// Close terminates browser streams and hijacked Agent connections before the
// database is closed.
func (s *Server) Close() {
	s.mu.Lock()
	if !s.closed {
		close(s.done)
	}
	s.closed = true
	conns := make([]*websocket.Conn, 0, len(s.streams))
	for _, conn := range s.streams {
		conns = append(conns, conn)
	}
	s.mu.Unlock()
	for _, conn := range conns {
		conn.CloseNow()
	}
	s.streamWG.Wait()
	s.geoip.Close()
}
