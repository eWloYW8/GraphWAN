package peer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/secure"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
	"github.com/eWloYW8/GraphWAN/internal/transport"
)

func peerConfigs(t *testing.T, kind model.Transport) (secure.Config, secure.Config) {
	t.Helper()
	apub, akey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	bpub, bkey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := secure.Config{Network: testutil.ID(1), Edge: testutil.ID(2), Local: testutil.ID(3), Peer: testutil.ID(4), Transport: kind, Cipher: model.ChaCha20Poly1305, Identity: akey, PeerIdentity: bpub}
	b := a
	b.Local, b.Peer = a.Peer, a.Local
	b.Identity = bkey
	b.PeerIdentity = apub
	return a, b
}
func sockets(t *testing.T, kind model.Transport) (transport.Conn, func(context.Context) (transport.Conn, error)) {
	t.Helper()
	if kind == model.TCP {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { listener.Close() })
		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		return transport.NewStream(conn), func(ctx context.Context) (transport.Conn, error) {
			conn, err := listener.Accept()
			if err != nil {
				return nil, err
			}
			return transport.NewStream(conn), nil
		}
	}
	left, err := transport.ListenUDP("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { left.Close() })
	right, err := transport.ListenUDP("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { right.Close() })
	conn, err := left.Dial(right.LocalAddr().(*net.UDPAddr).AddrPort())
	if err != nil {
		t.Fatal(err)
	}
	return conn, func(ctx context.Context) (transport.Conn, error) { return right.Accept(ctx) }
}

type faulty struct {
	transport.Conn
	mu        sync.Mutex
	drop      byte
	dropped   bool
	duplicate bool
}

func (f *faulty) Unreliable() bool { return true }
func (f *faulty) Authenticated() {
	if c, ok := f.Conn.(interface{ Authenticated() }); ok {
		c.Authenticated()
	}
}
func (f *faulty) Alive() {
	if c, ok := f.Conn.(interface{ Alive() }); ok {
		c.Alive()
	}
}
func (f *faulty) Send(ctx context.Context, message []byte) error {
	f.mu.Lock()
	drop := len(message) > 0 && message[0] == f.drop && !f.dropped
	if drop {
		f.dropped = true
	}
	f.mu.Unlock()
	if drop {
		return nil
	}
	if err := f.Conn.Send(ctx, message); err != nil {
		return err
	}
	if f.duplicate {
		return f.Conn.Send(ctx, message)
	}
	return nil
}
func TestEncryptedTCPAndUDP(t *testing.T) {
	for _, kind := range []model.Transport{model.TCP, model.UDP} {
		t.Run(string(kind), func(t *testing.T) { testRoundTrip(t, kind, 0, false) })
	}
}
func TestUDPLostHandshakeMessages(t *testing.T) {
	for _, kind := range []byte{helloKind, replyKind, finishKind, readyKind} {
		t.Run(string(rune('0'+kind)), func(t *testing.T) { testRoundTrip(t, model.UDP, kind, false) })
	}
}
func TestUDPDuplicateHandshakeAndData(t *testing.T) { testRoundTrip(t, model.UDP, 0, true) }
func testRoundTrip(t *testing.T, kind model.Transport, drop byte, duplicate bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a, b := peerConfigs(t, kind)
	outbound, accept := sockets(t, kind)
	if drop != 0 || duplicate {
		outbound = &faulty{Conn: outbound, drop: drop, duplicate: duplicate}
	}
	serverDone := make(chan error, 1)
	serverSession := make(chan string, 1)
	go func() {
		conn, err := accept(ctx)
		if err != nil {
			serverDone <- err
			return
		}
		if drop != 0 || duplicate {
			conn = &faulty{Conn: conn, drop: drop, duplicate: duplicate}
		}
		channel, err := Accept(ctx, conn, kind, func(h Hello) (secure.Config, error) {
			if h.Network != b.Network || h.Edge != b.Edge || h.Initiator != b.Peer || h.Responder != b.Local {
				return secure.Config{}, errors.New("unconfigured edge")
			}
			return b, nil
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer channel.Close()
		serverSession <- channel.ID()
		payload, err := channel.Receive(ctx)
		if err != nil {
			serverDone <- err
			return
		}
		if !bytes.Equal(payload, []byte("virtual IP packet")) {
			serverDone <- errors.New("incorrect decrypted payload")
			return
		}
		serverDone <- channel.Send(ctx, []byte("reply packet"))
	}()
	channel, err := Dial(ctx, outbound, a)
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	select {
	case id := <-serverSession:
		if id != channel.ID() {
			t.Fatal("peer session mismatch")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := channel.Send(ctx, []byte("virtual IP packet")); err != nil {
		t.Fatal(err)
	}
	payload, err := channel.Receive(ctx)
	if err != nil || !bytes.Equal(payload, []byte("reply packet")) {
		t.Fatalf("encrypted roundtrip: %q %v", payload, err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}
func TestListenerRejectsUnauthorizedEdge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a, _ := peerConfigs(t, model.TCP)
	outbound, accept := sockets(t, model.TCP)
	done := make(chan error, 1)
	go func() {
		conn, err := accept(ctx)
		if err != nil {
			done <- err
			return
		}
		_, err = Accept(ctx, conn, model.TCP, func(Hello) (secure.Config, error) { return secure.Config{}, errors.New("no configured edge") })
		done <- err
	}()
	if channel, err := Dial(ctx, outbound, a); err == nil {
		channel.Close()
		t.Fatal("unauthorized edge established")
	}
	if err := <-done; err == nil {
		t.Fatal("listener accepted unauthorized edge")
	}
}
