package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/link"
	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/peer"
	"github.com/graphwan/graphwan/internal/routing"
	"github.com/graphwan/graphwan/internal/testutil"
	"github.com/graphwan/graphwan/internal/transport"
)

func TestDNSCacheRefreshAndPruning(t *testing.T) {
	d := newEndpointDNS()
	answers := []netip.Addr{netip.MustParseAddr("::ffff:127.0.0.2"), netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("127.0.0.1"), netip.IPv4Unspecified(), netip.MustParseAddr("ff02::1")}
	var failure error
	d.lookup = func(context.Context, string, string) ([]netip.Addr, error) { return slices.Clone(answers), failure }
	d.configure(map[string]bool{"peer.test": true})
	refresh := func() []netip.Addr {
		d.mu.Lock()
		d.entries["peer.test"].next = time.Time{}
		d.mu.Unlock()
		d.addresses(t.Context(), "peer.test")
		d.wg.Wait()
		return d.addresses(t.Context(), "peer.test")
	}
	want := []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("127.0.0.2")}
	if got := refresh(); !slices.Equal(got, want) {
		t.Fatalf("normalized answers: %v", got)
	}
	failure = errors.New("temporary DNS outage")
	answers = nil
	if got := refresh(); !slices.Equal(got, want) {
		t.Fatal("transient lookup failure discarded cached targets")
	}
	failure = &net.DNSError{IsNotFound: true}
	if got := refresh(); len(got) != 0 {
		t.Fatal("authoritative absence retained dial targets")
	}
	d.configure(nil)
	if got := d.addresses(t.Context(), "peer.test"); len(got) != 0 || len(d.entries) != 0 {
		t.Fatal("removed endpoint kept cache")
	}
}

func TestDNSConcurrencyCancellationAndLateResults(t *testing.T) {
	d := newEndpointDNS()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls atomic.Int32
	d.lookup = func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
		calls.Add(1)
		<-ctx.Done()
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	hosts := map[string]bool{}
	for i := range 20 {
		hosts[fmt.Sprintf("%d.test", i)] = true
	}
	d.configure(hosts)
	for range 10 {
		for host := range hosts {
			d.addresses(ctx, host)
		}
	}
	if len(d.slots) != 4 {
		t.Fatalf("DNS concurrency = %d", len(d.slots))
	}
	d.configure(nil)
	cancel()
	d.wg.Wait()
	if calls.Load() != 4 || len(d.entries) != 0 || len(d.slots) != 0 {
		t.Fatal("lookup leak or removed hostname resurrected")
	}
	for host := range hosts {
		d.addresses(ctx, host)
	}
	if calls.Load() != 4 {
		t.Fatal("lookup started after cancellation")
	}
}

