package discovery

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/rand/v2"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/graphwan/graphwan/internal/model"
)

const ObservedLifetime = 2 * time.Minute

type Binding func(context.Context, netip.AddrPort) (netip.AddrPort, error)

// Observe resolves both address families and probes with four bounded workers.
// The caller supplies a binding operation on its live data socket. The returned
// addresses are observations, not proof of public reachability or peer identity.
func Observe(ctx context.Context, identity []byte, servers []string, bind Binding) ([]model.Endpoint, error) {
	if err := model.ValidateSTUNServers(servers); err != nil {
		return nil, err
	}
	var workers sync.WaitGroup
	var mu sync.Mutex
	errs := []error{}
	results := map[netip.AddrPort]bool{}
	addError := func(err error) { mu.Lock(); errs = append(errs, err); mu.Unlock() }
	// One worker per configured service prevents a slow service or a large DNS
	// answer from monopolizing all probes. Each transaction gets at most a second
	// (including its first retransmission), within the caller's round deadline.
	for _, server := range servers {
		workers.Go(func() {
			host, port, _ := net.SplitHostPort(server)
			n, _ := strconv.ParseUint(port, 10, 16)
			addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				addError(err)
				return
			}
			for _, address := range probeOrder(addresses) {
				if ctx.Err() != nil {
					return
				}
				attempt, cancel := context.WithTimeout(ctx, time.Second)
				mapped, err := bind(attempt, netip.AddrPortFrom(address.Unmap(), uint16(n)))
				cancel()
				if err != nil {
					addError(err)
					continue
				}
				mu.Lock()
				results[mapped] = true
				mu.Unlock()
			}
		})
	}
	workers.Wait()
	endpoints := []model.Endpoint{}
	expires := time.Now().Add(ObservedLifetime)
	for address := range results {
		endpointURL := "udp://" + address.String()
		sum := sha256.Sum256(append(append([]byte{}, identity...), []byte("/observed/"+endpointURL)...))
		endpoints = append(endpoints, model.Endpoint{ID: model.ID(hex.EncodeToString(sum[:16])), Source: model.Observed, Transport: model.UDP, URL: endpointURL, ExpiresAt: expires})
	}
	slices.SortFunc(endpoints, func(a, b model.Endpoint) int { return cmp.Compare(a.ID, b.ID) })
	return endpoints, errors.Join(errs...)
}

// Alternate families, rotating addresses between rounds so failures cannot
// permanently hide later DNS answers. Resolve memory is owned by net.Resolver.
func probeOrder(addresses []netip.Addr) []netip.Addr {
	v4, v6 := []netip.Addr{}, []netip.Addr{}
	for _, ip := range addresses {
		if ip.Unmap().Is4() {
			v4 = append(v4, ip)
		} else {
			v6 = append(v6, ip)
		}
	}
	for _, family := range [][]netip.Addr{v4, v6} {
		rand.Shuffle(len(family), func(i, j int) { family[i], family[j] = family[j], family[i] })
	}
	result := make([]netip.Addr, 0, min(32, len(addresses)))
	for i := 0; len(result) < min(32, len(addresses)); i++ {
		if i < len(v4) {
			result = append(result, v4[i])
		}
		if i < len(v6) && len(result) < 32 {
			result = append(result, v6[i])
		}
	}
	return result
}
