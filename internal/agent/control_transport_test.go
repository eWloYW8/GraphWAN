package agent_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/agent"
	"github.com/eWloYW8/GraphWAN/internal/controltransport"
	"github.com/eWloYW8/GraphWAN/internal/model"
)

// All carriers use one actual TCP listener, including enrollment, the inner
// mTLS WebSocket, configuration pushes and telemetry in the opposite direction.
func TestControlCarriersSharePort(t *testing.T) {
	h := newController(t, true)
	var clients []*agent.Client
	for _, kind := range []string{"tcp", "websocket", "grpc", "wss"} {
		cache, err := agent.OpenCache(filepath.Join(t.TempDir(), "agent.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cache.Close() })
		client, err := agent.NewClient(cache, &testRuntime{}, agent.Options{Server: h.server.URL, ServerTransport: kind, Name: kind, EnrollmentToken: h.token(t), Roots: h.roots, Logger: quiet()})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(client.Close)
		cancel, done := runClient(t, client)
		t.Cleanup(func() { stopClient(t, cancel, done) })
		clients = append(clients, client)
		waitFor(t, func() bool { return client.Report().AppliedRevision != 0 })
	}
	state := h.state(t)
	if len(state.Agents) != 4 {
		t.Fatalf("enrolled %d agents", len(state.Agents))
	}
	// Rebind the same controller port while clients keep running. Every
	// carrier must reconnect without a new token, identity or empty config.
	h.carrier.Close()
	listener, err := net.Listen("tcp", h.server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	restarted := controltransport.NewServer(listener, h.server.Config.Handler, h.server.TLS)
	restartedDone := make(chan error, 1)
	go func() { restartedDone <- restarted.Serve() }()
	t.Cleanup(func() { restarted.Close(); <-restartedDone; restarted.Wait() })
	for _, a := range state.Agents {
		var next model.State
		h.request(t, "PATCH", "/api/v1/agents/"+string(a.ID), map[string]any{"name": a.Name + " updated"}, strconv.FormatUint(state.Revision, 10), 200, &next)
		state = next
	}
	for _, client := range clients {
		waitFor(t, func() bool { return client.Report().AppliedRevision == state.Revision })
	}
	waitFor(t, func() bool {
		var reports []model.AgentStatus
		h.request(t, "GET", "/api/v1/telemetry", nil, "", 200, &reports)
		if len(reports) != 4 {
			return false
		}
		for _, r := range reports {
			if !r.Connected || r.AppliedRevision != state.Revision {
				return false
			}
		}
		return true
	})
	// Revocation is enforced by the inner control handler on every carrier,
	// including already established streams and subsequent reconnects.
	for _, a := range state.Agents {
		var next model.State
		h.request(t, "PATCH", "/api/v1/agents/"+string(a.ID), map[string]any{"revoked": true}, strconv.FormatUint(state.Revision, 10), 200, &next)
		state = next
	}
	waitFor(t, func() bool {
		var reports []model.AgentStatus
		h.request(t, "GET", "/api/v1/telemetry", nil, "", 200, &reports)
		for _, r := range reports {
			if r.Connected {
				return false
			}
		}
		return len(reports) == 4
	})
}

func TestControlCarriersKeepInnerAuthentication(t *testing.T) {
	h := newController(t, true)
	for _, kind := range []string{"tcp", "websocket", "grpc", "wss"} {
		t.Run(kind, func(t *testing.T) {
			// A successfully established carrier grants no Agent identity.
			tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: h.roots, MinVersion: tls.VersionTLS13}, DialContext: controltransport.DialContext(kind, h.roots)}
			defer tr.CloseIdleConnections()
			client := &http.Client{Transport: tr, Timeout: 3 * time.Second}
			req, _ := http.NewRequest("GET", h.server.URL+"/api/v1/agent/control", nil)
			req.Header.Set("X-Forwarded-Client-Cert", "pretend-authenticated")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != 401 {
				t.Fatalf("unauthenticated control returned %d", resp.StatusCode)
			}
			cache, err := agent.OpenCache(filepath.Join(t.TempDir(), "agent.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer cache.Close()
			untrusted, err := agent.NewClient(cache, &testRuntime{}, agent.Options{Server: h.server.URL, ServerTransport: kind, Name: kind, EnrollmentToken: h.token(t), Roots: x509.NewCertPool(), Logger: quiet()})
			if err != nil {
				t.Fatal(err)
			}
			defer untrusted.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := untrusted.Run(ctx); err == nil {
				t.Fatal("untrusted server accepted")
			}
			if len(h.state(t).Agents) != 0 {
				t.Fatal("enrollment token sent to untrusted server")
			}
		})
	}
}

func TestControlCarrierSwitchRetainsIdentity(t *testing.T) {
	h := newController(t, true)
	cache, err := agent.OpenCache(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	var id model.ID
	for _, kind := range []string{"tcp", "websocket", "grpc", "wss", "tcp"} {
		opts := agent.Options{Server: h.server.URL, ServerTransport: kind, Name: "switch", Roots: h.roots, Logger: quiet()}
		if id == "" {
			opts.EnrollmentToken = h.token(t)
		}
		c, err := agent.NewClient(cache, &testRuntime{}, opts)
		if err != nil {
			t.Fatal(err)
		}
		cancel, done := runClient(t, c)
		waitFor(t, func() bool {
			var reports []model.AgentStatus
			h.request(t, "GET", "/api/v1/telemetry", nil, "", 200, &reports)
			return len(reports) == 1 && reports[0].Connected && c.Report().AppliedRevision != 0
		})
		reg, err := cache.Registration()
		if err != nil {
			t.Fatal(err)
		}
		if id != "" && reg.AgentID != id {
			t.Fatal("carrier switch changed identity")
		}
		id = reg.AgentID
		stopClient(t, cancel, done)
		c.Close()
		waitFor(t, func() bool {
			var reports []model.AgentStatus
			h.request(t, "GET", "/api/v1/telemetry", nil, "", 200, &reports)
			return len(reports) == 1 && !reports[0].Connected
		})
	}
	if len(h.state(t).Agents) != 1 {
		t.Fatal("carrier switch enrolled duplicate agent")
	}
}
