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

	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/testutil"
)

func TestNativeFreeBSDMTUReconcile(t *testing.T) {
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
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	for _, d := range before.devices {
		if _, err := net.InterfaceByName(d.Name()); err == nil {
			t.Fatal("Agent shutdown leaked TUN")
		}
	}
}
