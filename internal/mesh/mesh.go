// Package mesh reconciles configured peer edges over independently live Links.
package mesh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/graphwan/graphwan/internal/link"
	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/peer"
	"github.com/graphwan/graphwan/internal/secure"
	"github.com/graphwan/graphwan/internal/transport"
	"google.golang.org/grpc"
)

type Receive func(context.Context, model.ID, []byte) error
type key struct{ network, peer model.ID }
type policy struct {
	network   model.ID
	self      model.ID
	cipher    model.CipherSuite
	peer      model.Peer
	endpoints []model.Endpoint
}
type Mesh struct {
	dns          *endpointDNS
	identity     ed25519.PrivateKey
	ctx          context.Context
	cancel       context.CancelFunc
	listener     net.Listener
	tcp          *transport.TCP
	punches      map[punchKey]*transport.PunchMux
	punchDials   map[punchKey]*punchDial
	webListener  *connIngress
	webServer    *http.Server
	grpcListener *connIngress
	grpcServer   *grpc.Server
	grpcSlots    chan struct{}
	tls          *tls.Config
	sniffSlots   chan struct{}
	pending      map[net.Conn]bool
	udp          *transport.UDP
	quic         *transport.QUICHub
	receive      Receive
	mu           sync.Mutex
	groups       map[key]*group
	slots        chan struct{}
	acceptSlots  chan struct{}
	wg           sync.WaitGroup
	closed       bool
	linkOptions  link.Options
}

func New(parent context.Context, identity ed25519.PrivateKey, host string, port uint16, receive Receive) (*Mesh, error) {
	if len(identity) != ed25519.PrivateKeySize || receive == nil {
		return nil, errors.New("mesh requires identity and packet receiver")
	}
	tlsConfig, err := transport.PeerServerTLS(identity)
	if err != nil {
		return nil, err
	}
	listener, err := transport.ListenTCP(parent, net.JoinHostPort(host, strconv.Itoa(int(port))))
	if err != nil {
		return nil, err
	}
	udp, quicHub, err := transport.ListenUDPQUIC(listener.Addr().String(), identity)
	if err != nil {
		listener.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	m := &Mesh{grpcSlots: make(chan struct{}, 512), sniffSlots: make(chan struct{}, 8), pending: map[net.Conn]bool{}, identity: bytes.Clone(identity), ctx: ctx, cancel: cancel, listener: listener, udp: udp, receive: receive, groups: map[key]*group{}, slots: make(chan struct{}, 8), acceptSlots: make(chan struct{}, 8)}
	m.dns = newEndpointDNS()
	m.quic = quicHub
	m.tcp = listener
	m.punches = map[punchKey]*transport.PunchMux{}
	m.punchDials = map[punchKey]*punchDial{}
	m.tls = tlsConfig
	m.tls.NextProtos = []string{"h2", "http/1.1"}
	m.startHTTP()
	m.startGRPC()
	m.wg.Add(3)
	go m.acceptTCP()
	go m.acceptUDP()
	go m.acceptQUIC()
	return m, nil
}
func (m *Mesh) Port() uint16 { return uint16(m.listener.Addr().(*net.TCPAddr).Port) }

func (m *Mesh) STUNBinding(ctx context.Context, kind model.Transport, server netip.AddrPort) (netip.AddrPort, error) {
	if kind == model.TCP {
		return m.tcp.STUNBinding(ctx, server)
	}
	if kind != model.UDP {
		return netip.AddrPort{}, errors.New("unsupported STUN transport")
	}
	return m.udp.STUNBinding(ctx, server)
}
func (m *Mesh) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.cancel()
	groups := m.groups
	m.groups = map[key]*group{}
	punches := m.punches
	m.punches = map[punchKey]*transport.PunchMux{}
	pending := make([]net.Conn, 0, len(m.pending))
	for conn := range m.pending {
		pending = append(pending, conn)
	}
	m.mu.Unlock()
	m.listener.Close()
	m.webListener.Close()
	m.webServer.Close()
	m.grpcListener.Close()
	m.grpcServer.Stop()
	for _, conn := range pending {
		conn.Close()
	}
	m.udp.Close()
	for _, session := range punches {
		session.Close()
	}
	for _, g := range groups {
		g.close()
	}
	m.wg.Wait()
	m.dns.wg.Wait()
	return nil
}