func TestDNSAllAddressesRetainedAcrossTransports(t *testing.T) {
	for _, kind := range []model.Transport{model.UDP, model.TCP, model.QUIC, model.WS, model.WSS, model.GRPC} {
		t.Run(string(kind), func(t *testing.T) {
			ctx, meshes, state, delivered := webMeshesOn(t, "")
			var mu sync.Mutex
			answers := []netip.Addr{netip.MustParseAddr("127.0.0.2"), netip.MustParseAddr("127.0.0.1"), netip.IPv6Loopback(), netip.MustParseAddr("127.0.0.2")}
			for _, m := range meshes {
				m.linkOptions = link.Options{Heartbeat: 10 * time.Millisecond, Timeout: 500 * time.Millisecond, RenewAfter: time.Second}
				m.dns.refresh = 30 * time.Millisecond
				m.dns.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
					mu.Lock()
					defer mu.Unlock()
					return slices.Clone(answers), nil
				}
			}
			endpoint := model.Endpoint{ID: testutil.ID(180), Source: model.Manual, Transport: kind, URL: fmt.Sprintf("%s://peer.example.test:%d", kind, meshes[1].Port())}
			if kind == model.WS || kind == model.WSS || kind == model.GRPC {
				endpoint.URL += "/custom/overlay"
			}
			state.Agents[0].Endpoints = nil // Only the hostname's dialing direction.
			state.Agents[1].Endpoints = []model.Endpoint{endpoint}
			edge := &state.Networks[0].Edges[0]
			edge.Transports = []model.Transport{kind}
			base := link.Candidate{ID: link.CandidateID(edge.ID, state.Networks[0].Nodes[0].ID, endpoint.ID, 4, link.Direct), Endpoint: endpoint, Family: 4, Method: link.Direct}
			first, _ := link.ResolveCandidate(base, netip.MustParseAddr("127.0.0.1"))
			second, _ := link.ResolveCandidate(base, netip.MustParseAddr("127.0.0.2"))
			edge.PreferredCandidate = second.ID
			applyWebState(t, state, meshes)
			healthy := func(m *Mesh) map[string]bool {
				result := map[string]bool{}
				for _, stat := range m.Report() {
					if stat.Healthy {
						result[stat.CandidateID] = true
					}
				}
				return result
			}
			waitWeb(t, ctx, func() bool {
				for _, m := range meshes {
					h := healthy(m)
					if len(h) != 2 || !h[first.ID] || !h[second.ID] {
						return false
					}
				}
				return commonWeb(meshes, second.ID)
			})
			old := map[string]bool{}
			for _, stat := range meshes[0].Report() {
				old[stat.LinkID] = true
			}
			mu.Lock()
			answers = []netip.Addr{netip.MustParseAddr("127.0.0.3"), netip.MustParseAddr("127.0.0.1")}
			mu.Unlock()
			third, _ := link.ResolveCandidate(base, netip.MustParseAddr("127.0.0.3"))
			waitWeb(t, ctx, func() bool {
				for _, m := range meshes {
					h := healthy(m)
					if len(h) != 3 || !h[first.ID] || !h[second.ID] || !h[third.ID] {
						return false
					}
				}
				for _, stat := range meshes[0].Report() {
					if stat.CandidateID == second.ID && stat.Healthy && !old[stat.LinkID] {
						return commonWeb(meshes, second.ID)
					}
				}
				return false
			})
			for i, m := range meshes {
				frame := make([]byte, 128)
				copy(frame, "DNS multiple addresses")
				waitWeb(t, ctx, func() bool {
					err := m.Send(ctx, state.Networks[0].ID, state.Networks[0].Nodes[1-i].ID, frame)
					if err != nil && !errors.Is(err, link.ErrUnavailable) {
						t.Fatal(err)
					}
					return err == nil
				})
				select {
				case got := <-delivered:
					if string(got) != string(frame) {
						t.Fatal("corrupt data")
					}
				case <-ctx.Done():
					t.Fatal("DNS path data stalled")
				}
			}
			state.Agents[1].Endpoints = nil
			applyWebState(t, state, meshes)
			waitWeb(t, ctx, func() bool { return len(meshes[0].Report()) == 0 && len(meshes[1].Report()) == 0 })
		})
	}
}

func TestDNSUDPUnreachableFirstAnswer(t *testing.T) {
	ctx, meshes, state, _ := webMeshes(t)
	meshes[0].dns.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.2"), netip.MustParseAddr("127.0.0.1")}, nil
	}
	state.Networks[0].Edges[0].Transports = []model.Transport{model.UDP}
	state.Agents[0].Endpoints = nil
	state.Agents[1].Endpoints = []model.Endpoint{{ID: testutil.ID(180), Source: model.Manual, Transport: model.UDP, URL: fmt.Sprintf("udp://peer.example.test:%d", meshes[1].Port())}}
	applyWebState(t, state, meshes)
	waitWeb(t, ctx, func() bool { return commonWeb(meshes, "") })
	for _, stat := range meshes[0].Report() {
		if stat.Healthy && stat.Remote != fmt.Sprintf("127.0.0.1:%d", meshes[1].Port()) {
			t.Fatal("unexpected live target")
		}
	}
}

