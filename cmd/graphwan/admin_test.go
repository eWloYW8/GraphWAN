package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/control"
	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/store"
	"github.com/graphwan/graphwan/internal/testutil"
)

const adminTestPassword = "correct horse battery staple"

type adminHarness struct {
	db      *store.Store
	flags   []string
	logouts atomic.Int32
}

func newAdminHarness(t *testing.T, before func(*store.Store, *http.Request)) *adminHarness {
	t.Helper()
	t.Setenv("GRAPHWAN_ADMIN_PASSWORD", adminTestPassword)
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	app, err := control.New(db, control.Options{Password: adminTestPassword})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	h := &adminHarness{db: db}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if before != nil {
			before(db, r)
		}
		app.ServeHTTP(w, r)
		if r.URL.Path == "/api/v1/logout" {
			// Reuse the old cookie directly: logout must invalidate the session,
			// not just clear the CLI's cookie jar.
			probe := httptest.NewRequest("GET", "/api/v1/session", nil)
			probe.Header.Set("Cookie", r.Header.Get("Cookie"))
			response := httptest.NewRecorder()
			app.ServeHTTP(response, probe)
			if response.Code != 401 || r.Header.Get("Cookie") == "" {
				t.Errorf("logout did not invalidate authenticated session: %d", response.Code)
			}
			h.logouts.Add(1)
		}
	}))
	server.TLS, err = app.Authority().ServerTLS([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, app.Authority().PEM, 0600); err != nil {
		t.Fatal(err)
	}
	h.flags = []string{"--server", server.URL, "--ca", ca}
	return h
}

func (h *adminHarness) run(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	var out bytes.Buffer
	err := runAdmin(context.Background(), append(args, h.flags...), &out)
	if bytes.Contains(out.Bytes(), []byte(adminTestPassword)) || bytes.Contains(out.Bytes(), []byte("csrf_token")) {
		t.Fatal("credentials in output")
	}
	return out.Bytes(), err
}

