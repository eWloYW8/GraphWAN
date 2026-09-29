//go:build darwin

package discovery

import (
	"encoding/binary"
	"fmt"
	"net"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	darwinGetInterfaceType = 0xc020699f // SIOCGIFTYPE, struct ifreq (32 bytes).
	darwinGetExtendedFlags = 0xc020698e // SIOCGIFEFLAGS, uint64 union member.
)

func physicalInterfaces(interfaces []net.Interface) (map[int]bool, error) {
	// Darwin has no SOCK_CLOEXEC; prevent concurrent ifconfig execs from
	// inheriting the socket between socket(2) and setting FD_CLOEXEC.
	syscall.ForkLock.RLock()
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err == nil {
		unix.CloseOnExec(fd)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	query := func(name string, operation uintptr) ([16]byte, error) {
		var request [32]byte
		copy(request[:16], name)
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), operation, uintptr(unsafe.Pointer(&request)))
		var result [16]byte
		copy(result[:], request[16:])
		if errno != 0 {
			return result, errno
		}
		return result, nil
	}
	result := make(map[int]bool, len(interfaces))
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if len(iface.Name) == 0 || len(iface.Name) >= unix.IFNAMSIZ {
			return nil, fmt.Errorf("invalid interface name %q", iface.Name)
		}
		typeInfo, err := query(iface.Name, darwinGetInterfaceType)
		if err != nil {
			return nil, fmt.Errorf("type for interface %s: %w", iface.Name, err)
		}
		flags, err := query(iface.Name, darwinGetExtendedFlags)
		if err != nil {
			return nil, fmt.Errorf("flags for interface %s: %w", iface.Name, err)
		}
		current, err := net.InterfaceByName(iface.Name)
		if err != nil || current.Index != iface.Index {
			return nil, fmt.Errorf("interface %s changed during discovery", iface.Name)
		}
		metadata := darwinInterfaceMetadata{
			kind: binary.NativeEndian.Uint32(typeInfo[:4]), family: binary.NativeEndian.Uint32(typeInfo[4:8]),
			subfamily: binary.NativeEndian.Uint32(typeInfo[8:12]), extendedFlags: binary.NativeEndian.Uint64(flags[:8]),
		}
		result[iface.Index] = metadata.physical(iface.Name)
	}
	return result, nil
}
