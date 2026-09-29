//go:build freebsd

package discovery

import (
	"errors"
	"fmt"
	"net"
	"runtime"
	"unsafe"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

func physicalInterfaces(interfaces []net.Interface) (map[int]bool, error) {
	// Fetch before cloners: a subsequently loaded virtual driver will be in
	// the cloner table, and interfaces created after this snapshot are rejected.
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
	cloners, err := freeBSDCloners()
	if err != nil {
		return nil, fmt.Errorf("interface cloners: %w", err)
	}
	result := make(map[int]bool, len(interfaces))
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
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
		// sys/net/if_mib.h: IFDATA_DRIVERNAME=3. This is the same read-only
		// sysctl used by libifconfig_get_orig_name, without invoking ifconfig.
		original, err := unix.SysctlArgs("net.link.generic.ifdata", iface.Index, 3)
		if err != nil {
			return nil, fmt.Errorf("driver for interface %s: %w", iface.Name, err)
		}
		result[iface.Index] = freeBSDPhysical(kind, original, cloners)
	}
	return result, nil
}

// sys/net/if.h: two C ints and a pointer, 12 bytes on 32-bit and 16 on 64-bit.
type freeBSDCloneRequest struct {
	Total, Count int32
	Buffer       *byte
}

func freeBSDCloners() (map[string]bool, error) {
	fd, err := unix.Socket(unix.AF_LOCAL, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	var request freeBSDCloneRequest
	query := func() error {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.SIOCIFGCLONERS, uintptr(unsafe.Pointer(&request)))
		if errno != 0 {
			return errno
		}
		return nil
	}
	if err := query(); err != nil {
		return nil, err
	}
	for range 3 {
		if request.Total < 0 || request.Total > 4096 {
			return nil, errors.New("invalid interface cloner count")
		}
		if request.Total == 0 {
			return map[string]bool{}, nil
		}
		request.Count = request.Total
		buffer := make([]byte, int(request.Count)*unix.IFNAMSIZ)
		request.Buffer = &buffer[0]
		if err := query(); err != nil {
			return nil, err
		}
		runtime.KeepAlive(buffer)
		if request.Total > request.Count {
			continue // A module registered more cloners between the two queries.
		}
		if request.Total < 0 {
			return nil, errors.New("invalid interface cloner count")
		}
		result := make(map[string]bool, request.Total)
		for i := range int(request.Total) {
			name := unix.ByteSliceToString(buffer[i*unix.IFNAMSIZ : (i+1)*unix.IFNAMSIZ])
			if name == "" {
				return nil, errors.New("incomplete interface cloner list")
			}
			result[name] = true
		}
		return result, nil
	}
	return nil, errors.New("interface cloner list kept changing")
}
