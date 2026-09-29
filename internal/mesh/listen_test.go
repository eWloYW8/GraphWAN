package mesh

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net"
	"testing"

	"github.com/eWloYW8/GraphWAN/internal/transport"
)

func TestListenTransportsPortCollision(t *testing.T) {
	for _, mode := range []string{"ephemeral", "fixed", "exhausted", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			identity := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			var released []net.Addr
			// Real TCP listeners with deliberately conflicting real UDP sockets.
			// The dependency lets us force the collision between the two binds.
			bind := func(ctx context.Context, _ string) (*transport.TCP, error) {
				calls++
				tcp, err := transport.ListenTCP(ctx, "127.0.0.1:0")
				if err != nil {
					return nil, err
				}
				if calls == 1 || mode == "exhausted" || mode == "canceled" {
					udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: tcp.Addr().(*net.TCPAddr).Port})
					if err != nil && !addressInUse(err) {
						tcp.Close()
						t.Fatal(err)
					}
					if udp != nil {
						t.Cleanup(func() { udp.Close() })
					}
					released = append(released, tcp.Addr())
					if mode == "canceled" {
						cancel()
					}
				}
				return tcp, nil
			}
			port := uint16(0)
			if mode == "fixed" {
				port = 24752
			}
			tcp, udp, _, err := listenTransports(ctx, identity, "127.0.0.1", port, bind)
			if mode == "ephemeral" {
				if err != nil || calls < 2 || calls > 8 {
					t.Fatalf("random pair retry: %d attempts, %v", calls, err)
				}
				tcp.Close()
				udp.Close()
			} else {
				wantCalls := 1
				if mode == "exhausted" {
					wantCalls = 8
				}
				if err == nil || calls != wantCalls || tcp != nil || udp != nil {
					t.Fatalf("failed pair: %d attempts, %v", calls, err)
				}
				if mode == "canceled" && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			}
			for _, addr := range released {
				rebound, err := net.Listen("tcp4", addr.String())
				if err != nil {
					t.Fatalf("failed TCP reservation leaked: %v", err)
				}
				rebound.Close()
			}
		})
	}
}
