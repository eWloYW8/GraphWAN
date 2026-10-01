package control

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/update"
)

func (s *Server) latestRelease(w http.ResponseWriter, r *http.Request) {
	a, err := s.releases.Latest(r.Context(), r.URL.Query().Get("os"), r.URL.Query().Get("arch"))
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	respond(w, 200, a)
}
func (s *Server) agentLatestRelease(w http.ResponseWriter, r *http.Request) {
	if _, err := s.authenticatedAgent(r); err != nil {
		fail(w, 401, "valid agent certificate required")
		return
	}
	s.latestRelease(w, r)
}
func (s *Server) updateAgent(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Source  string `json:"source"`
		Version string `json:"version"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Source != "github" && input.Source != "server" {
		fail(w, 422, "source must be github or server")
		return
	}
	state, err := s.db.Read()
	if err != nil {
		s.internal(w, err)
		return
	}
	id := model.ID(r.PathValue("id"))
	var status *model.AgentStatus
	for _, a := range s.telemetry(state) {
		if a.AgentID == id {
			copy := a
			status = &copy
			break
		}
	}
	if status == nil || !status.Connected || time.Since(status.LastSeen) > 45*time.Second || status.Update == nil || !status.Update.Managed {
		fail(w, 409, "agent must be online and managed by graphwan agent service")
		return
	}
	if status.Update.Phase == "downloading" || status.Update.Phase == "installing" {
		fail(w, 409, "agent update already in progress")
		return
	}
	a, err := s.releases.Latest(r.Context(), status.Update.OS, status.Update.Arch)
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	if input.Version != a.Version {
		fail(w, 409, "latest release changed; check again")
		return
	}
	if err := update.CheckVersion(status.Version, a.Version); err != nil {
		fail(w, 409, err.Error())
		return
	}
	s.change(w, r, 202, func(state *model.State) error {
		for i := range state.Agents {
			p := &state.Agents[i]
			if p.ID == id && !p.Revoked {
				if p.Update != nil && time.Since(p.Update.CreatedAt) < 30*time.Minute && (status.Update.RequestID != p.Update.ID || status.Update.Phase == "downloading" || status.Update.Phase == "installing") {
					return errUpdatePending
				}
				p.Update = &model.UpdateRequest{ID: model.NewID(), Source: input.Source, Asset: a, CreatedAt: time.Now().UTC()}
				return nil
			}
		}
		return errNotFound
	})
}

var errUpdatePending = errors.New("an update is already queued or in progress")

func (s *Server) downloadUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := s.authenticatedAgent(r)
	if err != nil {
		fail(w, 401, "valid agent certificate required")
		return
	}
	var asset model.ReleaseAsset
	if r.PathValue("id") == "latest" {
		asset, err = s.releases.Latest(r.Context(), r.URL.Query().Get("os"), r.URL.Query().Get("arch"))
		if err == nil && asset.SHA256 != r.URL.Query().Get("sha256") {
			fail(w, 409, "latest release changed; retry update")
			return
		}
	} else {
		state, e := s.db.Read()
		err = e
		if err == nil {
			for _, a := range state.Agents {
				if a.ID == id && a.Update != nil && string(a.Update.ID) == r.PathValue("id") {
					asset = a.Update.Asset
					break
				}
			}
		}
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	if asset.Validate() != nil {
		fail(w, 404, "update not found")
		return
	}
	// Large binaries must not inherit the short control API write deadline.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(20 * time.Minute))
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Minute)
	defer cancel()
	path, err := s.releases.Cached(ctx, asset)
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	f, err := os.Open(path)
	if err != nil {
		s.internal(w, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		s.internal(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, asset.Name, info.ModTime(), f)
}
