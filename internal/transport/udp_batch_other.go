//go:build !linux

package transport

import (
	"context"
	"errors"
	"net"
	"net/netip"
)

type udpBatchSocket struct{}
type udpBatchSend struct{}

func newUDPBatchSocket(*net.UDPConn) *udpBatchSocket { return nil }
func (d *Datagram) SendBatch(ctx context.Context, payloads [][]byte) error {
	if len(payloads) == 0 || len(payloads) > 128 {
		return errors.New("invalid UDP batch size")
	}
	for _, raw := range payloads {
		if len(raw) == 0 || len(raw) > MaxMessage {
			return errors.New("invalid UDP message size")
		}
	}
	for _, raw := range payloads {
		if err := d.Send(ctx, raw); err != nil {
			return err
		}
	}
	return nil
}

type udpPacketReader struct {
	socket  *net.UDPConn
	buffer  [MaxMessage + udpHeaderSize + 1]byte
	control [256]byte
}

func newUDPPacketReader(socket *net.UDPConn) *udpPacketReader {
	return &udpPacketReader{socket: socket}
}
func (r *udpPacketReader) read() ([]byte, []byte, int, netip.AddrPort, error) {
	n, control, flags, remote, err := r.socket.ReadMsgUDPAddrPort(r.buffer[:], r.control[:])
	if err != nil {
		return nil, nil, flags, remote, err
	}
	return r.buffer[:n], r.control[:control], flags, remote, err
}

func (r *udpPacketReader) buffered() bool { return false }
