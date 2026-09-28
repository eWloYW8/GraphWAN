package control_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/model"
)

type event struct {
	State    *model.State        `json:"state"`
	Revision uint64              `json:"revision"`
	Agents   []model.AgentStatus `json:"agents"`
}

func eventStream(t *testing.T, h *harness) (*http.Response, <-chan event, <-chan struct{}) {
	t.Helper()
	r, err := h.client.Get(h.server.URL + "/api/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("unexpected stream response %d %v", r.StatusCode, r.Header)
	}
	t.Cleanup(func() { r.Body.Close() })
	messages := make(chan event, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(r.Body)
		for scanner.Scan() {
			if raw, ok := strings.CutPrefix(scanner.Text(), "data: "); ok {
				var value event
				if json.Unmarshal([]byte(raw), &value) != nil {
					return
				}
				select {
				case messages <- value:
				default:
					return
				}
			}
		}
	}()
	return r, messages, done
}
func nextEvent(t *testing.T, messages <-chan event) event {
	t.Helper()
	select {
	case v := <-messages:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("missing browser event")
	}
	return event{}
}
func TestBrowserEventsStateChangeLimitsAndLogout(t *testing.T) {
	h := setup(t)
	h.request(t, "GET", "/api/v1/events", nil, nil, 401)
	h.login(t)
	h.request(t, "GET", "/api/v1/events", nil, map[string]string{"Origin": "https://foreign.invalid"}, 403)
	_, messages, done := eventStream(t, h)
	first := nextEvent(t, messages)
	if first.State == nil || first.State.Revision != 0 || len(first.Agents) != 0 {
		t.Fatal("initial state missing")
	}
	idle := nextEvent(t, messages)
	if idle.State != nil {
		t.Fatal("unchanged topology repeated in telemetry event")
	}
	h.request(t, "POST", "/api/v1/networks", map[string]any{"name": "browser network", "cidr": "10.1.0.0/24"}, map[string]string{"If-Match": "0"}, 201)
	changed := nextEvent(t, messages)
	if changed.State == nil || changed.Revision != 1 || changed.State.Networks[0].Name != "browser network" {
		t.Fatalf("topology change not streamed: %+v", changed)
	}
	for range 3 {
		_, events, _ := eventStream(t, h)
		nextEvent(t, events)
	}
	h.request(t, "GET", "/api/v1/events", nil, nil, 429)
	// Logout revokes already open views, not just future HTTP requests.
	h.request(t, "POST", "/api/v1/logout", nil, nil, 204)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("logged-out stream remained open")
	}
	h.request(t, "GET", "/api/v1/events", nil, nil, 401)
}
func TestBrowserEventsCloseWithController(t *testing.T) {
	h := setup(t)
	h.login(t)
	_, messages, done := eventStream(t, h)
	nextEvent(t, messages)
	closed := make(chan struct{})
	go func() { h.app.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("controller shutdown stalled")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("event connection leaked")
	}
	h.request(t, "GET", "/api/v1/events", nil, nil, 503)
}
func TestEmbeddedUIAndAssetRouting(t *testing.T) {
	h := setup(t)
	response, err := h.client.Get(h.server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !strings.Contains(string(raw), "GraphWAN") || response.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("embedded UI missing security headers or content")
	}
	start := strings.Index(string(raw), "/assets/")
	if start < 0 {
		t.Fatal("UI has no production assets")
	}
	asset := strings.Split(string(raw)[start:], "\"")[0]
	request, _ := http.NewRequestWithContext(context.Background(), "GET", h.server.URL+asset, nil)
	response, err = h.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 || !strings.Contains(response.Header.Get("Cache-Control"), "immutable") {
		t.Fatal("versioned asset is not served")
	}
	licenses := h.request(t, "GET", "/THIRD_PARTY_LICENSES.txt", nil, nil, 200)
	if !strings.Contains(string(licenses), "react@") || !strings.Contains(string(licenses), "MIT License") {
		t.Fatal("bundled dependency notices missing")
	}
	for _, path := range []string{"/api/v1/missing", "/assets/missing.js", "/assets/", "/config.db"} {
		h.request(t, "GET", path, nil, nil, 404)
	}
}
