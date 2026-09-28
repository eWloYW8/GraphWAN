//go:build linux && integration

package tunnel_test

import (
	"bytes"
	"encoding/binary"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/tunnel"
	"github.com/vishvananda/netlink"
)

func TestNativeLinuxTunnel(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("GRAPHWAN_TEST_NETNS") != "1" {
		t.Skip("run as root in an isolated network namespace with GRAPHWAN_TEST_NETNS=1")
	}
	selfNS, _ := os.Readlink("/proc/self/ns/net")
	initNS, _ := os.Readlink("/proc/1/ns/net")
	if selfNS == initNS {
		t.Fatal("integration test requires an isolated network namespace")
	}
	config := tunnel.Config{Name: "gw-integration", Address: netip.MustParsePrefix("10.240.31.1/24"), MTU: 1280}
	device, err := tunnel.Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer device.Close()
	if other, err := tunnel.Open(config); err == nil {
		other.Close()
		t.Fatal("attached to an existing interface")
	}
	iface, err := net.InterfaceByName(device.Name())
	if err != nil || iface.MTU != 1280 || iface.Flags&net.FlagUp == 0 {
		t.Fatalf("interface configuration: %+v %v", iface, err)
	}
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, route := range routes {
		if route.LinkIndex == iface.Index && route.Dst != nil && route.Dst.String() == "10.240.31.0/24" {
			found = true
		}
	}
	if !found {
		t.Fatal("connected route missing")
	}
	outgoing, err := net.DialUDP("udp4", &net.UDPAddr{IP: net.ParseIP("10.240.31.1")}, &net.UDPAddr{IP: net.ParseIP("10.240.31.2"), Port: 43210})
	if err != nil {
		t.Fatal(err)
	}
	defer outgoing.Close()
	outgoing.Write([]byte("outbound"))
	type result struct {
		raw []byte
		err error
	}
	read := make(chan result, 1)
	go func() {
		raw := make([]byte, 9001)
		for {
			n, err := device.Read(raw)
			if err != nil || bytes.Contains(raw[:max(0, n)], []byte("outbound")) {
				read <- result{append([]byte{}, raw[:max(0, n)]...), err}
				return
			}
		}
	}()
	select {
	case got := <-read:
		if got.err != nil || !bytes.Contains(got.raw, []byte("outbound")) {
			t.Fatalf("TUN read: %x %v", got.raw, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("TUN read blocked")
	}
	incoming, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("10.240.31.1"), Port: 54321})
	if err != nil {
		t.Fatal(err)
	}
	defer incoming.Close()
	incoming.SetReadDeadline(time.Now().Add(2 * time.Second))
	payload := []byte("inbound")
	raw := make([]byte, 28+len(payload))
	raw[0] = 0x45
	raw[8] = 64
	raw[9] = 17
	binary.BigEndian.PutUint16(raw[2:4], uint16(len(raw)))
	copy(raw[12:20], []byte{10, 240, 31, 2, 10, 240, 31, 1})
	binary.BigEndian.PutUint16(raw[20:22], 12345)
	binary.BigEndian.PutUint16(raw[22:24], 54321)
	binary.BigEndian.PutUint16(raw[24:26], uint16(8+len(payload)))
	copy(raw[28:], payload)
	var sum uint32
	for i := 0; i < 20; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(raw[i : i+2]))
	}
	for sum>>16 != 0 {
		sum = (sum & 65535) + (sum >> 16)
	}
	binary.BigEndian.PutUint16(raw[10:12], ^uint16(sum))
	if n, err := device.Write(raw); err != nil || n != len(raw) {
		t.Fatalf("TUN write: %d %v", n, err)
	}
	buf := make([]byte, 64)
	n, _, err := incoming.ReadFromUDP(buf)
	if err != nil || !bytes.Equal(buf[:n], payload) {
		t.Fatalf("kernel delivery: %q %v", buf[:n], err)
	}
	go func() {
		buf := make([]byte, 9001)
		for {
			n, err := device.Read(buf)
			if err != nil {
				read <- result{buf[:max(0, n)], err}
				return
			}
		}
	}()
	select {
	case got := <-read:
		t.Fatalf("idle read returned before Close: %v", got.err)
	case <-time.After(30 * time.Millisecond):
	}
	if err := device.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-read:
		if got.err == nil {
			t.Fatal("close did not interrupt blocked read")
		}
	case <-time.After(time.Second):
		t.Fatal("close left TUN read blocked")
	}
	if _, err := net.InterfaceByName(config.Name); err == nil {
		t.Fatal("owned interface survived close")
	}
}
