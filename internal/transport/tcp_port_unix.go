//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package transport

import (
	"syscall"

	"golang.org/x/sys/unix"
)

func reuseTCPPort(_, _ string, conn syscall.RawConn) error {
	var err error
	if outer := conn.Control(func(fd uintptr) {
		err = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
		if err == nil {
			err = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
		}
	}); outer != nil {
		return outer
	}
	return err
}
