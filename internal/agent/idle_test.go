package agent

import (
	"slices"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

func TestIdlePublication(t *testing.T) {
	previous := model.AgentReport{Version: "test", AppliedRevision: 3, Links: []model.LinkStatus{{LinkID: "a", Healthy: true, Active: true, RXBytes: 4}, {LinkID: "b", Healthy: true}}}
	next := previous
	next.Links = slices.Clone(previous.Links)
	slices.Reverse(next.Links)
	next.Links[0].RTTMillis = 7
	next.Links[0].Loss = 0.1
	next.Resources = &model.ResourceUsage{UptimeSeconds: 10}
	if reportChanged(previous, next) {
		t.Fatal("idle metrics or order triggered a full report")
	}
	for name, change := range map[string]func(*model.AgentReport){
		"data":      func(r *model.AgentReport) { r.Links[0].TXBytes++ },
		"health":    func(r *model.AgentReport) { r.Links[0].Healthy = false },
		"selection": func(r *model.AgentReport) { r.Links[0].Active = true },
		"removal":   func(r *model.AgentReport) { r.Links = r.Links[:1] },
		"revision":  func(r *model.AgentReport) { r.AppliedRevision++ },
		"error":     func(r *model.AgentReport) { r.RuntimeError = "tunnel unavailable" },
	} {
		r := next
		r.Links = slices.Clone(next.Links)
		change(&r)
		if !reportChanged(previous, r) {
			t.Fatalf("suppressed %s update", name)
		}
	}
	for _, tc := range []struct {
		name                              string
		compact, force, settling, changed bool
		elapsed                           time.Duration
		want                              bool
	}{
		{"legacy idle", false, false, false, false, 15 * time.Second, true},
		{"compact idle", true, false, false, false, 15 * time.Second, false},
		{"idle refresh", true, false, false, false, time.Minute, true},
		{"configuration ack", true, true, false, false, 0, true},
		{"zero final rate", true, false, true, false, 2 * time.Second, true},
		{"traffic or state", true, false, false, true, 2 * time.Second, true},
	} {
		if got := reportDue(tc.compact, tc.force, tc.settling, tc.changed, tc.elapsed); got != tc.want {
			t.Fatalf("%s: reportDue = %v, want %v", tc.name, got, tc.want)
		}
	}
	c := &Client{endpointsChanged: make(chan struct{}, 1)}
	endpoint := model.Endpoint{ID: "11111111111111111111111111111111", Transport: model.UDP, Source: model.Observed, URL: "udp://192.0.2.1:24752", ExpiresAt: time.Now().Add(2 * time.Minute)}
	publish := func(e []model.Endpoint, want bool) {
		t.Helper()
		if err := c.SetEndpoints(e); err != nil {
			t.Fatal(err)
		}
		select {
		case <-c.endpointsChanged:
			if !want {
				t.Fatal("redundant endpoint publication")
			}
		default:
			if want {
				t.Fatal("missing endpoint publication")
			}
		}
	}
	publish(nil, true)
	publish(nil, false)
	publish([]model.Endpoint{endpoint}, true)
	publish([]model.Endpoint{endpoint}, false)
	endpoint.ExpiresAt = endpoint.ExpiresAt.Add(20 * time.Second)
	publish([]model.Endpoint{endpoint}, false)
	endpoint.URL = "udp://192.0.2.1:24753"
	publish([]model.Endpoint{endpoint}, true)
	publish(nil, true)
}
