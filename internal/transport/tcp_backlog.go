package transport

import (
	"context"
	"net"
)

// TCPNotSentLowWater bounds unsent kernel backlog without restricting the TCP
// window or acknowledged/in-flight data. It is a soft threshold: one write/GSO
// allocation can overshoot it. Protocol framing buffers are additional.
const TCPNotSentLowWater = 64 * 1024

// DialTCP applies the same backlog policy before any TLS/HTTP framing is added.
func DialTCP(ctx context.Context, dialer *net.Dialer, network, address string) (net.Conn, error) {
	conn, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	if err = ConfigureTCP(conn); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}
