package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"reflect"
	"sort"
	"strconv"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/discovery"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/store"
	"github.com/eWloYW8/GraphWAN/internal/transport"
)

// InterfaceEndpoints intentionally includes VPN interfaces: these addresses can
// be valid controller entrances even when excluded from overlay peer discovery.
func InterfaceEndpoints(identity Identity, addr net.Addr) ([]model.ServerEndpoint, error) {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return nil, err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return nil, err
	}
	hosts := []string{}
	if !ip.IsUnspecified() {
		hosts = append(hosts, ip.String())
	} else {
		interfaces, err := net.Interfaces()
		if err != nil {
			return nil, err
		}
		for _, iface := range interfaces {
			if iface.Flags&net.FlagUp == 0 {
				continue
			}
			addresses, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, address := range addresses {
				prefix, err := netip.ParsePrefix(address.String())
				if err != nil {
					continue
				}
				a := prefix.Addr().Unmap()
				if a.IsUnspecified() || a.IsMulticast() || a.IsLinkLocalUnicast() {
					continue
				}
				hosts = append(hosts, a.String())
			}
		}
	}
	sort.Slice(hosts, func(i, j int) bool {
		a, _ := netip.ParseAddr(hosts[i])
		b, _ := netip.ParseAddr(hosts[j])
		if a.IsLoopback() != b.IsLoopback() {
			return !a.IsLoopback()
		}
		return hosts[i] < hosts[j]
	})
	out := []model.ServerEndpoint{}
	seen := map[string]bool{}
	for _, host := range hosts {
		u := "tcp://" + net.JoinHostPort(host, port)
		if seen[u] {
			continue
		}
		seen[u] = true
		sum := sha256.Sum256([]byte(string(identity.ID) + "/interface/" + u))
		out = append(out, model.ServerEndpoint{ID: model.ID(hex.EncodeToString(sum[:16])), URL: u, Transport: "tcp", Source: model.Interface})
		if len(out) >= model.MaxEndpoints-8 {
			break
		}
	}
	return out, nil
}
func (r *Runtime) StartDiscovery(listener *transport.TCP) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		tick := time.NewTicker(20 * time.Second)
		defer tick.Stop()
		for {
			endpoints, err := InterfaceEndpoints(r.Identity, listener.Addr())
			if err == nil {
				state, e := r.DB.Read()
				if e == nil {
					var stun []string
					for _, p := range state.Servers {
						if p.ID == r.Identity.ID {
							stun = p.STUNServers
						}
					}
					ctx, cancel := context.WithTimeout(r.ctx, 4*time.Second)
					observed, _ := discovery.Observe(ctx, r.Identity.Public(), stun, func(ctx context.Context, kind model.Transport, address netip.AddrPort) (netip.AddrPort, error) {
						if kind != model.TCP {
							return netip.AddrPort{}, errors.New("server STUN must use TCP")
						}
						return listener.STUNBinding(ctx, address)
					})
					cancel()
					urls := map[string]bool{}
					for _, e := range endpoints {
						urls[e.URL] = true
					}
					for _, e := range observed {
						if !urls[e.URL] {
							endpoints = append(endpoints, model.ServerEndpoint{ID: e.ID, URL: e.URL, Transport: "tcp", Source: model.Observed, ExpiresAt: e.ExpiresAt})
							urls[e.URL] = true
						}
					}
					raw, _ := json.Marshal(endpoints)
					r.DB.PutLocal("discovered-endpoints", raw)
					r.PublishEndpoints(endpoints)
				}
			}
			select {
			case <-r.ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
}
func (r *Runtime) PublishEndpoints(endpoints []model.ServerEndpoint) error {
	for range 8 {
		state, err := r.DB.Read()
		if err != nil {
			return err
		}
		_, err = r.DB.Update(state.Revision, func(state *model.State) error {
			for i := range state.Servers {
				p := &state.Servers[i]
				if p.ID != r.Identity.ID {
					continue
				}
				combined := []model.ServerEndpoint{}
				urls := map[string]bool{}
				for _, e := range p.Endpoints {
					if e.Source == model.Manual {
						combined = append(combined, e)
						urls[e.URL] = true
					}
				}
				for _, e := range endpoints {
					if !urls[e.URL] && len(combined) < model.MaxEndpoints {
						combined = append(combined, e)
						urls[e.URL] = true
					}
				}
				if reflect.DeepEqual(combined, p.Endpoints) {
					return errEndpointsUnchanged
				}
				p.Endpoints = combined
				return nil
			}
			return errors.New("server absent from cluster")
		})
		if errors.Is(err, errEndpointsUnchanged) {
			return nil
		}
		if errors.Is(err, store.ErrConflict) {
			continue
		}
		return err
	}
	return store.ErrConflict
}

var errEndpointsUnchanged = errors.New("endpoints unchanged")

func ValidateServerSTUN(servers []string) error {
	if err := model.ValidateSTUNServers(servers); err != nil {
		return err
	}
	for _, s := range servers {
		kind, address, err := model.ParseSTUNServer(s)
		if err != nil {
			return err
		}
		_, port, _ := net.SplitHostPort(address)
		_, err = strconv.ParseUint(port, 10, 16)
		if kind != model.TCP || err != nil {
			return errors.New("server discovery requires TCP STUN")
		}
	}
	return nil
}
