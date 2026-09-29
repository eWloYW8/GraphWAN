package mesh

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/link"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
	"github.com/eWloYW8/GraphWAN/internal/transport"
	"golang.org/x/net/http2"
)

func addGRPCEndpoints(meshes []*Mesh, state *model.State, path string) {
	for i, m := range meshes {
		state.Agents[i].Endpoints = append(state.Agents[i].Endpoints, model.Endpoint{ID: testutil.ID(170 + i), Transport: model.GRPC, Source: model.Manual, URL: fmt.Sprintf("grpc://127.0.0.1:%d%s", m.Port(), path)})
	}
	state.Networks[0].Edges[0].Transports = append(state.Networks[0].Edges[0].Transports, model.GRPC)
}

func TestGRPCDefaultEndpointRemoval(t *testing.T) {
	ctx, meshes, state, _ := webMeshes(t)
	addGRPCEndpoints(meshes, state, "")
	for i := range state.Agents {
		state.Agents[i].Endpoints = state.Agents[i].Endpoints[3:]
	}
	state.Networks[0].Edges[0].Transports = []model.Transport{model.GRPC}
	applyWebState(t, state, meshes)
	waitWeb(t, ctx, func() bool { return commonWeb(meshes, "") })
	removed := state.Agents[1].Endpoints[0]
	for i := range state.Agents {
		state.Agents[i].Endpoints = nil
	}
	state.Revision++
	applyWebState(t, state, meshes)
	waitWeb(t, ctx, func() bool { return len(meshes[0].Report()) == 0 && len(meshes[1].Report()) == 0 })
	if conn, err := transport.DialGRPC(ctx, removed, 4, state.Agents[1].PublicKey, nil); err == nil {
		conn.Close()
		t.Fatal("removed gRPC endpoint accepted new RPC")
	}
}

func TestGRPCShutdownClosesIncompleteHTTP2Prefaces(t *testing.T) {
	ctx, meshes, _, _ := webMeshes(t)
	m := meshes[0]
	clients := []net.Conn{}
	for range 12 {
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", m.Port()))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		clients = append(clients, conn)
		if _, err := conn.Write([]byte("P")); err != nil {
			t.Fatal(err)
		}
		// Let each connection reach gRPC so this tests HTTP/2-owned sockets,
		// rather than filling the separate preface classifier's eight slots.
		waitWeb(t, ctx, func() bool { return len(m.grpcSlots) == len(clients) })
	}
	done := make(chan struct{})
	go func() { m.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("incomplete HTTP/2 prefaces delayed shutdown")
	}
	if len(m.grpcSlots) != 0 {
		t.Fatal("gRPC connection slots leaked")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.grpcAdmissions) != 0 {
		t.Fatal("gRPC connection admission records leaked")
	}
}

