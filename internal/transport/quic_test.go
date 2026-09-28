package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/testutil"
)

func quicHubs(t *testing.T) ([]*UDP, []*QUICHub, []ed25519.PublicKey) {
	t.Helper()
	return quicHubsAt(t, "127.0.0.1:0")
}
func quicHubsAt(t *testing.T, address string) ([]*UDP, []*QUICHub, []ed25519.PublicKey) {
	t.Helper()
	udp := []*UDP{}
	hubs := []*QUICHub{}
	keys := []ed25519.PublicKey{}
	for range 2 {
		pub, key, _ := ed25519.GenerateKey(rand.Reader)
		u, q, err := ListenUDPQUIC(address, key)
		if err != nil {
			if address == "[::1]:0" {
				t.Skip("IPv6 loopback unavailable:", err)
			}
			t.Fatal(err)
		}
		t.Cleanup(func() { u.Close() })
		udp = append(udp, u)
		hubs = append(hubs, q)
		keys = append(keys, pub)
	}
	return udp, hubs, keys
}

func TestQUICIPv6AndTLSAdmission(t *testing.T) {
	udp, hubs, keys := quicHubsAt(t, "[::1]:0")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	endpoint := model.Endpoint{ID: testutil.ID(1), Source: model.Manual, Transport: model.QUIC, URL: "quic://" + udp[1].LocalAddr().String()}
	if conn, err := hubs[0].Dial(ctx, endpoint, 4, keys[1]); err == nil {
		conn.Close()
		t.Fatal("IPv6 endpoint bypassed IPv4-only policy")
	}
	if conn, err := hubs[0].Dial(ctx, endpoint, 6, keys[0]); err == nil {
		conn.Close()
		t.Fatal("wrong QUIC identity accepted")
	}
	a, err := hubs[0].Dial(ctx, endpoint, 6, keys[1])
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := hubs[1].Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	frame := bytes.Repeat([]byte{5}, 9000)
	if err := a.Send(ctx, frame); err != nil {
		t.Fatal(err)
	}
	if got, err := b.Receive(ctx); err != nil || !bytes.Equal(got, frame) {
		t.Fatal("IPv6 QUIC delivery:", err)
	}
}

func TestQUICWildcardListenerSecondaryAddress(t *testing.T) {
	_, clientHubs, _ := quicHubs(t)
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	udp, server, err := ListenUDPQUIC("0.0.0.0:0", key)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	endpoint := model.Endpoint{ID: testutil.ID(1), Source: model.Manual, Transport: model.QUIC, URL: fmt.Sprintf("quic://127.0.0.2:%d", udp.LocalAddr().(*net.UDPAddr).Port)}
	a, err := clientHubs[0].Dial(ctx, endpoint, 4, pub)
	if err != nil {
		t.Fatal("secondary destination on wildcard listener:", err)
	}
	defer a.Close()
	b, err := server.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := b.Send(ctx, []byte("secondary address")); err != nil {
		t.Fatal(err)
	}
	if got, err := a.Receive(ctx); err != nil || string(got) != "secondary address" {
		t.Fatal("secondary address reply:", err)
	}
}

