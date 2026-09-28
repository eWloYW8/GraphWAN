package agent

import (
	"cmp"
	"context"
	"crypto/ed25519"
	"github.com/graphwan/graphwan/internal/discovery"
	"github.com/graphwan/graphwan/internal/mesh"
	"github.com/graphwan/graphwan/internal/model"
	"slices"
	"strings"
	"time"
)

func (r *DataPlane) discover() {
	defer r.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var owner *mesh.Mesh
	var servers []string
	var observed []model.Endpoint
	var lastProbe time.Time
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
		case <-r.discoveryWake:
		}
		state := r.state.Load()
		if state == nil {
			continue
		}
		endpoints, err := discovery.Interfaces(r.identity.Public().(ed25519.PublicKey), state.snapshot.ListenPort)
		if err != nil {
			r.options.Logger.Warn("interface discovery failed", "error", err)
			continue
		}
		if owner != state.mesh || !slices.Equal(servers, state.snapshot.STUNServers) {
			owner, servers = state.mesh, slices.Clone(state.snapshot.STUNServers)
			observed, lastProbe = nil, time.Time{}
		}
		if len(servers) != 0 && time.Since(lastProbe) >= 20*time.Second {
			lastProbe = time.Now()
			ctx, cancel := context.WithTimeout(r.ctx, 3500*time.Millisecond)
			fresh, probeErr := discovery.Observe(ctx, r.identity.Public().(ed25519.PublicKey), servers, state.mesh.STUNBinding)
			cancel()
			if probeErr != nil && r.ctx.Err() == nil {
				r.options.Logger.Warn("STUN discovery incomplete", "error", probeErr)
			}
			byID := map[model.ID]model.Endpoint{}
			for _, endpoint := range observed {
				if time.Now().Before(endpoint.ExpiresAt) {
					byID[endpoint.ID] = endpoint
				}
			}
			for _, endpoint := range fresh {
				byID[endpoint.ID] = endpoint
			}
			observed = observed[:0]
			for _, endpoint := range byID {
				observed = append(observed, endpoint)
			}
			slices.SortFunc(observed, func(a, b model.Endpoint) int {
				if n := a.ExpiresAt.Compare(b.ExpiresAt); n != 0 {
					return n
				}
				return cmp.Compare(a.ID, b.ID)
			})
			if len(observed) > model.MaxEndpoints {
				observed = observed[len(observed)-model.MaxEndpoints:]
			}
		}
		// An interface/manual URL takes precedence when STUN finds no translation.
		seen := map[string]bool{}
		manualCount := 0
		for _, endpoint := range state.snapshot.Endpoints {
			if endpoint.Source == model.Manual {
				seen[endpoint.URL] = true
				manualCount++
			}
		}
		combined := make([]model.Endpoint, 0, len(endpoints)+len(observed))
		for _, endpoint := range append(endpoints, observed...) {
			if seen[endpoint.URL] || endpoint.Source == model.Observed && !time.Now().Before(endpoint.ExpiresAt) {
				continue
			}
			seen[endpoint.URL] = true
			combined = append(combined, endpoint)
		}
		slices.SortFunc(combined, func(a, b model.Endpoint) int { return strings.Compare(string(a.ID), string(b.ID)) })
		if limit := model.MaxEndpoints - manualCount; len(combined) > limit {
			r.options.Logger.Warn("discovered endpoint limit reached", "found", len(combined), "available", limit)
			combined = combined[:limit]
		}
		// Discard a probe completed for a replaced listener or STUN configuration.
		current := r.state.Load()
		if current == nil || current.mesh != state.mesh || !slices.Equal(current.snapshot.STUNServers, servers) {
			continue
		}
		if r.ctx.Err() != nil {
			return
		}
		err = r.options.Endpoints(combined)
		if err != nil {
			r.options.Logger.Warn("endpoint discovery failed", "error", err)
		}
	}
}
