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
	for _, addresses := range [][2]string{{"10.240.31.1/24", "10.240.31.2"}, {"fd42:6777::1/64", "fd42:6777::2"}} {
		t.Run(addresses[0], func(t *testing.T) {
			checkNativeLinuxTunnel(t, tunnel.Config{Name: "gw-integration", Address: netip.MustParsePrefix(addresses[0]), MTU: 1280}, netip.MustParseAddr(addresses[1]))
		})
	}
}

func checkNativeLinuxTunnel(t *testing.T, config tunnel.Config, remote netip.Addr) {
	family, udp, version := netlink.FAMILY_V4, "udp4", byte(4)
	if config.Address.Addr().Is6() {
		family, udp, version = netlink.FAMILY_V6, "udp6", 6
	}
	localIP, remoteIP := net.IP(config.Address.Addr().AsSlice()), net.IP(remote.AsSlice())
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
	routes, err := netlink.RouteList(nil, family)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, route := range routes {
		if route.LinkIndex == iface.Index && route.Dst != nil && route.Dst.String() == config.Address.Masked().String() {
			found = true
		}
	}
	if !found {
		t.Fatal("connected route missing")
	}
	outgoing, err := net.DialUDP(udp, &net.UDPAddr{IP: localIP}, &net.UDPAddr{IP: remoteIP, Port: 43210})
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
		if got.err != nil || len(got.raw) == 0 || got.raw[0]>>4 != version || !bytes.Contains(got.raw, []byte("outbound")) {
			t.Fatalf("TUN read: %x %v", got.raw, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("TUN read blocked")
	}
	incoming, err := net.ListenUDP(udp, &net.UDPAddr{IP: localIP, Port: 54321})
	if err != nil {
		t.Fatal(err)
	}
	defer incoming.Close()
	incoming.SetReadDeadline(time.Now().Add(2 * time.Second))
	payload := []byte("inbound")
	header := 20
	if version == 6 {
		header = 40
	}
	raw := make([]byte, header+8+len(payload))
	if version == 4 {
		raw[0], raw[8], raw[9] = 0x45, 64, 17
		binary.BigEndian.PutUint16(raw[2:4], uint16(len(raw)))
		copy(raw[12:16], remoteIP)
		copy(raw[16:20], localIP)
		binary.BigEndian.PutUint16(raw[10:12], checksum(raw[:20]))
	} else {
		raw[0], raw[6], raw[7] = 0x60, 17, 64
		binary.BigEndian.PutUint16(raw[4:6], uint16(len(raw)-40))
		copy(raw[8:24], remoteIP)
		copy(raw[24:40], localIP)
	}
	binary.BigEndian.PutUint16(raw[header:header+2], 12345)
	binary.BigEndian.PutUint16(raw[header+2:header+4], 54321)
	binary.BigEndian.PutUint16(raw[header+4:header+6], uint16(8+len(payload)))
	copy(raw[header+8:], payload)
	if version == 6 {
		// Unlike IPv4, IPv6 UDP requires a checksum including the pseudo-header.
		pseudo := make([]byte, 40)
		copy(pseudo, raw[8:40])
		binary.BigEndian.PutUint32(pseudo[32:36], uint32(len(raw)-40))
		pseudo[39] = 17
		sum := checksum(append(pseudo, raw[40:]...))
		if sum == 0 {
			sum = 0xffff
		}
		binary.BigEndian.PutUint16(raw[46:48], sum)
	}
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

func checksum(raw []byte) uint16 {
	var sum uint32
	for len(raw) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(raw[:2]))
		raw = raw[2:]
	}
	if len(raw) != 0 {
		sum += uint32(raw[0]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 65535) + (sum >> 16)
	}
	return ^uint16(sum)
}