func TestDNSTargetIntroductionCannotBypassCandidatePolicy(t *testing.T) {
	for _, variant := range []string{"wrong-hash", "wrong-family", "literal-override"} {
		t.Run(variant, func(t *testing.T) {
			ctx, meshes, state, _ := webMeshes(t)
			endpoint := model.Endpoint{ID: testutil.ID(180), Source: model.Manual, Transport: model.TCP, URL: fmt.Sprintf("tcp://peer.example.test:%d", meshes[1].Port())}
			base := link.Candidate{ID: link.CandidateID(state.Networks[0].Edges[0].ID, state.Networks[0].Nodes[0].ID, endpoint.ID, 4, link.Direct), Endpoint: endpoint, Family: 4, Method: link.Direct}
			resolved, _ := link.ResolveCandidate(base, netip.MustParseAddr("127.0.0.1"))
			intro := introduction{Candidate: resolved.ID, Target: resolved.Target.String()}
			switch variant {
			case "wrong-hash":
				intro.Candidate = base.ID
			case "wrong-family":
				base.Family = 6
				base.ID = link.CandidateID(state.Networks[0].Edges[0].ID, state.Networks[0].Nodes[0].ID, endpoint.ID, 6, link.Direct)
				resolved, _ = link.ResolveCandidate(base, netip.IPv6Loopback())
				intro = introduction{Candidate: resolved.ID, Target: resolved.Target.String()}
			case "literal-override":
				endpoint.URL = fmt.Sprintf("tcp://127.0.0.1:%d", meshes[1].Port())
			}
			state.Agents[0].Endpoints = nil
			state.Agents[1].Endpoints = []model.Endpoint{endpoint}
			responder, err := routing.Compile(*state, state.Agents[1].ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := meshes[1].Apply(responder); err != nil {
				t.Fatal(err)
			}
			snapshot, err := routing.Compile(*state, state.Agents[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			network := snapshot.Networks[0]
			cfg := &policy{network: network.ID, self: network.Self.ID, cipher: network.Cipher, peer: network.Peers[0], endpoints: snapshot.Endpoints}
			socket, err := (&net.Dialer{}).DialContext(ctx, "tcp4", fmt.Sprintf("127.0.0.1:%d", meshes[1].Port()))
			if err != nil {
				t.Fatal(err)
			}
			channel, err := peer.Dial(ctx, transport.NewStream(socket), meshes[0].secure(cfg, model.TCP))
			if err != nil {
				t.Fatal(err)
			}
			defer channel.Close()
			raw, _ := json.Marshal(intro)
			if err := channel.Send(ctx, append([]byte{0}, raw...)); err != nil {
				t.Fatal(err)
			}
			readCtx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			if _, err := channel.Receive(readCtx); err == nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("invalid target introduction not rejected: %v", err)
			}
		})
	}
}

func TestDNSIPv6AndPunchMethods(t *testing.T) {
	for _, kind := range []model.Transport{model.UDP, model.TCP, model.QUIC, model.WS, model.WSS, model.GRPC} {
		for _, method := range []link.Method{link.Direct, link.Punch} {
			if method == link.Punch && kind != model.UDP && kind != model.TCP {
				continue
			}
			t.Run(string(kind)+"/"+string(method), func(t *testing.T) {
				ctx, meshes, state, _ := webMeshesOn(t, "")
				meshes[0].dns.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
					return []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.IPv6Loopback()}, nil
				}
				edge := &state.Networks[0].Edges[0]
				edge.Transports = []model.Transport{kind}
				edge.Methods = model.ConnectionMethods{IPv6Direct: true}
				family, target := 6, netip.IPv6Loopback()
				if method == link.Punch {
					edge.Methods = model.ConnectionMethods{HolePunch: true}
					family, target = 4, netip.MustParseAddr("127.0.0.1")
				}
				endpoint := model.Endpoint{ID: testutil.ID(180), Source: model.Manual, Transport: kind, URL: fmt.Sprintf("%s://peer.example.test:%d", kind, meshes[1].Port())}
				state.Agents[0].Endpoints = nil
				state.Agents[1].Endpoints = []model.Endpoint{endpoint}
				base := link.Candidate{ID: link.CandidateID(edge.ID, state.Networks[0].Nodes[0].ID, endpoint.ID, family, method), Endpoint: endpoint, Family: family, Method: method}
				resolved, _ := link.ResolveCandidate(base, target)
				edge.PreferredCandidate = resolved.ID
				applyWebState(t, state, meshes)
				waitWeb(t, ctx, func() bool { return commonWeb(meshes, resolved.ID) })
				if method == link.Direct {
					for _, stat := range meshes[0].Report() {
						if stat.Healthy && stat.CandidateID != resolved.ID {
							t.Fatal("disabled IPv4 direct address connected")
						}
					}
				}
			})
		}
	}
}
