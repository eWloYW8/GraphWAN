package mesh

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/testutil"
)

// A reachable TCP port whose application never answers must not cause unlimited
// concurrent handshakes or permanently starve later configured endpoints.
func TestSchedulerBoundsStalledDialsAndTriesRemainingEndpoints(t *testing.T) {
	ctx, meshes, state, _ := webMeshes(t)
	state.Networks[0].Edges[0].Transports = []model.Transport{model.TCP}
	state.Agents[0].Endpoints = nil // Only one dialing direction.
	state.Agents[1].Endpoints = nil
	type accepted struct {
		endpoint model.ID
		conn     net.Conn
	}
	incoming := make(chan accepted, 128)
	stopped := make(chan struct{})
	var listeners []net.Listener
	var sockets []net.Conn
	var workers sync.WaitGroup
	t.Cleanup(func() {
		close(stopped)
		for _, listener := range listeners {
			listener.Close()
		}
		workers.Wait()
		for _, conn := range sockets {
			conn.Close()
		}
		for {
			select {
			case event := <-incoming:
				event.conn.Close()
			default:
				return
			}
		}
	})
	for i := range 16 {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, listener)
		endpoint := model.Endpoint{ID: testutil.ID(160 + i), Source: model.Manual, Transport: model.TCP, URL: "tcp://" + listener.Addr().String()}
		state.Agents[1].Endpoints = append(state.Agents[1].Endpoints, endpoint)
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				select {
				case incoming <- accepted{endpoint.ID, conn}:
				case <-stopped:
					conn.Close()
					return
				}
			}
		}()
	}
	applyWebState(t, state, meshes)
	next := func() accepted {
		t.Helper()
		select {
		case event := <-incoming:
			sockets = append(sockets, event.conn)
			return event
		case <-ctx.Done():
			t.Fatal("remaining endpoint was starved")
			return accepted{}
		}
	}
	first := make([]accepted, 8)
	seen := map[model.ID]bool{}
	for i := range first {
		first[i] = next()
		if seen[first[i].endpoint] {
			t.Fatal("duplicate attempt while its handshake was pending")
		}
		seen[first[i].endpoint] = true
	}
	select {
	case event := <-incoming:
		event.conn.Close()
		t.Fatal("more than eight concurrent outgoing handshakes")
	case <-time.After(500 * time.Millisecond):
	}
	closed := map[model.ID]time.Time{}
	for _, event := range first {
		closed[event.endpoint] = time.Now()
		event.conn.Close()
	}
	// Scheduling can be delayed by the host, so retries may interleave with new
	// candidates. Demand eventual coverage, without depending on timer ordering.
	retried := false
	for len(seen) < 16 || !retried {
		event := next()
		if previous, ok := closed[event.endpoint]; ok {
			if time.Since(previous) < 500*time.Millisecond {
				t.Fatal("failed dial bypassed minimum retry backoff")
			}
			retried = true
		}
		seen[event.endpoint] = true
		closed[event.endpoint] = time.Now()
		event.conn.Close()
	}
	// Removing the policy cancels every outstanding dial and releases all slots.
	state.Agents[1].Endpoints = nil
	applyWebState(t, state, meshes)
	waitWeb(t, ctx, func() bool { return len(meshes[0].slots) == 0 })
}
