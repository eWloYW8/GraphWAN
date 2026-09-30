// Package control implements controller management and agent enrollment APIs.
package control

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/coder/websocket"
	"github.com/eWloYW8/GraphWAN/internal/cluster"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/pki"
	"github.com/eWloYW8/GraphWAN/internal/store"
	"github.com/eWloYW8/GraphWAN/internal/webui"
)

const maxBody = 8 << 20

type Server struct {
	cluster      *cluster.Runtime
	reload       chan struct{}
	reloadOnce   sync.Once
	mu           sync.Mutex
	closed       bool
	done         chan struct{}
	eventClients map[[32]byte]int
	eventCount   int
	streamWG     sync.WaitGroup
	streams      map[model.ID]*websocket.Conn
	watchers     map[chan struct{}]bool
	statuses     map[model.ID]model.AgentStatus
	db           *store.Store
	auth         *auth
	ca           *pki.Authority
	log          *slog.Logger
	mux          *http.ServeMux
}
type Options struct {
	Password string
	Logger   *slog.Logger
}

func New(db *store.Store, options Options) (*Server, error) {
	auth, err := newAuth(db, options.Password)
	if err != nil {
		return nil, err
	}
	ca, err := pki.LoadOrCreate(db)
	if err != nil {
		return nil, err
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	s := &Server{
		reload: make(chan struct{}), db: db, auth: auth, ca: ca, log: options.Logger, mux: http.NewServeMux(),
		done: make(chan struct{}), eventClients: map[[32]byte]int{},
		streams: map[model.ID]*websocket.Conn{}, watchers: map[chan struct{}]bool{},
		statuses: map[model.ID]model.AgentStatus{},
	}
	s.mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		if s.cluster != nil && !s.cluster.Ready() {
			fail(w, 503, "controller initializing")
			return
		}
		respond(w, 200, map[string]string{"status": "ok"})
	})
	s.mux.HandleFunc("POST /api/v1/login", auth.login)
	s.mux.HandleFunc("POST /api/v1/logout", auth.require(auth.logout))
	s.mux.HandleFunc("GET /api/v1/session", auth.require(func(w http.ResponseWriter, r *http.Request) {
		session, _ := auth.find(r)
		respond(w, 200, map[string]string{"csrf_token": session.CSRF})
	}))
	s.mux.HandleFunc("GET /api/v1/state", auth.require(s.getState))
	s.mux.HandleFunc("POST /api/v1/networks", auth.require(s.createNetwork))
	s.mux.HandleFunc("PUT /api/v1/networks/{id}", auth.require(s.putNetwork))
	s.mux.HandleFunc("DELETE /api/v1/networks/{id}", auth.require(s.deleteNetwork))
	s.mux.HandleFunc("PATCH /api/v1/agents/{id}", auth.require(s.patchAgent))
	s.mux.HandleFunc("DELETE /api/v1/agents/{id}", auth.require(s.deleteAgent))
	s.mux.HandleFunc("POST /api/v1/enrollment-tokens", auth.require(s.createEnrollmentToken))
	s.mux.HandleFunc("POST /api/v1/enroll", s.enroll)
	s.mux.HandleFunc("GET /api/v1/agent/control", s.agentControl)
	s.mux.HandleFunc("GET /api/v1/telemetry", auth.require(s.getTelemetry))
	s.mux.HandleFunc("GET /api/v1/events", auth.require(s.events))
	s.mux.Handle("GET /", webui.Handler())
	return s, nil
}
func (s *Server) Authority() *pki.Authority { return s.ca }
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "same-origin")
	s.mux.ServeHTTP(w, r)
}
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}
func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(w, 415, "Content-Type must be application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		fail(w, 400, "invalid JSON request: "+err.Error())
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		fail(w, 400, "request must contain exactly one JSON object")
		return false
	}
	return true
}
func revision(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	raw := r.Header.Get("If-Match")
	if raw == "" {
		fail(w, 428, "If-Match revision is required")
		return 0, false
	}
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		raw = raw[1 : len(raw)-1]
	}
	rev, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		fail(w, 400, "invalid If-Match revision")
		return 0, false
	}
	return rev, true
}
func stateResponse(w http.ResponseWriter, status int, state model.State) {
	w.Header().Set("ETag", strconv.Quote(strconv.FormatUint(state.Revision, 10)))
	respond(w, status, state)
}
func (s *Server) getState(w http.ResponseWriter, r *http.Request) {
	state, err := s.db.Read()
	if err != nil {
		s.internal(w, err)
		return
	}
	stateResponse(w, 200, state)
}
func (s *Server) internal(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrUnavailable) {
		fail(w, 503, store.ErrUnavailable.Error())
		return
	}
	s.log.Error("controller request failed", "error", err)
	fail(w, 500, "internal server error")
}

var errNotFound = errors.New("resource not found")

