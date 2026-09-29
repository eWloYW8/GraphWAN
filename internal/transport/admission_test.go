package transport

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
)

func TestUDPPendingAdmissionSurvivesAuthenticationAndCloseRaces(t *testing.T) {
	h, err := ListenUDP(":0")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	remote := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), h.LocalAddr().(*net.UDPAddr).AddrPort().Port())
	peers := make([]*Datagram, 0, maxPendingUDP)
	for range maxPendingUDP {
		peer, err := h.Dial(remote)
		if err != nil {
			t.Fatal(err)
		}
		peers = append(peers, peer)
	}
	full := func() {
		t.Helper()
		if peer, err := h.Dial(remote); err == nil {
			peer.Close()
			t.Fatal("unauthenticated admission exceeded its bound")
		}
	}
	full()
	// Authentication releases exactly one reservation without closing the Link.
	peers[0].Authenticated()
	peers[0].Authenticated()
	newcomer, err := h.Dial(remote)
	if err != nil {
		t.Fatal("authenticated Link still occupies admission:", err)
	}
	full()
	peers[0].Close()
	peers[0].Authenticated() // A late handshake cannot release somebody else's slot.
	full()
	peers = append(peers[1:], newcomer)
	var wg sync.WaitGroup
	for _, peer := range peers {
		wg.Go(func() { peer.Authenticated() })
		wg.Go(func() { peer.Close() })
	}
	wg.Wait()
	h.mu.Lock()
	pending, live := h.pending, len(h.peers)
	h.mu.Unlock()
	if pending != 0 || live != 0 {
		t.Fatalf("authentication/close race leaked admission: pending=%d live=%d", pending, live)
	}
	peer, err := h.Dial(remote)
	if err != nil {
		t.Fatal(err)
	}
	h.Close()
	peer.Authenticated()
	peer.Close()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pending != 0 || len(h.peers) != 0 {
		t.Fatal("late authentication changed closed hub admission")
	}
}

func TestQUICPendingAdmissionRequiresNoiseAndReleasesOnce(t *testing.T) {
	udp, hubs, keys := quicHubs(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	a, err := hubs[0].Dial(ctx, model.Endpoint{ID: testutil.ID(1), Source: model.Manual, Transport: model.QUIC, URL: "quic://" + udp[1].LocalAddr().String()}, 4, keys[1])
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := hubs[1].Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	// TLS alone (including a verified server key) is not GraphWAN peer admission.
	for _, h := range hubs {
		if len(h.slots) != 1 {
			t.Fatal("TLS handshake bypassed pending Noise admission")
		}
	}
	connections := []*QUIC{a, b}
	for i, h := range hubs {
		releases := []func(){}
		for range cap(h.slots) - 1 {
			release, err := h.reserve()
			if err != nil {
				t.Fatal(err)
			}
			releases = append(releases, release)
		}
		if release, err := h.reserve(); err == nil {
			release()
			t.Fatal("QUIC pending admission exceeded its bound")
		}
		connections[i].Authenticated()
		connections[i].Authenticated()
		if len(h.slots) != cap(h.slots)-1 {
			t.Fatal("authentication did not release exactly one slot")
		}
		release, err := h.reserve()
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
		var wg sync.WaitGroup
		// Completion/cancellation/shutdown can all race on the same reservation.
		for _, release := range releases {
			for range 3 {
				wg.Go(release)
			}
		}
		wg.Wait()
		if len(h.slots) != 0 {
			t.Fatal("pending QUIC reservations leaked")
		}
	}
	a.Close()
	b.Close()
}
