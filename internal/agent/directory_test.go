package agent_test

import (
	"crypto/x509"
	"path/filepath"
	"testing"

	"github.com/eWloYW8/GraphWAN/internal/agent"
	"github.com/eWloYW8/GraphWAN/internal/cluster"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/pki"
	"github.com/eWloYW8/GraphWAN/internal/store"
)

func TestDirectoryPinsTrustRejectsRollbackAndPersistsRemoval(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ca, err := pki.LoadOrCreate(db)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := cluster.LoadIdentity(db)
	if err != nil {
		t.Fatal(err)
	}
	if err = cluster.Initialize(db, ca, &identity, nil); err != nil {
		t.Fatal(err)
	}
	state, _ := db.Read()
	directory := state.ServerDirectory()
	directory.Revision = 10
	path := filepath.Join(t.TempDir(), "agent.db")
	cache, err := agent.OpenCache(path)
	if err != nil {
		t.Fatal(err)
	}
	csr, _ := cache.CSR()
	id := model.NewID()
	certificate, _, err := ca.IssueAgent(id, csr)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-directory registration, then migrate through an authenticated snapshot.
	if err = cache.SaveRegistration(agent.Registration{AgentID: id, Server: "https://example.com", Certificate: certificate}); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca.PEM)
	if err = cache.SaveDirectory(directory, roots); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*model.ServerDirectory){
		"rollback":                      func(d *model.ServerDirectory) { d.Revision-- },
		"same revision changed content": func(d *model.ServerDirectory) { d.Servers[0].Name += " changed" },
		"cluster changed":               func(d *model.ServerDirectory) { d.Revision++; d.ClusterID = model.NewID() },
		"CA changed":                    func(d *model.ServerDirectory) { d.Revision++; d.CA = append(d.CA, '\n') },
	} {
		t.Run(name, func(t *testing.T) {
			bad := directory.Clone()
			edit(bad)
			if err := cache.SaveDirectory(bad, roots); err == nil {
				t.Fatal("accepted changed trust/history")
			}
		})
	}
	next := directory.Clone()
	next.Revision++
	next.Servers[0].Endpoints = []model.ServerEndpoint{{ID: model.NewID(), URL: "grpc://example.com:8443", Transport: "grpc", Source: model.Manual}}
	if err = cache.SaveDirectory(next, roots); err != nil {
		t.Fatal(err)
	}
	next.Revision++
	next.Servers[0].Endpoints = nil
	if err = cache.SaveDirectory(next, roots); err != nil {
		t.Fatal(err)
	}
	cache.Close()
	cache, err = agent.OpenCache(path)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	saved, _ := cache.Registration()
	if saved.Directory.Revision != 12 || len(saved.Directory.Servers[0].Endpoints) != 0 || len(saved.CA) == 0 {
		t.Fatal("directory removal/trust did not survive restart")
	}
}
