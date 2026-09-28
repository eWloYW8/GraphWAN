package discovery

import (
	"context"
	"errors"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/model"
)

func TestObservePartialFailureDeduplicationAndStableIdentity(t *testing.T) {
	servers := []string{"127.0.0.1:3478", "127.0.0.1:3479", "[::1]:3478", "127.0.0.1:3480"}
	var inflight atomic.Int32
	var calls atomic.Int32
	bind := func(ctx context.Context, kind model.Transport, remote netip.AddrPort) (netip.AddrPort, error) {
		calls.Add(1)
		if inflight.Add(1) > 4 {
			t.Error("unbounded probing")
		}
		defer inflight.Add(-1)
		if remote.Port() == 3480 {
			return netip.AddrPort{}, errors.New("unreachable server")
		}
		if remote.Addr().Is6() {
			return netip.MustParseAddrPort("[2001:db8::1]:45678"), nil
		}
		return netip.MustParseAddrPort("192.0.2.1:45678"), nil
	}
	first, err := Observe(context.Background(), []byte("agent A"), servers, bind)
	if err == nil || len(first) != 2 || calls.Load() != 4 {
		t.Fatal("partial results lost:", first, err)
	}
	second, _ := Observe(context.Background(), []byte("agent A"), servers, bind)
	other, _ := Observe(context.Background(), []byte("agent B"), servers, bind)
	for i, endpoint := range first {
		if endpoint.Validate() != nil || endpoint.Source != model.Observed || endpoint.Transport != model.UDP || !endpoint.ExpiresAt.After(time.Now()) || endpoint.ExpiresAt.After(time.Now().Add(ObservedLifetime)) {
			t.Fatal("invalid observation:", endpoint)
		}
		if endpoint.ID != second[i].ID || endpoint.URL != second[i].URL {
			t.Fatal("unstable mapping identity")
		}
		for _, e := range other {
			if e.ID == endpoint.ID {
				t.Fatal("agents share endpoint identity")
			}
		}
	}
	before := calls.Load()
	if _, err := Observe(context.Background(), nil, []string{"invalid"}, bind); err == nil || calls.Load() != before {
		t.Fatal("invalid configuration reached network")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, _ := Observe(ctx, nil, servers, bind); len(got) != 0 || calls.Load() != before {
		t.Fatal("canceled probe ran")
	}
}

func TestProbeOrderKeepsBothFamiliesAndBounds(t *testing.T) {
	ip4 := netip.MustParseAddr("192.0.2.1")
	ip6 := netip.MustParseAddr("2001:db8::1")
	addresses := []netip.Addr{}
	for range 64 {
		addresses = append(addresses, ip6)
		ip6 = ip6.Next()
	}
	addresses = append(addresses, ip4)
	ordered := probeOrder(addresses)
	if len(ordered) != 32 || !ordered[0].Is4() || !ordered[1].Is6() {
		t.Fatal("a large DNS family starved the other family")
	}
	if len(probeOrder(nil)) != 0 {
		t.Fatal("empty DNS response")
	}
}

func TestObserveKeepsProtocolSpecificMappings(t *testing.T) {
	for _, samePort := range []bool{false, true} {
		bind := func(_ context.Context, kind model.Transport, _ netip.AddrPort) (netip.AddrPort, error) {
			port := uint16(32752)
			if kind == model.TCP && !samePort {
				port = 33752
			}
			return netip.AddrPortFrom(netip.MustParseAddr("192.0.2.1"), port), nil
		}
		endpoints, err := Observe(context.Background(), []byte("identity"), []string{"127.0.0.1:3478", "tcp://127.0.0.1:3478"}, bind)
		if err != nil || len(endpoints) != 2 {
			t.Fatal("TCP and UDP observations conflated:", endpoints, err)
		}
		if endpoints[0].Transport == endpoints[1].Transport || endpoints[0].ID == endpoints[1].ID {
			t.Fatal("protocol omitted from mapping identity")
		}
		for _, endpoint := range endpoints {
			want := "udp://192.0.2.1:32752"
			if endpoint.Transport == model.TCP {
				want = "tcp://192.0.2.1:33752"
				if samePort {
					want = "tcp://192.0.2.1:32752"
				}
			}
			if endpoint.URL != want || endpoint.Validate() != nil {
				t.Fatal(endpoint)
			}
		}
	}
}
