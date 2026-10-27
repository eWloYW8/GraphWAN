package tunnel

import (
	"errors"
	"fmt"
	"maps"
	"net/netip"
)

type AutoRoute struct {
	Device Device
	Prefix netip.Prefix
}

type autoRouteKey struct {
	index  int
	prefix netip.Prefix
}

// AutoRouteManager is serialized by the Agent's configuration lock. It owns
// only routes it added, leaves pre-existing routes alone, and rolls back failed
// updates. TUN destruction also removes these nonpersistent interface routes.
type AutoRouteManager struct {
	owned map[autoRouteKey]subnetRoutes
}

func (m *AutoRouteManager) Apply(entries []AutoRoute) error {
	next := map[autoRouteKey]subnetRoutes{}
	prefixes := map[netip.Prefix]int{}
	for _, entry := range entries {
		device, ok := entry.Device.(interface{ routes() subnetRoutes })
		if !ok {
			return fmt.Errorf("TUN %s does not support automatic subnet routes", entry.Device.Name())
		}
		p := entry.Prefix
		if !p.IsValid() || p != p.Masked() || p.Bits() == 0 || p.Overlaps(entry.Device.Configuration().Address) {
			return fmt.Errorf("invalid automatic subnet route %s", p)
		}
		routes := device.routes()
		if index, exists := prefixes[p]; exists && index != routes.index {
			return fmt.Errorf("automatic subnet route %s is selected on multiple interfaces", p)
		}
		prefixes[p] = routes.index
		next[autoRouteKey{routes.index, p}] = routes
	}
	if m.owned == nil {
		m.owned = map[autoRouteKey]subnetRoutes{}
	}
	before := maps.Clone(m.owned)
	if err := m.replace(next); err != nil {
		return errors.Join(err, m.replace(before))
	}
	return nil
}

func (m *AutoRouteManager) replace(next map[autoRouteKey]subnetRoutes) error {
	for key, routes := range m.owned {
		if _, keep := next[key]; keep {
			continue
		}
		if err := routes.discardRoute(key.prefix); err != nil {
			return err
		}
		delete(m.owned, key)
	}
	for key, routes := range next {
		if _, ours := m.owned[key]; !ours {
			present, err := routes.owned(key.prefix, true)
			if err != nil {
				return err
			}
			if present {
				continue // Do not adopt or later remove a pre-existing route.
			}
		}
		// Record before mutation so rollback can inspect a partially added route.
		m.owned[key] = routes
		if err := routes.ensureRoute(key.prefix); err != nil {
			return err
		}
	}
	return nil
}
