// Package geoip locates advertised public Agent endpoints using a local MMDB.
package geoip

import (
	"cmp"
	"net/netip"
	"net/url"
	"slices"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

var excluded = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("3fff::/20"),
}
var publicIPv6 = netip.MustParsePrefix("2000::/3")

func publicIP(ip netip.Addr) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.Zone() != "" || ip.Is6() && !publicIPv6.Contains(ip) {
		return false
	}
	for _, prefix := range excluded {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

// Prefer STUN observations, then physical interface addresses, then manual IPs.
// IPv4 wins within each source; ties are stable. DNS-only manual entries may
// refer to proxies/CDNs, so they are not guessed to be the Agent's location.
func publicEndpoints(endpoints []model.Endpoint, now time.Time) []netip.Addr {
	type candidate struct {
		ip   netip.Addr
		rank int
	}
	candidates := make([]candidate, 0, len(endpoints))
	for _, ep := range endpoints {
		if ep.Source == model.Observed && !now.Before(ep.ExpiresAt) || !ep.ExpiresAt.IsZero() && !now.Before(ep.ExpiresAt) {
			continue
		}
		u, err := url.Parse(ep.URL)
		if err != nil {
			continue
		}
		ip, err := netip.ParseAddr(u.Hostname())
		if err != nil {
			continue
		}
		ip = ip.Unmap()
		if !publicIP(ip) {
			continue
		}
		rank := 0
		switch ep.Source {
		case model.Observed:
		case model.Interface:
			rank = 2
		case model.Manual:
			rank = 4
		default:
			continue
		}
		if ip.Is6() {
			rank++
		}
		candidates = append(candidates, candidate{ip, rank})
	}
	slices.SortFunc(candidates, func(a, b candidate) int {
		if order := cmp.Compare(a.rank, b.rank); order != 0 {
			return order
		}
		return a.ip.Compare(b.ip)
	})
	seen := map[netip.Addr]bool{}
	result := make([]netip.Addr, 0, len(candidates))
	for _, c := range candidates {
		if !seen[c.ip] {
			result = append(result, c.ip)
			seen[c.ip] = true
		}
	}
	return result
}
