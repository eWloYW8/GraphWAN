package cluster

import (
	"context"
	"net"
	"path/filepath"
	"testing"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/pki"
	"github.com/eWloYW8/GraphWAN/internal/store"
)

func TestBoundDiscoveryAndManualPersistence(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := LoadIdentity(db)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := pki.LoadOrCreate(db)
	if err != nil {
		t.Fatal(err)
	}
	addr := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8443}
	eps, err := InterfaceEndpoints(id, addr)
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 1 || eps[0].URL != "tcp://127.0.0.1:8443" || eps[0].Transport != "tcp" {
		t.Fatal("bound listener advertised other interfaces", eps)
	}
	again, _ := InterfaceEndpoints(id, addr)
	if again[0].ID != eps[0].ID {
		t.Fatal("unstable discovery ID")
	}
	manual := model.ServerEndpoint{ID: model.NewID(), Transport: "wss", URL: "wss://example.com:443", Source: model.Manual}
	if err = Initialize(db, ca, &id, []model.ServerEndpoint{manual}); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{DB: db, Identity: id, ctx: context.Background()}
	if err = r.PublishEndpoints(eps); err != nil {
		t.Fatal(err)
	}
	state, _ := db.Read()
	if len(state.Servers[0].Endpoints) != 2 || state.Servers[0].Endpoints[0] != manual {
		t.Fatal("discovery replaced manual entry")
	}
	if err = r.PublishEndpoints(eps); err != nil {
		t.Fatal(err)
	}
	same, _ := db.Read()
	if same.Revision != state.Revision {
		t.Fatal("unchanged discovery generated a new revision")
	}
	if err = r.PublishEndpoints(nil); err != nil {
		t.Fatal(err)
	}
	state, _ = db.Read()
	if len(state.Servers[0].Endpoints) != 1 || state.Servers[0].Endpoints[0] != manual {
		t.Fatal("removing disappeared interfaces lost manual endpoint")
	}
}
