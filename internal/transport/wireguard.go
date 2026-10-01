package transport

import (
	"context"
	"net/netip"
)

type WireGuardReceiver func(raw []byte, remote netip.AddrPort, replyControl []byte)

func (h *UDP) SetWireGuardReceiver(receive WireGuardReceiver) {
	h.mu.Lock()
	h.wireguard = receive
	h.mu.Unlock()
}
func (h *UDP) receiveWireGuard(raw []byte, remote netip.AddrPort, oob []byte) bool {
	if len(raw) < 4 || raw[1] != 0 || raw[2] != 0 || raw[3] != 0 || raw[0] < 1 || raw[0] > 4 {
		return false
	}
	// Recognized but malformed WireGuard datagrams must not reach QUIC.
	valid := raw[0] == 1 && len(raw) == 148 || raw[0] == 2 && len(raw) == 92 || raw[0] == 3 && len(raw) == 64 || raw[0] == 4 && len(raw) >= 32
	if valid {
		h.mu.Lock()
		receive := h.wireguard
		h.mu.Unlock()
		if receive != nil {
			receive(raw, remote, udpReplyControl(oob, remote))
		}
	}
	return true
}
func (h *UDP) SendWireGuard(ctx context.Context, raw []byte, remote netip.AddrPort, control []byte) error {
	return h.writeDatagramControl(ctx, raw, remote, control)
}