// Apply cannot fail after validation and does not bind new operating-system
// resources. The owning runtime prepares listener/TUN changes before calling it.
func (m *Mesh) Apply(snapshot model.Snapshot) error {
	if err := snapshot.Validate(snapshot.AgentID); err != nil {
		return err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return net.ErrClosed
	}
	hosts := map[string]bool{}
	for _, network := range snapshot.Networks {
		for _, peer := range network.Peers {
			for _, endpoint := range peer.Endpoints {
				if host := endpointHostname(endpoint.URL); host != "" {
					hosts[host] = true
				}
			}
		}
	}
	m.dns.configure(hosts)
	old := m.groups
	next := make(map[key]*group)
	retired := []*group{}
	for _, network := range snapshot.Networks {
		for _, p := range network.Peers {
			k := key{network.ID, p.Node.ID}
			cfg := &policy{network: network.ID, self: network.Self.ID, cipher: network.Cipher, peer: p, endpoints: snapshot.Endpoints}
			if existing := old[k]; existing != nil && compatible(existing.policy.Load(), cfg) {
				existing.policy.Store(cfg)
				existing.edge.SetPreferred(p.Edge.PreferredCandidate)
				next[k] = existing
			} else {
				next[k] = m.newGroup(cfg)
				if existing != nil {
					retired = append(retired, existing)
				}
			}
		}
	}
	for k, g := range old {
		if _, ok := next[k]; !ok {
			retired = append(retired, g)
		}
	}
	m.groups = next
	stalePunches := []*transport.PunchMux{}
	for k, session := range m.punches {
		if !m.allowsTCPPunchLocked(session.Identity()) {
			delete(m.punches, k)
			stalePunches = append(stalePunches, session)
		}
	}
	m.mu.Unlock()
	for _, session := range stalePunches {
		session.Close()
	}
	for _, g := range retired {
		g.close()
	}
	return nil
}
func compatible(a, b *policy) bool {
	if a.network != b.network || a.self != b.self || a.cipher != b.cipher || a.peer.Node.ID != b.peer.Node.ID || a.peer.Edge.ID != b.peer.Edge.ID || !bytes.Equal(a.peer.PublicKey, b.peer.PublicKey) || a.peer.Edge.Methods != b.peer.Edge.Methods || !slices.Equal(a.peer.Edge.Transports, b.peer.Edge.Transports) {
		return false
	}
	normalized := func(endpoints []model.Endpoint) []model.Endpoint {
		copy := []model.Endpoint{}
		for _, endpoint := range endpoints {
			// Discovery expiry/remapping governs new dials, not the identity of
			// already authenticated healthy sessions. STUN outages must not stop data.
			if endpoint.Source == model.Observed {
				continue
			}
			endpoint.ExpiresAt = time.Time{}
			copy = append(copy, endpoint)
		}
		return copy
	}
	return reflect.DeepEqual(normalized(a.endpoints), normalized(b.endpoints)) && reflect.DeepEqual(normalized(a.peer.Endpoints), normalized(b.peer.Endpoints))
}
func (m *Mesh) Send(ctx context.Context, network, remote model.ID, frame []byte) error {
	m.mu.Lock()
	g := m.groups[key{network, remote}]
	m.mu.Unlock()
	if g == nil {
		return link.ErrUnavailable
	}
	return g.edge.Send(ctx, frame)
}
func (m *Mesh) Report() []model.LinkStatus {
	m.mu.Lock()
	groups := make([]*group, 0, len(m.groups))
	for _, g := range m.groups {
		groups = append(groups, g)
	}
	m.mu.Unlock()
	result := []model.LinkStatus{}
	for _, g := range groups {
		result = append(result, g.edge.Report()...)
	}
	slices.SortFunc(result, func(a, b model.LinkStatus) int {
		if a.LinkID < b.LinkID {
			return -1
		}
		if a.LinkID > b.LinkID {
			return 1
		}
		return 0
	})
	return result
}
func (m *Mesh) secure(cfg *policy, kind model.Transport) secure.Config {
	return secure.Config{Network: cfg.network, Edge: cfg.peer.Edge.ID, Local: cfg.self, Peer: cfg.peer.Node.ID, Transport: kind, Cipher: cfg.cipher, Identity: m.identity, PeerIdentity: cfg.peer.PublicKey}
}
func (m *Mesh) acceptTCP() {
	defer m.wg.Done()
	for {
		conn, err := m.listener.Accept()
		if err != nil {
			return
		}
		m.classify(conn)
	}
}
func (m *Mesh) acceptUDP() {
	defer m.wg.Done()
	for {
		conn, err := m.udp.Accept(m.ctx)
		if err != nil {
			return
		}
		m.accept(conn, model.UDP)
	}
}
func (m *Mesh) acceptQUIC() {
	defer m.wg.Done()
	for {
		conn, err := m.quic.Accept(m.ctx)
		if err != nil {
			return
		}
		m.accept(conn, model.QUIC)
	}
}
func (m *Mesh) accept(conn transport.Conn, kind model.Transport) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		conn.Close()
		return
	}
	select {
	case m.acceptSlots <- struct{}{}:
	default:
		m.mu.Unlock()
		conn.Close()
		return
	}
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer func() { <-m.acceptSlots }()
		ctx, cancel := context.WithTimeout(m.ctx, 12*time.Second)
		defer cancel()
		var selected *group
		channel, err := peer.Accept(ctx, conn, kind, func(hello peer.Hello) (secure.Config, error) {
			m.mu.Lock()
			g := m.groups[key{hello.Network, hello.Initiator}]
			m.mu.Unlock()
			if g == nil {
				return secure.Config{}, errors.New("unconfigured peer")
			}
			cfg := g.policy.Load()
			if cfg.self != hello.Responder || cfg.peer.Edge.ID != hello.Edge || !slices.Contains(cfg.peer.Edge.Transports, kind) {
				return secure.Config{}, errors.New("edge policy denied")
			}
			selected = g
			return m.secure(cfg, kind), nil
		})
		if err != nil {
			return
		}
		introduction, err := receiveIntroduction(ctx, channel)
		if err != nil {
			channel.Close()
			return
		}
		cfg := selected.policy.Load()
		var candidate *link.Candidate
		// The remote dialing direction must target an endpoint actually advertised
		// by this Agent and allowed by this Edge's connection policy.
		for _, c := range selected.candidates(cfg, false) {
			if introduction.Target != "" {
				address, err := netip.ParseAddr(introduction.Target)
				if err != nil {
					continue
				}
				c, err = link.ResolveCandidate(c, address)
				if err != nil {
					continue
				}
			}
			if c.ID == introduction.Candidate && c.Endpoint.Transport == kind && candidateIngress(c, conn) {
				copy := c
				candidate = &copy
				break
			}
		}
		if candidate == nil {
			channel.Close()
			return
		}
		m.mu.Lock()
		stillCurrent := m.groups[key{cfg.network, cfg.peer.Node.ID}] == selected
		m.mu.Unlock()
		if !stillCurrent {
			channel.Close()
			return
		}
		selected.register(channel, *candidate)
	}()
}
func addressFamily(address net.Addr) int {
	host, _, err := net.SplitHostPort(address.String())
	if err != nil {
		return 0
	}
	ip, parseErr := netip.ParseAddr(host)
	if parseErr != nil {
		return 0
	}
	if ip.Unmap().Is4() {
		return 4
	}
	return 6
}