func TestQUICDatagramLossAndBlockedSendCancellation(t *testing.T) {
	udp, hubs, keys := quicHubs(t)
	proxy, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	backend := udp[1].LocalAddr().(*net.UDPAddr).AddrPort()
	var drop atomic.Bool
	var dropped atomic.Uint64
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
				if drop.Load() {
					dropped.Add(1)
					continue
				}
				proxy.WriteToUDPAddrPort(buffer[:n], backend)
			}
		}
	}()
	defer func() { proxy.Close(); <-done }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a, err := hubs[0].Dial(ctx, model.Endpoint{ID: testutil.ID(1), Source: model.Manual, Transport: model.QUIC, URL: "quic://" + proxy.LocalAddr().String()}, 4, keys[1])
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := hubs[1].Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	drop.Store(true)
	if err := a.Send(ctx, []byte("lost packet")); err != nil {
		t.Fatal(err)
	}
	noPacket := func() {
		t.Helper()
		wait, stop := context.WithTimeout(ctx, 200*time.Millisecond)
		defer stop()
		if got, err := b.Receive(wait); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("lost datagram reappeared: %q %v", got, err)
		}
	}
	noPacket()
	if dropped.Load() == 0 {
		t.Fatal("test did not drop a physical packet")
	}
	drop.Store(false)
	noPacket() // QUIC may retransmit ACK-eliciting control, never the lost data.
	if err := a.Send(ctx, []byte("next packet")); err != nil {
		t.Fatal(err)
	}
	if got, err := b.Receive(ctx); err != nil || string(got) != "next packet" {
		t.Fatal("QUIC did not recover after packet loss:", err)
	}
	drop.Store(true)
	blocked, stop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer stop()
	for {
		if err := a.Send(blocked, make([]byte, MaxMessage)); err != nil {
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			break
		}
	}
	select {
	case <-a.conn.Context().Done():
	case <-ctx.Done():
		t.Fatal("congestion-blocked Send did not cancel connection")
	}
}

