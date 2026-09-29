package transport

import (
	"errors"

	"golang.org/x/sys/windows"
)

func ipAddressInUse(err error) bool { return errors.Is(err, windows.WSAEADDRINUSE) }
func ipFamilyUnavailable(err error) bool {
	return errors.Is(err, windows.WSAEAFNOSUPPORT) || errors.Is(err, windows.WSAEPROTONOSUPPORT) || errors.Is(err, windows.WSAEADDRNOTAVAIL)
}
