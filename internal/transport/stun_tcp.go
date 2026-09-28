package transport

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"time"

	"github.com/pion/stun/v3"
)

type tcpBinding struct {
	conn     net.Conn // guarded by TCP.mu; only one transaction per server
	busy     bool
	lastUsed time.Time
	ctx      context.Context
	cancel   context.CancelFunc
}

// STUNBinding keeps the TCP observation connection open, refreshing its mapping
// from the SAME source port as peer dials. UDP mappings are never reused for TCP.
// Idle observations expire; loss of a service never closes the peer listener.
func (t *TCP) STUNBinding(parent context.Context, server netip.AddrPort) (mapped netip.AddrPort, err error) {
	server = netip.AddrPortFrom(server.Addr().Unmap(), server.Port())
	if !server.IsValid() || server.Port() == 0 || !stunUnicast(server.Addr()) {
		return mapped, errors.New("invalid TCP STUN server")
	}
	t.mu.Lock()
	select {
	case <-t.done:
		t.mu.Unlock()
		return mapped, net.ErrClosed
	default:
	}
	binding := t.bindings[server]
	if binding == nil {
		if len(t.bindings) >= maxSTUNTransactions {
			t.mu.Unlock()
			return mapped, errors.New("TCP STUN connection limit reached")
		}
		ctx, cancel := context.WithCancel(context.Background())
		binding = &tcpBinding{ctx: ctx, cancel: cancel}
		t.bindings[server] = binding
	}
	if binding.busy {
		t.mu.Unlock()
		return mapped, errors.New("TCP STUN transaction already pending")
	}
	binding.busy, binding.lastUsed = true, time.Now()
	t.wg.Add(1)
	conn := binding.conn
	t.mu.Unlock()
	defer t.wg.Done()
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	stop := context.AfterFunc(binding.ctx, cancel)
	defer func() {
		stop()
		cancel()
		t.mu.Lock()
		defer t.mu.Unlock()
		binding.busy = false
		if err != nil {
			binding.cancel()
			if conn != nil {
				conn.Close()
			}
			if t.bindings[server] == binding {
				delete(t.bindings, server)
			}
		}
	}()
	if conn == nil {
		conn, err = DialTCPPort(ctx, t.Addr().(*net.TCPAddr), server)
		if err != nil {
			return mapped, err
		}
		t.mu.Lock()
		binding.conn = conn
		t.mu.Unlock()
	}
	cleanup, err := deadline(ctx, conn.SetDeadline)
	if err != nil {
		return mapped, err
	}
	defer cleanup()
	request, err := stun.Build(stun.TransactionID, stun.BindingRequest, stun.Fingerprint)
	if err != nil {
		return mapped, err
	}
	if err := writeFull(conn, request.Raw); err != nil {
		return mapped, ctxError(ctx, err)
	}
	var header [20]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return mapped, ctxError(ctx, err)
	}
	size := int(binary.BigEndian.Uint16(header[2:4]))
	if size > 2028 || size%4 != 0 || [12]byte(header[8:20]) != request.TransactionID {
		return mapped, errors.New("invalid TCP STUN response")
	}
	raw := make([]byte, 20+size)
	copy(raw, header[:])
	if _, err := io.ReadFull(conn, raw[20:]); err != nil {
		return mapped, ctxError(ctx, err)
	}
	result, valid := parseSTUNResponse(raw, server.Addr().Is4())
	if !valid {
		return mapped, errors.New("invalid TCP STUN response")
	}
	return result.address, result.err
}
