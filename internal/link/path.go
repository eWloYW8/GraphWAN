package link

import (
	"encoding/hex"
	"net/netip"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

// Path identifies endpoint IPs as observed by the other end. Ports are excluded;
// IPv6 zones are retained to distinguish scoped paths on different interfaces.
type Path struct {
	Local, Remote netip.Addr
	Transport     model.Transport
}

func endpointAddress(raw string) (netip.AddrPort, bool) {
	a, err := netip.ParseAddrPort(raw)
	if err != nil || a.Port() == 0 || a.Addr().IsUnspecified() || a.Addr().IsMulticast() {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(a.Addr().Unmap(), a.Port()), true
}

func (l *Link) Path() (Path, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, aOK := endpointAddress(l.observedLocal)
	b, bOK := endpointAddress(l.channel.RemoteAddr().String())
	return Path{Local: a.Addr(), Remote: b.Addr(), Transport: l.info.Transport}, l.pathAcknowledged && aOK && bOK
}

// Retirement travels on the retained Link so loss/retries still work after the
// redundant Link closes. Only the Edge coordinator may request retirement.
type Retirement struct {
	ID  string
	Ack bool
}

func (l *Link) Retirements() <-chan Retirement { return l.retirements }
func (l *Link) SendRetirement(id string, ack bool) {
	raw, err := hex.DecodeString(id)
	if err != nil || len(raw) != 32 {
		return
	}
	kind := byte(retireMessage)
	if ack {
		kind = retiredMessage
	}
	l.enqueueControl(append([]byte{kind}, raw...))
}

func (l *Link) enqueueControl(raw []byte) {
	select {
	case l.control <- raw:
	default:
	}
}

func (l *Link) sendPathLocked() {
	remote, ok := endpointAddress(l.channel.RemoteAddr().String())
	if !ok {
		return
	}
	ack := byte(0)
	if l.observedLocal != "" {
		ack = 1
	}
	l.enqueueControl(append([]byte{pathMessage, ack}, remote.String()...))
}

func (l *Link) handlePathControl(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	if raw[0] == pathMessage {
		if len(raw) < 3 || len(raw) > 130 || raw[1] > 1 {
			return false
		}
		observed, ok := endpointAddress(string(raw[2:]))
		if !ok {
			return false
		}
		l.mu.Lock()
		defer l.mu.Unlock()
		first := l.observedLocal == ""
		if !first && l.observedLocal != observed.String() {
			return false
		}
		l.observedLocal, l.pathEnabled = observed.String(), true
		l.pathAcknowledged = l.pathAcknowledged || raw[1] == 1
		if first || raw[1] == 0 {
			l.sendPathLocked()
		}
		return true
	}
	if (raw[0] != retireMessage && raw[0] != retiredMessage) || len(raw) != 33 {
		return false
	}
	if _, ready := l.Path(); !ready {
		return true
	}
	select {
	case l.retirements <- Retirement{ID: hex.EncodeToString(raw[1:]), Ack: raw[0] == retiredMessage}:
	default:
	}
	return true
}
