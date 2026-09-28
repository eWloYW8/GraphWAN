package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"testing"
	"time"
)

func TestPunchMuxFixedPortAuthenticationAndIndependentStreams(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a, err := ListenTCP(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := ListenTCP(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	left, err := DialTCPPort(ctx, a.Addr().(*net.TCPAddr), b.Addr().(*net.TCPAddr).AddrPort())
	if err != nil {
		t.Fatal(err)
	}
	right, err := b.Accept()
	if err != nil {
		left.Close()
		t.Fatal(err)
	}
	if left.LocalAddr().String() != a.Addr().String() || right.RemoteAddr().String() != a.Addr().String() {
		t.Fatal("source port was not reused")
	}
	pubA, keyA, _ := ed25519.GenerateKey(rand.Reader)
	pubB, keyB, _ := ed25519.GenerateKey(rand.Reader)
	certA, _ := PeerServerTLS(keyA)
	certB, _ := PeerServerTLS(keyB)
	type result struct {
		mux *PunchMux
		err error
	}
	done := make(chan result, 1)
	go func() {
		m, e := NewPunchMux(ctx, right, pubB, certB, func(pub ed25519.PublicKey) bool { return pub.Equal(pubA) })
		done <- result{m, e}
	}()
	ma, err := NewPunchMux(ctx, left, pubA, certA, func(pub ed25519.PublicKey) bool { return pub.Equal(pubB) })
	if err != nil {
		t.Fatal(err)
	}
	defer ma.Close()
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	mb := got.mux
	defer mb.Close()
	for _, pair := range [][2]*PunchMux{{ma, mb}, {mb, ma}} {
		first, err := pair[0].Open(ctx)
		if err != nil {
			t.Fatal(err)
		}
		second, err := pair[1].Accept(ctx)
		if err != nil {
			t.Fatal(err)
		}
		payload := bytes.Repeat([]byte{42}, MaxMessage)
		if err := first.Send(ctx, payload); err != nil {
			t.Fatal(err)
		}
		if got, err := second.Receive(ctx); err != nil || !bytes.Equal(got, payload) {
			t.Fatal("mux delivery:", err)
		}
		short, stop := context.WithTimeout(ctx, 10*time.Millisecond)
		_, err = second.Receive(short)
		stop()
		if timeout, ok := err.(net.Error); !errors.Is(err, context.DeadlineExceeded) && (!ok || !timeout.Timeout()) {
			t.Fatal(err)
		}
		first.Close()
		second.Close()
		// Canceling this stream must leave the physical session and other streams usable.
		select {
		case <-ma.Done():
			t.Fatal("stream timeout closed physical session")
		default:
		}
		select {
		case <-mb.Done():
			t.Fatal("stream timeout closed physical session")
		default:
		}
	}

	// An authenticated peer cannot open unbounded logical channels.
	for ma.session.NumStreams() != 0 || mb.session.NumStreams() != 0 {
		select {
		case <-ctx.Done():
			t.Fatal("closed streams were not reclaimed")
		case <-time.After(time.Millisecond):
		}
	}
	var opened [][2]*PunchStream
	for range 32 {
		first, err := ma.Open(ctx)
		if err != nil {
			t.Fatal(err)
		}
		second, err := mb.Accept(ctx)
		if err != nil {
			t.Fatal(err)
		}
		opened = append(opened, [2]*PunchStream{first, second})
	}
	if extra, err := ma.Open(ctx); err == nil {
		extra.Close()
		t.Fatal("stream limit ignored")
	}
	if err := opened[0][0].Send(ctx, []byte("still alive")); err != nil {
		t.Fatal(err)
	}
	if raw, err := opened[0][1].Receive(ctx); err != nil || string(raw) != "still alive" {
		t.Fatal(err)
	}
	for _, pair := range opened {
		pair[0].Close()
		pair[1].Close()
	}
}

func TestPunchMuxRejectsUnprovenIdentityInBothTLSRoles(t *testing.T) {
	for _, spoofClient := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		left, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer left.Close()
		right, err := listener.Accept()
		if err != nil {
			t.Fatal(err)
		}
		defer right.Close()
		pubA, keyA, _ := ed25519.GenerateKey(rand.Reader)
		pubB, keyB, _ := ed25519.GenerateKey(rand.Reader)
		if (bytes.Compare(pubA, pubB) < 0) != spoofClient {
			pubA, pubB = pubB, pubA
			keyA, keyB = keyB, keyA
		}
		_, wrongKey, _ := ed25519.GenerateKey(rand.Reader)
		// A announces an authorized key but presents another identity's certificate.
		certA, _ := PeerServerTLS(wrongKey)
		certB, _ := PeerServerTLS(keyB)
		done := make(chan error, 1)
		go func() {
			m, err := NewPunchMux(ctx, right, pubB, certB, func(pub ed25519.PublicKey) bool { return pub.Equal(pubA) })
			if m != nil {
				m.Close()
			}
			done <- err
		}()
		m, err := NewPunchMux(ctx, left, pubA, certA, func(pub ed25519.PublicKey) bool { return pub.Equal(pubB) })
		if m != nil {
			m.Close()
		}
		if err == nil {
			t.Fatal("unverified physical session published by spoofing side")
		}
		if err := <-done; err == nil {
			t.Fatal("announced identity accepted without proof")
		}
	}
}
