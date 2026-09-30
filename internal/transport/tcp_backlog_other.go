//go:build !linux

package transport

import "net"

// Other platforms retain bounded user-space queues and native TCP buffering.
func ConfigureTCP(net.Conn) error { return nil }
