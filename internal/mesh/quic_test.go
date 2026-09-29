package mesh

import (
	"fmt"
	"net"
	"net/netip"
	"testing"

	"github.com/eWloYW8/GraphWAN/internal/link"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
)

func TestQUICCoexistsWithAllTransportsAndFallsBack(t *testing.T) {
	ctx, meshes, state, delivered := webMeshes(t)
	addGRPCEndpoints(meshes, state, "")
	// A UDP forwarder affects QUIC alone, while native UDP shares the destination
	// port directly. Closing it must leave previously connected standby Links up.
	proxy, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	backend := netip.MustParseAddrPort(fmt.Sprintf("127.0.0.1:%d", meshes[1].Port()))
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 65536)
		var client netip.AddrPort
		for {
			n, source, err := proxy.ReadFromUDPAddrPort(buffer)
			if err != nil {
				return
			}
			if source == backend {
				if client.IsValid() {
					proxy.WriteToUDPAddrPort(buffer[:n], client)
				}
			} else {
				client = source
				proxy.WriteToUDPAddrPort(buffer[:n], backend)
			}
		}
	}()
	defer func() { proxy.Close(); <-done }()
	for i, m := range meshes {
		state.Agents[i].Endpoints = append(state.Agents[i].Endpoints, model.Endpoint{ID: testutil.ID(200 + i), Transport: model.UDP, Source: model.Manual, URL: fmt.Sprintf("udp://127.0.0.1:%d", m.Port())})
	}
	endpoint := model.Endpoint{ID: testutil.ID(202), Transport: model.QUIC, Source: model.Manual, URL: "quic://" + proxy.LocalAddr().String()}
	state.Agents[1].Endpoints = append(state.Agents[1].Endpoints, endpoint)
	edge := &state.Networks[0].Edges[0]
	edge.Transports = append(edge.Transports, model.UDP, model.QUIC)
	preferred := link.CandidateID(edge.ID, state.Networks[0].Nodes[0].ID, endpoint.ID, 4, link.Direct)
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
				if stat.Healthy && stat.Transport != model.QUIC {
					standbys[stat.LinkID] = true
				}
			}
			if len(healthy) != 6 {
				return false
			}
		}
		return commonWeb(meshes, preferred)
	})
	transfer := func() {
		t.Helper()
		for i, m := range meshes {
			frame := make([]byte, 9000)
			frame[0] = byte(i)
			if err := m.Send(ctx, state.Networks[0].ID, state.Networks[0].Nodes[1-i].ID, frame); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-delivered:
				if len(got) != len(frame) || got[0] != frame[0] {
					t.Fatal("corrupted QUIC frame")
				}
			case <-ctx.Done():
				t.Fatal("QUIC forwarding stalled")
			}
		}
	}
	transfer()
	proxy.Close()
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
