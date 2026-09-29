package tunnel

import (
	"bytes"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testRing struct {
	receiving     chan struct{}
	resumeReceive chan struct{}
	closed        chan struct{}
	input         []byte
	released      atomic.Int32
	ended         atomic.Bool
	closeCount    atomic.Int32
	sends         atomic.Int32
	allocated     atomic.Bool
	full          bool
	activeSend    chan struct{}
	resumeSend    chan struct{}
}

func (r *testRing) receive() ([]byte, error) {
	if r.ended.Load() {
		panic("receive after session end")
	}
	if r.receiving != nil {
		close(r.receiving)
		<-r.resumeReceive
	}
	if r.input == nil {
		return nil, errRingEmpty
	}
	return r.input, nil
}
func (r *testRing) release([]byte) {
	if r.ended.Load() {
		panic("release after session end")
	}
	r.released.Add(1)
}
func (r *testRing) allocate(n int) ([]byte, error) {
	if r.ended.Load() {
		panic("allocate after session end")
	}
	if r.full {
		return nil, errors.New("ring full")
	}
	if !r.allocated.CompareAndSwap(false, true) {
		panic("concurrent allocation was not serialized")
	}
	return make([]byte, n), nil
}
func (r *testRing) send(packet []byte) {
	if r.activeSend != nil {
		close(r.activeSend)
		<-r.resumeSend
	}
	if r.ended.Load() {
		panic("send after session end")
	}
	if !bytes.Equal(packet, []byte{0x45, 1, 2, 3}) {
		panic("packet was corrupted")
	}
	r.sends.Add(1)
	r.allocated.Store(false)
}
func (r *testRing) wait() error { <-r.closed; return os.ErrClosed }
func (r *testRing) wake() error { close(r.closed); return nil }
func (r *testRing) close() error {
	if r.allocated.Load() {
		panic("session ended with packet storage in use")
	}
	r.ended.Store(true)
	r.closeCount.Add(1)
	return nil
}

func TestRingReadReleasesPacket(t *testing.T) {
	for _, size := range []int{2, 4} {
		r := &testRing{closed: make(chan struct{}), input: []byte{0x45, 1, 2, 3}}
		d := &ringDevice{ring: r}
		buf := make([]byte, size)
		n, err := d.Read(buf)
		if size == 2 {
			if n != 0 || !errors.Is(err, io.ErrShortBuffer) {
				t.Fatalf("truncated packet accepted: %d %v", n, err)
			}
		} else if err != nil || n != 4 || !bytes.Equal(buf, r.input) {
			t.Fatalf("packet read: %d %v", n, err)
		}
		if r.released.Load() != 1 {
			t.Fatal("receive ring packet leaked")
		}
		d.Close()
	}
}

func TestRingCloseDuringEmptyReceive(t *testing.T) {
	r := &testRing{closed: make(chan struct{}), receiving: make(chan struct{}), resumeReceive: make(chan struct{})}
	d := &ringDevice{ring: r}
	read := make(chan error, 1)
	go func() { _, err := d.Read(make([]byte, 9000)); read <- err }()
	<-r.receiving
	closed := make(chan error, 1)
	go func() { closed <- d.Close() }()
	<-r.closed // Close is already signaled before the driver's empty result.
	if r.ended.Load() {
		t.Fatal("session ended during an active receive")
	}
	close(r.resumeReceive)
	select {
	case err := <-read:
		if !errors.Is(err, os.ErrClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("lost close wakeup")
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil || r.closeCount.Load() != 1 {
		t.Fatal("close was not idempotent")
	}
	if _, err := d.Read(make([]byte, 10)); !errors.Is(err, os.ErrClosed) {
		t.Fatal("read accepted after close")
	}
	if _, err := d.Write([]byte{0x45, 1, 2, 3}); !errors.Is(err, os.ErrClosed) {
		t.Fatal("write accepted after close")
	}
}

func TestRingCloseWaitsForPacketStorage(t *testing.T) {
	r := &testRing{closed: make(chan struct{}), activeSend: make(chan struct{}), resumeSend: make(chan struct{})}
	d := &ringDevice{ring: r}
	written := make(chan error, 1)
	go func() { _, err := d.Write([]byte{0x45, 1, 2, 3}); written <- err }()
	<-r.activeSend
	closed := make(chan error, 1)
	go func() { closed <- d.Close() }()
	<-r.closed
	if r.ended.Load() {
		t.Fatal("mapped packet storage freed during send")
	}
	close(r.resumeSend)
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}

func TestRingConcurrentWritesAndClose(t *testing.T) {
	r := &testRing{closed: make(chan struct{})}
	d := &ringDevice{ring: r}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			for range 100 {
				_, err := d.Write([]byte{0x45, 1, 2, 3})
				if err != nil && !errors.Is(err, os.ErrClosed) {
					t.Error(err)
				}
			}
		})
	}
	wg.Go(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	wg.Wait()
	if r.closeCount.Load() != 1 {
		t.Fatal("session not closed exactly once")
	}
}

func TestRingCongestionIsPerPacket(t *testing.T) {
	r := &testRing{closed: make(chan struct{}), full: true}
	d := &ringDevice{ring: r}
	defer d.Close()
	if _, err := d.Write([]byte{0x45, 1, 2, 3}); err == nil || errors.Is(err, ErrUnavailable) {
		t.Fatal("congestion marked unavailable")
	}
	r.full = false
	if n, err := d.Write([]byte{0x45, 1, 2, 3}); n != 4 || err != nil {
		t.Fatal("write did not recover after congestion")
	}
}

func TestInterfaceSettingRollback(t *testing.T) {
	for _, failRestore := range []bool{false, true} {
		mtu := 1280
		err := changeSettings([]settingChange{
			{apply: func() error { mtu = 9000; return nil }, restore: func() error {
				if failRestore {
					return errors.New("rollback failed")
				}
				mtu = 1280
				return nil
			}},
			{apply: func() error { return errors.New("second family failed") }, restore: func() error { t.Fatal("unapplied setting restored"); return nil }},
		})
		if err == nil || errors.Is(err, ErrUnavailable) != failRestore {
			t.Fatalf("rollback error: %v", err)
		}
		if !failRestore && mtu != 1280 {
			t.Fatal("first family's MTU not restored")
		}
	}
}
