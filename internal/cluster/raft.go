package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/store"
	"github.com/hashicorp/raft"
	boltdb "github.com/hashicorp/raft-boltdb/v2"
)

const maxStateBytes = 64 << 20

type entry struct {
	Command *store.Command `json:"command,omitempty"`
	Image   *store.Image   `json:"image,omitempty"`
}
type machine struct {
	db      *store.Store
	changed func()
}

func (m *machine) Apply(log *raft.Log) any {
	var e entry
	if err := json.Unmarshal(log.Data, &e); err != nil {
		return err
	}
	var err error
	if e.Command != nil && e.Image == nil {
		err = m.db.Apply(*e.Command)
	} else if e.Image != nil && e.Command == nil {
		err = m.restore(*e.Image)
	} else {
		return errors.New("invalid replication entry")
	}
	if err == nil && m.changed != nil {
		m.changed()
	}
	return err
}
func (m *machine) restore(image store.Image) error {
	current, err := m.db.Version()
	if err != nil {
		return err
	}
	// The FSM is durable itself. Replay/snapshot installation must not undo
	// state already applied before a crash or imported during an authorized join.
	if current >= image.Version {
		return nil
	}
	return m.db.Import(image)
}
func (m *machine) Snapshot() (raft.FSMSnapshot, error) {
	image, err := m.db.Export()
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(image)
	if err != nil {
		return nil, err
	}
	return snapshot(raw), nil
}
func (m *machine) Restore(r io.ReadCloser) error {
	defer r.Close()
	raw, err := io.ReadAll(io.LimitReader(r, maxStateBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxStateBytes {
		return errors.New("cluster snapshot too large")
	}
	var image store.Image
	if err = json.Unmarshal(raw, &image); err != nil {
		return err
	}
	err = m.restore(image)
	if err == nil && m.changed != nil {
		m.changed()
	}
	return err
}

type snapshot []byte

func (s snapshot) Persist(sink raft.SnapshotSink) error {
	if _, err := sink.Write(s); err != nil {
		sink.Cancel()
		return err
	}
	return sink.Close()
}
func (s snapshot) Release() {}

type Runtime struct {
	joinGate  sync.RWMutex
	joining   bool
	status    statusCache
	admission chan struct{}
	DB        *store.Store
	Identity  Identity
	Raft      *raft.Raft
	layer     *streamLayer
	transport *raft.NetworkTransport
	logs      *boltdb.BoltStore
	ctx       context.Context
	cancel    context.CancelFunc
	ready     chan struct{}
	wg        sync.WaitGroup
	closeOnce sync.Once
	clients   *clients
	changed   func()
}

func Start(db *store.Store, identity Identity, directory string, changed func()) (*Runtime, error) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &Runtime{status: statusCache{peers: map[model.ID]remoteStatus{}}, admission: make(chan struct{}, 64), DB: db, Identity: identity, ctx: ctx, cancel: cancel, ready: make(chan struct{}), changed: changed}
	path := filepath.Join(directory, "raft", string(identity.Epoch))
	if err := os.MkdirAll(path, 0700); err != nil {
		cancel()
		return nil, err
	}
	var err error
	r.clients, err = newClients(db, identity)
	if err != nil {
		cancel()
		return nil, err
	}
	r.layer = newStreamLayer(r)
	r.transport = raft.NewNetworkTransport(r.layer, 4, 5*time.Second, io.Discard)
	r.logs, err = boltdb.NewBoltStore(filepath.Join(path, "raft.db"))
	if err != nil {
		r.transport.Close()
		r.clients.Close()
		cancel()
		return nil, err
	}
	snaps, err := raft.NewFileSnapshotStore(path, 2, io.Discard)
	if err != nil {
		r.logs.Close()
		r.transport.Close()
		r.clients.Close()
		cancel()
		return nil, err
	}
	existing, err := raft.HasExistingState(r.logs, r.logs, snaps)
	if err != nil {
		r.logs.Close()
		r.transport.Close()
		r.clients.Close()
		cancel()
		return nil, err
	}
	config := raft.DefaultConfig()
	config.LocalID = raft.ServerID(identity.ID)
	config.LogOutput = io.Discard
	config.SnapshotThreshold = 256
	config.SnapshotInterval = 30 * time.Second
	config.TrailingLogs = 128
	r.Raft, err = raft.NewRaft(config, &machine{db: db, changed: changed}, r.logs, r.logs, snaps, r.transport)
	if err != nil {
		r.logs.Close()
		r.transport.Close()
		r.clients.Close()
		cancel()
		return nil, err
	}
	if !existing && !identity.Joined {
		if err = r.Raft.BootstrapCluster(raft.Configuration{Servers: []raft.Server{{ID: config.LocalID, Address: raft.ServerAddress(identity.ID)}}}).Error(); err != nil {
			r.Close()
			return nil, err
		}
		image, err := db.Export()
		if err != nil {
			r.Close()
			return nil, err
		}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			defer close(r.ready)
			for r.ctx.Err() == nil {
				if r.Raft.State() == raft.Leader {
					raw, _ := json.Marshal(entry{Image: &image})
					if r.Raft.Apply(raw, 3*time.Second).Error() == nil {
						return
					}
				}
				select {
				case <-r.ctx.Done():
					return
				case <-time.After(50 * time.Millisecond):
				}
			}
		}()
	} else {
		close(r.ready)
	}
	db.SetCommitter(r.Commit)
	return r, nil
}

