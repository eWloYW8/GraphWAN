package discovery

import (
	"slices"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

// PublishedExpiry keeps a stable mapping's advertised lease until half of its
// lifetime remains. Probing continues normally; only redundant publication is
// suppressed. Never invent a lease beyond a successful observation or postpone
// a shortened validity window. Callers must first compare the endpoint identity.
func PublishedExpiry(previous, observed, now time.Time) time.Time {
	if previous.After(now.Add(ObservedLifetime/2)) && observed.After(previous) {
		return previous
	}
	return observed
}

func CoalesceEndpoints(previous, observed []model.Endpoint, now time.Time) []model.Endpoint {
	old := make(map[model.ID]model.Endpoint, len(previous))
	for _, endpoint := range previous {
		old[endpoint.ID] = endpoint
	}
	out := slices.Clone(observed)
	for i, endpoint := range out {
		prior, ok := old[endpoint.ID]
		if !ok || endpoint.Source != model.Observed {
			continue
		}
		expires := endpoint.ExpiresAt
		endpoint.ExpiresAt = prior.ExpiresAt
		if endpoint == prior {
			out[i].ExpiresAt = PublishedExpiry(prior.ExpiresAt, expires, now)
		}
	}
	return out
}
