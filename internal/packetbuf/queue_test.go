package packetbuf

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"time"
)

func TestPacketQueueBatchVisibilityAndWrap(t *testing.T) {
	q := NewQueue(512)
	defer q.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		var dst [32]*Buffer
		for batch := range 1000 {
			n, err := q.Read(ctx, dst[:])
			if err != nil {
				result <- err
				return
			}
			if n != 32 {
				result <- errors.New("producer batch fragmented")
				return
			}
			for i, b := range dst[:n] {
				if binary.BigEndian.Uint64(b.Data) != uint64(batch*32+i) {
					result <- errors.New("packet ordering corrupted")
					return
				}
				b.Release()
				dst[i] = nil
			}
			result <- nil
		}
	}()
	for batch := range 1000 {
		var incoming [32]*Buffer
		for i := range incoming {
			incoming[i] = Get(8)
			binary.BigEndian.PutUint64(incoming[i].Data, uint64(batch*32+i))
		}
		if n := q.Put(incoming[:]); n != 0 {
			t.Fatal("unexpected overflow")
		}
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	}
}
func TestPacketQueueOverflowCloseAndCancellation(t *testing.T) {
	q := NewQueue(512)
	// Hold every allocation up front so release observations cannot race reuse.
	buffers := make([]*Buffer, 512+10)
	for i := range buffers {
		buffers[i] = Get(1)
	}
	if dropped := q.Put(buffers); dropped != 10 {
		t.Fatalf("drops=%d", dropped)
	}
	for _, b := range buffers[512:] {
		if b.Data != nil {
			t.Fatal("overflow leaked")
		}
	}
	var dst [3]*Buffer
	if n, err := q.Read(t.Context(), dst[:]); err != nil || n != 3 {
		t.Fatal(n, err)
	}
	q.Close()
	q.Close()
	for _, b := range buffers[3:512] {
		if b.Data != nil {
			t.Fatal("close leaked")
		}
	}
	for _, b := range dst {
		if b.Data == nil {
			t.Fatal("close released consumer-owned buffer")
		}
		b.Release()
	}
	if _, err := q.Read(t.Context(), dst[:]); !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	late := Get(1)
	if q.Put([]*Buffer{late}) != 1 || late.Data != nil {
		t.Fatal("post-close ownership leak")
	}
	empty := NewQueue(512)
	defer empty.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := empty.Read(ctx, dst[:]); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