func TestGRPCPendingAdmissionRequiresNoise(t *testing.T) {
	ctx, meshes, state, _ := webMeshes(t)
	addGRPCEndpoints(meshes, state, "/admission")
	for i := range state.Agents {
		state.Agents[i].Endpoints = state.Agents[i].Endpoints[3:]
	}
	state.Networks[0].Edges[0].Transports = []model.Transport{model.GRPC}
	applyWebState(t, state, meshes)
	m := meshes[1]
	waitWeb(t, ctx, func() bool {
		return commonWeb(meshes, "") && len(m.grpcSlots) == 0
	})
	// The TLS handshake and HTTP/2 headers succeed, but this new RPC has not
	// authenticated a configured GraphWAN peer and must retain its reservation.
	conn, err := transport.DialGRPC(ctx, state.Agents[1].Endpoints[0], 4, state.Agents[1].PublicKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if len(m.grpcSlots) != 1 {
		t.Fatal("TLS/RPC headers bypassed Noise admission")
	}
	reserved := cap(m.grpcSlots) - 1
	for range reserved {
		m.grpcSlots <- struct{}{}
	}
	defer func() {
		for range reserved {
			select {
			case <-m.grpcSlots:
			default:
				t.Error("pending gRPC reservation was released more than once")
				return
			}
		}
	}()
	extra, remote := net.Pipe()
	defer extra.Close()
	defer remote.Close()
	if m.handoffGRPC(ctx, extra) {
		t.Fatal("full pending gRPC admission accepted another connection")
	}
	if err := conn.Send(ctx, []byte("invalid Noise hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Receive(ctx); err == nil {
		t.Fatal("invalid Noise hello was accepted")
	}
	// Receive errors close the client transport as well as the rejected RPC.
	conn.Close()
	waitWeb(t, ctx, func() bool { return len(m.grpcSlots) == reserved })
	if !commonWeb(meshes, "") {
		t.Fatal("failed authentication disrupted established Links")
	}
}

func TestGRPCSharesListenerAndFallsBackToEstablishedLinks(t *testing.T) {
	ctx, meshes, state, delivered := webMeshes(t)
	addGRPCEndpoints(meshes, state, "/custom")
	edge := &state.Networks[0].Edges[0]
	preferred := link.CandidateID(edge.ID, state.Networks[0].Nodes[0].ID, state.Agents[1].Endpoints[3].ID, 4, link.Direct)
	edge.PreferredCandidate = preferred
	applyWebState(t, state, meshes)
	standbys := map[string]bool{}
	waitWeb(t, ctx, func() bool {
		for _, m := range meshes {
			healthy := map[model.Transport]bool{}
			for _, stat := range m.Report() {
				if stat.Healthy {
					healthy[stat.Transport] = true
				}
				if stat.Healthy && stat.Transport != model.GRPC {
					standbys[stat.LinkID] = true
				}
			}
			if len(healthy) != 4 {
				return false
			}
		}
		return commonWeb(meshes, preferred)
	})
	transfer := func() {
		t.Helper()
		for i, m := range meshes {
			frame := make([]byte, 1400)
			frame[0] = byte(i)
			if err := m.Send(ctx, state.Networks[0].ID, state.Networks[0].Nodes[1-i].ID, frame); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-delivered:
				if len(got) != len(frame) || got[0] != frame[0] {
					t.Fatal("corrupted frame")
				}
			case <-ctx.Done():
				t.Fatal("frame delivery stalled")
			}
		}
	}
	transfer()
	// End both inbound gRPC servers; existing TCP/WS/WSS transports stay up.
	for _, m := range meshes {
		m.grpcServer.Stop()
	}
	waitWeb(t, ctx, func() bool {
		if !commonWeb(meshes, "") {
			return false
		}
		for _, m := range meshes {
			for _, stat := range m.Report() {
				if stat.Active && !standbys[stat.LinkID] {
					return false
				}
			}
		}
		return true
	})
	transfer()
}

func TestGRPCTLSProxyWithIPv6FrontendAndIPv4Backend(t *testing.T) {
	ctx, meshes, state, delivered := webMeshes(t)
	addGRPCEndpoints(meshes, state, "/custom")
	target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", meshes[1].Port()))
	reverse := httputil.NewSingleHostReverseProxy(target)
	h2 := &http2.Transport{AllowHTTP: true, DialTLSContext: func(ctx context.Context, _, address string, _ *tls.Config) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
	}}
	defer h2.CloseIdleConnections()
	reverse.Transport = h2
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skip("IPv6 loopback unavailable:", err)
	}
	proxy := httptest.NewUnstartedServer(reverse)
	proxy.Listener.Close()
	proxy.Listener = listener
	proxy.EnableHTTP2 = true
	proxy.TLS = meshes[1].tls.Clone()
	certificate, err := proxy.TLS.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	proxy.TLS.Certificates = []tls.Certificate{*certificate}
	proxy.StartTLS()
	// Stop mesh streams before httptest waits for active HTTP/2 handlers.
	defer func() {
		for _, m := range meshes {
			m.Close()
		}
		proxy.Close()
	}()
	for i := range state.Agents {
		state.Agents[i].Endpoints = state.Agents[i].Endpoints[3:]
	}
	state.Agents[1].Endpoints[0].URL = "grpc" + strings.TrimPrefix(proxy.URL, "https") + "/custom"
	state.Networks[0].Edges[0].Transports = []model.Transport{model.GRPC}
	state.Networks[0].Edges[0].Methods = model.ConnectionMethods{IPv6Direct: true}
	applyWebState(t, state, meshes)
	waitWeb(t, ctx, func() bool { return commonWeb(meshes, "") })
	for _, stat := range meshes[0].Report() {
		if stat.Active && !strings.HasPrefix(stat.Remote, "[::1]") {
			t.Fatal("gRPC dial bypassed IPv6 policy")
		}
	}
	if err := meshes[0].Send(ctx, state.Networks[0].ID, state.Networks[0].Nodes[1].ID, make([]byte, 1400)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-delivered:
	case <-ctx.Done():
		t.Fatal("proxy did not carry authenticated overlay data")
	}
	wrong := state.Agents[1].Endpoints[0]
	wrong.URL += "/unknown"
	if conn, err := transport.DialGRPC(ctx, wrong, 6, state.Agents[1].PublicKey, nil); err == nil {
		conn.Close()
		t.Fatal("unconfigured proxy path accepted")
	}
}
