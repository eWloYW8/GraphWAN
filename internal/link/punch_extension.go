package link

import (
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"net/url"
	"slices"
	"strconv"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

// Bound the search to neighboring ports of fresh, observed IPv4 mappings.
// No hostname, interface/manual endpoint, or other destination IP is scanned.
const MaxExtensionCandidates = 128

var extensionExcluded = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
}

func extensionAddress(ip netip.Addr) bool {
	if !ip.Is4() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range extensionExcluded {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func (c Candidate) Identity(edge, initiator model.ID) string {
	id := CandidateID(edge, initiator, c.Endpoint.ID, c.Family, c.Method)
	if c.Port != 0 {
		sum := sha256.Sum256([]byte(id + "/port/" + strconv.Itoa(int(c.Port))))
		return hex.EncodeToString(sum[:16])
	}
	return id
}

func extensionCandidates(bases []Candidate, enabled bool) []Candidate {
	if !enabled {
		return nil
	}
	type observation struct {
		base Candidate
		ip   netip.Addr
		port int
	}
	var observations []observation
	for _, c := range bases {
		if c.Method != Punch || c.Endpoint.Source != model.Observed || c.Family != 4 {
			continue
		}
		u, _ := url.Parse(c.Endpoint.URL)
		ip, err := netip.ParseAddr(u.Hostname())
		if err != nil || !extensionAddress(ip) {
			continue
		}
		port, err := strconv.Atoi(u.Port())
		if err != nil {
			continue
		}
		observations = append(observations, observation{c, ip, port})
	}
	// Per-observation lists are interleaved so one address or transport cannot
	// consume the entire bounded search budget.
	plans := make([][]int, len(observations))
	for i, o := range observations {
		seen := map[int]bool{o.port: true}
		add := func(port int) {
			if port < 1024 || port > 65535 || seen[port] {
				return
			}
			seen[port] = true
			plans[i] = append(plans[i], port)
		}
		// Multiple STUN destinations expose candidate allocator strides. We have
		// no observation ordering guarantee, so extrapolate in both directions.
		var strides []int
		for _, other := range observations {
			if other.ip != o.ip || other.base.Endpoint.Transport != o.base.Endpoint.Transport {
				continue
			}
			delta := other.port - o.port
			if delta < 0 {
				delta = -delta
			}
			if delta > 0 && delta <= 256 {
				strides = append(strides, delta)
			}
		}
		slices.Sort(strides)
		strides = slices.Compact(strides)
		if len(strides) > 4 {
			strides = strides[:4]
		}
		for n := 1; n <= 4; n++ {
			for _, stride := range strides {
				add(o.port + n*stride)
				add(o.port - n*stride)
			}
		}
		// Also covers sequential allocation when only one STUN server is reachable.
		for offset := 1; offset <= 32; offset++ {
			add(o.port + offset)
			add(o.port - offset)
		}
	}
	var result []Candidate
	seen := map[string]bool{}
	for round := 0; round < 96 && len(result) < MaxExtensionCandidates; round++ {
		for i, o := range observations {
			if round >= len(plans[i]) {
				continue
			}
			port := plans[i][round]
			key := string(o.base.Endpoint.Transport) + "/" + netip.AddrPortFrom(o.ip, uint16(port)).String()
			if seen[key] {
				continue
			}
			seen[key] = true
			c := o.base
			c.Port = uint16(port)
			sum := sha256.Sum256([]byte(c.ID + "/port/" + strconv.Itoa(port)))
			c.ID = hex.EncodeToString(sum[:16])
			c.Priority = 200 + len(result)
			result = append(result, c)
			if len(result) == MaxExtensionCandidates {
				break
			}
		}
	}
	return result
}
