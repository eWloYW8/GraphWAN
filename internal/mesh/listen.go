package mesh

import (
	"context"
	"crypto/ed25519"
	"net"
	"strconv"

	"github.com/eWloYW8/GraphWAN/internal/transport"
)

// TCP and UDP have separate ephemeral-port allocators. A randomly selected TCP
// port may already belong to a UDP listener. Retry the pair, releasing each
// failed TCP reservation; never change an explicitly requested port.
func listenTransports(ctx context.Context, identity ed25519.PrivateKey, host string, port uint16, listenTCP func(context.Context, string) (*transport.TCP, error)) (*transport.TCP, *transport.UDP, *transport.QUICHub, error) {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		tcp, err := listenTCP(ctx, net.JoinHostPort(host, strconv.Itoa(int(port))))
		if err != nil {
			return nil, nil, nil, err
		}
		udp, quic, err := transport.ListenUDPQUIC(tcp.Addr().String(), identity)
		if err == nil {
			return tcp, udp, quic, nil
		}
		tcp.Close()
		if port != 0 || attempt >= 7 || !addressInUse(err) {
			return nil, nil, nil, err
		}
	}
}
