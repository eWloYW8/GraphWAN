//go:build !windows

package transport

import (
	"errors"
	"net"
	"net/netip"
	"syscall"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// Packet information preserves the destination of an incoming datagram as the
// reply source on a wildcard listener. Enable both families on dual-stack sockets.
// Windows has a separate implementation using the native Winsock structures.
// A socket bound to a particular address already pins its source.
func enableUDPPacketInfo(socket *net.UDPConn) error {
	_ = ipv4.NewPacketConn(socket).SetControlMessage(ipv4.FlagDst|ipv4.FlagInterface, true)
	_ = ipv6.NewPacketConn(socket).SetControlMessage(ipv6.FlagDst|ipv6.FlagInterface, true)
	return nil
}

func udpReplyControl(oob []byte, remote netip.AddrPort) []byte {
	if len(oob) == 0 {
		return nil
	}
	if remote.Addr().Unmap().Is4() {
		var v4 ipv4.ControlMessage
		if v4.Parse(oob) == nil && v4.Dst.To4() != nil {
			return (&ipv4.ControlMessage{Src: v4.Dst}).Marshal()
		}
		var v6 ipv6.ControlMessage
		if v6.Parse(oob) == nil && v6.Dst.To4() != nil {
			return (&ipv4.ControlMessage{Src: v6.Dst.To4()}).Marshal()
		}
		return nil
	}
	var v6 ipv6.ControlMessage
	if v6.Parse(oob) == nil && v6.Dst != nil {
		return (&ipv6.ControlMessage{Src: v6.Dst, IfIndex: v6.IfIndex}).Marshal()
	}
	return nil
}

func udpReadTruncated(flags int, err error) bool {
	return flags&(syscall.MSG_TRUNC|syscall.MSG_CTRUNC) != 0 || errors.Is(err, syscall.EMSGSIZE)
}
