// Package transport carries bounded messages without prescribing encryption or
// overlay routing. Every transport must preserve complete message boundaries.
package transport

import (
	"context"
	"net"
)

// MaxMessage includes encrypted overlay frames, handshake and transport metadata.
const MaxMessage = 16384

type Conn interface {
	Send(context.Context, []byte) error
	Receive(context.Context) ([]byte, error)
	Close() error
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
}
