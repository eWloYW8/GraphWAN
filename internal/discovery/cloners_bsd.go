//go:build freebsd || openbsd || netbsd

package discovery

import (
	"errors"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// sys/net/if.h: two C ints and a pointer, 12 bytes on 32-bit and 16 on 64-bit.
type bsdCloneRequest struct {
	Total, Count int32
	Buffer       *byte
}

func bsdCloners() (map[string]bool, error) {
	fd, err := unix.Socket(unix.AF_LOCAL, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	var request bsdCloneRequest
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
