package wgaccess

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/packet"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
	"github.com/eWloYW8/GraphWAN/internal/transport"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
)

// Standard WireGuard clients exercise the real shared UDP/QUIC socket, network
// isolation, source admission, session retention and revocation without OS TUNs.
func TestSharedPortWireGuard(t *testing.T) {
	_, identity, _ := ed25519.GenerateKey(rand.Reader)
	udp, _, err := transport.ListenUDPQUIC("127.0.0.1:0", identity)
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	incoming := make(chan struct {
		network model.ID
		raw     []byte
	}, 16)
	h := New(context.Background(), identity, uint16(udp.LocalAddr().(*net.UDPAddr).Port), udp.SendWireGuard, func(n model.ID, raw []byte) {
		incoming <- struct {
			network model.ID
			raw     []byte
		}{n, bytes.Clone(raw)}
	})
	defer h.Close()
	udp.SetWireGuardReceiver(h.Receive)
	var snapshot model.Snapshot
	var clients []*device.Device
	var tunnels []*memoryTUN
	received := make([]chan []byte, 2)
	for i := range 2 {
		private, _ := ecdh.X25519().GenerateKey(rand.Reader)
		id := testutil.ID(100 + i)
		node := testutil.ID(110 + i)
		address := netip.MustParseAddr(fmt.Sprintf("10.%d.0.2", i+1))
		if i == 1 {
			address = netip.MustParseAddr("fd42:2::2")
		}
		source := netip.MustParseAddr(fmt.Sprintf("10.%d.0.1", i+1))
		if i == 1 {
			source = netip.MustParseAddr("fd42:2::1")
		}
		snapshot.Networks = append(snapshot.Networks, model.NetworkConfig{ID: id, MTU: 1420, Self: model.Node{Address: source}, Peers: []model.Peer{{Node: model.Node{ID: node, Address: address, WireGuard: &model.WireGuardNode{PublicKey: base64.StdEncoding.EncodeToString(private.PublicKey().Bytes())}}, PublicKey: private.PublicKey().Bytes(), Edge: model.Edge{ID: testutil.ID(120 + i)}}}})
		if err := h.Apply(snapshot); err != nil {
			t.Fatal(err)
		}
		received[i] = make(chan []byte, 16)
		receive := received[i]
		var tun *memoryTUN
		tun = newTUN(1420, id, func(_ model.ID, raw []byte) {
			if len(raw) == 52 && raw[0]>>4 == 4 && raw[9] == 1 && raw[20] == 8 || len(raw) == 72 && raw[0]>>4 == 6 && raw[6] == 58 && raw[40] == 128 {
				_ = tun.send(probeReply(raw))
				return
			}
			receive <- bytes.Clone(raw)
		})
		client := device.NewDevice(tun, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
		defer client.Close()
		key, _ := base64.StdEncoding.DecodeString(h.networks[id].publicKey)
		config := fmt.Sprintf("private_key=%s\npublic_key=%s\nendpoint=%s\nallowed_ip=0.0.0.0/0\nallowed_ip=::/0\n", hex.EncodeToString(private.Bytes()), hex.EncodeToString(key), udp.LocalAddr())
		if err := client.IpcSet(config); err != nil {
			t.Fatal(err)
		}
		if err := client.Up(); err != nil {
			t.Fatal(err)
		}
		clients = append(clients, client)
		tunnels = append(tunnels, tun)
	}
	for i, tun := range tunnels {
		source := fmt.Sprintf("10.%d.0.2", i+1)
		destination := fmt.Sprintf("10.%d.0.1", i+1)
		if i == 1 {
			source, destination = "fd42:2::2", "fd42:2::1"
		}
		raw := testIP(source, destination)
		if i == 0 {
			raw = append(raw, make([]byte, 1420-len(raw))...)
			binary.BigEndian.PutUint16(raw[2:4], 1420)
		}
		if err := tun.send(raw); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-incoming:
			if got.network != snapshot.Networks[i].ID || !bytes.Equal(got.raw, raw) {
				t.Fatal("cross-network or corrupted ingress")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("WireGuard handshake/ingress timeout")
		}
		reply := testIP(destination, source)
		frame, err := (packet.Packet{Header: packet.Header{Network: snapshot.Networks[i].ID, Source: testutil.ID(90 + i), Destination: snapshot.Networks[i].Peers[0].Node.ID, HopLimit: 32}, Payload: reply}).MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		found, err := h.SendFrame(snapshot.Networks[i].ID, snapshot.Networks[i].Peers[0].Node.ID, frame)
		if !found || err != nil {
			t.Fatal(found, err)
		}
		select {
		case got := <-received[i]:
			if !bytes.Equal(got, reply) {
				t.Fatal("corrupted egress")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("WireGuard egress timeout")
		}
	}
	before := h.networks[snapshot.Networks[0].ID]
	if err := h.Apply(snapshot.Clone()); err != nil {
		t.Fatal(err)
	}
	if h.networks[snapshot.Networks[0].ID] != before {
		t.Fatal("unchanged configuration reset sessions")
	}
	for _, report := range h.Report() {
		if !report.Healthy || report.WireGuardPublicKey == "" {
			t.Fatalf("missing handshake report: %+v", report)
		}
	}
	deadline := time.Now().Add(time.Second)
	for {
		reports := h.Report()
		valid := len(reports) == 2
		for _, r := range reports {
			valid = valid && r.RTTValid && r.RTTMillis > 0 && !r.RTTMeasuredAt.IsZero()
		}
		if valid {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("missing tunneled IPv4/IPv6 RTT", reports)
		}
		time.Sleep(time.Millisecond)
	}
	// A client cannot impersonate a different network's address.
	tunnels[0].send(testIPv4("10.2.0.2", "10.1.0.1"))
	select {
	case <-incoming:
		t.Fatal("spoofed source admitted")
	case <-time.After(100 * time.Millisecond):
	}
	snapshot.Networks = snapshot.Networks[1:]
	if err := h.Apply(snapshot); err != nil {
		t.Fatal(err)
	}
	tunnels[0].send(testIPv4("10.1.0.2", "10.1.0.1"))
	select {
	case <-incoming:
		t.Fatal("revoked peer admitted")
	case <-time.After(100 * time.Millisecond):
	}
}
func testIPv4(source, destination string) []byte {
	raw := make([]byte, 28)
	raw[0] = 0x45
	raw[8] = 64
	raw[9] = 17
	binary.BigEndian.PutUint16(raw[2:4], uint16(len(raw)))
	a, b := netip.MustParseAddr(source).As4(), netip.MustParseAddr(destination).As4()
	copy(raw[12:16], a[:])
	copy(raw[16:20], b[:])
	return raw
}

func testIP(source, destination string) []byte {
	if netip.MustParseAddr(source).Is4() {
		return testIPv4(source, destination)
	}
	raw := make([]byte, 48)
	raw[0] = 0x60
	raw[6] = 17
	raw[7] = 64
	binary.BigEndian.PutUint16(raw[4:6], 8)
	a, b := netip.MustParseAddr(source).As16(), netip.MustParseAddr(destination).As16()
	copy(raw[8:24], a[:])
	copy(raw[24:40], b[:])
	return raw
}
