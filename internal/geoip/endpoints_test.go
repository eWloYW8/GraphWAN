package geoip

import (
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

func TestPublicEndpoints(t *testing.T) {
	now := time.Now()
	endpoints := []model.Endpoint{
		{URL: "tcp://5.9.0.1:24752", Source: model.Manual},
		{URL: "udp://8.8.8.8:24752", Source: model.Interface},
		{URL: "udp://[2001:4860:4860::8888]:24752", Source: model.Observed, ExpiresAt: now.Add(time.Minute)},
		{URL: "tcp://1.1.1.1:24752", Source: model.Observed, ExpiresAt: now.Add(time.Minute)},
		{URL: "udp://1.1.1.1:24752", Source: model.Observed, ExpiresAt: now.Add(time.Minute)},
		{URL: "tcp://9.9.9.9:24752", Source: model.Observed, ExpiresAt: now},
		{URL: "tcp://4.2.2.2:24752", Source: model.Observed},
		{URL: "wss://proxy.example.com:443/tunnel", Source: model.Manual},
	}
	for _, ip := range []string{"10.1.2.4", "172.17.0.1", "192.168.1.1", "127.0.0.1", "169.254.1.1", "100.64.0.1", "198.18.0.1", "192.0.2.1", "203.0.113.1", "0.1.2.3", "240.0.0.1", "224.0.0.1", "[::1]", "[fc00::1]", "[fe80::1]", "[2001:db8::1]", "[3fff::1]"} {
		endpoints = append(endpoints, model.Endpoint{URL: "tcp://" + ip + ":24752", Source: model.Interface})
	}
	expected := []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("2001:4860:4860::8888"), netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("5.9.0.1")}
	if got := publicEndpoints(endpoints, now); !slices.Equal(got, expected) {
		t.Fatalf("public endpoints = %v; want %v", got, expected)
	}
	// Configuration order must not change which address determines geography.
	slices.Reverse(endpoints)
	if got := publicEndpoints(endpoints, now); !slices.Equal(got, expected) {
		t.Fatalf("reversed public endpoints = %v; want %v", got, expected)
	}
}

func TestPrivateAgentsDoNotDownloadDatabase(t *testing.T) {
	service := New(t.TempDir(), "", nil)
	defer service.Close()
	result := service.Locations([]model.Agent{{ID: "private", Endpoints: []model.Endpoint{{URL: "tcp://10.1.2.4:24752", Source: model.Interface}}}})
	if result.Pending || result.Agents["private"].Location != nil || len(result.Agents["private"].PublicIPs) != 0 || result.Agents["private"].Reason == "" {
		t.Fatalf("private-only agents should remain unlocated without a download: %+v", result)
	}
}