type group struct {
	mesh     *Mesh
	policy   atomic.Pointer[policy]
	edge     *link.Edge
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	links    map[string]*link.Link
	attempts map[string]*attempt
	retained map[string]link.Candidate
	wg       sync.WaitGroup
}
type attempt struct {
	inflight bool
	next     time.Time
	delay    time.Duration
}

func (m *Mesh) newGroup(cfg *policy) *group {
	ctx, cancel := context.WithCancel(m.ctx)
	g := &group{mesh: m, edge: link.NewCoordinatedEdge(cfg.self, cfg.peer.Node.ID, cfg.peer.Edge.PreferredCandidate), ctx: ctx, cancel: cancel, links: map[string]*link.Link{}, attempts: map[string]*attempt{}, retained: map[string]link.Candidate{}}
	g.policy.Store(cfg)
	g.wg.Add(1)
	go g.schedule()
	return g
}
func (g *group) close() {
	g.cancel()
	g.mu.Lock()
	links := make([]*link.Link, 0, len(g.links))
	for _, l := range g.links {
		links = append(links, l)
	}
	g.mu.Unlock()
	for _, l := range links {
		l.Close()
	}
	g.wg.Wait()
}
func (g *group) register(channel *peer.Channel, candidate link.Candidate) {
	g.mu.Lock()
	if g.ctx.Err() != nil {
		g.mu.Unlock()
		channel.Close()
		return
	}
	cfg := g.policy.Load()
	l, err := link.New(g.ctx, channel, link.Info{NetworkID: cfg.network, EdgeID: cfg.peer.Edge.ID, PeerID: cfg.peer.Node.ID, CandidateID: candidate.ID, Transport: candidate.Endpoint.Transport}, g.mesh.linkOptions)
	if err != nil {
		g.mu.Unlock()
		channel.Close()
		return
	}
	g.links[l.ID()] = l
	if candidate.Endpoint.Source == model.Observed || candidate.Target.IsValid() {
		g.retained[candidate.ID] = candidate
	}
	g.edge.Add(l)
	g.wg.Add(2)
	g.mu.Unlock()
	// A slow local TUN or downstream peer must not hold up path negotiation.
	go func() {
		defer g.wg.Done()
		for {
			select {
			case message := <-l.Selections():
				g.edge.HandleSelection(l, message)
			case <-l.Done():
				return
			case <-g.ctx.Done():
				return
			}
		}
	}()
	go func() {
		defer g.wg.Done()
		defer func() { l.Close(); g.edge.Remove(l.ID()); g.mu.Lock(); delete(g.links, l.ID()); g.mu.Unlock() }()
		for {
			select {
			case raw, ok := <-l.Packets():
				if !ok {
					return
				}
				g.mesh.receive(g.ctx, cfg.peer.Node.ID, raw)
			case <-g.ctx.Done():
				return
			}
		}
	}()
}

func candidateIngress(candidate link.Candidate, conn transport.Conn) bool {
	if candidate.Endpoint.Transport == model.TCP {
		_, punched := conn.(interface{ TCPPunch() bool })
		if (candidate.Method == link.Punch) != punched {
			return false
		}
	}
	if path, ok := conn.(interface{ EndpointPath() string }); ok {
		parsed, err := url.Parse(candidate.Endpoint.URL)
		if err != nil || candidate.Endpoint.Source != model.Manual {
			return false
		}
		if candidate.Endpoint.Transport == model.GRPC {
			return transport.GRPCMethod(parsed) == path.EndpointPath()
		}
		return transport.WebSocketPath(parsed) == path.EndpointPath()
	}
	return candidate.Family == addressFamily(conn.RemoteAddr())
}
