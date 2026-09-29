package mesh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/graphwan/graphwan/internal/link"
	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/transport"
)

type punchKey struct {
	remote   netip.AddrPort
	identity [32]byte
}
type punchDial struct {
	done chan struct{}
	err  error
}

func (m *Mesh) allowsTCPPunchLocked(identity ed25519.PublicKey) bool {
	if m.closed {
		return false
	}
	for _, g := range m.groups {
		cfg := g.policy.Load()
		if cfg.peer.Edge.Methods.HolePunch && slices.Contains(cfg.peer.Edge.Transports, model.TCP) && bytes.Equal(cfg.peer.PublicKey, identity) {
			return true
		}
	}
	return false
}
func (m *Mesh) allowsTCPPunch(identity ed25519.PublicKey) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.allowsTCPPunchLocked(identity)
}
func punchAddress(addr net.Addr) (netip.AddrPort, error) {
	value, err := netip.ParseAddrPort(addr.String())
	if err != nil {
		return value, err
	}
	return netip.AddrPortFrom(value.Addr().Unmap(), value.Port()), nil
}

func (m *Mesh) installPunch(session *transport.PunchMux) bool {
	address, err := punchAddress(session.RemoteAddr())
	if err != nil {
		return false
	}
	k := punchKey{address, [32]byte(session.Identity())}
	m.mu.Lock()
	if !m.allowsTCPPunchLocked(session.Identity()) || len(m.punches) >= 64 {
		m.mu.Unlock()
		return false
	}
	if old := m.punches[k]; old != nil {
		select {
		case <-old.Done():
		default:
			m.mu.Unlock()
			return false
		}
	}
	m.punches[k] = session
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer func() {
			session.Close()
			m.mu.Lock()
			if m.punches[k] == session {
				delete(m.punches, k)
			}
			m.mu.Unlock()
		}()
		for {
			conn, err := session.Accept(m.ctx)
			if err != nil {
				return
			}
			m.accept(conn, model.TCP)
		}
	}()
	return true
}

func (m *Mesh) dialTCPPunch(ctx context.Context, candidate link.Candidate, identity ed25519.PublicKey) (*transport.PunchStream, error) {
	u, err := url.Parse(candidate.Endpoint.URL)
	if err != nil || len(identity) != ed25519.PublicKeySize {
		return nil, errors.New("invalid TCP punch candidate")
	}
	target, err := candidate.DialTarget()
	if err != nil {
		return nil, err
	}
	addresses := []netip.Addr{target}
	if !target.IsValid() {
		addresses, err = net.DefaultResolver.LookupNetIP(ctx, "ip"+strconv.Itoa(candidate.Family), u.Hostname())
		if err != nil {
			return nil, err
		}
	}
	port, _ := strconv.ParseUint(u.Port(), 10, 16)
	var last error = errors.New("no address for TCP punch candidate")
	for i, address := range addresses {
		attempt := ctx
		cancel := func() {}
		if deadline, ok := ctx.Deadline(); ok {
			attempt, cancel = context.WithTimeout(ctx, time.Until(deadline)/time.Duration(len(addresses)-i))
		}
		conn, err := m.punchStream(attempt, netip.AddrPortFrom(address.Unmap(), uint16(port)), identity)
		cancel()
		if err == nil {
			return conn, nil
		}
		last = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, last
}

func (m *Mesh) punchStream(ctx context.Context, remote netip.AddrPort, identity ed25519.PublicKey) (*transport.PunchStream, error) {
	k := punchKey{remote, [32]byte(identity)}
	m.mu.Lock()
	if !m.allowsTCPPunchLocked(identity) {
		m.mu.Unlock()
		return nil, errors.New("TCP punching is not permitted")
	}
	if session := m.punches[k]; session != nil {
		select {
		case <-session.Done():
			delete(m.punches, k)
		default:
			m.mu.Unlock()
			return session.Open(ctx)
		}
	}
	if pending := m.punchDials[k]; pending != nil {
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-pending.done:
			if pending.err != nil {
				return nil, pending.err
			}
			return m.punchStream(ctx, remote, identity)
		}
	}
	if len(m.punches)+len(m.punchDials) >= 64 {
		m.mu.Unlock()
		return nil, errors.New("TCP punch connection limit reached")
	}
	pending := &punchDial{done: make(chan struct{})}
	m.punchDials[k] = pending
	m.mu.Unlock()
	var result *transport.PunchStream
	conn, err := transport.DialTCPPort(ctx, m.listener.Addr().(*net.TCPAddr), remote)
	if err == nil {
		var session *transport.PunchMux
		session, err = transport.NewPunchMux(ctx, conn, m.identity.Public().(ed25519.PublicKey), m.tls, func(pub ed25519.PublicKey) bool { return pub.Equal(identity) && m.allowsTCPPunch(pub) })
		if err == nil {
			if !m.installPunch(session) {
				session.Close()
				err = errors.New("TCP punch connection no longer permitted")
			} else {
				result, err = session.Open(ctx)
			}
		}
	}
	m.mu.Lock()
	pending.err = err
	delete(m.punchDials, k)
	close(pending.done)
	m.mu.Unlock()
	return result, err
}
