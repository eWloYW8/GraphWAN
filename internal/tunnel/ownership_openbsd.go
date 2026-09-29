//go:build openbsd

package tunnel

import (
	"errors"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

func nativeRecoveryContext() error {
	rtable, err := unix.Getrtable()
	if err != nil {
		return err
	}
	if rtable != 0 {
		return errors.New("OpenBSD TUN recovery requires routing table 0")
	}
	return nil
}
func nativeDestroyPersistentInterface(name string, index int) error {
	return destroyOpenBSDInterface(name, index)
}

func nativeInterfaceDescription(name string, set string) (string, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	defer unix.Close(fd)
	var buffer [64]byte // OpenBSD IFDESCRSIZE, includes the terminating NUL.
	var request openBSDIfreq
	copy(request.Name[:], name)
	*(*uintptr)(unsafe.Pointer(&request.Data[0])) = uintptr(unsafe.Pointer(&buffer[0]))
	operation := uintptr(unix.SIOCGIFDESCR)
	if set != "" {
		if len(set) >= len(buffer) {
			return "", errors.New("TUN ownership marker too long")
		}
		copy(buffer[:], set)
		operation = unix.SIOCSIFDESCR
	}
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), operation, uintptr(unsafe.Pointer(&request)))
	runtime.KeepAlive(&buffer)
	if errno != 0 {
		return "", errno
	}
	return strings.TrimRight(string(buffer[:]), "\x00"), nil
}
