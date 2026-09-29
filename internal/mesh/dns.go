package mesh

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/link"
)

type dnsEntry struct {
	addresses []netip.Addr
	next      time.Time
	inflight  bool
}

// A Mesh shares lookups between its Networks and Edges. Resolution never blocks
// link health/selection; both DNS concurrency and lookup duration are bounded.
// Entries are pruned when configuration no longer references their hostname.
type endpointDNS struct {
	mu      sync.Mutex
	entries map[string]*dnsEntry
	slots   chan struct{}
	wg      sync.WaitGroup
	lookup  func(context.Context, string, string) ([]netip.Addr, error)
	refresh time.Duration
}

func newEndpointDNS() *endpointDNS {
	return &endpointDNS{entries: map[string]*dnsEntry{}, slots: make(chan struct{}, 4), lookup: net.DefaultResolver.LookupNetIP, refresh: 30 * time.Second}
}

func (d *endpointDNS) configure(hosts map[string]bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for host := range d.entries {
		if !hosts[host] {
			delete(d.entries, host)
		}
	}
	for host := range hosts {
		if d.entries[host] == nil {
			d.entries[host] = &dnsEntry{}
		}
	}
}

func (d *endpointDNS) addresses(ctx context.Context, host string) []netip.Addr {
	d.mu.Lock()
	defer d.mu.Unlock()
	entry := d.entries[host]
	if entry == nil || ctx.Err() != nil {
		return nil
	}
	if !entry.inflight && !time.Now().Before(entry.next) {
		select {
		case d.slots <- struct{}{}:
			entry.inflight = true
			d.wg.Add(1)
			go func() {
				defer d.wg.Done()
				defer func() { <-d.slots }()
				lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				addresses, err := d.lookup(lookupCtx, "ip", host)
				cancel()
				for i := range addresses {
					addresses[i] = addresses[i].Unmap()
				}
				slices.SortFunc(addresses, netip.Addr.Compare)
				addresses = slices.Compact(addresses)
				addresses = slices.DeleteFunc(addresses, func(a netip.Addr) bool {
					return !a.IsValid() || a.Zone() != "" || a.IsUnspecified() || a.IsMulticast()
				})
				d.mu.Lock()
				defer d.mu.Unlock()
				entry.inflight = false
				entry.next = time.Now().Add(d.refresh)
				var dnsError *net.DNSError
				if err == nil || (errors.As(err, &dnsError) && dnsError.IsNotFound) {
					entry.addresses = addresses
				}
			}()
		default:
		}
	}
	// Published slices are immutable after the lookup completes.
	return entry.addresses
}

func endpointHostname(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if _, err := netip.ParseAddr(u.Hostname()); err == nil {
		return ""
	}
	return u.Hostname()
}

func (g *group) resolveCandidates(bases []link.Candidate) []link.Candidate {
	result := make([]link.Candidate, 0, len(bases))
	for _, base := range bases {
		host := endpointHostname(base.Endpoint.URL)
		if host == "" {
			result = append(result, base)
			continue
		}
		for _, address := range g.mesh.dns.addresses(g.mesh.ctx, host) {
			if candidate, err := link.ResolveCandidate(base, address); err == nil {
				result = append(result, candidate)
			}
		}
	}
	return result
}
