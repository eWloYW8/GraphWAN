package transport

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/netip"
	"testing"
	"time"
)

func TestUDPWildcardReplySource(t *testing.T) {
	for _, shared := range []bool{false, true} {
		for _, target := range []string{"127.0.0.2", "::1"} {
			t.Run(fmt.Sprintf("shared=%t/%s", shared, target), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				var server *UDP
				var err error
				if shared {
					_, key, _ := ed25519.GenerateKey(rand.Reader)
					server, _, err = ListenUDPQUIC(":0", key)
				} else {
					server, err = ListenUDP(":0")
				}
				if err != nil {
					t.Fatal(err)
				}
				defer server.Close()
				client, err := ListenUDP(":0")
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				bound := server.LocalAddr().String()
				address := netip.MustParseAddrPort(bound)
				address = netip.AddrPortFrom(netip.MustParseAddr(target), address.Port())
				outgoing, err := client.Dial(address)
				if err != nil {
					t.Fatal(err)
				}
				defer outgoing.Close()
				// An oversized packet must not close the shared listener. Windows
				// reports WSAEMSGSIZE; Unix commonly reports MSG_TRUNC instead.
				oversized := make([]byte, MaxMessage+udpHeaderSize+512)
				copy(oversized, udpMagic[:])
				copy(oversized[4:], outgoing.key.token[:])
				if err := client.writeDatagram(ctx, oversized, address); err != nil {
					t.Fatal(err)
				}
				if err := outgoing.Send(ctx, []byte("request")); err != nil {
					t.Fatal(err)
				}
				incoming, err := server.Accept(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer incoming.Close()
				if got, err := incoming.Receive(ctx); err != nil || string(got) != "request" {
					t.Fatalf("request: %q %v", got, err)
				}
				if err := incoming.Send(ctx, []byte("reply")); err != nil {
					t.Fatal(err)
				}
				if got, err := outgoing.Receive(ctx); err != nil || string(got) != "reply" {
					t.Fatalf("reply source did not match dial target %s: %q %v", target, got, err)
				}
			})
		}
	}
}
