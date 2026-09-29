package transport

import (
	"errors"
	"io"
	"net"
	"net/netip"
)

// Wildcard endpoints must cover both address families even on kernels that do
// not implement IPv4-mapped IPv6 sockets. Explicit addresses retain normal Go
// binding semantics. All successful binds share a port, and partial failures
// release every reservation. Only an unavailable family may be omitted.
func listenIP[T io.Closer](address string, bind func(family, address string) (T, net.Addr, error)) ([]T, error) {
	host, requestedPort, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ip, _ := netip.ParseAddr(host)
	if host != "" && (!ip.Unmap().IsUnspecified() || ip.Zone() != "") {
		socket, _, err := bind("", address)
		if err != nil {
			return nil, err
		}
		return []T{socket}, nil
	}
	for attempt := 0; attempt < 8; attempt++ {
		var sockets []T
		var unavailable error
		port := requestedPort
		for _, family := range []struct{ network, host string }{{"6", "::"}, {"4", "0.0.0.0"}} {
			socket, local, bindError := bind(family.network, net.JoinHostPort(family.host, port))
			if bindError != nil {
				if ipFamilyUnavailable(bindError) {
					unavailable = errors.Join(unavailable, bindError)
					continue
				}
				err = bindError
				break
			}
			sockets = append(sockets, socket)
			_, port, err = net.SplitHostPort(local.String())
			if err != nil {
				break
			}
		}
		if err == nil {
			if len(sockets) != 0 {
				return sockets, nil
			}
			return nil, unavailable
		}
		for _, socket := range sockets {
			_ = socket.Close()
		}
		if (requestedPort != "" && requestedPort != "0") || !ipAddressInUse(err) || attempt == 7 {
			return nil, err
		}
		err = nil
	}
	panic("unreachable IP bind retry")
}

func listenUDPSockets(address string) ([]*net.UDPConn, error) {
	return listenIP(address, func(family, address string) (*net.UDPConn, net.Addr, error) {
		network := "udp" + family
		addr, err := net.ResolveUDPAddr(network, address)
		if err != nil {
			return nil, nil, err
		}
		socket, err := net.ListenUDP(network, addr)
		if err != nil {
			return nil, nil, err
		}
		return socket, socket.LocalAddr(), nil
	})
}
