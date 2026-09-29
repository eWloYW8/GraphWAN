//go:build !windows

package transport

import (
	"net"
	"syscall"
	"testing"
)

func TestWildcardBindUnavailableFamily(t *testing.T) {
	for _, unavailable := range []string{"4", "6", "both"} {
		t.Run(unavailable, func(t *testing.T) {
			sockets, err := listenIP(":0", func(family, address string) (*net.UDPConn, net.Addr, error) {
				if family == unavailable || unavailable == "both" {
					return nil, nil, &net.OpError{Op: "listen", Err: syscall.EAFNOSUPPORT}
				}
				addr, err := net.ResolveUDPAddr("udp"+family, address)
				if err != nil {
					return nil, nil, err
				}
				socket, err := net.ListenUDP("udp"+family, addr)
				if err != nil {
					return nil, nil, err
				}
				return socket, socket.LocalAddr(), nil
			})
			for _, socket := range sockets {
				defer socket.Close()
			}
			if unavailable == "both" {
				if err == nil || len(sockets) != 0 {
					t.Fatal("missing-family failure lost")
				}
			} else if err != nil || len(sockets) != 1 {
				t.Fatalf("single-family fallback: %d sockets, %v", len(sockets), err)
			}
		})
	}
}