func (r *Runtime) Close() {
	r.closeOnce.Do(func() {
		r.cancel()
		r.DB.SetCommitter(func(store.Command) error { return store.ErrUnavailable })
		if r.Raft != nil {
			r.Raft.Shutdown().Error()
		}
		if r.transport != nil {
			r.transport.Close()
		}
		if r.clients != nil {
			r.clients.Close()
		}
		r.wg.Wait()
		if r.logs != nil {
			r.logs.Close()
		}
	})
}
func (r *Runtime) Commit(command store.Command) error {
	r.joinGate.RLock()
	defer r.joinGate.RUnlock()
	if r.joining {
		return store.ErrUnavailable
	}
	select {
	case <-r.ready:
	case <-r.ctx.Done():
		return store.ErrUnavailable
	case <-time.After(4 * time.Second):
		return store.ErrUnavailable
	}
	if r.ctx.Err() != nil {
		return store.ErrUnavailable
	}
	if r.Raft.State() == raft.Leader {
		return r.Apply(command)
	}
	_, leader := r.Raft.LeaderWithID()
	if leader == "" {
		return store.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(r.ctx, 6*time.Second)
	defer cancel()
	var result commitResult
	if err := r.request(ctx, model.ID(leader), "/api/v1/cluster/commit", command, &result); err != nil {
		return fmt.Errorf("%w: %v", store.ErrUnavailable, err)
	}
	if result.Conflict {
		for {
			v, err := r.DB.Version()
			if err != nil {
				return err
			}
			if v >= result.Version {
				return store.ErrConflict
			}
			select {
			case <-ctx.Done():
				return store.ErrUnavailable
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	if result.Error != "" {
		return fmt.Errorf("%w: %s", store.ErrUnavailable, result.Error)
	}
	for {
		v, err := r.DB.Version()
		if err != nil {
			return err
		}
		if v >= command.Expected+1 {
			return nil
		}
		select {
		case <-ctx.Done():
			return store.ErrUnavailable
		case <-time.After(5 * time.Millisecond):
		}
	}
}
func (r *Runtime) Apply(command store.Command) error {
	if r.Raft.State() != raft.Leader {
		return store.ErrUnavailable
	}
	raw, err := json.Marshal(entry{Command: &command})
	if err != nil {
		return err
	}
	if len(raw) > maxStateBytes {
		return errors.New("cluster transaction too large")
	}
	select {
	case r.admission <- struct{}{}:
	default:
		return store.ErrUnavailable
	}
	future := r.Raft.Apply(raw, 3*time.Second)
	// Apply's timeout bounds enqueueing, not quorum confirmation.
	done := make(chan error, 1)
	go func() { defer func() { <-r.admission }(); done <- future.Error() }()
	select {
	case err = <-done:
	case <-r.ctx.Done():
		return store.ErrUnavailable
	case <-time.After(5 * time.Second):
		return store.ErrUnavailable
	}
	if err != nil {
		return fmt.Errorf("%w: %v", store.ErrUnavailable, err)
	}
	if result := future.Response(); result != nil {
		if err, ok := result.(error); ok {
			return err
		}
		return errors.New("invalid commit result")
	}
	return nil
}

type commitResult struct {
	Version  uint64 `json:"version"`
	Conflict bool   `json:"conflict,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (r *Runtime) Ready() bool {
	select {
	case <-r.ready:
		return true
	default:
		return false
	}
}
