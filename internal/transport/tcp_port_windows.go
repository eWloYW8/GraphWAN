package transport

import (
	"syscall"

	"golang.org/x/sys/windows"
)

func reuseTCPPort(_, _ string, conn syscall.RawConn) error {
	var err error
	if outer := conn.Control(func(fd uintptr) {
		err = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_REUSEADDR, 1)
	}); outer != nil {
		return outer
	}
	return err
}
