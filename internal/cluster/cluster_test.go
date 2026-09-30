package cluster

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/controltransport"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/pki"
	"github.com/eWloYW8/GraphWAN/internal/store"
	"github.com/hashicorp/raft"
)

type testServer struct {
	db       *store.Store
	runtime  *Runtime
	carriers *controltransport.Server
	path     string
	id       Identity
	addr     string
}

func startTestServer(t *testing.T, path, addr string) *testServer {
	t.Helper()
	db, err := store.Open(filepath.Join(path, "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = InstallPending(db); err != nil {
		t.Fatal(err)
	}
	ca, err := pki.LoadOrCreate(db)
	if err != nil {
		t.Fatal(err)
	}
	id, err := LoadIdentity(db)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	endpoints, err := InterfaceEndpoints(id, listener.Addr())
	if err != nil {
		t.Fatal(err)
	}
	if err = Initialize(db, ca, &id, endpoints); err != nil {
		t.Fatal(err)
	}
	runtime, err := Start(db, id, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	runtime.Register(mux)
	runtime.RegisterJoin(mux, ca)
	cfg, err := ca.ServerTLS([]string{"127.0.0.1", (model.Server{ID: id.ID}).TLSName()})
	if err != nil {
		t.Fatal(err)
	}
	carriers := controltransport.NewServer(listener, mux, cfg)
	go carriers.Serve()
	runtime.StartMembership(nil)
	node := &testServer{db: db, runtime: runtime, carriers: carriers, path: path, id: id, addr: listener.Addr().String()}
	t.Cleanup(node.close)
	return node
}
func (n *testServer) close() {
	if n.runtime != nil {
		n.runtime.Close()
		n.carriers.Close()
		n.carriers.Wait()
		n.db.Close()
		n.runtime = nil
	}
}
func eventually(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	end := time.Now().Add(timeout)
	for time.Now().Before(end) {
		if fn() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("condition timed out")
}
func TestThreeServersJoinFailoverAndRestart(t *testing.T) {
	a := startTestServer(t, t.TempDir(), "127.0.0.1:0")
	eventually(t, 8*time.Second, func() bool { return a.runtime.Raft.State() == raft.Leader })
	nodes := []*testServer{a}
	for range 2 {
		invitation, err := a.runtime.Invite()
		if err != nil {
			t.Fatal(err)
		}
		b := startTestServer(t, t.TempDir(), "127.0.0.1:0")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err = b.runtime.PrepareJoin(ctx, invitation)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		path, addr := b.path, b.addr
		b.close()
		b = startTestServer(t, path, addr)
		nodes = append(nodes, b)
		eventually(t, 15*time.Second, func() bool { return a.runtime.Status().Voters == len(nodes) })
	}
	b, c := nodes[1], nodes[2]
	// Any member can originate a write, including a follower.
	if err := b.db.WriteRecords(func(records *store.Records) error { return records.Put("test/shared-secret", []byte("first")) }); err != nil {
		t.Fatal(err)
	}
	version, _ := b.db.Version()
	eventually(t, 5*time.Second, func() bool { v, _ := c.db.Version(); return v == version })
	image, _ := c.db.Export()
	if string(image.Records["test/shared-secret"]) != "first" {
		t.Fatal("security records not replicated")
	}
	// A one-time record remains single-use with concurrent writers on replicas.
	if err := a.db.WriteRecords(func(records *store.Records) error { return records.Put("test/one-time", []byte("unused")) }); err != nil {
		t.Fatal(err)
	}
	tokenVersion, _ := a.db.Version()
	eventually(t, 5*time.Second, func() bool {
		x, _ := b.db.Version()
		y, _ := c.db.Version()
		return x == tokenVersion && y == tokenVersion
	})
	var winners atomic.Int32
	var writes sync.WaitGroup
	for _, node := range []*testServer{a, b, c} {
		writes.Add(1)
		go func(node *testServer) {
			defer writes.Done()
			err := node.db.WriteRecords(func(records *store.Records) error {
				if records.Get("test/one-time") == nil {
					return errors.New("already used")
				}
				return records.Delete("test/one-time")
			})
			if err == nil {
				winners.Add(1)
			}
		}(node)
	}
	writes.Wait()
	if winners.Load() != 1 {
		t.Fatalf("one-time record consumed %d times", winners.Load())
	}
	a.close()
	eventually(t, 10*time.Second, func() bool { return b.runtime.Raft.State() == raft.Leader || c.runtime.Raft.State() == raft.Leader })
	if err := c.db.WriteRecords(func(records *store.Records) error { return records.Put("test/shared-secret", []byte("second")) }); err != nil {
		t.Fatal(err)
	}
	leader := b
	if c.runtime.Raft.State() == raft.Leader {
		leader = c
	}
	for i := 0; i < 160; i++ {
		if err := leader.db.WriteRecords(func(records *store.Records) error { return records.Put("test/compact", []byte(fmt.Sprint(i))) }); err != nil {
			t.Fatal(err)
		}
	}
	if err := leader.runtime.Raft.Snapshot().Error(); err != nil {
		t.Fatal(err)
	}
	path, addr := a.path, a.addr
	a = startTestServer(t, path, addr)
	version, _ = leader.db.Version()
	deadline := time.Now().Add(12 * time.Second)
	for {
		v, _ := a.db.Version()
		if v == version {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("snapshot catch-up: version=%d want=%d peer=%v leader=%v", v, version, a.runtime.Raft.Stats(), leader.runtime.Raft.Stats())
		}
		time.Sleep(25 * time.Millisecond)
	}
	image, _ = a.db.Export()
	if string(image.Records["test/shared-secret"]) != "second" {
		t.Fatal("restart lost committed state")
	}
	a.close()
	b.close()
	// A minority must not change persistent state, including one-time tokens.
	before, _ := c.db.Version()
	err := c.db.WriteRecords(func(records *store.Records) error { return records.Put("test/minority", []byte("bad")) })
	if !errors.Is(err, store.ErrUnavailable) {
		t.Fatalf("minority write: %v", err)
	}
	after, _ := c.db.Version()
	if after != before {
		t.Fatal("minority committed a write")
	}
}