func (s *Server) change(w http.ResponseWriter, r *http.Request, status int, fn func(*model.State) error) {
	expected, ok := revision(w, r)
	if !ok {
		return
	}
	state, err := s.db.Update(expected, fn)
	if errors.Is(err, store.ErrUnavailable) {
		s.internal(w, err)
		return
	}
	if errors.Is(err, store.ErrConflict) {
		fail(w, 409, "configuration changed; reload and retry")
		return
	}
	if errors.Is(err, errNotFound) {
		fail(w, 404, err.Error())
		return
	}
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	s.notify()
	stateResponse(w, status, state)
}
func newIDs(n *model.Network) {
	if n.ID == "" {
		n.ID = model.NewID()
	}
	for i := range n.Nodes {
		if n.Nodes[i].ID == "" {
			n.Nodes[i].ID = model.NewID()
		}
	}
	for i := range n.Edges {
		if n.Edges[i].ID == "" {
			n.Edges[i].ID = model.NewID()
		}
	}
}
func (s *Server) createNetwork(w http.ResponseWriter, r *http.Request) {
	var n model.Network
	if !decode(w, r, &n) {
		return
	}
	newIDs(&n)
	if n.MTU == 0 {
		n.MTU = model.DefaultMTU
	}
	if n.Cipher == "" {
		n.Cipher = model.ChaCha20Poly1305
	}
	if n.Nodes == nil {
		n.Nodes = []model.Node{}
	}
	if n.Edges == nil {
		n.Edges = []model.Edge{}
	}
	s.change(w, r, 201, func(state *model.State) error { state.Networks = append(state.Networks, n); return nil })
}
func (s *Server) putNetwork(w http.ResponseWriter, r *http.Request) {
	var n model.Network
	if !decode(w, r, &n) {
		return
	}
	if string(n.ID) != r.PathValue("id") {
		fail(w, 400, "network ID must match request path")
		return
	}
	newIDs(&n)
	s.change(w, r, 200, func(state *model.State) error {
		for i := range state.Networks {
			if state.Networks[i].ID == n.ID {
				state.Networks[i] = n
				return nil
			}
		}
		return errNotFound
	})
}
func (s *Server) deleteNetwork(w http.ResponseWriter, r *http.Request) {
	s.change(w, r, 200, func(state *model.State) error {
		for i, n := range state.Networks {
			if string(n.ID) == r.PathValue("id") {
				state.Networks = append(state.Networks[:i], state.Networks[i+1:]...)
				return nil
			}
		}
		return errNotFound
	})
}
func (s *Server) patchAgent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name                *string           `json:"name"`
		ListenPort          *uint16           `json:"listen_port"`
		Revoked             *bool             `json:"revoked"`
		ManualEndpoints     *[]model.Endpoint `json:"manual_endpoints"`
		STUNServers         *[]string         `json:"stun_servers"`
		ExcludeContainerIPs *bool             `json:"exclude_container_ips"`
	}
	if !decode(w, r, &in) {
		return
	}
	s.change(w, r, 200, func(state *model.State) error {
		for i := range state.Agents {
			a := &state.Agents[i]
			if string(a.ID) != r.PathValue("id") {
				continue
			}
			if in.Name != nil {
				a.Name = *in.Name
			}
			if in.ListenPort != nil {
				a.ListenPort = *in.ListenPort
			}
			if in.ExcludeContainerIPs != nil {
				a.ExcludeContainerIPs = *in.ExcludeContainerIPs
			}
			if in.STUNServers != nil {
				a.STUNServers = append([]string(nil), (*in.STUNServers)...)
			}
			if in.Revoked != nil {
				a.Revoked = *in.Revoked
			}
			if in.ManualEndpoints != nil {
				endpoints := []model.Endpoint{}
				for _, e := range a.Endpoints {
					if e.Source != model.Manual {
						endpoints = append(endpoints, e)
					}
				}
				for _, e := range *in.ManualEndpoints {
					e.Source = model.Manual
					if e.ID == "" {
						e.ID = model.NewID()
					}
					endpoints = append(endpoints, e)
				}
				a.Endpoints = endpoints
			}
			return nil
		}
		return errNotFound
	})
}
func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	s.change(w, r, 200, func(state *model.State) error {
		id := model.ID(r.PathValue("id"))
		found := false
		for i, a := range state.Agents {
			if a.ID == id {
				state.Agents = append(state.Agents[:i], state.Agents[i+1:]...)
				found = true
				break
			}
		}
		if !found {
			return errNotFound
		}
		for i := range state.Networks {
			n := &state.Networks[i]
			removed := map[model.ID]bool{}
			nodes := []model.Node{}
			for _, node := range n.Nodes {
				if node.AgentID == id {
					removed[node.ID] = true
				} else {
					nodes = append(nodes, node)
				}
			}
			n.Nodes = nodes
			edges := []model.Edge{}
			for _, edge := range n.Edges {
				if !removed[edge.A] && !removed[edge.B] {
					edges = append(edges, edge)
				}
			}
			n.Edges = edges
		}
		return nil
	})
}

func bearer(r *http.Request) string {
	raw := r.Header.Get("Authorization")
	if !strings.HasPrefix(raw, "Bearer ") || len(raw) > 128 {
		return ""
	}
	return strings.TrimPrefix(raw, "Bearer ")
}
