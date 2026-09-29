//go:build windows

package transport

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"

	"golang.org/x/sys/windows"
)

func enableUDPPacketInfo(socket *net.UDPConn) error {
	conn, err := socket.SyscallConn()
	if err != nil {
		return err
	}
	var setupError error
	err = conn.Control(func(fd uintptr) {
		s := windows.Handle(fd)
		address, err := windows.Getsockname(s)
		if err != nil {
			setupError = err
			return
		}
		if _, ipv6 := address.(*windows.SockaddrInet6); ipv6 {
			if err := windows.SetsockoptInt(s, windows.IPPROTO_IPV6, windows.IPV6_PKTINFO, 1); err != nil {
				setupError = err
				return
			}
			only, err := windows.GetsockoptInt(s, windows.IPPROTO_IPV6, windows.IPV6_V6ONLY)
			if err != nil || only != 0 {
				setupError = err
				return
			}
		}
		setupError = windows.SetsockoptInt(s, windows.IPPROTO_IP, windows.IP_PKTINFO, 1)
		if errors.Is(setupError, windows.WSAEINVAL) {
			// Microsoft documents this error on a dual-stack socket when IPv4
			// is disabled. Confirm that condition instead of hiding other errors.
			probe, probeError := windows.Socket(windows.AF_INET, windows.SOCK_DGRAM, windows.IPPROTO_UDP)
			if probeError == nil {
				_ = windows.Closesocket(probe)
			} else if errors.Is(probeError, windows.WSAEAFNOSUPPORT) {
				setupError = nil
			}
		}
	})
	if err = errors.Join(err, setupError); err != nil {
		return fmt.Errorf("enable UDP packet information: %w", err)
	}
	return nil
}

func udpReplyControl(oob []byte, remote netip.AddrPort) []byte {
	return winsockReplyControl(oob, remote, strconv.IntSize/8)
}

func udpReadTruncated(flags int, err error) bool {
	return flags&(windows.MSG_TRUNC|windows.MSG_CTRUNC) != 0 || errors.Is(err, windows.WSAEMSGSIZE)
}
