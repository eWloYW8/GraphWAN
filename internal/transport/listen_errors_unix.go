//go:build !windows

package transport

import (
	"errors"
	"syscall"
)

func ipAddressInUse(err error) bool { return errors.Is(err, syscall.EADDRINUSE) }
func ipFamilyUnavailable(err error) bool {
	return errors.Is(err, syscall.EAFNOSUPPORT) || errors.Is(err, syscall.EPROTONOSUPPORT) || errors.Is(err, syscall.EADDRNOTAVAIL)
}
