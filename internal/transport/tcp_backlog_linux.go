//go:build linux

package transport

import (
	"fmt"
	"golang.org/x/sys/unix"
	"net"
)

// ConfigureTCP is called on raw sockets before wrapping them in TLS or HTTP.
// Non-TCP connections (e.g. in-memory adapters) need no kernel configuration.
func ConfigureTCP(conn net.Conn) error {
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return nil
	}
	raw, err := tcp.SyscallConn()
	if err != nil {
		return err
	}
	var optionErr error
	if err = raw.Control(func(fd uintptr) {
		optionErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_NOTSENT_LOWAT, TCPNotSentLowWater)
	}); err != nil {
		return err
	}
	if optionErr != nil {
		return fmt.Errorf("set TCP unsent backlog: %w", optionErr)
	}
	return nil
}
