package link

import (
	"context"
	"testing"
	"time"
)

// Exercise real probe/liveness state transitions with a clock supplied by the
// test; no sockets, goroutines, sleeps or deployed peers are needed.
func TestIdleProbesAndFailureDetection(t *testing.T) {
	for name, compact := range map[string]bool{"legacy": false, "compact": true} {
		t.Run(name, func(t *testing.T) {
			newLink := func() *Link {
				return &Link{ctx: context.Background(), options: (Options{}).defaults(), control: make(chan []byte, 8), pending: map[uint64]time.Time{}, compactProbes: compact}
			}
			start := time.Unix(1000, 0)
			reply := func(l *Link, now time.Time) {
				t.Helper()
				select {
				case raw := <-l.control:
					wantSize := 9
					if compact {
						wantSize = 3
					}
					if len(raw) != wantSize {
						t.Fatalf("probe size = %d, want %d", len(raw), wantSize)
					}
					raw[0]++
					if !l.handleProbe(raw, now.Add(time.Millisecond)) {
						t.Fatal("valid reply rejected")
					}
				default:
					t.Fatal("expected probe")
				}
			}
			l := newLink()
			for second := 0; second < 60; second++ {
				now := start.Add(time.Duration(second) * time.Second)
				if !l.heartbeat(now) {
					t.Fatal("healthy idle link expired")
				}
				if second%10 == 0 {
					reply(l, now)
				} else if len(l.control) != 0 {
					t.Fatal("redundant idle probe")
				}
				if !l.healthyLocked(now.Add(2 * time.Millisecond)) {
					t.Fatal("healthy link marked offline between idle probes")
				}
			}
			if l.sent != 6 {
				t.Fatalf("idle probes = %d, want 6 per minute", l.sent)
			}
			// User data immediately restores the next active tick; active cadence lasts
			// through a short lull, then returns to idle cadence.
			l.tx.Add(1)
			now := start.Add(60 * time.Second)
			l.heartbeat(now)
			reply(l, now)
			l.heartbeat(now.Add(time.Second))
			reply(l, now.Add(time.Second))
			for second := 62; second <= 70; second++ {
				now = start.Add(time.Duration(second) * time.Second)
				if !l.heartbeat(now) {
					t.Fatal("link expired during active-to-idle transition")
				}
				if second < 70 {
					reply(l, now)
				} else if len(l.control) != 0 {
					t.Fatal("link did not return to idle cadence")
				}
			}
			// A valid retry response restores health after a lost probe.
			l = newLink()
			l.heartbeat(start)
			reply(l, start)
			l.heartbeat(start.Add(10 * time.Second))
			l.heartbeat(start.Add(11 * time.Second))
			l.recordPong(l.nextPing, start.Add(11*time.Second+time.Millisecond))
			if !l.heartbeat(start.Add(16*time.Second)) || !l.healthyLocked(start.Add(16*time.Second)) {
				t.Fatal("lost probe caused failure despite a valid retry reply")
			}
			// An unrecognized pong cannot prevent failure. A missing idle response
			// triggers one-second retries and dies five seconds after that first probe.
			l = newLink()
			l.heartbeat(start)
			reply(l, start)
			for second := 1; second <= 15; second++ {
				now = start.Add(time.Duration(second) * time.Second)
				l.recordPong(999, now)
				alive := l.heartbeat(now)
				if alive != (second < 15) {
					t.Fatalf("liveness at second %d = %v", second, alive)
				}
			}
			if l.healthyLocked(now) || l.sent != 6 {
				t.Fatal("failed link remained healthy or retries were not accelerated")
			}
			// Initial admission must still require a reply and expire after five seconds.
			l = newLink()
			l.heartbeat(start)
			if l.healthyLocked(start) || l.heartbeat(start.Add(5*time.Second)) {
				t.Fatal("unresponsive initial link survived")
			}
		})
	}
}

func TestCompactProbeCompatibility(t *testing.T) {
	l := &Link{ctx: context.Background(), options: (Options{}).defaults(), control: make(chan []byte, 8), pending: map[uint64]time.Time{}}
	start := time.Unix(1000, 0)
	// A new dialer must use legacy probes until the responder confirms support.
	l.heartbeat(start)
	if raw := <-l.control; len(raw) != 9 || raw[0] != pingMessage {
		t.Fatal("sent compact probe before capability confirmation")
	}
	for _, compact := range []bool{false, true} {
		ping := encodePing(123, compact)
		if !l.handleProbe(ping, start) {
			t.Fatal("valid probe rejected")
		}
		pong := <-l.control
		if len(pong) != len(ping) || pong[0] != ping[0]+1 || l.compactProbes != compact {
			t.Fatal("probe echo or capability negotiation failed")
		}
		if !l.lastPong.IsZero() {
			t.Fatal("incoming ping incorrectly established bidirectional health")
		}
	}
	// No nonce reuse at the compact boundary, even under custom probe cadence.
	l.nextPing = 0xfffe
	for _, size := range []int{3, 9} {
		start = start.Add(time.Second)
		l.heartbeat(start)
		ping := <-l.control
		if len(ping) != size {
			t.Fatal("incorrect probe size at nonce boundary")
		}
		if size == 9 {
			l.handleProbe([]byte{compactPongMessage, 0, 0}, start)
			if _, pending := l.pending[0x10000]; !pending {
				t.Fatal("wrapped compact reply acknowledged a new probe")
			}
		}
		ping[0]++
		l.handleProbe(ping, start.Add(time.Millisecond))
		if _, pending := l.pending[l.nextPing]; pending {
			t.Fatal("valid reply did not acknowledge probe")
		}
		l.tx.Add(1)
	}
	for _, raw := range [][]byte{nil, {compactPingMessage}, {compactPongMessage, 1}, {pingMessage, 0, 1}, {compactPingMessage, 0, 1, 2}} {
		if l.handleProbe(raw, start) {
			t.Fatalf("accepted malformed probe %x", raw)
		}
	}
}
