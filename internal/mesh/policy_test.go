package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/link"
	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/peer"
	"github.com/graphwan/graphwan/internal/routing"
	"github.com/graphwan/graphwan/internal/testutil"
	"github.com/graphwan/graphwan/internal/transport"
)

func policyEndpoint(m *Mesh, id int, kind model.Transport, host string) model.Endpoint {
	source := model.Manual
	if kind == model.TCP || kind == model.UDP {
		source = model.Interface
	}
	return model.Endpoint{ID: testutil.ID(id), Source: source, Transport: kind, URL: fmt.Sprintf("%s://%s", kind, net.JoinHostPort(host, fmt.Sprint(m.Port())))}
}

// Map stable candidate preferences to concrete live sessions, so tests detect
// needless reconnects even when the replacement has the same CandidateID.
func healthyPolicyLinks(m *Mesh) map[string]string {
	result := map[string]string{}
	for _, stat := range m.Report() {
		if stat.Healthy {
			result[stat.CandidateID] = stat.LinkID
		}
	}
	return result
}

func TestPolicyEditsPreserveUnaffectedLinks(t *testing.T) {
	for _, kind := range []model.Transport{model.TCP, model.UDP, model.QUIC, model.WS, model.WSS, model.GRPC} {
		t.Run(string(kind), func(t *testing.T) {
			ctx, meshes, state, delivered := webMeshesOn(t, "")
			for i, m := range meshes {
				m.linkOptions = link.Options{Heartbeat: 10 * time.Millisecond, Timeout: 500 * time.Millisecond, RenewAfter: time.Hour}
				state.Agents[i].Endpoints = []model.Endpoint{policyEndpoint(m, 180+i, kind, "127.0.0.1")}
			}
			edge := &state.Networks[0].Edges[0]
			edge.Transports = []model.Transport{kind}
			applyWebState(t, state, meshes)
			waitLinks := func(count int) {
				t.Helper()
				waitWeb(t, ctx, func() bool {
					return len(healthyPolicyLinks(meshes[0])) == count && len(healthyPolicyLinks(meshes[1])) == count && commonWeb(meshes, "")
				})
			}
			assertRetained := func(want map[string]string) {
				t.Helper()
				for i, m := range meshes {
					got := healthyPolicyLinks(m)
					for candidate, id := range want {
						if got[candidate] != id {
							t.Fatalf("agent %d replaced unaffected candidate %s: %s -> %s", i, candidate, id, got[candidate])
						}
					}
				}
			}
			waitLinks(2)
			original := healthyPolicyLinks(meshes[0])
			state.Agents[1].Endpoints = append(state.Agents[1].Endpoints, policyEndpoint(meshes[1], 182, kind, "127.0.0.2"))
			applyWebState(t, state, meshes)
			waitLinks(3)
			assertRetained(original)
			state.Agents[1].Endpoints = state.Agents[1].Endpoints[:1]
			applyWebState(t, state, meshes)
			waitLinks(2)
			assertRetained(original)

			// Editing a URL preserves its preference identity but revokes only
			// the session bound to the old address, not the reverse direction.
			changed := link.CandidateID(edge.ID, state.Networks[0].Nodes[0].ID, state.Agents[1].Endpoints[0].ID, 4, link.Direct)
			oldID := original[changed]
			delete(original, changed)
			state.Agents[1].Endpoints[0] = policyEndpoint(meshes[1], 181, kind, "127.0.0.2")
			applyWebState(t, state, meshes)
			waitLinks(2)
			assertRetained(original)
			if got := healthyPolicyLinks(meshes[0])[changed]; got == "" || got == oldID {
				t.Fatal("changed endpoint kept its old session")
			}

			original = healthyPolicyLinks(meshes[0])
			state.Agents[1].Endpoints = append(state.Agents[1].Endpoints, policyEndpoint(meshes[1], 183, kind, "::1"))
			edge.Methods.IPv6Direct = true
			applyWebState(t, state, meshes)
			waitLinks(3)
			assertRetained(original)
			ipv6 := link.CandidateID(edge.ID, state.Networks[0].Nodes[0].ID, testutil.ID(183), 6, link.Direct)
			v6ID := healthyPolicyLinks(meshes[0])[ipv6]
			edge.Methods.IPv4Direct = false
			applyWebState(t, state, meshes)
			waitLinks(1)
			assertRetained(map[string]string{ipv6: v6ID})

			// A newly enabled transport becomes a healthy standby; removing
			// the original transport must preserve that established standby.
			other := model.TCP
			if kind == other {
				other = model.UDP
			}
			edge.Transports = []model.Transport{kind, other}
			state.Agents[1].Endpoints = append(state.Agents[1].Endpoints, policyEndpoint(meshes[1], 184, other, "::1"))
			applyWebState(t, state, meshes)
			waitLinks(2)
			assertRetained(map[string]string{ipv6: v6ID})
			standby := link.CandidateID(edge.ID, state.Networks[0].Nodes[0].ID, testutil.ID(184), 6, link.Direct)
			standbyID := healthyPolicyLinks(meshes[0])[standby]
			edge.Transports = []model.Transport{other}
			applyWebState(t, state, meshes)
			waitLinks(1)
			assertRetained(map[string]string{standby: standbyID})
			probe := make([]byte, 128)
			copy(probe, "traffic after policy changes")
			waitWeb(t, ctx, func() bool {
				err := meshes[0].Send(ctx, state.Networks[0].ID, state.Networks[0].Nodes[1].ID, probe)
				if err != nil && !errors.Is(err, link.ErrUnavailable) {
					t.Fatal(err)
				}
				select {
				case got := <-delivered:
					if string(got) != string(probe) {
						t.Fatal("corrupt forwarded packet")
					}
					return true
				default:
					return false
				}
			})
		})
	}
}

