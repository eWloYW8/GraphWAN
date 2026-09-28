package control_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/graphwan/graphwan/internal/control"
	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/store"
)

const password = "correct horse battery staple"

type harness struct {
	server *httptest.Server
	app    *control.Server
	client *http.Client
	csrf   string
	roots  *x509.CertPool
}

func setup(t *testing.T) *harness {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	app, err := control.New(db, control.Options{Password: password})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(app)
	server.TLS, err = app.Authority().ServerTLS([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	server.StartTLS()
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(app.Authority().PEM)
	jar, _ := cookiejar.New(nil)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}
	h := &harness{server: server, app: app, client: &http.Client{Jar: jar, Transport: transport, Timeout: 5 * time.Second}, roots: roots}
	t.Cleanup(func() { app.Close(); server.Close(); transport.CloseIdleConnections(); db.Close() })
	return h
}
func (h *harness) request(t *testing.T, method, path string, body any, headers map[string]string, want int) []byte {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, h.server.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if h.csrf != "" {
		req.Header.Set("X-CSRF-Token", h.csrf)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, resp.StatusCode, want, data)
	}
	return data
}
func (h *harness) login(t *testing.T) {
	t.Helper()
	raw := h.request(t, "POST", "/api/v1/login", map[string]string{"password": password}, nil, 200)
	var result struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	h.csrf = result.CSRF
}
func (h *harness) state(t *testing.T) model.State {
	t.Helper()
	var s model.State
	if err := json.Unmarshal(h.request(t, "GET", "/api/v1/state", nil, nil, 200), &s); err != nil {
		t.Fatal(err)
	}
	return s
}
func (h *harness) token(t *testing.T) string {
	t.Helper()
	var result struct {
		Token string `json:"token"`
	}
	json.Unmarshal(h.request(t, "POST", "/api/v1/enrollment-tokens", map[string]int{"ttl_seconds": 60}, nil, 201), &result)
	return result.Token
}

type enrollment struct {
	AgentID     model.ID `json:"agent_id"`
	Certificate []byte   `json:"certificate"`
	CA          []byte   `json:"ca_certificate"`
}