func FuzzQUICFragments(f *testing.F) {
	f.Add(fragment(1, make([]byte, 1400), 0))
	f.Add(fragment(2, []byte("packet"), 0))
	f.Add([]byte("malformed"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		var r quicReassembler
		got := r.receive(raw, time.Now())
		if len(got) > MaxMessage || len(r.pending) > quicReassemblies {
			t.Fatal("fragment bounds exceeded")
		}
		for _, record := range r.pending {
			if len(record.raw) > MaxMessage {
				t.Fatal("oversized allocation")
			}
		}
	})
}
func TestQUICDatagramsShareSocketWithLegacyUDP(t *testing.T) {
	udp, hubs, keys := quicHubs(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	endpoint := model.Endpoint{ID: testutil.ID(1), Source: model.Manual, Transport: model.QUIC, URL: "quic://" + udp[1].LocalAddr().String()}
	dialCtx, cancelDial := context.WithCancel(ctx)
	a, err := hubs[0].Dial(dialCtx, endpoint, 4, keys[1])
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	cancelDial()
	b, err := hubs[1].Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, size := range []int{1, 1024, 1025, 1400, 9000, MaxMessage} {
		frame := bytes.Repeat([]byte{byte(size)}, size)
		for _, pair := range [][2]*QUIC{{a, b}, {b, a}} {
			if err := pair[0].Send(ctx, frame); err != nil {
				t.Fatal(err)
			}
			got, err := pair[1].Receive(ctx)
			if err != nil || !bytes.Equal(frame, got) {
				t.Fatalf("size %d: %v", size, err)
			}
		}
	}
	// Legacy native-only UDP implementations still reach the very same port,
	// including packets larger than quic-go's receive buffer and path budget.
	legacy, err := ListenUDP("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	native, err := legacy.Dial(udp[1].LocalAddr().(*net.UDPAddr).AddrPort())
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	frame := bytes.Repeat([]byte{37}, MaxMessage)
	if err := native.Send(ctx, frame); err != nil {
		t.Fatal(err)
	}
	remote, err := udp[1].Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	got, err := remote.Receive(ctx)
	if err != nil || !bytes.Equal(got, frame) {
		t.Fatal("legacy UDP frame lost:", err)
	}
	if err := remote.Send(ctx, frame); err != nil {
		t.Fatal(err)
	}
	got, err = native.Receive(ctx)
	if err != nil || !bytes.Equal(got, frame) {
		t.Fatal("legacy UDP reply lost:", err)
	}
	// Receive deadlines must leave a datagram session alive for handshake retries.
	short, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	if _, err := b.Receive(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if err := a.Send(ctx, []byte("after timeout")); err != nil {
		t.Fatal(err)
	}
	if got, err := b.Receive(ctx); err != nil || string(got) != "after timeout" {
		t.Fatal("receive timeout closed QUIC:", err)
	}
	if err := a.Send(ctx, make([]byte, MaxMessage+1)); err == nil {
		t.Fatal("oversized message accepted")
	}
	if !a.Unreliable() || a.LocalAddr().String() != udp[0].LocalAddr().String() {
		t.Fatal("QUIC did not preserve datagram/socket contract")
	}
}

func fragment(id uint64, raw []byte, offset int) []byte {
	size := min(quicFragmentPayload, len(raw)-offset)
	out := make([]byte, quicFragmentHeader+size)
	out[0] = 1
	binary.BigEndian.PutUint64(out[1:9], id)
	binary.BigEndian.PutUint16(out[9:11], uint16(len(raw)))
	binary.BigEndian.PutUint16(out[11:13], uint16(offset))
	copy(out[13:], raw[offset:offset+size])
	return out
}
func TestQUICReassemblyReorderingLossAndResourceBounds(t *testing.T) {
	now := time.Now()
	var r quicReassembler
	raw := bytes.Repeat([]byte{19}, MaxMessage)
	for offset := len(raw) - quicFragmentPayload; offset >= 0; offset -= quicFragmentPayload {
		got := r.receive(fragment(1, raw, offset), now)
		if offset == 0 {
			if !bytes.Equal(got, raw) {
				t.Fatal("out-of-order fragments corrupted message")
			}
		} else {
			if got != nil {
				t.Fatal("partial message delivered")
			}
			if r.receive(fragment(1, raw, offset), now) != nil {
				t.Fatal("duplicate completed message")
			}
		}
	}
	if len(r.pending) != 0 {
		t.Fatal("completed message retained")
	}
	for id := uint64(2); id < 100; id++ {
		if r.receive(fragment(id, raw, 0), now) != nil {
			t.Fatal("incomplete message delivered")
		}
	}
	if len(r.pending) != quicReassemblies {
		t.Fatal("reassembly bound not enforced")
	}
	// Duplicate fragments do not extend expiry. Lost fragments remain lost.
	r.receive(fragment(2, raw, 0), now.Add(quicAssemblyLifetime-time.Millisecond))
	r.receive(fragment(100, []byte("probe"), 0), now.Add(quicAssemblyLifetime))
	if len(r.pending) != 0 {
		t.Fatal("lost message survived expiry")
	}
	for offset := quicFragmentPayload; offset < len(raw); offset += quicFragmentPayload {
		if r.receive(fragment(2, raw, offset), now.Add(quicAssemblyLifetime)) != nil {
			t.Fatal("expired partial message was resurrected")
		}
	}
	for _, mutate := range []func([]byte){
		func(p []byte) { p[0] = 2 },
		func(p []byte) { clear(p[1:9]) },
		func(p []byte) { binary.BigEndian.PutUint16(p[9:11], MaxMessage+1) },
		func(p []byte) { binary.BigEndian.PutUint16(p[11:13], 1) },
		func(p []byte) { binary.BigEndian.PutUint16(p[11:13], MaxMessage) },
	} {
		bad := fragment(101, raw, 0)
		mutate(bad)
		if r.receive(bad, now) != nil {
			t.Fatal("malformed fragment accepted")
		}
		if r.pending[101] != nil {
			t.Fatal("malformed fragment allocated an assembly")
		}
	}
}

func TestQUICHubClosureInterruptsDatagramWaiters(t *testing.T) {
	udp, hubs, keys := quicHubs(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
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
	done := make(chan error, 1)
	go func() { _, err := b.Receive(ctx); done <- err }()
	if err := hubs[1].Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("shutdown did not cancel receive:", err)
		}
	case <-ctx.Done():
		t.Fatal("QUIC close stalled")
	}
	if _, err := udp[1].Accept(ctx); !errors.Is(err, net.ErrClosed) {
		t.Fatal("shared native UDP did not close:", err)
	}
}
