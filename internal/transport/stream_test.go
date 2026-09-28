package transport_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/transport"
)

func TestStreamConcurrentFrames(t *testing.T) {
	a, b := net.Pipe()
	left, right := transport.NewStream(a), transport.NewStream(b)
	defer left.Close()
	defer right.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			if err := left.Send(ctx, bytes.Repeat([]byte{byte(i)}, 1024)); err != nil {
				t.Error(err)
			}
		})
	}
	seen := map[byte]bool{}
	for range 32 {
		payload, err := right.Receive(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(payload) != 1024 || !bytes.Equal(payload, bytes.Repeat(payload[:1], 1024)) {
			t.Fatal("interleaved frames")
		}
		seen[payload[0]] = true
	}
	wg.Wait()
	if len(seen) != 32 {
		t.Fatal("lost concurrent frames")
	}
}
func TestStreamBoundsAndCancellation(t *testing.T) {
	t.Run("bounds", func(t *testing.T) {
		a, b := net.Pipe()
		left, right := transport.NewStream(a), transport.NewStream(b)
		defer left.Close()
		defer right.Close()
		done := make(chan error, 1)
		go func() {
			var prefix [4]byte
			binary.BigEndian.PutUint32(prefix[:], transport.MaxMessage+1)
			_, err := a.Write(prefix[:])
			done <- err
		}()
		if _, err := right.Receive(context.Background()); err == nil {
			t.Fatal("unbounded frame accepted")
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		a, b := net.Pipe()
		left, right := transport.NewStream(a), transport.NewStream(b)
		defer left.Close()
		defer right.Close()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { _, err := right.Receive(ctx); done <- err }()
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("receive ignored cancellation")
		}
	})
	t.Run("partial", func(t *testing.T) {
		a, b := net.Pipe()
		right := transport.NewStream(b)
		defer a.Close()
		defer right.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := a.Write([]byte{0, 0}); done <- err }()
		if _, err := right.Receive(ctx); err == nil {
			t.Fatal("partial frame succeeded")
		}
		<-done
		// The stream cannot resume safely after consuming half a length prefix.
		if _, err := right.Receive(context.Background()); err == nil {
			t.Fatal("partial frame stream not closed")
		}
	})
}