func (h *harness) enroll(t *testing.T, name string) (enrollment, tls.Certificate) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	token := h.token(t)
	body := map[string]any{"name": name, "csr": csr}
	headers := map[string]string{"Authorization": "Bearer " + token}
	raw := h.request(t, "POST", "/api/v1/enroll", body, headers, 201)
	// Successful enrollment consumes its token in the same transaction as the agent.
	h.request(t, "POST", "/api/v1/enroll", body, headers, 401)
	var result enrollment
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(result.Certificate, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if err != nil {
		t.Fatal(err)
	}
	return result, cert
}
func TestManagementAuthenticationAndTransactions(t *testing.T) {
	h := setup(t)
	h.request(t, "GET", "/api/v1/state", nil, nil, 401)
	h.request(t, "POST", "/api/v1/login", map[string]string{"password": "wrong"}, nil, 401)
	h.request(t, "POST", "/api/v1/login", map[string]string{"password": password}, map[string]string{"Origin": "https://evil.example"}, 403)
	h.login(t)
	body := map[string]any{"name": "Office", "cidr": "10.70.0.0/24"}
	h.request(t, "POST", "/api/v1/networks", body, map[string]string{"If-Match": "0", "X-CSRF-Token": "bad"}, 403)
	h.request(t, "POST", "/api/v1/networks", body, nil, 428)
	h.request(t, "POST", "/api/v1/networks", body, map[string]string{"If-Match": "0"}, 201)
	h.request(t, "POST", "/api/v1/networks", body, map[string]string{"If-Match": "0"}, 409)
	state := h.state(t)
	if state.Revision != 1 || len(state.Networks) != 1 {
		t.Fatal(state)
	}
	invalid := state.Networks[0]
	invalid.MTU = 1
	h.request(t, "PUT", "/api/v1/networks/"+string(invalid.ID), invalid, map[string]string{"If-Match": "1"}, 422)
	if h.state(t).Revision != 1 {
		t.Fatal("invalid edit consumed revision")
	}
	h.request(t, "DELETE", "/api/v1/networks/"+string(invalid.ID), nil, map[string]string{"If-Match": "1"}, 200)
	h.request(t, "POST", "/api/v1/logout", nil, nil, 204)
	h.request(t, "GET", "/api/v1/state", nil, nil, 401)
}
func TestEnrollmentControlPushAndRevocation(t *testing.T) {
	h := setup(t)
	h.login(t)
	enrolled, certificate := h.enroll(t, "Laptop")
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: h.roots, Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, h.server.URL+"/api/v1/agent/control", &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	var msg model.ControlMessage
	if err := wsjson.Read(ctx, conn, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != "config" || msg.Snapshot.AgentID != enrolled.AgentID || len(msg.Snapshot.Networks) != 0 {
		t.Fatalf("initial config=%+v", msg)
	}
	state := h.state(t)
	n := model.Network{Name: "Home", CIDR: netip.MustParsePrefix("10.80.0.0/24"), Nodes: []model.Node{{AgentID: enrolled.AgentID, Name: "Laptop", Address: netip.MustParseAddr("10.80.0.1")}}}
	h.request(t, "POST", "/api/v1/networks", n, map[string]string{"If-Match": strconv.FormatUint(state.Revision, 10)}, 201)
	if err := wsjson.Read(ctx, conn, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != "config" || len(msg.Snapshot.Networks) != 1 {
		t.Fatalf("updated config=%+v", msg)
	}
	revision := msg.Snapshot.Revision
	report := model.AgentReport{Version: "test", AppliedRevision: revision, Links: []model.LinkStatus{}}
	if err := wsjson.Write(ctx, conn, model.ControlMessage{Type: "ack", Report: &report}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		var statuses []model.AgentStatus
		json.Unmarshal(h.request(t, "GET", "/api/v1/telemetry", nil, nil, 200), &statuses)
		if len(statuses) == 1 && statuses[0].AppliedRevision == revision && statuses[0].Connected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("applied ACK not observed")
		}
		time.Sleep(time.Millisecond * 5)
	}
	_, browser, _ := eventStream(t, h)
	observed := nextEvent(t, browser)
	if len(observed.Agents) != 1 || !observed.Agents[0].Connected || observed.Agents[0].Version != "test" || observed.Agents[0].AppliedRevision != revision {
		t.Fatal("mTLS Agent report did not reach browser stream")
	}
	h.request(t, "PATCH", "/api/v1/agents/"+string(enrolled.AgentID), map[string]bool{"revoked": true}, map[string]string{"If-Match": strconv.FormatUint(revision, 10)}, 200)
	if err := wsjson.Read(ctx, conn, &msg); err == nil {
		t.Fatal("revoked connection still open")
	}
	if next, resp, err := websocket.Dial(ctx, h.server.URL+"/api/v1/agent/control", &websocket.DialOptions{HTTPClient: client}); err == nil {
		next.CloseNow()
		t.Fatal("revoked agent reconnected")
	} else if resp == nil || resp.StatusCode != 401 {
		t.Fatalf("unexpected revocation response: %v %+v", err, resp)
	}
	observed = nextEvent(t, browser)
	if observed.State == nil || observed.Agents[0].Connected {
		t.Fatal("revocation did not reach browser stream")
	}
	// Browser cookies alone are not agent authentication.
	h.request(t, "GET", "/api/v1/agent/control", nil, nil, 401)
}
func TestEnrollmentRollbackAndConcurrentTokenUse(t *testing.T) {
	h := setup(t)
	h.login(t)
	token := h.token(t)
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	csr, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	h.request(t, "POST", "/api/v1/enroll", map[string]any{"name": "", "csr": csr}, map[string]string{"Authorization": "Bearer " + token}, 422)
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Go(func() {
			body, _ := json.Marshal(map[string]any{"name": "agent", "csr": csr})
			req, _ := http.NewRequest("POST", h.server.URL+"/api/v1/enroll", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := h.client.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			results <- resp.StatusCode
		})
	}
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for status := range results {
		counts[status]++
	}
	if counts[201] != 1 || counts[401] != 1 {
		t.Fatal(fmt.Sprintf("token consumption statuses: %v", counts))
	}
	if state := h.state(t); len(state.Agents) != 1 || state.Revision != 1 {
		t.Fatal("registration did not roll back atomically")
	}
}
