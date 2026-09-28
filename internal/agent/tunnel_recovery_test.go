package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/testutil"
	"github.com/graphwan/graphwan/internal/tunnel"
)

type recoveryDevice struct {
	config        tunnel.Config
	input, output chan []byte
	done          chan struct{}
	once          sync.Once
	writeFailure  atomic.Bool
	packetFailure atomic.Bool
}

func (d *recoveryDevice) Configuration() tunnel.Config { return d.config }
func (d *recoveryDevice) Name() string                 { return "test" }
func (d *recoveryDevice) Read(raw []byte) (int, error) {
	select {
	case <-d.done:
		return 0, io.EOF
	case input := <-d.input:
		return copy(raw, input), nil
	}
}
func (d *recoveryDevice) Write(raw []byte) (int, error) {
	if d.packetFailure.Load() {
		return 0, errors.New("packet rejected")
	}
	if d.writeFailure.Load() {
		return 0, tunnel.ErrUnavailable
	}
	select {
	case <-d.done:
		return 0, io.ErrClosedPipe
	case d.output <- bytes.Clone(raw):
		return len(raw), nil
	}
}
func (d *recoveryDevice) Close() error { d.once.Do(func() { close(d.done) }); return nil }

func recoveryRuntime(t *testing.T) (*DataPlane, model.Snapshot, *atomic.Bool, *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	listener.Close()
	_, identity, _ := ed25519.GenerateKey(rand.Reader)
	fail, calls := &atomic.Bool{}, &atomic.Int32{}
	r, err := NewDataPlane(context.Background(), identity, DataPlaneOptions{BindHost: "127.0.0.1", Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), TunnelFactory: func(cfg tunnel.Config) (tunnel.Device, error) {
		calls.Add(1)
		if fail.Load() {
			return nil, errors.New("injected creation failure")
		}
		return &recoveryDevice{config: cfg, input: make(chan []byte, 1), output: make(chan []byte, 1), done: make(chan struct{})}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	config := model.Snapshot{Schema: model.SchemaVersion, Revision: 7, AgentID: testutil.ID(10), ListenPort: port}
	for i, address := range []string{"10.42.0.1", "10.43.0.1"} {
		node := model.Node{ID: testutil.ID(30 + i), AgentID: config.AgentID, Name: "local", Address: netip.MustParseAddr(address)}
		config.Networks = append(config.Networks, model.NetworkConfig{ID: testutil.ID(20 + i), Name: address, CIDR: netip.PrefixFrom(node.Address, 24).Masked(), MTU: 1280, Cipher: model.ChaCha20Poly1305, Self: node, Directory: []model.Destination{{NodeID: node.ID, Address: node.Address}}})
	}
	if err := r.Apply(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	return r, config, fail, calls
}
func waitRecovery(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !fn() {
		if time.Now().After(deadline) {
			t.Fatal("recovery condition timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
func checkRecoveryDelivery(t *testing.T, d *recoveryDevice) {
	t.Helper()
	raw := make([]byte, 28)
	raw[0], raw[8], raw[9] = 0x45, 64, 17
	binary.BigEndian.PutUint16(raw[2:4], uint16(len(raw)))
	copy(raw[12:16], d.config.Address.Addr().AsSlice())
	copy(raw[16:20], raw[12:16])
	binary.BigEndian.PutUint16(raw[24:26], 8)
	d.input <- raw
	select {
	case got := <-d.output:
		if !bytes.Equal(got, raw) {
			t.Fatal("corrupt local packet")
		}
	case <-time.After(time.Second):
		t.Fatal("TUN forwarding did not resume")
	}
}

func TestTUNRecoveryPreservesOtherNetworksAndBacksOffFailures(t *testing.T) {
	r, config, fail, calls := recoveryRuntime(t)
	initial := r.state.Load()
	id := config.Networks[0].ID
	old := initial.devices[id]
	healthy := initial.devices[config.Networks[1].ID]
	fail.Store(true)
	old.Close() // The live reader encounters EOF, independently of a control update.
	waitRecovery(t, func() bool { return r.Health() != nil })
	if !strings.Contains(r.Health().Error(), config.Networks[0].Name) {
		t.Fatal("health lost network identity")
	}
	checkRecoveryDelivery(t, healthy.Device.(*recoveryDevice))
	r.repairTunnels(time.Now())
	if calls.Load() != 2 {
		t.Fatal("retried before backoff elapsed")
	}
	r.repairTunnels(time.Now().Add(time.Minute))
	if calls.Load() != 3 || !strings.Contains(r.Health().Error(), "injected creation failure") {
		t.Fatal("failed attempt not reported")
	}
	r.repairTunnels(time.Now())
	if calls.Load() != 3 {
		t.Fatal("failed factory caused busy retry")
	}
	fail.Store(false)
	r.repairTunnels(time.Now().Add(time.Minute))
	next := r.state.Load()
	if r.Health() != nil || calls.Load() != 4 || next.devices[id] == old || next.devices[config.Networks[1].ID] != healthy || next.mesh != initial.mesh || next.router != initial.router || next.snapshot.Revision != config.Revision {
		t.Fatal("recovery changed healthy resources or configuration")
	}
	checkRecoveryDelivery(t, next.devices[id].Device.(*recoveryDevice))
	checkRecoveryDelivery(t, healthy.Device.(*recoveryDevice))
	// Old device errors cannot poison the newly published incarnation.
	r.failTunnel(id, old, errors.New("late obsolete error"))
	if r.Health() != nil {
		t.Fatal("obsolete device damaged recovered health")
	}
}

func TestTUNRecoveryWriteFailureRemovalAndShutdown(t *testing.T) {
	r, config, _, calls := recoveryRuntime(t)
	id := config.Networks[0].ID
	old := r.state.Load().devices[id]
	old.Device.(*recoveryDevice).writeFailure.Store(true)
	if err := r.deliver(context.Background(), id, []byte("packet")); !errors.Is(err, tunnel.ErrUnavailable) {
		t.Fatal(err)
	}
	if r.Health() == nil {
		t.Fatal("fatal write left a healthy device")
	}
	config.Revision++
	config.Networks = config.Networks[1:]
	if err := r.Apply(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	r.repairTunnels(time.Now().Add(time.Minute))
	if r.Health() != nil || calls.Load() != 2 {
		t.Fatal("removed membership resurrected by repair")
	}
	r.Close()
	r.repairTunnels(time.Now().Add(time.Minute))
	if calls.Load() != 2 || r.state.Load() != nil {
		t.Fatal("repair ran after shutdown")
	}
}

func TestApplyingSameConfigurationNeverReusesFailedTUN(t *testing.T) {
	r, config, _, calls := recoveryRuntime(t)
	old := r.state.Load().devices[config.Networks[0].ID]
	old.Close()
	waitRecovery(t, func() bool { return r.Health() != nil })
	if err := r.Apply(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || r.Health() != nil || r.state.Load().devices[config.Networks[0].ID] == old {
		t.Fatal("failed TUN reused")
	}
	checkRecoveryDelivery(t, r.state.Load().devices[config.Networks[0].ID].Device.(*recoveryDevice))
}

func TestFatalTUNWriteDoesNotWaitForConfigurationRetirement(t *testing.T) {
	r, config, _, _ := recoveryRuntime(t)
	id := config.Networks[0].ID
	r.state.Load().devices[id].Device.(*recoveryDevice).writeFailure.Store(true)
	// Apply owns this lock while Mesh.Close waits for in-flight packet callbacks.
	// A failed delivery must finish without taking it, otherwise both deadlock.
	r.applyMu.Lock()
	done := make(chan error, 1)
	go func() { done <- r.deliver(context.Background(), id, []byte("packet")) }()
	select {
	case err := <-done:
		r.applyMu.Unlock()
		if !errors.Is(err, tunnel.ErrUnavailable) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		r.applyMu.Unlock()
		t.Fatal("packet failure blocked on configuration retirement")
	}
}

func TestRejectedTUNPacketDoesNotReplaceDevice(t *testing.T) {
	r, config, _, calls := recoveryRuntime(t)
	id := config.Networks[0].ID
	old := r.state.Load().devices[id]
	device := old.Device.(*recoveryDevice)
	device.packetFailure.Store(true)
	if err := r.deliver(context.Background(), id, []byte("rejected packet")); err == nil {
		t.Fatal("write error hidden")
	}
	r.repairTunnels(time.Now().Add(time.Minute))
	if r.Health() != nil || calls.Load() != 2 || r.state.Load().devices[id] != old {
		t.Fatal("packet rejection recreated the device")
	}
	device.packetFailure.Store(false)
	checkRecoveryDelivery(t, device)
}
