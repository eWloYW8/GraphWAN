package transport_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"github.com/eWloYW8/GraphWAN/internal/packetbuf"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/transport"
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

func TestStreamBatchFramingAndBufferReuse(t *testing.T) {
	a, b := net.Pipe()
	left, right := transport.NewStream(a), transport.NewStream(b)
	defer left.Close()
	defer right.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		for round := range 4 {
			messages := make([][]byte, 32)
			for i := range messages {
				messages[i] = bytes.Repeat([]byte{byte(round*32 + i)}, 100+i*83)
			}
			if err := left.SendBatch(ctx, messages); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	var first []byte
	for round := range 4 {
		for i := range 32 {
			raw, err := right.Receive(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, bytes.Repeat([]byte{byte(round*32 + i)}, 100+i*83)) {
				t.Fatal("batch changed a frame boundary or payload")
			}
			if first == nil {
				first = raw
			}
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, make([]byte, 100)) {
		t.Fatal("receive reused a caller-owned frame")
	}
	if err := left.SendBatch(ctx, [][]byte{{1}, nil}); err == nil {
		t.Fatal("accepted an invalid batch")
	}
}

func TestStreamReceiveBatchDoesNotWaitForPartialNextFrame(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	stream := transport.NewStream(b)
	defer stream.Close()
	go func() { a.Write([]byte{0, 0, 0, 3, 1, 2, 3, 0, 0}) }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	packets, err := stream.ReceiveBatch(ctx)
	if err != nil || len(packets) != 1 || !bytes.Equal(packets[0], []byte{1, 2, 3}) {
		t.Fatal("batch waited for incomplete next frame", packets, err)
	}
	partial, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	if _, err := stream.ReceiveBatch(partial); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("partial next frame ignored deadline", err)
	}
}

func TestStreamOwnedBatchSurvivesSubsequentReads(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	left, right := transport.NewStream(a), transport.NewStream(b)
	defer right.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		for i := range 100 {
			if err := left.Send(ctx, bytes.Repeat([]byte{byte(i)}, 1280)); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	var held []*packetbuf.Buffer
	defer func() { packetbuf.ReleaseAll(held) }()
	for len(held) < 100 {
		batch, err := right.ReceiveOwnedBatch(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, batch...)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for i, b := range held {
		if !bytes.Equal(b.Data, bytes.Repeat([]byte{byte(i)}, 1280)) {
			t.Fatal("live packet overwritten by subsequent read")
		}
	}
}
