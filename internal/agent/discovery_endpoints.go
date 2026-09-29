package agent

import (
	"cmp"
	"net/netip"
	"net/url"
	"slices"
	"time"

	"github.com/graphwan/graphwan/internal/model"
)

// Limit discovery without allowing a large container/link-local inventory to
// evict public STUN mappings. Manual entries reserve capacity and URL ownership;
// interfaces retain their identity when STUN reports the identical URL.
func selectDiscoveredEndpoints(configured, interfaces, observed []model.Endpoint, now time.Time) ([]model.Endpoint, int) {
	seen := map[string]bool{}
	manualCount := 0
	for _, endpoint := range configured {
		if endpoint.Source == model.Manual {
			seen[endpoint.URL] = true
			manualCount++
		}
	}
	combined := make([]model.Endpoint, 0, len(interfaces)+len(observed))
	for _, endpoints := range [][]model.Endpoint{interfaces, observed} {
		for _, endpoint := range endpoints {
			if seen[endpoint.URL] || endpoint.Source == model.Observed && !now.Before(endpoint.ExpiresAt) {
				continue
			}
			seen[endpoint.URL] = true
			combined = append(combined, endpoint)
		}
	}
	total := len(combined)
	limit := max(0, model.MaxEndpoints-manualCount)
	if total > limit {
		slices.SortFunc(combined, func(a, b model.Endpoint) int {
			if n := cmp.Compare(discoveryPriority(a), discoveryPriority(b)); n != 0 {
				return n
			}
			return cmp.Compare(a.ID, b.ID)
		})
		combined = combined[:limit]
	}
	// Publish in stable ID order regardless of whether the limit was reached.
	slices.SortFunc(combined, func(a, b model.Endpoint) int { return cmp.Compare(a.ID, b.ID) })
	return combined, total
}

func discoveryPriority(endpoint model.Endpoint) int {
	if endpoint.Source == model.Observed {
		return 0
	}
	u, err := url.Parse(endpoint.URL)
	if err != nil {
		return 3
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || ip.IsLinkLocalUnicast() {
		return 3
	}
	if ip.IsPrivate() {
		return 2
	}
	return 1
}
