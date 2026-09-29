package agent_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/agent"
	"github.com/eWloYW8/GraphWAN/internal/control"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/store"
)

type controller struct {
	app            *control.Server
	server         *httptest.Server
	admin          *http.Client
	roots          *x509.CertPool
	csrf           string
	dropEnrollment atomic.Bool
}

func newController(t *testing.T) *controller {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	app, err := control.New(db, control.Options{Password: "test-controller-password"})
	if err != nil {
		t.Fatal(err)
	}
	h := &controller{app: app, roots: x509.NewCertPool()}
	h.roots.AppendCertsFromPEM(app.Authority().PEM)
	h.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/enroll" && h.dropEnrollment.Swap(false) {
			// Simulate a lost response after the controller commits enrollment.
			recorder := httptest.NewRecorder()
			app.ServeHTTP(recorder, r)
			if recorder.Code != 201 {
				t.Errorf("enrollment did not commit before drop: %s", recorder.Body.String())
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		app.ServeHTTP(w, r)
	}))
	h.server.TLS, err = app.Authority().ServerTLS([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	h.server.StartTLS()
	jar, _ := cookiejar.New(nil)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: h.roots, MinVersion: tls.VersionTLS13}}
	h.admin = &http.Client{Jar: jar, Transport: transport, Timeout: 5 * time.Second}
	t.Cleanup(func() { app.Close(); h.server.Close(); transport.CloseIdleConnections(); db.Close() })
	var login struct {
		CSRF string `json:"csrf_token"`
	}
	h.request(t, "POST", "/api/v1/login", map[string]string{"password": "test-controller-password"}, "", 200, &login)
	h.csrf = login.CSRF
	return h
}
func (h *controller) request(t *testing.T, method, path string, body any, revision string, want int, out any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(method, h.server.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", h.csrf)
	if revision != "" {
		req.Header.Set("If-Match", revision)
	}
	resp, err := h.admin.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s %s: %d: %s", method, path, resp.StatusCode, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatal(err)
		}
	}
}
func (h *controller) token(t *testing.T) string {
	t.Helper()
	var result struct {
		Token string `json:"token"`
	}
	h.request(t, "POST", "/api/v1/enrollment-tokens", map[string]int{}, "", 201, &result)
	return result.Token
}
func (h *controller) state(t *testing.T) model.State {
	t.Helper()
	var state model.State
	h.request(t, "GET", "/api/v1/state", nil, "", 200, &state)
	return state
}
func waitFor(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !fn() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func runClient(t *testing.T, c *agent.Client) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	return cancel, done
}
func stopClient(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client did not stop")
	}
}
func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestClientControlAndOfflineRestart(t *testing.T) {
	h := newController(t)
	path := filepath.Join(t.TempDir(), "agent.db")
	cache, err := agent.OpenCache(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cache.Close() }()
	runtime := &testRuntime{}
	options := agent.Options{Server: h.server.URL, Name: "test-agent", EnrollmentToken: h.token(t), Roots: h.roots, Logger: quiet(), Version: "test"}
	client, err := agent.NewClient(cache, runtime, options)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	cancel, done := runClient(t, client)
	defer cancel()
	waitFor(t, func() bool { _, ok := runtime.last(); return ok })
	state := h.state(t)
	if len(state.Agents) != 1 {
		t.Fatal("agent not enrolled")
	}
	n := model.Network{Name: "Office", CIDR: netip.MustParsePrefix("10.99.0.0/24"), Nodes: []model.Node{{AgentID: state.Agents[0].ID, Name: "desktop", Address: netip.MustParseAddr("10.99.0.1")}}}
	var next model.State
	h.request(t, "POST", "/api/v1/networks", n, strconv.FormatUint(state.Revision, 10), 201, &next)
	waitFor(t, func() bool { return client.Report().AppliedRevision == next.Revision })
	waitFor(t, func() bool {
		var reports []model.AgentStatus
		h.request(t, "GET", "/api/v1/telemetry", nil, "", 200, &reports)
		return len(reports) == 1 && reports[0].AppliedRevision == next.Revision && reports[0].Resources != nil && reports[0].Resources.CPUPercent != nil && reports[0].Resources.Validate() == nil
	})
	// A device failure is runtime health, not a configuration-application failure.
	runtime.mu.Lock()
	runtime.healthError = errors.New("network Office: TUN device unavailable")
	runtime.mu.Unlock()
	waitFor(t, func() bool {
		var reports []model.AgentStatus
		h.request(t, "GET", "/api/v1/telemetry", nil, "", 200, &reports)
		return len(reports) == 1 && reports[0].RuntimeError != "" && reports[0].ConfigError == "" && reports[0].AppliedRevision == next.Revision
	})
	runtime.mu.Lock()
	runtime.healthError = nil
	runtime.mu.Unlock()
	waitFor(t, func() bool {
		var reports []model.AgentStatus
		h.request(t, "GET", "/api/v1/telemetry", nil, "", 200, &reports)
		return len(reports) == 1 && reports[0].RuntimeError == "" && reports[0].AppliedRevision == next.Revision
	})
	desired, applied, err := cache.Snapshots()
	if err != nil || desired.Revision != next.Revision || applied.Revision != next.Revision {
		t.Fatalf("configuration was not persisted: %v", err)
	}
	h.app.Close()
	h.server.Close()
	// Controller loss does not apply an empty config or discard runtime state.
	time.Sleep(100 * time.Millisecond)
	if last, _ := runtime.last(); last.Revision != next.Revision || len(last.Networks) != 1 {
		t.Fatal("controller outage cleared configuration")
	}
	stopClient(t, cancel, done)
	client.Close()
	key := cache.PrivateKey()
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	cache, err = agent.OpenCache(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(key, cache.PrivateKey()) {
		t.Fatal("agent restart changed identity")
	}
	restarted := &testRuntime{}
	options.EnrollmentToken = ""
	client2, err := agent.NewClient(cache, restarted, options)
	if err != nil {
		t.Fatal(err)
	}
	defer client2.Close()
	cancel2, done2 := runClient(t, client2)
	defer cancel2()
	waitFor(t, func() bool {
		last, ok := restarted.last()
		return ok && last.Revision == next.Revision && len(last.Networks) == 1
	})
	stopClient(t, cancel2, done2)
}

func TestEnrollmentResponseLossRecoversSameIdentity(t *testing.T) {
	h := newController(t)
	cache, err := agent.OpenCache(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	runtime := &testRuntime{}
	options := agent.Options{Server: h.server.URL, Name: "recoverable", EnrollmentToken: h.token(t), Roots: h.roots, Logger: quiet()}
	client, err := agent.NewClient(cache, runtime, options)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	h.dropEnrollment.Store(true)
	if err := client.Run(context.Background()); err == nil {
		t.Fatal("dropped enrollment response was not reported")
	}
	initial := h.state(t)
	if len(initial.Agents) != 1 {
		t.Fatal("drop did not occur after commit")
	}
	cancel, done := runClient(t, client)
	defer cancel()
	waitFor(t, func() bool { _, ok := runtime.last(); return ok })
	state := h.state(t)
	if state.Revision != initial.Revision || len(state.Agents) != 1 || state.Agents[0].ID != initial.Agents[0].ID {
		t.Fatal("retry created a new identity or revision")
	}
	stopClient(t, cancel, done)
}

func TestClientRequiresTrustedHTTPS(t *testing.T) {
	cache, err := agent.OpenCache(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	for _, server := range []string{"http://localhost:8443", "https://user:secret@example.com", "https://example.com/path", "https://example.com?token=secret"} {
		if c, err := agent.NewClient(cache, &testRuntime{}, agent.Options{Server: server}); err == nil {
			c.Close()
			t.Fatalf("accepted %s", server)
		}
	}
	h := newController(t)
	c, err := agent.NewClient(cache, &testRuntime{}, agent.Options{Server: h.server.URL, Name: "untrusted", EnrollmentToken: h.token(t), Roots: x509.NewCertPool(), Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Run(context.Background()); err == nil {
		t.Fatal("untrusted controller accepted")
	}
	if len(h.state(t).Agents) != 0 {
		t.Fatal("token sent before server authentication")
	}
}
