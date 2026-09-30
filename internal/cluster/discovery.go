package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"reflect"
	"strconv"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/discovery"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/store"
	"github.com/eWloYW8/GraphWAN/internal/transport"
)

// InterfaceEndpoints shares the Agent's physical-interface and container
// filtering. An explicitly bound loopback listener remains usable for local
// development; wildcard listeners never advertise loopback or virtual NICs.
func InterfaceEndpoints(identity Identity, addr net.Addr) ([]model.ServerEndpoint, error) {
	host, portText, err := net.SplitHostPort(addr.String())
	if err != nil {
		return nil, err
	}
	bound, err := netip.ParseAddr(host)
	if err != nil {
		return nil, err
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return nil, errors.New("server discovery requires a nonzero listen port")
	}
	if bound.IsLoopback() {
		endpointURL := "tcp://" + net.JoinHostPort(bound.String(), portText)
		sum := sha256.Sum256([]byte(string(identity.ID) + "/interface/" + endpointURL))
		return []model.ServerEndpoint{{ID: model.ID(hex.EncodeToString(sum[:16])), URL: endpointURL, Transport: "tcp", Source: model.Interface}}, nil
	}
	endpoints, err := discovery.InterfacesWithOptions(identity.Public(), uint16(port), discovery.InterfaceOptions{ExcludeContainerIPs: true})
	if err != nil {
		return nil, err
	}
	out := []model.ServerEndpoint{}
	for _, endpoint := range endpoints {
		if endpoint.Transport != model.TCP {
			continue
		}
		if !bound.IsUnspecified() {
			parsed, err := url.Parse(endpoint.URL)
			if err != nil {
				return nil, err
			}
			address, err := netip.ParseAddr(parsed.Hostname())
			if err != nil {
				return nil, err
			}
			if address != bound.Unmap() {
				continue
			}
		}
		out = append(out, model.ServerEndpoint{ID: endpoint.ID, URL: endpoint.URL, Transport: "tcp", Source: model.Interface})
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
