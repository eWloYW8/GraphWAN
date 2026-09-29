package transport

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestWildcardBindCollisionCleanup(t *testing.T) {
	for _, ephemeral := range []bool{false, true} {
		t.Run(strconv.FormatBool(ephemeral), func(t *testing.T) {
			blocker, err := net.ListenUDP("udp4", &net.UDPAddr{})
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Close()
			port := strconv.Itoa(blocker.LocalAddr().(*net.UDPAddr).Port)
			requested := ":" + port
			if ephemeral {
				requested = ":0"
			}
			var opened []*net.UDPConn
			attempts := 0
			sockets, err := listenIP(requested, func(family, address string) (*net.UDPConn, net.Addr, error) {
				if family == "6" {
					attempts++
					if attempts == 1 {
						address = net.JoinHostPort("::", port)
					}
				}
				addr, e := net.ResolveUDPAddr("udp"+family, address)
				if e != nil {
					return nil, nil, e
				}
				conn, e := net.ListenUDP("udp"+family, addr)
				if e != nil {
					return nil, nil, e
				}
				opened = append(opened, conn)
				return conn, conn.LocalAddr(), nil
			})
			for _, socket := range sockets {
				defer socket.Close()
			}
			if ephemeral {
				if err != nil || len(sockets) != 2 || attempts < 2 {
					t.Fatalf("retry: %d sockets, %d attempts, %v", len(sockets), attempts, err)
				}
				if sockets[0].LocalAddr().(*net.UDPAddr).Port != sockets[1].LocalAddr().(*net.UDPAddr).Port {
					t.Fatal("different ports")
				}
			} else if err == nil || len(sockets) != 0 || attempts != 1 {
				t.Fatalf("fixed port collision: %v", err)
			}
			if len(opened) == 0 {
				t.Fatal("did not exercise partial reservation")
			}
			if _, err := opened[0].WriteToUDP([]byte{1}, &net.UDPAddr{IP: net.IPv6loopback, Port: 9}); !errors.Is(err, net.ErrClosed) {
				t.Fatal("failed bind leaked first socket:", err)
			}
		})
	}
}

func TestWildcardBindUnexpectedFailureCleanup(t *testing.T) {
	sentinel := errors.New("injected bind failure")
	var first *net.UDPConn
	_, err := listenIP(":0", func(family, address string) (*net.UDPConn, net.Addr, error) {
		if family == "4" {
			return nil, nil, sentinel
		}
		addr, e := net.ResolveUDPAddr("udp6", address)
		if e != nil {
			return nil, nil, e
		}
		first, e = net.ListenUDP("udp6", addr)
		if e != nil {
			return nil, nil, e
		}
		return first, first.LocalAddr(), nil
	})
	if !errors.Is(err, sentinel) || first == nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := first.WriteToUDP([]byte{1}, &net.UDPAddr{IP: net.IPv6loopback, Port: 9}); !errors.Is(err, net.ErrClosed) {
		t.Fatal("partial reservation leaked:", err)
	}
}

func TestTCPWildcardBothFamiliesAndClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	listener, err := ListenTCP(ctx, ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	for _, host := range []string{"127.0.0.1", "::1"} {
		dialer := &net.Dialer{}
		client, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		accepted := make(chan net.Conn, 1)
		go func() { conn, _ := listener.Accept(); accepted <- conn }()
		var server net.Conn
		select {
		case server = <-accepted:
			if server == nil {
				t.Fatal("accept failed")
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		defer server.Close()
		server.SetDeadline(time.Now().Add(time.Second))
		client.SetDeadline(time.Now().Add(time.Second))
		if _, err := client.Write([]byte{42}); err != nil {
			t.Fatal(err)
		}
		var got [1]byte
		if _, err := io.ReadFull(server, got[:]); err != nil || got[0] != 42 {
			t.Fatal("TCP delivery:", err)
		}
	}
	done := make(chan error, 1)
	go func() { _, err := listener.Accept(); done <- err }()
	listener.Close()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("close did not unblock Accept")
	}
}
