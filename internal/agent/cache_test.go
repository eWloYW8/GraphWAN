package agent_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/eWloYW8/GraphWAN/internal/agent"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/pki"
	"github.com/eWloYW8/GraphWAN/internal/routing"
	"github.com/eWloYW8/GraphWAN/internal/store"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
)

type testRuntime struct {
	mu           sync.Mutex
	applied      []model.Snapshot
	failRevision uint64
	healthError  error
	before       func(model.Snapshot)
}

func (r *testRuntime) Apply(ctx context.Context, s model.Snapshot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.before != nil {
		r.before(s)
	}
	if s.Revision == r.failRevision && r.failRevision != 0 {
		return errors.New("resource unavailable")
	}
	r.applied = append(r.applied, s)
	return nil
}
func (r *testRuntime) Report() []model.LinkStatus { return nil }
func (r *testRuntime) Health() error              { r.mu.Lock(); defer r.mu.Unlock(); return r.healthError }
func (r *testRuntime) last() (model.Snapshot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.applied) == 0 {
		return model.Snapshot{}, false
	}
	return r.applied[len(r.applied)-1], true
}

func registeredCache(t *testing.T) (*agent.Cache, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.db")
	cache, err := agent.OpenCache(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ca, err := pki.LoadOrCreate(db)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := cache.CSR()
	if err != nil {
		t.Fatal(err)
	}
	certificate, _, err := ca.IssueAgent(testutil.ID(10), csr)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveRegistration(agent.Registration{AgentID: testutil.ID(10), Server: "https://example.com", Certificate: certificate}); err != nil {
		t.Fatal(err)
	}
	return cache, path
}
func snapshot(t *testing.T, revision uint64) model.Snapshot {
	t.Helper()
	s := testutil.Topology()
	s.Revision = revision
	config, err := routing.Compile(s, testutil.ID(10))
	if err != nil {
		t.Fatal(err)
	}
	return config
}
func TestCacheIdentityAndConfigurationSurviveRestart(t *testing.T) {
	cache, path := registeredCache(t)
	key := cache.PrivateKey()
	s := snapshot(t, 7)
	if err := cache.MarkApplied(s); err == nil {
		t.Fatal("applied before durable desired state")
	}
	if err := cache.SaveDesired(s); err != nil {
		t.Fatal(err)
	}
	if err := cache.MarkApplied(s); err != nil {
		t.Fatal(err)
	}
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	cache, err := agent.OpenCache(path)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	if !bytes.Equal(key, cache.PrivateKey()) {
		t.Fatal("identity changed after restart")
	}
	desired, applied, err := cache.Snapshots()
	if err != nil {
		t.Fatal(err)
	}
	if desired.Revision != 7 || applied.Revision != 7 {
		t.Fatal("snapshots not restored")
	}
	if err := cache.SaveDesired(snapshot(t, 6)); err == nil {
		t.Fatal("revision rollback accepted")
	}
	modified := s.Clone()
	modified.ListenPort++
	if err := cache.SaveDesired(modified); err == nil {
		t.Fatal("same revision accepted different content")
	}
	desired.Networks = nil
	again, _, err := cache.Snapshots()
	if err != nil || len(again.Networks) != 1 {
		t.Fatal("read aliases durable data")
	}
}
func TestReconcilePersistenceOrderingAndFailedUpdate(t *testing.T) {
	cache, _ := registeredCache(t)
	defer cache.Close()
	runtime := &testRuntime{failRevision: 8}
	runtime.before = func(s model.Snapshot) {
		desired, _, err := cache.Snapshots()
		if err != nil || desired == nil || desired.Revision != s.Revision {
			t.Fatal("runtime invoked before persistence", err)
		}
	}
	reconciler := agent.NewReconciler(cache, runtime)
	if err := reconciler.Accept(context.Background(), snapshot(t, 7)); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Accept(context.Background(), snapshot(t, 8)); err == nil {
		t.Fatal("application failure lost")
	}
	report := reconciler.Report("test")
	if report.AppliedRevision != 7 || report.ConfigError == "" {
		t.Fatal("failed update acknowledged", report)
	}
	desired, applied, err := cache.Snapshots()
	if err != nil {
		t.Fatal(err)
	}
	if desired.Revision != 8 || applied.Revision != 7 {
		t.Fatal("desired/applied state confused")
	}
	if last, _ := runtime.last(); last.Revision != 7 {
		t.Fatal("working configuration discarded")
	}
	restarted := &testRuntime{failRevision: 8}
	restore := agent.NewReconciler(cache, restarted)
	if err := restore.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if last, _ := restarted.last(); last.Revision != 7 {
		t.Fatal("failed startup did not restore previous config")
	}
	restarted.failRevision = 0
	if err := restore.Accept(context.Background(), *desired); err != nil {
		t.Fatal(err)
	}
	if report := restore.Report("test"); report.AppliedRevision != 8 || report.ConfigError != "" {
		t.Fatal("same revision retry did not recover", report)
	}
}

func TestRegisteredClientIgnoresBootstrapFlags(t *testing.T) {
	cache, _ := registeredCache(t)
	defer cache.Close()
	config := snapshot(t, 7)
	if err := cache.SaveDesired(config); err != nil {
		t.Fatal(err)
	}
	runtime := &testRuntime{}
	client, err := agent.NewClient(cache, runtime, agent.Options{Server: "not a URL", ServerTransport: "invalid"})
	if err != nil {
		t.Fatal("registered Agent must ignore bootstrap flags:", err)
	}
	client.Close()
	if _, applied := runtime.last(); applied {
		t.Fatal("restored configuration before checking controller identity")
	}
}
