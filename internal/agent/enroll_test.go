package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/pki"
	"github.com/eWloYW8/GraphWAN/internal/store"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInvitationEnrollmentAndRepeat(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ca, err := pki.LoadOrCreate(db)
	if err != nil {
		t.Fatal(err)
	}
	cache, err := OpenCache(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	fixture := testutil.Topology()
	server := model.Server{ID: testutil.ID(90), Name: "test server", PublicKey: fixture.Agents[0].PublicKey}
	invite := model.AgentInvitation{Kind: "graphwan-agent", Version: 1, Token: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), ExpiresAt: time.Now().Add(time.Hour), Directory: model.ServerDirectory{ClusterID: testutil.ID(99), Revision: 1, CA: ca.PEM, Servers: []model.Server{server}}}
	requests := 0
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/v1/enroll" || r.Header.Get("Authorization") != "Bearer "+invite.Token {
			w.WriteHeader(403)
			return
		}
		var in struct {
			Name string
			CSR  []byte
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			w.WriteHeader(400)
			return
		}
		cert, _, err := ca.IssueAgent(testutil.ID(80), in.CSR)
		if err != nil {
			w.WriteHeader(400)
			return
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"agent_id": testutil.ID(80), "certificate": cert, "ca_certificate": ca.PEM, "servers": invite.Directory})
	}))
	srv.TLS, err = ca.ServerTLS([]string{server.TLSName()})
	if err != nil {
		t.Fatal(err)
	}
	srv.StartTLS()
	defer srv.Close()
	invite.Directory.Servers[0].Endpoints = []model.ServerEndpoint{{ID: testutil.ID(91), Transport: "tcp", URL: strings.Replace(srv.URL, "https://", "tcp://", 1), Source: model.Manual}}
	encoded, err := invite.Encode()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := model.ParseAgentInvitation(encoded)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := Enroll(context.Background(), cache, parsed, "test agent")
	if err != nil {
		t.Fatal(err)
	}
	if reg.AgentID != testutil.ID(80) {
		t.Fatal("wrong agent")
	}
	parsed.ExpiresAt = time.Now().Add(-time.Hour)
	again, err := Enroll(context.Background(), cache, parsed, "different name")
	if err != nil || again.AgentID != reg.AgentID || requests != 1 {
		t.Fatalf("repeat changed identity: %v", err)
	}
	parsed.Directory.ClusterID = testutil.ID(98)
	if _, err := Enroll(context.Background(), cache, parsed, "test"); err == nil {
		t.Fatal("accepted another cluster")
	}
	parsed = invite
	parsed.Kind = "server"
	if _, err := parsed.Encode(); err == nil {
		t.Fatal("accepted wrong invitation kind")
	}
}
