package transport

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type gatherConn struct {
	net.Conn
	calls atomic.Int32
	err   error
}

type countedWriteConn struct {
	net.Conn
	writes atomic.Int32
}

type framingSink struct{ net.Conn }

func (framingSink) Write(b []byte) (int, error)      { return len(b), nil }
func (framingSink) SetWriteDeadline(time.Time) error { return nil }

type gatherFramingSink struct{ framingSink }

func (gatherFramingSink) WriteBuffers(parts net.Buffers) (int64, error) {
	var size int64
	for _, part := range parts {
		size += int64(len(part))
	}
	return size, nil
}

// Measure transport framing CPU/copies only, excluding crypto and network I/O.
func BenchmarkTransportFraming(b *testing.B) {
	messages := make([][]byte, 32)
	var framed []byte
	for i := range messages {
		messages[i] = bytes.Repeat([]byte{byte(i)}, 1280)
		framed = binary.BigEndian.AppendUint32(framed, uint32(len(messages[i])))
		framed = append(framed, messages[i]...)
	}
	for _, mode := range []string{"copy", "gather", "producer-framed"} {
		b.Run(mode, func(b *testing.B) {
			var conn net.Conn = framingSink{}
			if mode == "gather" {
				conn = gatherFramingSink{}
			}
			sender := NewStream(conn)
			ctx := context.Background()
			b.ReportAllocs()
			b.SetBytes(32 * 1280)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var err error
				if mode == "producer-framed" {
					err = sender.SendFramedBatch(ctx, framed)
				} else {
					err = sender.SendBatch(ctx, messages)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func (c *countedWriteConn) Write(data []byte) (int, error) {
	c.writes.Add(1)
	return c.Conn.Write(data)
}

func TestStreamFramedBatchPreservesWireAndOwnership(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	conn := &countedWriteConn{Conn: a}
	sender, receiver := NewStream(conn), NewStream(b)
	messages := [][]byte{[]byte("first"), bytes.Repeat([]byte("payload"), 1200), []byte("last")}
	var frames []byte
	for _, data := range messages {
		frames = binary.BigEndian.AppendUint32(frames, uint32(len(data)))
		frames = append(frames, data...)
	}
	original := bytes.Clone(frames)
	done := make(chan error, 1)
	go func() { done <- sender.SendFramedBatch(ctx, frames) }()
	for _, want := range messages {
		got, err := receiver.Receive(ctx)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("framed batch changed record bytes", len(got), err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if conn.writes.Load() != 1 || sender.writeBuffer != nil || !bytes.Equal(frames, original) {
		t.Fatal("contiguous batch copied, split or mutated owned framing")
	}
}

func TestStreamFramedBatchRejectsMalformedInputBeforeWrite(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	conn := &countedWriteConn{Conn: a}
	sender := NewStream(conn)
	for _, frames := range [][]byte{nil, {0}, {0, 0, 0, 0}, {0, 0, 0, 2, 1}, binary.BigEndian.AppendUint32(nil, MaxMessage+1), bytes.Repeat([]byte{0, 0, 0, 1, 1}, 129)} {
		if err := sender.SendFramedBatch(context.Background(), frames); err == nil {
			t.Fatal("invalid framed input accepted")
		}
	}
	if conn.writes.Load() != 0 {
		t.Fatal("malformed framing reached the socket")
	}
}

func TestStreamFramedBatchCancellation(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	s := NewStream(a)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.SendFramedBatch(ctx, []byte{0, 0, 0, 1, 1}) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("framed write ignored cancellation")
	}
}

func (c *gatherConn) WriteBuffers(parts net.Buffers) (int64, error) {
	c.calls.Add(1)
	if c.err != nil {
		return 0, c.err
	}
	return parts.WriteTo(c.Conn)
}

func TestStreamWrappedGatherAndFallback(t *testing.T) {
	for _, gather := range []bool{false, true} {
		t.Run(map[bool]string{false: "fallback", true: "gather"}[gather], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			wrapped := &gatherConn{Conn: a}
			var conn net.Conn = a
			if gather {
				conn = wrapped
			}
			sender, receiver := NewStream(conn), NewStream(b)
			messages := [][]byte{[]byte("first"), bytes.Repeat([]byte("payload"), 1200), []byte("last")}
			done := make(chan error, 1)
			go func() { done <- sender.SendBatch(ctx, messages) }()
			for _, want := range messages {
				got, err := receiver.Receive(ctx)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("got %d bytes, err=%v", len(got), err)
				}
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if gather && (wrapped.calls.Load() != 1 || sender.writeBuffer != nil) {
				t.Fatal("wrapped gather path flattened the batch")
			}
			if !gather && len(sender.writeBuffer) == 0 {
				t.Fatal("fallback framing was not exercised")
			}
		})
	}
}

func TestStreamGatherFailureClosesConnection(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	want := io.ErrShortWrite
	s := NewStream(&gatherConn{Conn: a, err: want})
	if err := s.SendBatch(context.Background(), [][]byte{[]byte("data")}); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if _, err := b.Read(make([]byte, 1)); err != io.EOF {
		t.Fatal("failed frame left the underlying connection open", err)
	}
}

func TestStreamGatherCancellation(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	s := NewStream(&gatherConn{Conn: a})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.SendBatch(ctx, [][]byte{[]byte("blocked")}) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("gather write ignored cancellation")
	}
}
