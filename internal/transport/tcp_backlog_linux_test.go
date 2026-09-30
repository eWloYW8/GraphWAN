//go:build linux

package transport

import (
	"context"
	"golang.org/x/sys/unix"
	"net"
	"testing"
	"time"
)

func TestTCPBacklogBothEnds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
	defer cancel()
	listener, err := ListenTCP(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := DialTCP(ctx, &net.Dialer{}, "tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	for _, conn := range []net.Conn{client, server} {
		raw, err := conn.(*net.TCPConn).SyscallConn()
		if err != nil {
			t.Fatal(err)
		}
		var value int
		var optionErr error
		err = raw.Control(func(fd uintptr) {
			value, optionErr = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_NOTSENT_LOWAT)
		})
		if err != nil || optionErr != nil || value != TCPNotSentLowWater {
			t.Fatalf("value=%d err=%v/%v", value, err, optionErr)
		}
	}
}
