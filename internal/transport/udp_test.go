package transport_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/transport"
)

func TestUDPMultiplexesIndependentDatagrams(t *testing.T) {
	left, err := transport.ListenUDP("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer left.Close()
	right, err := transport.ListenUDP("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer right.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a, err := left.Dial(right.LocalAddr().(*net.UDPAddr).AddrPort())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := left.Dial(right.LocalAddr().(*net.UDPAddr).AddrPort())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := a.Send(ctx, []byte("first")); err != nil {
		t.Fatal(err)
	}
	first, err := right.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if err := b.Send(ctx, []byte("second")); err != nil {
		t.Fatal(err)
	}
	second, err := right.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	for _, tt := range []struct {
		peer *transport.Datagram
		want string
	}{{first, "first"}, {second, "second"}} {
		raw, err := tt.peer.Receive(ctx)
		if err != nil || !bytes.Equal(raw, []byte(tt.want)) {
			t.Fatalf("demultiplex: %q %v", raw, err)
		}
		if err := tt.peer.Send(ctx, append([]byte("reply:"), raw...)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		peer *transport.Datagram
		want string
	}{{a, "reply:first"}, {b, "reply:second"}} {
		raw, err := tt.peer.Receive(ctx)
		if err != nil || string(raw) != tt.want {
			t.Fatalf("reply: %q %v", raw, err)
		}
	}
}
func TestUDPRejectsUnknownFramesAndClosesWaiters(t *testing.T) {
	hub, err := transport.ListenUDP("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := net.DialUDP("udp", nil, hub.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.Write([]byte("not a GraphWAN datagram"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := hub.Accept(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("malformed datagram allocated peer", err)
	}
	done := make(chan error, 1)
	go func() { _, err := hub.Accept(context.Background()); done <- err }()
	hub.Close()
	if err := <-done; !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
}

func TestUDPConnectionBoundAndSlotRelease(t *testing.T) {
	testUDPConnectionBound(t, "127.0.0.1:0", false)
}
func TestUDPWildcardConnectionBoundBothFamilies(t *testing.T) {
	testUDPConnectionBound(t, ":0", true)
}
func testUDPConnectionBound(t *testing.T, bind string, wildcard bool) {
	hub, err := transport.ListenUDP(bind)
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	remote := hub.LocalAddr().(*net.UDPAddr).AddrPort()
	var first *transport.Datagram
	for i := range 512 {
		if wildcard {
			ip := netip.MustParseAddr("127.0.0.1")
			if i%2 == 1 {
				ip = netip.IPv6Loopback()
			}
			remote = netip.AddrPortFrom(ip, remote.Port())
		}
		peer, err := hub.Dial(remote)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = peer
		}
	}
	if _, err := hub.Dial(remote); err == nil {
		t.Fatal("UDP peer bound exceeded")
	}
	first.Close()
	if peer, err := hub.Dial(remote); err != nil {
		t.Fatal("closed peer did not release capacity", err)
	} else {
		peer.Close()
	}
}

func TestUDPBatchPreservesDatagramsAndReplies(t *testing.T) {
	for _, bind := range []string{"127.0.0.1:0", "[::1]:0"} {
		t.Run(bind, func(t *testing.T) {
			left, err := transport.ListenUDP(bind)
			if err != nil {
				t.Fatal(err)
			}
			defer left.Close()
			right, err := transport.ListenUDP(bind)
			if err != nil {
				t.Fatal(err)
			}
			defer right.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			sender, err := left.Dial(right.LocalAddr().(*net.UDPAddr).AddrPort())
			if err != nil {
				t.Fatal(err)
			}
			defer sender.Close()
			messages := make([][]byte, 32)
			for i := range messages {
				messages[i] = bytes.Repeat([]byte{byte(i)}, 100+i*3)
			}
			if err := sender.SendBatch(ctx, messages); err != nil {
				t.Fatal(err)
			}
			receiver, err := right.Accept(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer receiver.Close()
			received := 0
			for received < len(messages) {
				batch, err := receiver.ReceiveBatch(ctx)
				if err != nil {
					t.Fatal(err)
				}
				for _, raw := range batch {
					if received >= len(messages) || !bytes.Equal(raw, messages[received]) {
						t.Fatal("datagram boundary or order changed")
					}
					received++
				}
				if err := receiver.SendBatch(ctx, batch); err != nil {
					t.Fatal(err)
				}
			}
			for _, want := range messages {
				got, err := sender.Receive(ctx)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatal("batch reply corrupted", err)
				}
			}
			cancel()
			if err := sender.SendBatch(ctx, messages); !errors.Is(err, context.Canceled) {
				t.Fatal("batch ignored cancellation", err)
			}
		})
	}
}
