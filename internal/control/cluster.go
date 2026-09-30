package control

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/cluster"
	"github.com/eWloYW8/GraphWAN/internal/model"
)

func (s *Server) StartCluster(identity cluster.Identity, directory string) (*cluster.Runtime, error) {
	runtime, err := cluster.Start(s.db, identity, directory, s.notify)
	if err != nil {
		return nil, err
	}
	s.cluster = runtime
	runtime.Register(s.mux)
	runtime.RegisterJoin(s.mux, s.ca)
	s.mux.HandleFunc("GET /api/v1/servers/status", s.auth.require(func(w http.ResponseWriter, r *http.Request) { respond(w, 200, runtime.Status()) }))
	s.mux.HandleFunc("POST /api/v1/servers/invitation", s.auth.require(func(w http.ResponseWriter, r *http.Request) {
		invitation, err := runtime.Invite()
		if err != nil {
			s.internal(w, err)
			return
		}
		respond(w, 201, map[string]string{"invitation": invitation})
	}))
	var join sync.Mutex
	s.mux.HandleFunc("POST /api/v1/servers/join", s.auth.require(func(w http.ResponseWriter, r *http.Request) {
		if !join.TryLock() {
			fail(w, 409, "join already in progress")
			return
		}
		defer join.Unlock()
		var input struct {
			Invitation string `json:"invitation"`
		}
		if !decode(w, r, &input) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		if err := runtime.PrepareJoin(ctx, input.Invitation); err != nil {
			fail(w, 422, err.Error())
			return
		}
		respond(w, 202, map[string]bool{"restarting": true})
		s.reloadOnce.Do(func() { close(s.reload) })
	}))
	s.mux.HandleFunc("PATCH /api/v1/servers/{id}", s.auth.require(s.patchServer))
	runtime.StartMembership(s.localTelemetry)
	return runtime, nil
}
func (s *Server) Reload() <-chan struct{} { return s.reload }
func (s *Server) localTelemetry() []model.AgentStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.AgentStatus, 0, len(s.statuses))
	for _, status := range s.statuses {
		status.Resources = status.Resources.Clone()
		status.Links = append([]model.LinkStatus{}, status.Links...)
		out = append(out, status)
	}
	return out
}
func (s *Server) patchServer(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name            *string                 `json:"name"`
		ManualEndpoints *[]model.ServerEndpoint `json:"manual_endpoints"`
		STUNServers     *[]string               `json:"stun_servers"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.STUNServers != nil {
		if err := cluster.ValidateServerSTUN(*input.STUNServers); err != nil {
			fail(w, 422, err.Error())
			return
		}
	}
	s.change(w, r, 200, func(state *model.State) error {
		for i := range state.Servers {
			p := &state.Servers[i]
			if string(p.ID) != r.PathValue("id") {
				continue
			}
			if input.Name != nil {
				p.Name = *input.Name
			}
			if input.STUNServers != nil {
				p.STUNServers = append([]string{}, (*input.STUNServers)...)
			}
			if input.ManualEndpoints != nil {
				endpoints := []model.ServerEndpoint{}
				urls := map[string]bool{}
				for _, e := range *input.ManualEndpoints {
					e.Source = model.Manual
					e.ExpiresAt = time.Time{}
					if e.ID == "" {
						e.ID = model.NewID()
					}
					if urls[e.URL] {
						return errors.New("duplicate endpoint URL")
					}
					urls[e.URL] = true
					endpoints = append(endpoints, e)
				}
				for _, e := range p.Endpoints {
					if e.Source != model.Manual && !urls[e.URL] {
						endpoints = append(endpoints, e)
					}
				}
				p.Endpoints = endpoints
			}
			return nil
		}
		return errNotFound
	})
}
