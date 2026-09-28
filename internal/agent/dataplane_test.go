package agent_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/agent"
	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/routing"
	"github.com/graphwan/graphwan/internal/testutil"
	"github.com/graphwan/graphwan/internal/tunnel"
)

type memoryTunnel struct {
	config  tunnel.Config
	read    chan []byte
	written chan []byte
	closed  chan struct{}
	once    sync.Once
}

func newMemoryTunnel(config tunnel.Config) *memoryTunnel {
	return &memoryTunnel{config: config, read: make(chan []byte, 16), written: make(chan []byte, 16), closed: make(chan struct{})}
}
func (t *memoryTunnel) Name() string                 { return "memory" }
func (t *memoryTunnel) Configuration() tunnel.Config { return t.config }
func (t *memoryTunnel) Read(buf []byte) (int, error) {
	select {
	case <-t.closed:
		return 0, io.EOF
	case raw := <-t.read:
		if len(raw) > len(buf) {
			return 0, io.ErrShortBuffer
		}
		return copy(buf, raw), nil
	}
}
func (t *memoryTunnel) Write(raw []byte) (int, error) {
	select {
	case <-t.closed:
		return 0, io.ErrClosedPipe
	case t.written <- bytes.Clone(raw):
		return len(raw), nil
	}
}
func (t *memoryTunnel) Close() error { t.once.Do(func() { close(t.closed) }); return nil }

func availablePorts(t *testing.T, n int) []uint16 {
	t.Helper()
	listeners := []net.Listener{}
	defer func() {
		for _, l := range listeners {
			l.Close()
		}
	}()
	ports := []uint16{}
	for range n {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, l)
		ports = append(ports, uint16(l.Addr().(*net.TCPAddr).Port))
	}
	return ports
}
func runtimePacket(source, destination byte) []byte {
	raw := make([]byte, 28)
	raw[0] = 0x45
	raw[8] = 64
	raw[9] = 17
	binary.BigEndian.PutUint16(raw[2:4], 28)
	copy(raw[12:20], []byte{10, 42, 0, source, 10, 42, 0, destination})
	binary.BigEndian.PutUint16(raw[24:26], 8)
	return raw
}
func TestDataPlaneAutomaticallyConnectsAndForwards(t *testing.T) {
	state := testutil.Topology()
	state.Agents = state.Agents[:3]
	state.Networks[0].Nodes = state.Networks[0].Nodes[:3]
	state.Networks[0].Edges = state.Networks[0].Edges[:2]
	ports := availablePorts(t, 3)
	for i := range state.Agents {
		a := &state.Agents[i]
		a.ListenPort = ports[i]
		address := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(ports[i])))
		a.Endpoints = []model.Endpoint{{ID: testutil.ID(100 + i*2), Transport: model.UDP, Source: model.Manual, URL: "udp://" + address}, {ID: testutil.ID(101 + i*2), Transport: model.TCP, Source: model.Manual, URL: "tcp://" + address}}
	}
	for i := range state.Networks[0].Edges {
		state.Networks[0].Edges[i].Methods = model.ConnectionMethods{IPv4Direct: true}
	}
	runtimes := []*agent.DataPlane{}
	devices := []*memoryTunnel{}
	defer func() {
		for _, runtime := range runtimes {
			runtime.Close()
		}
	}()
	for i := range 3 {
		seed := make([]byte, ed25519.SeedSize)
		seed[0] = byte(i + 1)
		key := ed25519.NewKeyFromSeed(seed)
		runtime, err := agent.NewDataPlane(context.Background(), key, agent.DataPlaneOptions{BindHost: "127.0.0.1", Logger: quiet(), TunnelFactory: func(config tunnel.Config) (tunnel.Device, error) {
			device := newMemoryTunnel(config)
			devices = append(devices, device)
			return device, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		runtimes = append(runtimes, runtime)
		snapshot, err := routing.Compile(state, state.Agents[i].ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := runtime.Apply(context.Background(), snapshot); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool {
		selected := map[model.ID]string{}
		counts := map[model.ID]int{}
		for _, runtime := range runtimes {
			for _, s := range runtime.Report() {
				if s.Active {
					if prior := selected[s.EdgeID]; prior != "" && prior != s.LinkID {
						return false
					}
					selected[s.EdgeID] = s.LinkID
					counts[s.EdgeID]++
				}
			}
		}
		for _, edge := range state.Networks[0].Edges {
			if counts[edge.ID] != 2 {
				return false
			}
		}
		return true
	})
	for _, direction := range [][2]int{{0, 2}, {2, 0}} {
		raw := runtimePacket(byte(direction[0]+1), byte(direction[1]+1))
		devices[direction[0]].read <- raw
		select {
		case got := <-devices[direction[1]].written:
			if !bytes.Equal(got, raw) {
				t.Fatal("virtual packet corrupted")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("multi-hop runtime forwarding timed out")
		}
	}
	// A graph weight-only change must preserve authenticated peer connections.
	before := map[string]bool{}
	for _, stat := range runtimes[0].Report() {
		if stat.Healthy {
			before[stat.LinkID] = true
		}
	}
	state.Revision++
	state.Networks[0].Edges[0].Weight++
	updated, err := routing.Compile(state, state.Agents[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimes[0].Apply(context.Background(), updated); err != nil {
		t.Fatal(err)
	}
	retained := 0
	for _, stat := range runtimes[0].Report() {
		if before[stat.LinkID] && stat.Healthy {
			retained++
		}
	}
	if retained == 0 {
		t.Fatal("route-only update destroyed live links")
	}
	// Removing membership owns and cleans up its own interface.
	updated.Revision++
	updated.Networks = nil
	if err := runtimes[0].Apply(context.Background(), updated); err != nil {
		t.Fatal(err)
	}
	select {
	case <-devices[0].closed:
	default:
		t.Fatal("removed TUN not closed")
	}
	if len(runtimes[0].Report()) != 0 {
		t.Fatal("removed edges retained links")
	}
}
func TestDataPlanePreparationFailureKeepsOldResources(t *testing.T) {
	cache, _ := registeredCache(t)
	defer cache.Close()
	port := availablePorts(t, 1)[0]
	var device *memoryTunnel
	failCreate := false
	runtime, err := agent.NewDataPlane(context.Background(), cache.PrivateKey(), agent.DataPlaneOptions{BindHost: "127.0.0.1", Logger: quiet(), TunnelFactory: func(config tunnel.Config) (tunnel.Device, error) {
		if failCreate {
			return nil, errors.New("injected TUN failure")
		}
		device = newMemoryTunnel(config)
		return device, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	initial := snapshot(t, 7)
	initial.ListenPort = port
	if err := runtime.Apply(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	failCreate = true
	changed := initial.Clone()
	changed.Revision++
	changed.Networks[0].MTU++
	if err := runtime.Apply(context.Background(), changed); err == nil {
		t.Fatal("resource preparation failure swallowed")
	}
	select {
	case <-device.closed:
		t.Fatal("failed update closed previous TUN")
	default:
	}
	// Existing loop still handles local delivery using the old forwarding table.
	raw := runtimePacket(1, 1)
	device.read <- raw
	select {
	case got := <-device.written:
		if !bytes.Equal(got, raw) {
			t.Fatal("old forwarding changed")
		}
	case <-time.After(time.Second):
		t.Fatal("old runtime stopped after failed update")
	}
}