func TestPolicyRemovalCancelsInflightHandshake(t *testing.T) {
	ctx, meshes, state, _ := webMeshes(t)
	state.Networks[0].Edges[0].Transports = []model.Transport{model.TCP}
	for i, m := range meshes {
		m.linkOptions = link.Options{Heartbeat: 10 * time.Millisecond, Timeout: 500 * time.Millisecond, RenewAfter: time.Hour}
		state.Agents[i].Endpoints = []model.Endpoint{policyEndpoint(m, 180+i, model.TCP, "127.0.0.1")}
	}
	applyWebState(t, state, meshes)
	waitWeb(t, ctx, func() bool {
		return len(healthyPolicyLinks(meshes[0])) == 2 && len(healthyPolicyLinks(meshes[1])) == 2 && commonWeb(meshes, "")
	})
	original := healthyPolicyLinks(meshes[0])
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	state.Agents[1].Endpoints = append(state.Agents[1].Endpoints, model.Endpoint{ID: testutil.ID(185), Source: model.Manual, Transport: model.TCP, URL: "tcp://" + listener.Addr().String()})
	applyWebState(t, state, meshes)
	var socket net.Conn
	select {
	case socket = <-accepted:
	case <-ctx.Done():
		t.Fatal("handshake never started")
	}
	defer socket.Close()
	state.Agents[1].Endpoints = state.Agents[1].Endpoints[:1]
	applyWebState(t, state, meshes)
	socket.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := io.Copy(io.Discard, socket); err != nil {
		t.Fatalf("revoked handshake was not promptly closed: %v", err)
	}
	for _, m := range meshes {
		for candidate, id := range original {
			if healthyPolicyLinks(m)[candidate] != id {
				t.Fatal("canceling pending handshake replaced an established session")
			}
		}
	}
	waitWeb(t, ctx, func() bool { return len(meshes[0].slots) == 0 })
}

func TestPolicyChangeRejectsStaleIntroduction(t *testing.T) {
	for _, change := range []string{"replace", "remove", "disable-method", "disable-transport"} {
		t.Run(change, func(t *testing.T) {
			ctx, meshes, state, _ := webMeshesOn(t, "")
			endpoint := policyEndpoint(meshes[1], 180, model.TCP, "127.0.0.1")
			state.Agents[0].Endpoints = nil
			state.Agents[1].Endpoints = []model.Endpoint{endpoint}
			applyResponder := func() {
				t.Helper()
				snapshot, err := routing.Compile(*state, state.Agents[1].ID)
				if err != nil {
					t.Fatal(err)
				}
				if err := meshes[1].Apply(snapshot); err != nil {
					t.Fatal(err)
				}
			}
			applyResponder()
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
			switch change {
			case "replace":
				state.Agents[1].Endpoints[0] = policyEndpoint(meshes[1], 180, model.TCP, "127.0.0.2")
			case "remove":
				state.Agents[1].Endpoints = nil
			case "disable-method":
				state.Networks[0].Edges[0].Methods = model.ConnectionMethods{IPv6Direct: true}
			case "disable-transport":
				state.Networks[0].Edges[0].Transports = []model.Transport{model.UDP}
			}
			applyResponder()
			intro := introduction{Candidate: link.CandidateID(cfg.peer.Edge.ID, cfg.self, endpoint.ID, 4, link.Direct), Endpoint: endpointFingerprint(endpoint)}
			raw, _ := json.Marshal(intro)
			if err := channel.Send(ctx, append([]byte{0}, raw...)); err != nil {
				t.Fatal(err)
			}
			readCtx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			if _, err := channel.Receive(readCtx); err == nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("stale introduction was not rejected: %v", err)
			}
		})
	}
}
