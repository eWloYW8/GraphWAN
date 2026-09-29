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

	"github.com/eWloYW8/GraphWAN/internal/link"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/transport"
)

type punchKey struct {
	remote   netip.AddrPort
	identity [32]byte
	local    netip.AddrPort // Zero for an outgoing reservation before routing.
}
type punchDial struct {
	done chan struct{}
	err  error
}

// Each authenticated neighbor has its own bounded allowance. A busy peer must
// not consume the connection slots needed by unrelated configured neighbors.
// Count a dial and its installed connection once; the current key can replace
// its pending reservation or a closed session even when the allowance is full.
func (m *Mesh) punchConnectionAvailableLocked(k punchKey) bool {
	used := 0
	installedTargets := map[punchKey]bool{}
	for existing := range m.punches {
		if existing.identity != k.identity {
			continue
		}
		installedTargets[punchKey{remote: existing.remote, identity: existing.identity}] = true
		if existing != k {
			used++
		}
	}
	reservation := punchKey{remote: k.remote, identity: k.identity}
	for pending := range m.punchDials {
		if pending.identity == k.identity && pending != reservation {
			if !installedTargets[pending] {
				used++
			}
		}
	}
	return used < max(64, m.punchCapacityLocked(k.identity[:], true))
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

// Budget one peer's multiplexed streams from locally authorized configuration,
// never from remote stream requests or observed live stream counts. Endpoint
// aliases and IPv6 egress scopes can produce distinct candidates on one socket.
// Four slots per potential candidate cover current/replacement sessions plus
// retiring generations; the fixed headroom covers bounded concurrent admission.
func (m *Mesh) punchStreamCapacityLocked(identity ed25519.PublicKey) int {
	return m.punchCapacityLocked(identity, false)
}

// Physical sessions also account for DNS fanout: one hostname may reach many
// distinct sockets. Local endpoints matter for incoming connections; resolving
// their names uses the same bounded asynchronous cache as outgoing candidates.
func (m *Mesh) punchCapacityLocked(identity ed25519.PublicKey, resolve bool) int {
	budget := 16
	maximum := int(^uint(0) >> 1)
	count := func(endpoints []model.Endpoint) (tcp, scopes int) {
		for _, endpoint := range endpoints {
			if endpoint.Transport == model.TCP {
				addresses := 1
				if resolve && m.dns != nil {
					if host := endpointHostname(endpoint.URL); host != "" {
						addresses = max(1, len(m.dns.addresses(m.ctx, host)))
					}
				}
				tcp += addresses
			}
			if endpoint.Transport == model.UDP && endpoint.Source == model.Interface && (link.Candidate{Endpoint: endpoint}).NeedsScope() {
				scopes++
			}
		}
		return
	}
	for _, g := range m.groups {
		cfg := g.policy.Load()
		if !cfg.peer.Edge.Enabled || !cfg.peer.Edge.Methods.HolePunch || !slices.Contains(cfg.peer.Edge.Transports, model.TCP) || !bytes.Equal(cfg.peer.PublicKey, identity) {
			continue
		}
		localTCP, localScopes := count(cfg.endpoints)
		peerTCP, peerScopes := count(cfg.peer.Endpoints)
		increment := 4 * max(2, (localTCP+peerTCP)*max(1, localScopes, peerScopes))
		if increment > maximum-budget {
			return maximum
		}
		budget += increment
	}
	return max(32, budget)
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
	local, err := punchAddress(session.LocalAddr())
	if err != nil {
		return false
	}
	// The same remote source port can reach several local NICs. Pooling only
	// by the remote half would reject those independent TCP four-tuples.
	k := punchKey{remote: address, identity: [32]byte(session.Identity()), local: local}
	m.mu.Lock()
	if !m.allowsTCPPunchLocked(session.Identity()) || !m.punchConnectionAvailableLocked(k) {
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
	session.EnsureStreamCapacity(m.punchStreamCapacityLocked(session.Identity()))
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
	k := punchKey{remote: remote, identity: [32]byte(identity)}
	m.mu.Lock()
	if !m.allowsTCPPunchLocked(identity) {
		m.mu.Unlock()
		return nil, errors.New("TCP punching is not permitted")
	}
	for existing, session := range m.punches {
		if existing.remote != remote || existing.identity != k.identity {
			continue
		}
		select {
		case <-session.Done():
			delete(m.punches, existing)
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
	if !m.punchConnectionAvailableLocked(k) {
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
