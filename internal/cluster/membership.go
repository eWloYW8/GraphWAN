package cluster

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/hashicorp/raft"
)

type Status struct {
	Version string              `json:"version,omitempty"`
	Update  *model.UpdateStatus `json:"update,omitempty"`
	ID      model.ID            `json:"id"`
	Leader  model.ID            `json:"leader"`
	Applied uint64              `json:"applied_index"`
	Commit  uint64              `json:"commit_index"`
	Voters  int                 `json:"voters"`
	Agents  []model.AgentStatus `json:"agents"`
}
type remoteStatus struct {
	status Status
	at     time.Time
}
type statusCache struct {
	mu       sync.Mutex
	peers    map[model.ID]remoteStatus
	local    func() []model.AgentStatus
	software func() (string, *model.UpdateStatus)
}

func (r *Runtime) Status() Status {
	_, leader := r.Raft.LeaderWithID()
	out := Status{ID: r.Identity.ID, Leader: model.ID(leader), Applied: r.Raft.AppliedIndex(), Commit: r.Raft.CommitIndex(), Agents: []model.AgentStatus{}}
	future := r.Raft.GetConfiguration()
	if future.Error() == nil {
		for _, p := range future.Configuration().Servers {
			if p.Suffrage == raft.Voter {
				out.Voters++
			}
		}
	}
	r.status.mu.Lock()
	local := r.status.local
	software := r.status.software
	r.status.mu.Unlock()
	if software != nil {
		out.Version, out.Update = software()
	}
	if local != nil {
		out.Agents = local()
	}
	return out
}
func (r *Runtime) RemoteAgents() []model.AgentStatus {
	r.status.mu.Lock()
	defer r.status.mu.Unlock()
	out := []model.AgentStatus{}
	for _, p := range r.status.peers {
		if time.Since(p.at) < 10*time.Second {
			out = append(out, p.status.Agents...)
		}
	}
	return out
}
func (r *Runtime) StartMembership(local func() []model.AgentStatus) {
	r.status.mu.Lock()
	r.status.local = local
	r.status.mu.Unlock()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-r.ctx.Done():
				return
			case <-tick.C:
			}
			state, err := r.DB.Read()
			if err != nil {
				continue
			}
			allowed := make(map[model.ID]bool)
			for _, p := range state.Servers {
				if p.ID != r.Identity.ID && !p.Revoked {
					allowed[p.ID] = true
				}
			}
			r.layer.channels.prune(allowed)
			// Probe in parallel: an unreachable member must not delay healthy peers.
			var wg sync.WaitGroup
			for _, p := range state.Servers {
				if p.ID == r.Identity.ID || p.Revoked {
					continue
				}
				wg.Add(1)
				go func(p model.Server) {
					defer wg.Done()
					ctx, cancel := context.WithTimeout(r.ctx, 2*time.Second)
					defer cancel()
					var status Status
					if r.request(ctx, p.ID, "/api/v1/cluster/status", struct{}{}, &status) == nil && status.ID == p.ID {
						r.status.mu.Lock()
						r.status.peers[p.ID] = remoteStatus{status: status, at: time.Now()}
						r.status.mu.Unlock()
					}
				}(p)
			}
			wg.Wait()
			if r.Raft.State() != raft.Leader {
				continue
			}
			future := r.Raft.GetConfiguration()
			if future.Error() != nil {
				continue
			}
			members := map[model.ID]raft.Server{}
			for _, p := range future.Configuration().Servers {
				members[model.ID(p.ID)] = p
			}
			for _, p := range state.Servers {
				if p.Revoked || p.ID == r.Identity.ID {
					continue
				}
				member, ok := members[p.ID]
				if !ok {
					r.Raft.AddNonvoter(raft.ServerID(p.ID), raft.ServerAddress(p.ID), 0, time.Second).Error()
					break
				}
				if member.Suffrage == raft.Voter {
					continue
				}
				r.status.mu.Lock()
				remote := r.status.peers[p.ID]
				r.status.mu.Unlock()
				if time.Since(remote.at) < 3*time.Second && remote.status.Applied >= r.Raft.CommitIndex() {
					r.Raft.AddVoter(member.ID, member.Address, 0, time.Second).Error()
					break
				}
			}
		}
	}()
}
func (r *Runtime) registerStatus(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/cluster/status", func(w http.ResponseWriter, req *http.Request) {
		if !r.Authenticate(req) {
			http.Error(w, "server identity required", 403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(r.Status())
	})
}

// Software status travels over the existing bidirectional cluster channel.
func (r *Runtime) SetSoftwareStatus(fn func() (string, *model.UpdateStatus)) {
	r.status.mu.Lock()
	defer r.status.mu.Unlock()
	r.status.software = fn
}
func (r *Runtime) ServerStatuses() []Status {
	out := []Status{r.Status()}
	r.status.mu.Lock()
	defer r.status.mu.Unlock()
	for _, p := range r.status.peers {
		if time.Since(p.at) < 10*time.Second {
			out = append(out, p.status)
		}
	}
	return out
}
