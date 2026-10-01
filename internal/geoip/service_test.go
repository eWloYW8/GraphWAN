package geoip

import (
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func apiResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

// Exercises deduplication across Agents/polling, restart persistence, stale
// fallback and provider-wide rate-limit backoff without external network I/O.
func TestLocationCacheAndRateLimit(t *testing.T) {
	directory := t.TempDir()
	s := New(directory, nil)
	defer s.Close()
	ip := netip.MustParseAddr("210.32.159.174")
	var calls atomic.Int32
	var limited atomic.Bool
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.String() != "https://api.ip.sb/geoip/"+ip.String() || !strings.HasPrefix(r.UserAgent(), "GraphWAN-") {
			t.Errorf("unexpected request: %s %s", r.URL, r.UserAgent())
		}
		if limited.Load() {
			response := apiResponse(429, "{}")
			response.Header.Set("Retry-After", "1800")
			return response, nil
		}
		return apiResponse(200, `{"ip":"210.32.159.174","city":"Hangzhou","country":"China","country_code":"CN","latitude":30.2943,"longitude":120.1663}`), nil
	})
	agents := []model.Agent{{ID: "a", Endpoints: []model.Endpoint{{Source: model.Interface, URL: "tcp://210.32.159.174:24752"}}}, {ID: "b", Endpoints: []model.Endpoint{{Source: model.Interface, URL: "udp://210.32.159.174:24752"}}}}
	if !s.Locations(agents).Pending {
		t.Fatal("cold lookup must run asynchronously")
	}
	s.wg.Wait()
	for range 3 {
		result := s.Locations(agents)
		if result.Pending || result.Database != "IP.SB" || result.Agents["a"].Location == nil || result.Agents["a"].Location.City != "Hangzhou" || result.Agents["b"].Location == nil {
			t.Fatalf("unexpected cached result: %+v", result)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("shared IP requested %d times", calls.Load())
	}
	restarted := New(directory, nil)
	defer restarted.Close()
	restarted.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("fresh disk cache must not query the API")
		return nil, fmt.Errorf("unexpected request")
	})
	if result := restarted.Locations(agents); result.Pending || result.Agents["a"].Location == nil {
		t.Fatalf("cache lost on restart: %+v", result)
	}
	limited.Store(true)
	s.mu.Lock()
	entry := s.cache[ip]
	entry.ExpiresAt = time.Now().Add(-time.Second)
	s.cache[ip] = entry
	s.mu.Unlock()
	if result := s.Locations(agents); !result.Pending || result.Agents["a"].Location == nil {
		t.Fatal("stale location must remain available while refreshing")
	}
	s.wg.Wait()
	result := s.Locations(agents)
	if result.Pending || result.Error == "" || result.Agents["a"].Location == nil || time.Until(s.retryAt) < 29*time.Minute {
		t.Fatalf("429 must preserve stale data and honor Retry-After: %+v", result)
	}
	if calls.Load() != 2 {
		t.Fatalf("unexpected retries: %d", calls.Load())
	}
}

func TestProviderResponseValidation(t *testing.T) {
	s := New("", nil)
	defer s.Close()
	for _, tc := range []struct {
		body    string
		invalid bool
	}{
		{`{"ip":"210.32.159.174","country":"China"}`, false},
		{`{"ip":"8.8.8.8","latitude":30,"longitude":120}`, true},
		{`{"ip":"210.32.159.174","latitude":91,"longitude":120}`, true},
		{`<html>blocked</html>`, true},
	} {
		s.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return apiResponse(200, tc.body), nil })
		location, _, err := s.lookup(netip.MustParseAddr("210.32.159.174"))
		if (err != nil) != tc.invalid || location != nil {
			t.Fatalf("response %s: location=%+v, error=%v", tc.body, location, err)
		}
	}
}
