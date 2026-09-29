//go:build windows && integration

package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"net/netip"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
	"github.com/eWloYW8/GraphWAN/internal/tunnel"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// Inject only the second Network's rejection; both devices, their readers and
// the first Network's configuration/rollback use the real Windows kernel.
type rejectingWindowsDevice struct {
	tunnel.Device
	reject *atomic.Bool
}

func (d *rejectingWindowsDevice) Reconfigure(config tunnel.Config) error {
	if d.reject.Load() && config.Address.Addr().Is6() {
		return errors.New("injected second-Network update failure")
	}
	return d.Device.(tunnel.Reconfigurable).Reconfigure(config)
}

func TestNativeWindowsConfigurationReconcile(t *testing.T) {
	if os.Getenv("GRAPHWAN_TEST_WINDOWS") != "1" {
		t.Skip("requires GRAPHWAN_TEST_WINDOWS=1 on a disposable elevated Windows host")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Fatal("requires an elevated administrator token")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	listener.Close()
	_, identity, _ := ed25519.GenerateKey(rand.Reader)
	var reject atomic.Bool
	r, err := NewDataPlane(context.Background(), identity, DataPlaneOptions{
		BindHost: "127.0.0.1",
		TunnelFactory: func(config tunnel.Config) (tunnel.Device, error) {
			device, err := tunnel.Open(config)
			if err != nil {
				return nil, err
			}
			return &rejectingWindowsDevice{Device: device, reject: &reject}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	config := model.Snapshot{Schema: model.SchemaVersion, Revision: 1, AgentID: testutil.ID(10), ListenPort: port}
	for i, address := range []string{"10.240.33.1/24", "fd42:6779::1/64"} {
		prefix := netip.MustParsePrefix(address)
		node := model.Node{ID: testutil.ID(30 + i), AgentID: config.AgentID, Name: "native", Address: prefix.Addr()}
		config.Networks = append(config.Networks, model.NetworkConfig{ID: testutil.ID(20 + i), Name: address, CIDR: prefix.Masked(), MTU: 1280, Cipher: model.ChaCha20Poly1305, Self: node, Directory: []model.Destination{{NodeID: node.ID, Address: node.Address}}})
	}
	if err := r.Apply(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	original := r.state.Load()
	check := func(snapshot model.Snapshot) {
		t.Helper()
		state := r.state.Load()
		if state.mesh != original.mesh || r.Health() != nil {
			t.Fatalf("update replaced Mesh or failed runtime: %v", r.Health())
		}
		for _, network := range snapshot.Networks {
			device := state.devices[network.ID]
			if device != original.devices[network.ID] {
				t.Fatal("update replaced live TUN reader")
			}
			want := tunnel.Config{Name: device.Name(), Address: netip.PrefixFrom(network.Self.Address, network.CIDR.Bits()), MTU: network.MTU}
			checkWindowsKernelConfig(t, device, want)
		}
	}
	check(config)
	rejected := config.Clone()
	rejected.Revision++
	rejected.Networks[0].CIDR = netip.MustParsePrefix("10.240.33.0/25")
	rejected.Networks[0].MTU = 9000
	rejected.Networks[1].MTU = 9000
	reject.Store(true)
	err = r.Apply(context.Background(), rejected)
	reject.Store(false)
	if err == nil || r.state.Load() != original {
		t.Fatal("rejected update changed applied state")
	}
	check(config)
	for i, pair := range [][2]string{
		{"10.240.33.1/25", "fd42:6779::1/80"},
		{"10.240.33.1/24", "fd42:6779::1/64"},
		{"10.240.36.2/24", "fd42:6783::2/64"},
		{"fd42:6784::1/64", "10.240.37.1/24"},
	} {
		config.Revision++
		for j, address := range pair {
			prefix := netip.MustParsePrefix(address)
			config.Networks[j].CIDR = prefix.Masked()
			config.Networks[j].Self.Address = prefix.Addr()
			config.Networks[j].Directory[0].Address = prefix.Addr()
			config.Networks[j].MTU = []int{9000, 1280}[i%2]
		}
		if err := r.Apply(context.Background(), config); err != nil {
			t.Fatal(err)
		}
		check(config)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	for _, device := range original.devices {
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := net.InterfaceByName(device.Name()); err != nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("Agent shutdown leaked owned Wintun adapter")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func checkWindowsKernelConfig(t *testing.T, device tunnel.Device, want tunnel.Config) {
	t.Helper()
	if device.Configuration() != want {
		t.Fatalf("reported configuration: %+v, want %+v", device.Configuration(), want)
	}
	iface, err := net.InterfaceByName(device.Name())
	if err != nil {
		t.Fatal(err)
	}
	luid, err := winipcfg.LUIDFromIndex(uint32(iface.Index))
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range []winipcfg.AddressFamily{windows.AF_INET, windows.AF_INET6} {
		row, err := luid.IPInterface(family)
		if err != nil || row.NLMTU != uint32(want.MTU) {
			t.Fatalf("kernel MTU: %+v %v", row, err)
		}
	}
	rows, err := winipcfg.GetUnicastIPAddressTable(windows.AF_UNSPEC)
	if err != nil {
		t.Fatal(err)
	}
	var addresses []netip.Prefix
	for _, row := range rows {
		if row.InterfaceLUID == luid && !row.Address.Addr().IsLinkLocalUnicast() {
			addresses = append(addresses, netip.PrefixFrom(row.Address.Addr(), int(row.OnLinkPrefixLength)))
		}
	}
	if len(addresses) != 1 || addresses[0] != want.Address {
		t.Fatalf("kernel addresses: %v, want %s", addresses, want.Address)
	}
}