func (h *adminHarness) state(t *testing.T) model.State {
	t.Helper()
	s, err := h.db.Read()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (h *adminHarness) seed(t *testing.T) model.State {
	t.Helper()
	s, err := h.db.Update(h.state(t).Revision, func(s *model.State) error {
		fixture := testutil.Topology()
		s.Agents, s.Networks = fixture.Agents, fixture.Networks
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAdminNetworkCreateAndList(t *testing.T) {
	h := newAdminHarness(t, nil)
	for i, cidr := range []string{"10.80.0.0/24", "fd80::/64"} {
		args := []string{"network", "create", "--name", "CLI network", "--cidr", cidr}
		wantMTU := 1280
		if i == 1 {
			args = append(args, "--mtu", "9000")
			wantMTU = 9000
		}
		before := h.state(t)
		raw, err := h.run(t, args...)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Revision uint64        `json:"revision"`
			Network  model.Network `json:"network"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		after := h.state(t)
		if result.Revision != before.Revision+1 || !reflect.DeepEqual(result.Network, after.Networks[i]) || result.Network.CIDR.String() != cidr || result.Network.MTU != wantMTU || result.Network.Cipher != model.ChaCha20Poly1305 || len(result.Network.Nodes) != 0 || len(result.Network.Edges) != 0 {
			t.Fatalf("unexpected created network: %s", raw)
		}
	}
	raw, err := h.run(t, "network", "list")
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Revision uint64          `json:"revision"`
		Networks []model.Network `json:"networks"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatal(err)
	}
	state := h.state(t)
	if listed.Revision != state.Revision || !reflect.DeepEqual(listed.Networks, state.Networks) || h.logouts.Load() != 3 {
		t.Fatal("list disagrees with persisted state or sessions were not closed")
	}
}

func TestAdminNodesAndEdgePreserveConfiguration(t *testing.T) {
	h := newAdminHarness(t, nil)
	initial := h.seed(t)
	// The same Agent may have a different Node identity/address in another Network.
	second := initial.Networks[0]
	second.ID, second.Name = model.NewID(), "Second network"
	second.CIDR = netip.MustParsePrefix("fd80::/64")
	second.Nodes = []model.Node{initial.Networks[0].Nodes[0]}
	second.Nodes[0].ID = model.NewID()
	second.Nodes[0].Address = netip.MustParseAddr("fd80::1")
	second.Edges = []model.Edge{}
	initial, err := h.db.Update(initial.Revision, func(s *model.State) error {
		s.Networks = append(s.Networks, second)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, filter := range []string{"", string(second.ID)} {
		args := []string{"node", "list"}
		want := 6
		if filter != "" {
			args = append(args, "--network", filter)
			want = 1
		}
		raw, err := h.run(t, args...)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Revision uint64       `json:"revision"`
			Nodes    []listedNode `json:"nodes"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		if result.Revision != initial.Revision || len(result.Nodes) != want {
			t.Fatalf("unexpected nodes: %s", raw)
		}
		last := result.Nodes[len(result.Nodes)-1]
		if last.NetworkID != second.ID || last.NetworkName != second.Name || !reflect.DeepEqual(last.Node, second.Nodes[0]) {
			t.Fatalf("missing membership identity: %+v", last)
		}
	}
	n := initial.Networks[0]
	args := []string{"edge", "add", "--network", string(n.ID), "--a", string(n.Nodes[0].ID), "--b", string(n.Nodes[4].ID), "--weight", "4294967295", "--transports", "udp,tcp,quic,ws,wss,grpc", "--enabled=false", "--ipv4-direct=false", "--ipv6-direct=false", "--preferred-candidate", "candidate-preference"}
	raw, err := h.run(t, args...)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Revision uint64     `json:"revision"`
		Edge     model.Edge `json:"edge"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	wantEdge := model.Edge{ID: result.Edge.ID, A: n.Nodes[0].ID, B: n.Nodes[4].ID, Weight: 1<<32 - 1, Transports: []model.Transport{model.UDP, model.TCP, model.QUIC, model.WS, model.WSS, model.GRPC}, Methods: model.ConnectionMethods{HolePunch: true}, PreferredCandidate: "candidate-preference"}
	if result.Revision != initial.Revision+1 || !reflect.DeepEqual(result.Edge, wantEdge) {
		t.Fatalf("unexpected edge: %s", raw)
	}
	want := initial.Clone()
	want.Revision++
	want.Networks[0].Edges = append(want.Networks[0].Edges, wantEdge)
	if !reflect.DeepEqual(h.state(t), want) {
		t.Fatal("edge addition changed unrelated configuration")
	}
	if output, err := h.run(t, args...); err == nil || !strings.Contains(err.Error(), "duplicate edge") || len(output) != 0 {
		t.Fatalf("duplicate edge was not rejected: %s, %v", output, err)
	}
	if output, err := h.run(t, "node", "list", "--network", string(model.NewID())); err == nil || !strings.Contains(err.Error(), "not found") || len(output) != 0 {
		t.Fatalf("missing network was not reported: %s, %v", output, err)
	}
	if !reflect.DeepEqual(h.state(t), want) || h.logouts.Load() != 5 {
		t.Fatal("failed operation changed state or leaked a session")
	}
}

func TestAdminConcurrentEditIsNotOverwritten(t *testing.T) {
	var puts atomic.Int32
	h := newAdminHarness(t, func(db *store.Store, r *http.Request) {
		if r.Method != "PUT" {
			return
		}
		puts.Add(1)
		s, err := db.Read()
		if err != nil {
			t.Error(err)
			return
		}
		_, err = db.Update(s.Revision, func(s *model.State) error {
			s.Networks[0].Name = "Concurrent edit"
			return nil
		})
		if err != nil {
			t.Error(err)
		}
	})
	initial := h.seed(t)
	n := initial.Networks[0]
	raw, err := h.run(t, "edge", "add", "--network", string(n.ID), "--a", string(n.Nodes[0].ID), "--b", string(n.Nodes[4].ID))
	if err == nil || !strings.Contains(err.Error(), "HTTP 409") || len(raw) != 0 {
		t.Fatalf("expected conflict: %s, %v", raw, err)
	}
	initial.Revision++
	initial.Networks[0].Name = "Concurrent edit"
	if !reflect.DeepEqual(h.state(t), initial) || puts.Load() != 1 || h.logouts.Load() != 1 {
		t.Fatal("conflict overwrote state, retried mutation or leaked session")
	}
}

type failedAdminOutput struct{}

func (failedAdminOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestAdminEmptyListsDefaultsAndFailedOutput(t *testing.T) {
	h := newAdminHarness(t, nil)
	for _, noun := range []string{"network", "node"} {
		raw, err := h.run(t, noun, "list")
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]json.RawMessage
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		if string(result[noun+"s"]) != "[]" {
			t.Fatalf("empty list is not an array: %s", raw)
		}
	}
	initial := h.seed(t)
	n := initial.Networks[0]
	_, err := h.run(t, "edge", "add", "--network", string(n.ID), "--a", string(n.Nodes[0].ID), "--b", string(n.Nodes[4].ID))
	if err != nil {
		t.Fatal(err)
	}
	s := h.state(t)
	edge := s.Networks[0].Edges[len(n.Edges)]
	if !edge.Enabled || edge.Weight != 1 || !reflect.DeepEqual(edge.Transports, []model.Transport{model.UDP, model.TCP}) || !edge.Methods.IPv4Direct || !edge.Methods.IPv6Direct || !edge.Methods.HolePunch || edge.PreferredCandidate != "" {
		t.Fatalf("unexpected edge defaults: %+v", edge)
	}
	args := append([]string{"network", "create", "--name", "Committed despite closed output", "--cidr", "fd80::/64"}, h.flags...)
	err = runAdmin(context.Background(), args, failedAdminOutput{})
	if !errors.Is(err, io.ErrClosedPipe) || !strings.Contains(err.Error(), "operation succeeded") {
		t.Fatalf("ambiguous output error: %v", err)
	}
	after := h.state(t)
	if after.Revision != s.Revision+1 || len(after.Networks) != 2 || after.Networks[1].Name != "Committed despite closed output" || h.logouts.Load() != 4 {
		t.Fatal("output failure lost transaction or leaked session")
	}
}

func TestAdminMalformedMutationResponseIsNotRetried(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `{"schema":1,"revision":0}`, `{"schema":1,"revision":2}`, `{"schema":1,"revision":1} {}`} {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			w.WriteHeader(201)
			_, _ = io.WriteString(w, body)
		}))
		client, err := newAdminClient(server.URL, "", true)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err = client.change(ctx, "POST", "/api/v1/networks", model.Network{}, 0, 201)
		cancel()
		client.http.CloseIdleConnections()
		server.Close()
		if err == nil || requests.Load() != 1 {
			t.Fatalf("accepted or retried malformed mutation response %q: %v", body, err)
		}
	}
}

func TestAdminTLSAndPassword(t *testing.T) {
	var logins atomic.Int32
	h := newAdminHarness(t, func(_ *store.Store, r *http.Request) {
		if r.URL.Path == "/api/v1/login" {
			logins.Add(1)
		}
	})
	initial := h.state(t)
	err := runAdmin(context.Background(), []string{"network", "list", "--server", h.flags[1]}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "certificate") || logins.Load() != 0 {
		t.Fatalf("untrusted TLS reached login: %v", err)
	}
	t.Setenv("GRAPHWAN_ADMIN_PASSWORD", "wrong password")
	if _, err := h.run(t, "network", "list"); err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("bad password: %v", err)
	}
	if !reflect.DeepEqual(h.state(t), initial) || h.logouts.Load() != 0 {
		t.Fatal("failed authentication changed state")
	}
}

func TestAdminValidationBeforeAuthentication(t *testing.T) {
	t.Setenv("GRAPHWAN_ADMIN_PASSWORD", adminTestPassword)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	validIDs := []string{"--network", string(testutil.ID(1)), "--a", string(testutil.ID(2)), "--b", string(testutil.ID(3))}
	cases := [][]string{
		{"network"}, {"network", "delete"}, {"node", "list", "extra"}, {"node", "list", "--timeout", "0s"},
		{"network", "create", "--cidr", "10.1.0.0/24"},
		{"network", "create", "--name", "bad", "--cidr", "10.1.0.1/24"},
		{"network", "create", "--name", "bad", "--cidr", "::/0"},
		{"network", "create", "--name", "bad", "--cidr", "fd00::/64", "--mtu", "1279"},
		{"node", "list", "--network", "bad"}, {"edge", "add"},
	}
	for _, tail := range [][]string{{"--weight", "0"}, {"--weight", "4294967296"}, {"--transports", "udp,udp"}, {"--transports", "invalid"}, {"--ipv4-direct=false", "--ipv6-direct=false", "--hole-punch=false"}} {
		args := append([]string{"edge", "add"}, validIDs...)
		cases = append(cases, append(args, tail...))
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out bytes.Buffer
			err := runAdmin(context.Background(), append(args, "--server", server.URL, "--http"), &out)
			if err == nil || out.Len() != 0 {
				t.Fatalf("invalid input succeeded: %v, %s", err, out.String())
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatal("invalid flags reached server")
	}
	for _, origin := range []string{server.URL, "http://192.0.2.1", "http://localhost", "https://user:secret@example.com", "https://example.com/path", "https://example.com?", "https://example.com/#fragment", "https://example.com:65536"} {
		allow := origin != server.URL
		if c, err := newAdminClient(origin, "", allow); err == nil {
			c.http.CloseIdleConnections()
			t.Errorf("accepted invalid origin %q", origin)
		}
	}
}

func TestAdminRefusesCredentialAndMutationRedirects(t *testing.T) {
	t.Setenv("GRAPHWAN_ADMIN_PASSWORD", adminTestPassword)
	var leaked atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Add(1) }))
	defer destination.Close()
	for _, stage := range []string{"/api/v1/login", "/api/v1/networks"} {
		t.Run(stage, func(t *testing.T) {
			var mutations atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == stage {
					mutations.Add(1)
					http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
					return
				}
				switch r.URL.Path {
				case "/api/v1/login":
					_, _ = io.WriteString(w, `{"csrf_token":"token"}`)
				case "/api/v1/state":
					_ = json.NewEncoder(w).Encode(model.EmptyState())
				default:
					w.WriteHeader(204)
				}
			}))
			defer server.Close()
			err := runAdmin(context.Background(), []string{"network", "create", "--name", "test", "--cidr", "10.80.0.0/24", "--server", server.URL, "--http"}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "HTTP 307") || mutations.Load() != 1 || leaked.Load() != 0 {
				t.Fatalf("redirect followed or retried: %v", err)
			}
		})
	}
}

func TestAdminTimeoutAndMalformedResponses(t *testing.T) {
	t.Setenv("GRAPHWAN_ADMIN_PASSWORD", adminTestPassword)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	err := runAdmin(context.Background(), []string{"node", "list", "--server", server.URL, "--http", "--timeout", "50ms"}, io.Discard)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout was not applied: %v", err)
	}
	for _, body := range []string{`{}`, `{"csrf_token":"ok"} {}`, `not JSON`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
		client, err := newAdminClient(server.URL, "", true)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err = client.login(ctx, adminTestPassword)
		cancel()
		client.http.CloseIdleConnections()
		server.Close()
		if err == nil {
			t.Fatalf("accepted malformed login %q", body)
		}
	}
}
