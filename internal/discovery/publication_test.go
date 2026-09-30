package discovery

import (
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

func TestObservedLeasePublication(t *testing.T) {
	start := time.Unix(1000, 0)
	prior := model.Endpoint{ID: "mapping", URL: "udp://192.0.2.1:24752", Transport: model.UDP, Source: model.Observed, ExpiresAt: start.Add(ObservedLifetime)}
	for _, seconds := range []int{20, 59, 60, 120, 140} {
		now := start.Add(time.Duration(seconds) * time.Second)
		fresh := prior
		fresh.ExpiresAt = now.Add(ObservedLifetime)
		out := CoalesceEndpoints([]model.Endpoint{prior}, []model.Endpoint{fresh}, now)
		want := fresh.ExpiresAt
		if seconds < 60 {
			want = prior.ExpiresAt
		}
		if out[0].ExpiresAt != want {
			t.Fatalf("incorrect lease at %ds", seconds)
		}
	}
	shortened := start.Add(time.Minute)
	if PublishedExpiry(prior.ExpiresAt, shortened, start) != shortened {
		t.Fatal("extended shortened observation")
	}
	if len(CoalesceEndpoints([]model.Endpoint{prior}, nil, start)) != 0 {
		t.Fatal("retained removed endpoint")
	}
}
