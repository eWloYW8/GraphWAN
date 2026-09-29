//go:build netbsd

package tunnel

import (
	"errors"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// NetBSD 11 sys/sys/sockio.h; these requests are absent from x/sys's constants.
const netBSDSetDescription = 0x8090698e
const netBSDGetDescription = 0xc090698f

// ifreq's union is sockaddr_storage-sized. The length/pointer member includes
// native pointer alignment; preserve the 144-byte ioctl ABI on 32/64-bit CPUs.
type netBSDDescriptionRequest struct {
	Name    [unix.IFNAMSIZ]byte
	Length  uint32
	Buffer  *byte
	Padding [128 - 2*unsafe.Sizeof(uintptr(0))]byte
}

func nativeRecoveryContext() error { return nil }
func nativeDestroyPersistentInterface(name string, index int) error {
	return destroyNetBSDInterface(name, index)
}
func nativeInterfaceDescription(name, set string) (string, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	defer unix.Close(fd)
	var buffer [64]byte
	request := netBSDDescriptionRequest{Length: uint32(len(buffer)), Buffer: &buffer[0]}
	copy(request.Name[:], name)
	operation := uintptr(netBSDGetDescription)
	if set != "" {
		if len(set) >= len(buffer) {
			return "", errors.New("TUN ownership marker too long")
		}
		copy(buffer[:], set)
		operation = netBSDSetDescription
	}
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), operation, uintptr(unsafe.Pointer(&request)))
	runtime.KeepAlive(buffer)
	if errno == unix.ENOMSG && set == "" {
		return "", nil
	} // An interface with no description.
	if errno != 0 {
		return "", errno
	}
	return strings.TrimRight(string(buffer[:]), "\x00"), nil
}
