package mesh

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/graphwan/graphwan/internal/link"
	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/peer"
	"github.com/graphwan/graphwan/internal/routing"
	"github.com/graphwan/graphwan/internal/testutil"
	"github.com/graphwan/graphwan/internal/transport"
)

func webMeshes(t *testing.T) (context.Context, []*Mesh, *model.State, <-chan []byte) {
	t.Helper()
	return webMeshesOn(t, "127.0.0.1")
}

func webMeshesOn(t *testing.T, host string) (context.Context, []*Mesh, *model.State, <-chan []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	state := testutil.Topology()
	state.Agents = state.Agents[:2]
	state.Networks[0].Nodes = state.Networks[0].Nodes[:2]
	state.Networks[0].Edges = state.Networks[0].Edges[:1]
	state.Networks[0].Edges[0].Methods = model.ConnectionMethods{IPv4Direct: true}
	state.Networks[0].Edges[0].Transports = []model.Transport{model.TCP, model.WS, model.WSS}
	delivered := make(chan []byte, 16)
	meshes := []*Mesh{}
	for i := range 2 {
		seed := make([]byte, ed25519.SeedSize)
		seed[0] = byte(i + 1)
		m, err := New(ctx, ed25519.NewKeyFromSeed(seed), host, 0, func(ctx context.Context, _ model.ID, raw []byte) error {
			select {
			case delivered <- raw:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { m.Close() })
		meshes = append(meshes, m)
		state.Agents[i].ListenPort = m.Port()
		state.Agents[i].Endpoints = nil
		for j, kind := range []model.Transport{model.TCP, model.WS, model.WSS} {
			path := ""
			if kind != model.TCP {
				path = "/custom/overlay"
			}
			state.Agents[i].Endpoints = append(state.Agents[i].Endpoints, model.Endpoint{ID: testutil.ID(100 + i*3 + j), Source: model.Manual, Transport: kind, URL: fmt.Sprintf("%s://127.0.0.1:%d%s", kind, m.Port(), path)})
		}
	}
	return ctx, meshes, &state, delivered
}
func TestManualTransportIntroductionMustMatchIngressPath(t *testing.T) {
	for _, kind := range []model.Transport{model.WS, model.GRPC} {
		t.Run(string(kind), func(t *testing.T) {
			ctx, meshes, state, _ := webMeshes(t)
			index := 1
			if kind == model.GRPC {
				addGRPCEndpoints(meshes, state, "/custom")
				index = 3
			}
			other := state.Agents[1].Endpoints[index]
			other.ID = testutil.ID(150)
			other.URL += "/other"
			state.Agents[1].Endpoints = append(state.Agents[1].Endpoints, other)
			// Only the responder reconciles; the test dialer supplies the authenticated
			// initiator identity and then lies about which allowed endpoint it used.
			snapshot, err := routing.Compile(*state, state.Agents[1].ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := meshes[1].Apply(snapshot); err != nil {
				t.Fatal(err)
			}
			var conn transport.Conn
			if kind == model.GRPC {
				conn, err = transport.DialGRPC(ctx, state.Agents[1].Endpoints[index], 4, state.Agents[1].PublicKey, nil)
			} else {
				conn, err = transport.DialWebSocket(ctx, state.Agents[1].Endpoints[index], 4, state.Agents[1].PublicKey, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			initiator, err := routing.Compile(*state, state.Agents[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			network := initiator.Networks[0]
			cfg := &policy{network: network.ID, self: network.Self.ID, cipher: network.Cipher, peer: network.Peers[0], endpoints: initiator.Endpoints}
			channel, err := peer.Dial(ctx, conn, meshes[0].secure(cfg, kind))
			if err != nil {
				t.Fatal(err)
			}
			defer channel.Close()
			candidate := link.CandidateID(network.Peers[0].Edge.ID, network.Self.ID, other.ID, 4, link.Direct)
			intro, _ := json.Marshal(introduction{Candidate: candidate})
			if err := channel.Send(ctx, append([]byte{0}, intro...)); err != nil {
				t.Fatal(err)
			}
			readCtx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			if _, err := channel.Receive(readCtx); err == nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("wrong ingress path was not immediately rejected: %v", err)
			}

		})
	}
}

func TestWebSocketHTTPAdmission(t *testing.T) {
	ctx, meshes, state, _ := webMeshes(t)
	applyWebState(t, state, meshes)
	for _, test := range []struct {
		name, path, protocol, origin string
	}{
		{"unknown path", "/unknown", "graphwan.ws.v1", ""},
		{"query", "/custom/overlay?token=1", "graphwan.ws.v1", ""},
		{"browser", "/custom/overlay", "graphwan.ws.v1", "https://example.com"},
		{"unknown protocol", "/custom/overlay", "other.v1", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			header := http.Header{}
			if test.origin != "" {
				header.Set("Origin", test.origin)
			}
			conn, response, err := websocket.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:%d%s", meshes[1].Port(), test.path), &websocket.DialOptions{HTTPHeader: header, Subprotocols: []string{test.protocol}})
			if err == nil {
				conn.CloseNow()
				t.Fatal("invalid upgrade was accepted")
			}
			if response == nil || response.StatusCode != 400 && response.StatusCode != 404 {
				t.Fatalf("request failed without an admission rejection: %v", err)
			}
		})
	}
}
func applyWebState(t *testing.T, state *model.State, meshes []*Mesh) {
	t.Helper()
	for i, m := range meshes {
		snapshot, err := routing.Compile(*state, state.Agents[i].ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Apply(snapshot); err != nil {
			t.Fatal(err)
		}
	}
}
func waitWeb(t *testing.T, ctx context.Context, condition func() bool) {
	t.Helper()
	for !condition() {
		select {
		case <-ctx.Done():
			t.Fatal("WebSocket mesh condition timed out")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func commonWeb(meshes []*Mesh, candidate string) bool {
	active := ""
	for _, m := range meshes {
		current := ""
		for _, stat := range m.Report() {
			if stat.Active && (candidate == "" || stat.CandidateID == candidate) {
				current = stat.LinkID
			}
		}
		if current == "" || active != "" && active != current {
			return false
		}
		active = current
	}
	return true
}
func TestTCPWSWSSShareListenerAndPreferredPath(t *testing.T) {
	ctx, meshes, state, delivered := webMeshes(t)
	applyWebState(t, state, meshes)
	waitWeb(t, ctx, func() bool {
		for _, m := range meshes {
			healthy := map[model.Transport]bool{}
			for _, stat := range m.Report() {
				if stat.Healthy {
					healthy[stat.Transport] = true
				}
			}
			if len(healthy) != 3 {
				return false
			}
		}
		return commonWeb(meshes, "")
	})
	for _, kind := range []model.Transport{model.WS, model.WSS, model.TCP} {
		var endpoint model.Endpoint
		for _, e := range state.Agents[1].Endpoints {
			if e.Transport == kind {
				endpoint = e
			}
		}
		candidate := link.CandidateID(state.Networks[0].Edges[0].ID, state.Networks[0].Nodes[0].ID, endpoint.ID, 4, link.Direct)
		state.Networks[0].Edges[0].PreferredCandidate = candidate
		state.Revision++
		applyWebState(t, state, meshes)
		waitWeb(t, ctx, func() bool { return commonWeb(meshes, candidate) })
		for i, m := range meshes {
			frame := make([]byte, 1400)
			frame[0] = byte(i)
			if err := m.Send(ctx, state.Networks[0].ID, state.Networks[0].Nodes[1-i].ID, frame); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-delivered:
				if len(got) != len(frame) || got[0] != byte(i) {
					t.Fatal("web transport corrupted payload")
				}
			case <-ctx.Done():
				t.Fatal("web forwarding stalled")
			}
		}
	}
	// HTTP URLs must match a manual endpoint; no browser origin or guessed path.
	for _, path := range []string{"/not-advertised", "/custom/overlay?redirect=1"} {
		req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d%s", meshes[1].Port(), path), nil)
		req.Header.Set("Sec-WebSocket-Protocol", "graphwan.ws.v1")
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 400 && response.StatusCode != 404 {
			t.Fatalf("unconfigured path accepted: %d", response.StatusCode)
		}
	}
	// Removing the manual WS/WSS endpoints retires their sessions and preserves
	// the remaining configured TCP route after reconciliation.
	for i := range state.Agents {
		state.Agents[i].Endpoints = state.Agents[i].Endpoints[:1]
	}
	state.Revision++
	state.Networks[0].Edges[0].PreferredCandidate = ""
	applyWebState(t, state, meshes)
	waitWeb(t, ctx, func() bool { return commonWeb(meshes, "") })
	for _, m := range meshes {
		for _, stat := range m.Report() {
			if stat.Transport != model.TCP {
				t.Fatal("removed manual endpoint retained session")
			}
		}
	}
}
func TestWebSocketReverseProxyAcrossAddressFamilies(t *testing.T) {
	for offset, kind := range []model.Transport{model.WS, model.WSS} {
		t.Run(string(kind), func(t *testing.T) {
			index := offset + 1
			ctx, meshes, state, delivered := webMeshes(t)
			target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", meshes[1].Port()))
			listener, err := net.Listen("tcp6", "[::1]:0")
			if err != nil {
				t.Skip("IPv6 loopback unavailable:", err)
			}
			proxy := httptest.NewUnstartedServer(httputil.NewSingleHostReverseProxy(target))
			proxy.Listener.Close()
			proxy.Listener = listener
			if kind == model.WSS {
				proxy.TLS = meshes[1].tls.Clone()
				certificate, err := proxy.TLS.GetCertificate(nil)
				if err != nil {
					t.Fatal(err)
				}
				proxy.TLS.Certificates = []tls.Certificate{*certificate}
				proxy.StartTLS()
			} else {
				proxy.Start()
			}
			defer proxy.Close()
			state.Networks[0].Edges[0].Transports = []model.Transport{kind}
			state.Networks[0].Edges[0].Methods = model.ConnectionMethods{IPv6Direct: true}
			state.Agents[0].Endpoints = state.Agents[0].Endpoints[index : index+1]
			state.Agents[1].Endpoints = state.Agents[1].Endpoints[index : index+1]
			state.Agents[1].Endpoints[0].URL = string(kind) + strings.TrimPrefix(strings.TrimPrefix(proxy.URL, "https"), "http") + "/custom/overlay"
			applyWebState(t, state, meshes)
			waitWeb(t, ctx, func() bool { return commonWeb(meshes, "") })
			// The initiator honors IPv6 policy although the proxy's backend hop is IPv4.
			for _, stat := range meshes[0].Report() {
				if stat.Active && !strings.HasPrefix(stat.Remote, "[::1]") {
					t.Fatal("candidate family was bypassed")
				}
			}
			frame := make([]byte, 100)
			if err := meshes[0].Send(ctx, state.Networks[0].ID, state.Networks[0].Nodes[1].ID, frame); err != nil {
				t.Fatal(err)
			}
			select {
			case <-delivered:
			case <-ctx.Done():
				t.Fatal("proxy forwarding stalled")
			}
		})
	}
}

func TestClassifierShutdownClosesPendingPrefaces(t *testing.T) {
	_, meshes, _, _ := webMeshes(t)
	clients := []net.Conn{}
	for range 12 {
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", meshes[0].Port()))
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, conn)
		defer conn.Close()
	}
	done := make(chan struct{})
	go func() { meshes[0].Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pending prefaces delayed shutdown")
	}
	for _, conn := range clients {
		conn.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := conn.Read(make([]byte, 1)); err == nil {
			t.Fatal("pending socket was not closed")
		} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
			t.Fatal("pending socket stayed open until the read deadline")
		}
	}
}

func TestSlowHTTPHeadersRetainAdmissionSlots(t *testing.T) {
	ctx, meshes, _, _ := webMeshes(t)
	m := meshes[0]
	for range cap(m.sniffSlots) {
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", m.Port()))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := conn.Write([]byte("GET / HTTP/1.1\r\n")); err != nil {
			t.Fatal(err)
		}
	}
	waitWeb(t, ctx, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.sniffSlots) == cap(m.sniffSlots) && len(m.pending) == 0
	})
	extra, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", m.Port()))
	if err != nil {
		t.Fatal(err)
	}
	defer extra.Close()
	extra.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := extra.Read(make([]byte, 1)); err == nil {
		t.Fatal("slow HTTP headers bypassed admission limit")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("excess unauthenticated connection stayed open")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if len(m.sniffSlots) != 0 {
		t.Fatal("HTTP close leaked admission slots")
	}
}
