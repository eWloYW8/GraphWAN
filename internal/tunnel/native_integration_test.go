//go:build (linux || freebsd || darwin) && integration

package tunnel_test

import (
	"bytes"
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/tunnel"
)

func checkNativeTunnel(t *testing.T, config tunnel.Config, remote netip.Addr) {
	device, err := tunnel.Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer device.Close()
	checkConfiguredNativeTunnel(t, device, config, remote)
}

func checkConfiguredNativeTunnel(t *testing.T, device tunnel.Device, config tunnel.Config, remote netip.Addr) {
	t.Helper()
	udp, version := "udp4", byte(4)
	if config.Address.Addr().Is6() {
		udp, version = "udp6", 6
	}
	localIP, remoteIP := net.IP(config.Address.Addr().AsSlice()), net.IP(remote.AsSlice())
	config.Name = device.Name()
	if other, err := tunnel.Open(config); err == nil {
		other.Close()
		t.Fatal("attached to an existing interface")
	}
	iface, err := net.InterfaceByName(device.Name())
	if err != nil || iface.MTU != config.MTU || iface.Flags&net.FlagUp == 0 {
		t.Fatalf("interface configuration: %+v %v", iface, err)
	}
	checkNativeRoute(t, iface, config)
	outgoing, err := net.DialUDP(udp, &net.UDPAddr{IP: localIP}, &net.UDPAddr{IP: remoteIP, Port: 43210})
	if err != nil {
		t.Fatal(err)
	}
	defer outgoing.Close()
	header := 20
	if version == 6 {
		header = 40
	}
	outbound := bytes.Repeat([]byte("o"), config.MTU-header-8)
	copy(outbound, "outbound")
	if _, err := outgoing.Write(outbound); err != nil {
		t.Fatal(err)
	}
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
		if len(got.raw) != config.MTU || !bytes.Equal(got.raw[header+8:], outbound) {
			t.Fatal("full-MTU outbound packet was corrupted or truncated")
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
	payload := bytes.Repeat([]byte("i"), config.MTU-header-8)
	copy(payload, "inbound")
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
	buf := make([]byte, config.MTU)
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
	if err := device.Close(); err != nil {
		t.Fatalf("repeated close: %v", err)
	}
	select {
	case got := <-read:
		if got.err == nil {
			t.Fatal("close did not interrupt blocked read")
		}
	case <-time.After(time.Second):
		t.Fatal("close left TUN read blocked")
	}
	// Darwin detaches utun asynchronously after the final descriptor closes.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := net.InterfaceByName(config.Name); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owned interface survived close")
		}
		time.Sleep(time.Millisecond)
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
