package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

type Receipt struct {
	Name       string `json:"name"`
	Executable string `json:"executable"`
	Data       string `json:"data"`
}
type Journal struct {
	Status model.UpdateStatus `json:"status"`
	Seen   map[model.ID]bool  `json:"seen"`
}

func WriteJSON(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".update-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func ReadJournal(data string) Journal {
	var j Journal
	raw, err := os.ReadFile(filepath.Join(data, "update-state.json"))
	if err == nil {
		_ = json.Unmarshal(raw, &j)
	}
	if j.Seen == nil {
		j.Seen = map[model.ID]bool{}
	}
	return j
}
func SaveJournal(data string, j Journal) error {
	return WriteJSON(filepath.Join(data, "update-state.json"), j)
}

type Installer func(Receipt, string, model.UpdateRequest) error

// Manager owns one update worker. Its lifetime is the Agent process, not a
// control connection; failover never cancels a verified installation.
type Manager struct {
	mu      sync.Mutex
	receipt Receipt
	version string
	install Installer
	busy    bool
	ctx     context.Context
}

func NewManager(ctx context.Context, r Receipt, version string, install Installer) *Manager {
	j := ReadJournal(r.Data)
	if j.Status.Phase == "downloading" || j.Status.Phase == "installing" && time.Since(j.Status.UpdatedAt) > 25*time.Minute {
		j.Status.Phase = "failed"
		j.Status.Error = "previous update interrupted; request a new update"
		j.Status.UpdatedAt = time.Now().UTC()
		_ = SaveJournal(r.Data, j)
	}
	return &Manager{ctx: ctx, receipt: r, version: version, install: install}
}
func (m *Manager) Status() *model.UpdateStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := ReadJournal(m.receipt.Data)
	s := j.Status
	s.Managed = true
	s.Service = m.receipt.Name
	s.OS = runtime.GOOS
	s.Arch = runtime.GOARCH
	return &s
}
func (m *Manager) Start(req model.UpdateRequest, client *http.Client, origin string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := ReadJournal(m.receipt.Data)
	if j.Seen[req.ID] {
		client.CloseIdleConnections()
		return nil
	}
	if m.busy || j.Status.Phase == "installing" {
		return errors.New("update already in progress")
	}
	if err := req.ID.Validate(); err != nil {
		return err
	}
	if req.Source != "github" && req.Source != "server" {
		return errors.New("invalid update source")
	}
	j.Seen[req.ID] = true
	j.Status = model.UpdateStatus{RequestID: req.ID, Version: req.Asset.Version, Phase: "downloading", UpdatedAt: time.Now().UTC()}
	if err := SaveJournal(m.receipt.Data, j); err != nil {
		return err
	}
	m.busy = true
	go func() {
		defer func() { m.mu.Lock(); m.busy = false; m.mu.Unlock(); client.CloseIdleConnections() }()
		ctx, cancel := context.WithTimeout(m.ctx, 20*time.Minute)
		defer cancel()
		if err := m.perform(ctx, req, client, origin); err != nil {
			m.mu.Lock()
			j := ReadJournal(m.receipt.Data)
			j.Status.Phase = "failed"
			j.Status.Error = err.Error()
			j.Status.UpdatedAt = time.Now().UTC()
			_ = SaveJournal(m.receipt.Data, j)
			m.mu.Unlock()
		}
	}()
	return nil
}
func (m *Manager) Poll(newClient func() *http.Client, origin string) {
	path := filepath.Join(m.receipt.Data, "update-request.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var req model.UpdateRequest
	if json.Unmarshal(raw, &req) != nil {
		return
	}
	client := newClient()
	if err = m.Start(req, client, origin); err == nil {
		_ = os.Remove(path)
	} else {
		client.CloseIdleConnections()
	}
}
func (m *Manager) perform(ctx context.Context, req model.UpdateRequest, client *http.Client, origin string) error {
	local := req.Asset.Version == ""
	releases := New("")
	if local {
		var err error
		if req.Source == "github" {
			req.Asset, err = releases.Latest(ctx, runtime.GOOS, runtime.GOARCH)
		} else {
			request, e := http.NewRequestWithContext(ctx, "GET", origin+"/api/v1/agent/update/latest?os="+runtime.GOOS+"&arch="+runtime.GOARCH, nil)
			if e != nil {
				return e
			}
			resp, e := client.Do(request)
			if e != nil {
				return e
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				return fmt.Errorf("server release metadata: HTTP %d", resp.StatusCode)
			}
			err = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&req.Asset)
		}
		if err != nil {
			return err
		}
	}
	if err := req.Validate(); err != nil {
		return err
	}
	if req.Asset.OS != runtime.GOOS || req.Asset.Arch != runtime.GOARCH {
		return errors.New("release platform does not match agent")
	}
	if err := CheckVersion(m.version, req.Asset.Version); err != nil {
		return err
	}
	if time.Since(req.CreatedAt) > 30*time.Minute {
		return errors.New("update request expired; request a new update")
	}
	// Staging beside the executable makes replacement a same-filesystem rename.
	f, err := os.CreateTemp(filepath.Dir(m.receipt.Executable), ".graphwan-download-*")
	if err != nil {
		return err
	}
	stage := f.Name()
	f.Close()
	os.Remove(stage)
	handedOff := false
	defer func() {
		if !handedOff {
			os.Remove(stage)
		}
	}()
	url := req.Asset.URL
	downloader := releases.Client
	if req.Source == "server" {
		downloader = client
		path := string(req.ID)
		if local {
			path = "latest?os=" + runtime.GOOS + "&arch=" + runtime.GOARCH + "&sha256=" + req.Asset.SHA256
		}
		url = origin + "/api/v1/agent/update/download/" + path
	}
	if err := Fetch(ctx, downloader, url, stage, req.Asset); err != nil {
		return err
	}
	m.mu.Lock()
	j := ReadJournal(m.receipt.Data)
	j.Status.Version = req.Asset.Version
	j.Status.Phase = "installing"
	j.Status.UpdatedAt = time.Now().UTC()
	err = SaveJournal(m.receipt.Data, j)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if err = m.install(m.receipt, stage, req); err != nil {
		return err
	}
	handedOff = true
	return nil
}
