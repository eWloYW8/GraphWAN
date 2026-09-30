package agent_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/agent"
	"github.com/eWloYW8/GraphWAN/internal/cluster"
	"github.com/eWloYW8/GraphWAN/internal/control"
	"github.com/eWloYW8/GraphWAN/internal/controltransport"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/store"
)

type replica struct {
	*controller
	db         *store.Store
	runtime    *cluster.Runtime
	path, addr string
	stopped    bool
}

func replicaController(t *testing.T, path, addr string) *replica {
	t.Helper()
	db, err := store.Open(filepath.Join(path, "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = cluster.InstallPending(db); err != nil {
		t.Fatal(err)
	}
	app, err := control.New(db, control.Options{Password: "test-controller-password", Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := cluster.LoadIdentity(db)
	if err != nil {
		t.Fatal(err)
	}
	eps, err := cluster.InterfaceEndpoints(identity, listener.Addr())
	if err != nil {
		t.Fatal(err)
	}
	if err = cluster.Initialize(db, app.Authority(), &identity, eps); err != nil {
		t.Fatal(err)
	}
	runtime, err := app.StartCluster(identity, path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := app.Authority().ServerTLS([]string{"127.0.0.1", (model.Server{ID: identity.ID}).TLSName()})
	if err != nil {
		t.Fatal(err)
	}
	carriers := controltransport.NewServer(listener, app, cfg)
	go carriers.Serve()
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(app.Authority().PEM)
	jar, _ := cookiejar.New(nil)
	h := &controller{app: app, server: &httptest.Server{URL: "https://" + listener.Addr().String()}, carrier: carriers, roots: roots, admin: &http.Client{Jar: jar, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}, Timeout: 8 * time.Second}}
	r := &replica{controller: h, db: db, runtime: runtime, path: path, addr: listener.Addr().String()}
	t.Cleanup(r.stop)
	var login struct {
		CSRF string `json:"csrf_token"`
	}
	h.request(t, "POST", "/api/v1/login", map[string]string{"password": "test-controller-password"}, "", 200, &login)
	h.csrf = login.CSRF
	return r
}
func (r *replica) stop() {
	if r.stopped {
		return
	}
	r.stopped = true
	r.app.Close()
	r.runtime.Close()
	r.carrier.Close()
	r.carrier.Wait()
	r.admin.CloseIdleConnections()
	r.db.Close()
}
func TestAgentDirectoryFailoverAndBootstrapFlagsIgnored(t *testing.T) {
	a := replicaController(t, t.TempDir(), "127.0.0.1:0")
	var replicas []*replica
	for range 2 {
		invitation, err := a.runtime.Invite()
		if err != nil {
			t.Fatal(err)
		}
		r := replicaController(t, t.TempDir(), "127.0.0.1:0")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err = r.runtime.PrepareJoin(ctx, invitation)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		path, addr := r.path, r.addr
		r.stop()
		r = replicaController(t, path, addr)
		replicas = append(replicas, r)
		waitReplica(t, func() bool { return a.runtime.Status().Voters == len(replicas)+1 })
	}
	cachePath := filepath.Join(t.TempDir(), "agent.db")
	cache, err := agent.OpenCache(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cache.Close() }()
	client, err := agent.NewClient(cache, &testRuntime{}, agent.Options{Server: a.server.URL, ServerTransport: "wss", Name: "failover", EnrollmentToken: a.token(t), Roots: a.roots})
	if err != nil {
		t.Fatal(err)
	}
	cancel, done := runClient(t, client)
	defer cancel()
	waitReplica(t, func() bool {
		reg, _ := cache.Registration()
		return reg != nil && reg.Directory != nil && len(reg.Directory.Servers) == 3 && client.Report().AppliedRevision > 0
	})
	original, _ := cache.Registration()
	a.stop()
	waitReplica(t, func() bool {
		for _, r := range replicas {
			statuses := r.runtime.Status().Agents
			for _, s := range statuses {
				if s.AgentID == original.AgentID && s.Connected {
					return true
				}
			}
		}
		return false
	})
	stopClient(t, cancel, done)
	client.Close()
	cache.Close()
	cache, err = agent.OpenCache(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	client, err = agent.NewClient(cache, &testRuntime{}, agent.Options{Server: "invalid ignored bootstrap", ServerTransport: "invalid"})
	if err != nil {
		t.Fatal(err)
	}
	cancel, done = runClient(t, client)
	defer func() { stopClient(t, cancel, done); client.Close() }()
	waitReplica(t, func() bool {
		for _, r := range replicas {
			statuses := r.runtime.Status().Agents
			for _, s := range statuses {
				if s.AgentID == original.AgentID && s.Connected {
					return true
				}
			}
		}
		return false
	})
	current, _ := cache.Registration()
	if current.AgentID != original.AgentID {
		t.Fatal("failover re-enrolled the Agent")
	}
	// An Agent certificate cannot invoke Server replication RPCs.
	cert, err := cache.TLSCertificate(*current)
	if err != nil {
		t.Fatal(err)
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: replicas[0].roots, Certificates: []tls.Certificate{cert}}}
	defer tr.CloseIdleConnections()
	req, _ := http.NewRequest("POST", replicas[0].server.URL+"/api/v1/cluster/status", nil)
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("agent accepted as server: %d", resp.StatusCode)
	}
}

func waitReplica(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !fn() {
		if time.Now().After(deadline) {
			t.Fatal("replica condition not reached")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
