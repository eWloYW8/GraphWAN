//go:build dragonfly

package discovery

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

// DragonFly/amd64 sys/net/if.h and sys/net/lagg/if_lagg.h. These are GET
// operations only; classification must never open or mutate a network device.
const (
	dragonFlyGetHardware = 0xc020693e // SIOCGHWADDR, ifreq (32 bytes).
	dragonFlyGetLAGG     = 0xc0146991 // SIOCGLAGGFLAGS, lagg_reqflags (20 bytes).
)

type dragonFlyDataRequest struct {
	Name [16]byte
	Data unsafe.Pointer
	Pad  [8]byte
}

type dragonFlyDriverRequest struct {
	Name    [16]byte
	Command uint64
	Length  uint64
	Data    unsafe.Pointer
}

type dragonFlyLAGGRequest struct {
	Name  [16]byte
	Flags uint32
}

// Fail compilation if the Go representation disagrees with the ioctl ABI.
var (
	_ [32 - unsafe.Sizeof(dragonFlyDataRequest{})]byte
	_ [unsafe.Sizeof(dragonFlyDataRequest{}) - 32]byte
	_ [40 - unsafe.Sizeof(dragonFlyDriverRequest{})]byte
	_ [unsafe.Sizeof(dragonFlyDriverRequest{}) - 40]byte
	_ [20 - unsafe.Sizeof(dragonFlyLAGGRequest{})]byte
	_ [unsafe.Sizeof(dragonFlyLAGGRequest{}) - 20]byte
)

func dragonFlyIoctl(fd int, operation uintptr, request unsafe.Pointer) (bool, error) {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), operation, uintptr(request))
	switch errno {
	case 0:
		return true, nil
	case unix.EINVAL, unix.EOPNOTSUPP, unix.ENOTTY:
		return false, nil // This driver does not implement the GET operation.
	default:
		return false, errno // Disappearance, permission and I/O errors are not type evidence.
	}
}

func dragonFlyQuery(fd int, iface net.Interface, operation dragonFlyProbe) (bool, error) {
	var name [16]byte
	copy(name[:], iface.Name)
	switch operation {
	case dragonFlyHardware:
		var address [16]byte // struct sockaddr; sa_data begins at byte 2.
		request := dragonFlyDataRequest{Name: name, Data: unsafe.Pointer(&address)}
		ok, err := dragonFlyIoctl(fd, dragonFlyGetHardware, unsafe.Pointer(&request))
		runtime.KeepAlive(&address)
		if ok && !bytes.Equal(address[2:8], iface.HardwareAddr) {
			return false, errors.New("hardware address changed during discovery")
		}
		return ok, err
	case dragonFlyVLAN:
		var config [18]byte // struct vlanreq: parent[IFNAMSIZ], uint16 tag.
		request := dragonFlyDataRequest{Name: name, Data: unsafe.Pointer(&config)}
		ok, err := dragonFlyIoctl(fd, unix.SIOCGIFGENERIC, unsafe.Pointer(&request))
		runtime.KeepAlive(&config)
		return ok, err
	case dragonFlyBridge:
		var cacheSize uint32                                                                                   // struct ifbrparam's largest union member.
		request := dragonFlyDriverRequest{Name: name, Command: 5, Length: 4, Data: unsafe.Pointer(&cacheSize)} // BRDGGCACHE
		ok, err := dragonFlyIoctl(fd, unix.SIOCGDRVSPEC, unsafe.Pointer(&request))
		runtime.KeepAlive(&cacheSize)
		return ok, err
	case dragonFlyLAGG:
		request := dragonFlyLAGGRequest{Name: name}
		return dragonFlyIoctl(fd, dragonFlyGetLAGG, unsafe.Pointer(&request))
	default:
		return false, errors.New("unknown interface probe")
	}
}

func physicalInterfaces(interfaces []net.Interface) (map[int]bool, error) {
	raw, err := route.FetchRIB(unix.AF_UNSPEC, route.RIBTypeInterface, 0)
	if err != nil {
		return nil, fmt.Errorf("interface RIB: %w", err)
	}
	messages, err := route.ParseRIB(route.RIBTypeInterface, raw)
	if err != nil {
		return nil, fmt.Errorf("parse interface RIB: %w", err)
	}
	byIndex := map[int]*route.InterfaceMessage{}
	for _, message := range messages {
		if iface, ok := message.(*route.InterfaceMessage); ok {
			byIndex[iface.Index] = iface
		}
	}
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	result := make(map[int]bool, len(interfaces))
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || len(iface.HardwareAddr) != 6 {
			continue
		}
		if iface.Name == "" || len(iface.Name) >= unix.IFNAMSIZ || strings.ContainsRune(iface.Name, '\x00') {
			return nil, fmt.Errorf("invalid interface name %q", iface.Name)
		}
		message := byIndex[iface.Index]
		if message == nil || message.Name != iface.Name {
			return nil, fmt.Errorf("interface %s changed during discovery", iface.Name)
		}
		kind := 0
		for _, metric := range message.Sys() {
			if metric, ok := metric.(*route.InterfaceMetrics); ok {
				kind = metric.Type
			}
		}
		physical, err := dragonFlyPhysical(kind, func(operation dragonFlyProbe) (bool, error) {
			return dragonFlyQuery(fd, iface, operation)
		})
		if err != nil {
			return nil, fmt.Errorf("interface %s: %w", iface.Name, err)
		}
		current, err := net.InterfaceByName(iface.Name)
		if err != nil || current.Index != iface.Index || !bytes.Equal(current.HardwareAddr, iface.HardwareAddr) {
			return nil, fmt.Errorf("interface %s changed during discovery", iface.Name)
		}
		result[iface.Index] = physical
	}
	return result, nil
}
