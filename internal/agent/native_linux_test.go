//go:build linux && integration

package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
	"github.com/eWloYW8/GraphWAN/internal/tunnel"
	"github.com/vishvananda/netlink"
)

func TestNativeLinuxConfigurationReconcile(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("GRAPHWAN_TEST_NETNS") != "1" {
		t.Skip("requires root in an isolated network namespace with GRAPHWAN_TEST_NETNS=1")
	}
	self, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := os.Readlink("/proc/1/ns/net")
	if err != nil || self == initial {
		t.Fatal("must isolate the host network", err)
	}
	loopback, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(loopback); err != nil {
		t.Fatal(err)
	}
	foreign := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "unrelated-nic"}}
	if err := netlink.LinkAdd(foreign); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = netlink.LinkDel(foreign) })
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	listener.Close()
	_, identity, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var reject atomic.Bool
	ctx := context.Background()
	r, err := NewDataPlane(ctx, identity, DataPlaneOptions{BindHost: "127.0.0.1", TunnelFactory: func(config tunnel.Config) (tunnel.Device, error) {
		// Only the second allocation is rejected. The first prepared TUN and
		// its cleanup, and both preserved old devices, use the real kernel.
		if reject.Load() && config.Address.Addr().Is6() {
			return nil, errors.New("injected second-Network allocation failure")
		}
		return tunnel.Open(config)
	}})
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
	if err := r.Apply(ctx, config); err != nil {
		t.Fatal(err)
	}
	original := r.state.Load()
	check := func() {
		t.Helper()
		state := r.state.Load()
		if state.mesh != original.mesh || r.Health() != nil {
			t.Fatalf("update replaced Mesh or failed runtime: %v", r.Health())
		}
		if len(state.devices) != len(config.Networks) {
			t.Fatal("incorrect per-Network device count")
		}
		for _, network := range config.Networks {
			device := state.devices[network.ID]
			iface, err := net.InterfaceByName(device.Name())
			if err != nil {
				t.Fatal(err)
			}
			want := tunnel.Config{Name: iface.Name, Address: netip.PrefixFrom(network.Self.Address, network.CIDR.Bits()), MTU: network.MTU}
			if device.Configuration() != want || iface.MTU != want.MTU || iface.Flags&net.FlagUp == 0 {
				t.Fatalf("kernel/reported TUN configuration mismatch: %+v %+v, want %+v", iface, device.Configuration(), want)
			}
			addresses, err := iface.Addrs()
			if err != nil {
				t.Fatal(err)
			}
			found := 0
			for _, address := range addresses {
				prefix, err := netip.ParsePrefix(address.String())
				if err != nil {
					t.Fatal(err)
				}
				if prefix.Addr().IsLinkLocalUnicast() {
					continue
				}
				if prefix != want.Address {
					t.Fatalf("unexpected TUN address: %v", prefix)
				}
				found++
			}
			if found != 1 {
				t.Fatalf("configured addresses = %d, want 1", found)
			}
			routes, err := netlink.RouteGet(net.IP(want.Address.Addr().Next().AsSlice()))
			if err != nil || len(routes) != 1 || routes[0].LinkIndex != iface.Index {
				t.Fatalf("kernel selected the wrong overlay route: %+v, %v", routes, err)
			}
		}
		interfaces, err := net.Interfaces()
		if err != nil {
			t.Fatal(err)
		}
		owned := 0
		for _, iface := range interfaces {
			if strings.HasPrefix(iface.Name, "gw") {
				owned++
			}
		}
		if owned != len(config.Networks) {
			t.Fatalf("owned TUNs = %d, want %d; leaked preparation/retirement", owned, len(config.Networks))
		}
		unrelated, err := net.InterfaceByName(foreign.Name)
		if err != nil || unrelated.Index != foreign.Index {
			t.Fatal("unrelated interface changed", err)
		}
	}
	check()
	rejected := config.Clone()
	rejected.Revision++
	for i := range rejected.Networks {
		rejected.Networks[i].MTU = 9000
	}
	rejected.Networks[0].CIDR = netip.MustParsePrefix("10.240.33.0/25")
	reject.Store(true)
	err = r.Apply(ctx, rejected)
	reject.Store(false)
	if err == nil || r.state.Load() != original {
		t.Fatal("rejected update changed applied runtime")
	}
	check()
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
		if err := r.Apply(ctx, config); err != nil {
			t.Fatal(err)
		}
		check()
	}
	before := r.state.Load()
	config.Revision++
	if err := r.Apply(ctx, config); err != nil {
		t.Fatal(err)
	}
	for id, device := range before.devices {
		if r.state.Load().devices[id] != device {
			t.Fatal("unchanged config replaced a healthy TUN")
		}
	}
	for _, count := range []int{1, 0, 2} {
		config.Revision++
		config.Networks = config.Networks[:count]
		if err := r.Apply(ctx, config); err != nil {
			t.Fatal(err)
		}
		check()
	}
	closing := r.state.Load()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	for _, device := range closing.devices {
		if _, err := net.InterfaceByName(device.Name()); err == nil {
			t.Fatal("Agent shutdown leaked a TUN")
		}
	}
	if iface, err := net.InterfaceByName(foreign.Name); err != nil || iface.Index != foreign.Index {
		t.Fatal("Agent shutdown changed the unrelated interface", err)
	}
}
