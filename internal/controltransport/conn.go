// Package controltransport carries the controller's authenticated HTTPS and
// WebSocket protocol over TCP, WebSocket, WSS or a gRPC byte tunnel.
package controltransport

import (
	"errors"
	"net"
	"sync"
)

const (
	WebSocketPath     = "/api/v1/agent/tunnel"
	WebSocketProtocol = "graphwan.control.v1"
	GRPCMethod        = "/graphwan.control.v1.Tunnel/Connect"
	GRPCProtocol      = "graphwan-control-v1"
	maxChunk          = 32 << 10
)

// A pipe supplies real net.Conn deadlines and bounded backpressure. The pumps
// translate message boundaries into a byte stream; TLS owns the inner framing.
// There is only one reader and one writer of the message transport.
type tunnelConn struct {
	net.Conn
	local, remote net.Addr
	done          chan struct{}
	stop          func()
}

func byteTunnel(send func([]byte) error, receive func() ([]byte, error), closeTransport func(), local, remote net.Addr) *tunnelConn {
	app, pump := net.Pipe()
	c := &tunnelConn{Conn: app, local: local, remote: remote, done: make(chan struct{})}
	c.stop = sync.OnceFunc(func() {
		app.Close()
		pump.Close()
		close(c.done)
		closeTransport()
	})
	go func() {
		defer c.stop()
		buf := make([]byte, maxChunk)
		for {
			n, err := pump.Read(buf)
			if err != nil {
				return
			}
			if err := send(buf[:n]); err != nil {
				return
			}
		}
	}()
	go func() {
		defer c.stop()
		for {
			buf, err := receive()
			if err != nil || len(buf) == 0 || len(buf) > maxChunk {
				return
			}
			if _, err := pump.Write(buf); err != nil {
				return
			}
		}
	}()
	return c
}

func (c *tunnelConn) Close() error         { c.stop(); return nil }
func (c *tunnelConn) LocalAddr() net.Addr  { return c.local }
func (c *tunnelConn) RemoteAddr() net.Addr { return c.remote }

type address string

func (a address) Network() string { return "tcp" }
func (a address) String() string  { return string(a) }

type messageStream interface {
	SendMsg(any) error
	RecvMsg(any) error
}

var errChunk = errors.New("invalid control tunnel chunk")
