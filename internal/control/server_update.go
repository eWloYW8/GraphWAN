package control

import (
	"errors"
	"net/http"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/cluster"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/update"
)

func (s *Server) serverSoftware(w http.ResponseWriter, r *http.Request) {
	type software struct {
		ID      model.ID            `json:"id"`
		Version string              `json:"version"`
		Update  *model.UpdateStatus `json:"update,omitempty"`
	}
	out := []software{}
	for _, status := range s.cluster.ServerStatuses() {
		out = append(out, software{status.ID, status.Version, status.Update})
	}
	respond(w, 200, out)
}
func serverUpdatePending(server model.Server, status cluster.Status, now time.Time) bool {
	u := server.Update
	if status.Update != nil && (status.Update.Phase == "downloading" || status.Update.Phase == "installing") {
		return true
	}
	if u == nil || now.Sub(u.CreatedAt) > 30*time.Minute {
		return false
	}
	if status.Version == u.Asset.Version {
		return false
	}
	return status.Update == nil || status.Update.RequestID != u.ID || (status.Update.Phase != "failed" && status.Update.Phase != "succeeded")
}
func (s *Server) updateServer(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Version string `json:"version"`
	}
	if !decode(w, r, &input) {
		return
	}
	id := model.ID(r.PathValue("id"))
	statuses := map[model.ID]cluster.Status{}
	for _, status := range s.cluster.ServerStatuses() {
		statuses[status.ID] = status
	}
	target, online := statuses[id]
	if !online || target.Update == nil || !target.Update.Managed {
		fail(w, 409, "server must be online and managed by graphwan server service")
		return
	}
	if target.Update.Phase == "downloading" || target.Update.Phase == "installing" {
		fail(w, 409, "server update already in progress")
		return
	}
	asset, err := s.releases.Latest(r.Context(), target.Update.OS, target.Update.Arch)
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	if input.Version != asset.Version {
		fail(w, 409, "latest release changed; check again")
		return
	}
	if err = update.CheckVersion(target.Version, asset.Version); err != nil {
		fail(w, 409, err.Error())
		return
	}
	s.change(w, r, 202, func(state *model.State) error {
		for _, server := range state.Servers {
			if !server.Revoked && serverUpdatePending(server, statuses[server.ID], time.Now()) {
				return errors.New("another server update is queued or in progress")
			}
		}
		for i := range state.Servers {
			server := &state.Servers[i]
			if server.ID == id && !server.Revoked {
				server.Update = &model.UpdateRequest{ID: model.NewID(), Source: "github", Asset: asset, CreatedAt: time.Now().UTC()}
				return nil
			}
		}
		return errNotFound
	})
}
func (s *Server) startServerUpdates() {
	if s.updater == nil {
		return
	}
	s.streamWG.Add(1)
	go func() {
		defer s.streamWG.Done()
		timer := time.NewTicker(time.Second)
		defer timer.Stop()
		var accepted model.ID
		for {
			select {
			case <-s.done:
				return
			case <-timer.C:
			}
			if !s.cluster.Ready() {
				continue
			}
			state, err := s.db.Read()
			if err != nil {
				continue
			}
			for _, server := range state.Servers {
				if server.ID != s.cluster.Identity.ID || server.Revoked || server.Update == nil || server.Update.ID == accepted {
					continue
				}
				client := update.GitHubClient()
				if err = s.updater.Start(*server.Update, client, ""); err == nil {
					accepted = server.Update.ID
				} else {
					client.CloseIdleConnections()
				}
			}
		}
	}()
}
