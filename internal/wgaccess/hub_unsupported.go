//go:build !linux && !darwin && !windows && !freebsd && !openbsd

package wgaccess

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"net/netip"
	"runtime"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

// The upstream WireGuard device imports platform-specific IPC implementations,
// even though GraphWAN does not expose its UAPI. Keep ordinary mesh networking
// and controller builds available on targets that upstream cannot compile.
type Send func(context.Context, []byte, netip.AddrPort, []byte) error
type Hub struct{}

func New(context.Context, ed25519.PrivateKey, uint16, Send, func(model.ID, []byte)) *Hub {
	return &Hub{}
}
func (*Hub) Apply(snapshot model.Snapshot) error {
	for _, network := range snapshot.Networks {
		for _, peer := range network.Peers {
			if peer.Node.WireGuard != nil {
				return fmt.Errorf("WireGuard access is not supported on %s; attach this client to a Linux, Windows, macOS, FreeBSD or OpenBSD agent", runtime.GOOS)
			}
		}
	}
	return nil
}
func (*Hub) Receive([]byte, netip.AddrPort, []byte)             {}
func (*Hub) SendFrame(model.ID, model.ID, []byte) (bool, error) { return false, nil }
func (*Hub) Close()                                             {}
func (*Hub) Report() []model.LinkStatus                         { return nil }
