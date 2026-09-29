//go:build freebsd && integration

package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/testutil"
	"github.com/graphwan/graphwan/internal/tunnel"
)

func TestNativeFreeBSDConfigurationReconcile(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("GRAPHWAN_TEST_VM") != "1" {
		t.Skip("requires disposable root FreeBSD VM")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	listener.Close()
	_, identity, _ := ed25519.GenerateKey(rand.Reader)
	r, err := NewDataPlane(context.Background(), identity, DataPlaneOptions{BindHost: "127.0.0.1"})
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
	before := r.state.Load()
	// A real kernel conflict on the second Network must roll back the first
	// Network's completed prefix/MTU update, preserving both old runtimes.
	foreignConfig := tunnel.Config{Address: netip.MustParsePrefix("fd42:67aa::1/64"), MTU: 1280}
	foreign, err := tunnel.Open(foreignConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	rejected := config.Clone()
	rejected.Revision++
	rejected.Networks[0].CIDR = netip.PrefixFrom(rejected.Networks[0].Self.Address, 25).Masked()
	rejected.Networks[0].MTU = 9000
	rejected.Networks[1].CIDR = foreignConfig.Address.Masked()
	rejected.Networks[1].Self.Address = foreignConfig.Address.Addr()
	rejected.Networks[1].Directory[0].Address = foreignConfig.Address.Addr()
	rejected.Networks[1].MTU = 9000
	if err := r.Apply(context.Background(), rejected); err == nil {
		t.Fatal("duplicate IPv6 address accepted")
	}
	if r.state.Load() != before || r.Health() != nil {
		t.Fatal("rejected update changed applied runtime")
	}
	for _, network := range config.Networks {
		d := before.devices[network.ID]
		iface, err := net.InterfaceByName(d.Name())
		if err != nil || iface.MTU != network.MTU {
			t.Fatalf("rollback MTU: %v %v", iface, err)
		}
		addresses, err := iface.Addrs()
		if err != nil {
			t.Fatal(err)
		}
		want := netip.PrefixFrom(network.Self.Address, network.CIDR.Bits()).String()
		if len(addresses) != 1 || addresses[0].String() != want {
			t.Fatalf("rollback address: %v, want %s", addresses, want)
		}
	}
	for _, mtu := range []int{9000, 1280} {
		config.Revision++
		for i := range config.Networks {
			config.Networks[i].MTU = mtu
		}
		if err := r.Apply(context.Background(), config); err != nil {
			t.Fatal(err)
		}
		after := r.state.Load()
		if after.mesh != before.mesh {
			t.Fatal("MTU edit replaced mesh")
		}
		for _, network := range config.Networks {
			d := after.devices[network.ID]
			if d != before.devices[network.ID] {
				t.Fatal("MTU edit replaced TUN")
			}
			iface, err := net.InterfaceByName(d.Name())
			if err != nil || iface.MTU != mtu || d.Configuration().MTU != mtu {
				t.Fatalf("native MTU: %+v %v", iface, err)
			}
		}
		if r.Health() != nil {
			t.Fatal(r.Health())
		}
	}
	for _, pair := range [][2]string{
		{"10.240.33.1/25", "fd42:6779::1/80"},
		{"10.240.33.1/24", "fd42:6779::1/64"},
		{"10.240.36.2/24", "fd42:6783::2/64"},
		{"fd42:6784::1/64", "10.240.37.1/24"},
	} {
		config.Revision++
		for i, address := range pair {
			prefix := netip.MustParsePrefix(address)
			config.Networks[i].CIDR = prefix.Masked()
			config.Networks[i].Self.Address = prefix.Addr()
			config.Networks[i].Directory[0].Address = prefix.Addr()
		}
		if err := r.Apply(context.Background(), config); err != nil {
			t.Fatal(err)
		}
		after := r.state.Load()
		if after.mesh != before.mesh {
			t.Fatal("address update replaced Mesh")
		}
		for i, network := range config.Networks {
			d := after.devices[network.ID]
			if d != before.devices[network.ID] {
				t.Fatal("address update replaced TUN reader")
			}
			iface, err := net.InterfaceByName(d.Name())
			if err != nil {
				t.Fatal(err)
			}
			addresses, err := iface.Addrs()
			if err != nil {
				t.Fatal(err)
			}
			if len(addresses) != 1 || addresses[0].String() != pair[i] {
				t.Fatalf("configured address: %v, want %s", addresses, pair[i])
			}
		}
		// An existing blocked reader must survive the temporary address removal.
		time.Sleep(20 * time.Millisecond)
		if r.Health() != nil {
			t.Fatal(r.Health())
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	for _, d := range before.devices {
		if _, err := net.InterfaceByName(d.Name()); err == nil {
			t.Fatal("Agent shutdown leaked TUN")
		}
	}
}
