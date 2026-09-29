//go:build windows

package transport

import (
	"fmt"
	"strconv"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWinsockNativeABIAndTruncation(t *testing.T) {
	if unsafe.Sizeof(windows.WSACMSGHDR{}) != uintptr(strconv.IntSize/8+8) ||
		unsafe.Sizeof(windows.IN_PKTINFO{}) != 8 || unsafe.Sizeof(windows.IN6_PKTINFO{}) != 20 {
		t.Fatal("unsupported Winsock packet control ABI")
	}
	for _, flags := range []int{windows.MSG_TRUNC, windows.MSG_CTRUNC} {
		if !udpReadTruncated(flags, nil) {
			t.Fatal("truncation flag ignored")
		}
	}
	if !udpReadTruncated(0, fmt.Errorf("receive: %w", windows.WSAEMSGSIZE)) {
		t.Fatal("wrapped WSAEMSGSIZE ignored")
	}
	if udpReadTruncated(0, windows.WSAENOTSOCK) {
		t.Fatal("fatal socket error treated as truncation")
	}
}
