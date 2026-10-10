package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/routing"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
	"github.com/eWloYW8/GraphWAN/internal/tunnel"
)

type packetTestTUN struct {
	config  tunnel.Config
	in, out chan []byte
	done    chan struct{}
	once    sync.Once
}

func (d *packetTestTUN) Name() string                 { return "packet-test" }
func (d *packetTestTUN) Configuration() tunnel.Config { return d.config }
func (d *packetTestTUN) Read(b []byte) (int, error) {
	select {
	case p := <-d.in:
		return copy(b, p), nil
	case <-d.done:
		return 0, net.ErrClosed
	}
}
func (d *packetTestTUN) Write(b []byte) (int, error) {
	select {
	case d.out <- bytes.Clone(b):
		return len(b), nil
	case <-d.done:
		return 0, net.ErrClosed
	}
}
func (d *packetTestTUN) Close() error { d.once.Do(func() { close(d.done) }); return nil }

// Count physical connections independently of Mesh's logical Link reporting.
func countingPacketProxy(t *testing.T, upstream string) (string, *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var count atomic.Int32
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			incoming, err := listener.Accept()
			if err != nil {
				return
			}
			count.Add(1)
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer incoming.Close()
				outgoing, err := (&net.Dialer{}).DialContext(ctx, "tcp4", upstream)
				if err != nil {
					return
				}
				defer outgoing.Close()
				stop := context.AfterFunc(ctx, func() { incoming.Close(); outgoing.Close() })
				defer stop()
				done := make(chan struct{})
				go func() {
					io.Copy(outgoing, incoming)
					outgoing.(*net.TCPConn).CloseWrite()
					close(done)
				}()
				io.Copy(incoming, outgoing)
				incoming.(*net.TCPConn).CloseWrite()
				<-done
			}()
		}
	}()
	t.Cleanup(func() { cancel(); listener.Close(); workers.Wait() })
	return listener.Addr().String(), &count
}

func TestTCPPacketsReuseConvergedTunnels(t *testing.T) {
	for _, carrier := range []model.Transport{model.TCP, model.UDP} {
		t.Run(string(carrier), func(t *testing.T) { testTCPPacketsReuseConvergedTunnels(t, carrier) })
	}
}

func testTCPPacketsReuseConvergedTunnels(t *testing.T, carrier model.Transport) {
	state := testutil.Topology()
	state.Agents = state.Agents[:3]
	state.Networks[0].Nodes = state.Networks[0].Nodes[:3]
	state.Networks[0].Edges = state.Networks[0].Edges[:2]
	for i := range state.Agents {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		state.Agents[i].ListenPort = uint16(listener.Addr().(*net.TCPAddr).Port)
		listener.Close()
		state.Agents[i].Endpoints = nil
	}
	entrance := fmt.Sprintf("127.0.0.1:%d", state.Agents[1].ListenPort)
	var connections *atomic.Int32
	if carrier == model.TCP {
		entrance, connections = countingPacketProxy(t, entrance)
	}
	// Only B advertises a reachable entrance. A and C must both dial out;
	// B-to-C application traffic must reuse C's already admitted tunnel.
	state.Agents[1].Endpoints = []model.Endpoint{{ID: testutil.ID(31), Transport: carrier, Source: model.Manual, URL: string(carrier) + "://" + entrance}}
	for i := range state.Networks[0].Edges {
		state.Networks[0].Edges[i].Transports = []model.Transport{carrier}
		state.Networks[0].Edges[i].Methods = model.ConnectionMethods{IPv4Direct: true}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var agents [3]*DataPlane
	var devices [3]*packetTestTUN
	for _, i := range []int{1, 0, 2} {
		seed := make([]byte, ed25519.SeedSize)
		seed[0] = byte(i + 1)
		agent, err := NewDataPlane(ctx, ed25519.NewKeyFromSeed(seed), DataPlaneOptions{
			BindHost: "127.0.0.1",
			TunnelFactory: func(config tunnel.Config) (tunnel.Device, error) {
				devices[i] = &packetTestTUN{config: config, in: make(chan []byte, 128), out: make(chan []byte, 128), done: make(chan struct{})}
				return devices[i], nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		agents[i] = agent
		t.Cleanup(func() { agent.Close() })
		snapshot, err := routing.Compile(state, state.Agents[i].ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := agent.Apply(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		ready := true
		for i, agent := range agents {
			active := 0
			for _, status := range agent.Report() {
				if status.Active && status.Healthy {
					active++
				}
			}
			want := 1
			if i == 1 {
				want = 2
			}
			ready = ready && active == want
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("one-way tunnels did not converge")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var before int32
	if connections != nil {
		before = connections.Load()
		if before != 2 {
			t.Fatalf("expected two converged physical tunnels, got %d", before)
		}
	}
	for _, pair := range [][2]int{{0, 2}, {2, 0}} {
		from, to := pair[0], pair[1]
		for i := range 32 {
			flags := []byte{0x02, 0x12, 0x10, 0x18, 0x11, 0x04}[i%6]
			raw := transparentTCPPacket(state.Networks[0].Nodes[from].Address, state.Networks[0].Nodes[to].Address, uint16(30000+i), flags)
			devices[from].in <- raw
			select {
			case got := <-devices[to].out:
				// Transit forwarding delivers the inner packet unchanged.
				if !bytes.Equal(got, raw) {
					t.Fatalf("TCP headers/options/payload changed for flags %x", flags)
				}
			case <-time.After(time.Second):
				t.Fatal("a new TCP tuple did not use the converged tunnel promptly")
			}
		}
	}
	if connections != nil {
		if got := connections.Load(); got != before {
			t.Fatalf("business TCP packets opened %d additional physical connections", got-before)
		}
	}
	select {
	case <-devices[1].out:
		t.Fatal("transit TCP was injected into the intermediate TUN")
	default:
	}
}

func packetTestChecksum(b []byte) uint16 {
	var sum uint32
	for len(b) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(b))
		b = b[2:]
	}
	if len(b) != 0 {
		sum += uint32(b[0]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

func transparentTCPPacket(source, destination netip.Addr, port uint16, flags byte) []byte {
	payload := []byte("unchanged TCP application payload")
	raw := make([]byte, 20+28+len(payload))
	raw[0], raw[8], raw[9] = 0x45, 64, 6
	binary.BigEndian.PutUint16(raw[2:4], uint16(len(raw)))
	a, b := source.As4(), destination.As4()
	copy(raw[12:16], a[:])
	copy(raw[16:20], b[:])
	tcp := raw[20:]
	binary.BigEndian.PutUint16(tcp[0:2], port)
	binary.BigEndian.PutUint16(tcp[2:4], 443)
	binary.BigEndian.PutUint32(tcp[4:8], 0xf1234567+uint32(port))
	binary.BigEndian.PutUint32(tcp[8:12], 0x12345678)
	tcp[12], tcp[13] = 7<<4, flags
	binary.BigEndian.PutUint16(tcp[14:16], 4096)
	copy(tcp[20:28], []byte{2, 4, 4, 176, 1, 3, 3, 7})
	copy(tcp[28:], payload)
	pseudo := append(bytes.Clone(raw[12:20]), 0, 6, byte(len(tcp)>>8), byte(len(tcp)))
	binary.BigEndian.PutUint16(tcp[16:18], packetTestChecksum(append(pseudo, tcp...)))
	binary.BigEndian.PutUint16(raw[10:12], packetTestChecksum(raw[:20]))
	return raw
}
