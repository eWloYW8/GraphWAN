package transport

import (
	"context"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/graphwan/graphwan/internal/model"
	quic "github.com/quic-go/quic-go"
)

const quicProtocol = "graphwan.quic.v1"

type QUICHub struct {
	udp      *UDP
	engine   *quic.Transport
	listener *quic.Listener
	slots    chan struct{}
}

// ListenUDPQUIC owns one UDP socket for native UDP and QUIC. Native-only hubs
// retain their independent ListenUDP constructor, including wire compatibility.
func ListenUDPQUIC(address string, identity ed25519.PrivateKey) (*UDP, *QUICHub, error) {
	tlsConfig, err := PeerServerTLS(identity)
	if err != nil {
		return nil, nil, err
	}
	tlsConfig.NextProtos = []string{quicProtocol}
	addr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return nil, nil, err
	}
	socket, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, nil, err
	}
	udp := newUDP(socket, false)
	h := &QUICHub{udp: udp, slots: make(chan struct{}, 512)}
	reset, err := hkdf.Key(sha256.New, identity.Seed(), nil, "GraphWAN QUIC stateless reset v1", 32)
	if err != nil {
		udp.Close()
		return nil, nil, err
	}
	var resetKey quic.StatelessResetKey
	copy(resetKey[:], reset)
	h.engine = &quic.Transport{
		Conn:                  &quicSocket{PacketConn: socket, hub: udp},
		ConnectionIDGenerator: quicIDs{}, StatelessResetKey: &resetKey,
		DisableVersionNegotiationPackets: true,
		VerifySourceAddress:              func(net.Addr) bool { return true },
		ConnContext: func(ctx context.Context, _ *quic.ClientInfo) (context.Context, error) {
			if err := h.reserve(); err != nil {
				return nil, err
			}
			context.AfterFunc(ctx, func() { <-h.slots })
			return ctx, nil
		},
	}
	udp.closeQUIC = h.engine.Close
	h.listener, err = h.engine.Listen(tlsConfig, quicConfig())
	if err != nil {
		udp.Close()
		return nil, nil, err
	}
	return udp, h, nil
}
func quicConfig() *quic.Config {
	return &quic.Config{Versions: []quic.Version{quic.Version1}, EnableDatagrams: true,
		HandshakeIdleTimeout: 5 * time.Second, MaxIdleTimeout: 15 * time.Second,
		InitialPacketSize: 1200, DisablePathMTUDiscovery: true,
		MaxIncomingStreams: -1, MaxIncomingUniStreams: -1,
		InitialStreamReceiveWindow: 16 * 1024, MaxStreamReceiveWindow: 16 * 1024,
		InitialConnectionReceiveWindow: 32 * 1024, MaxConnectionReceiveWindow: 32 * 1024,
	}
}
func (h *QUICHub) reserve() error {
	select {
	case <-h.udp.done:
		return net.ErrClosed
	default:
	}
	select {
	case h.slots <- struct{}{}:
		return nil
	default:
		return errors.New("QUIC connection limit reached")
	}
}
func (h *QUICHub) Close() error { return h.udp.Close() }
func (h *QUICHub) Accept(ctx context.Context) (*QUIC, error) {
	for {
		conn, err := h.listener.Accept(ctx)
		if err != nil {
			return nil, err
		}
		if !conn.ConnectionState().SupportsDatagrams.Remote {
			conn.CloseWithError(1, "datagram support required")
			continue
		}
		return newQUIC(conn), nil
	}
}
func (h *QUICHub) Dial(ctx context.Context, endpoint model.Endpoint, family int, identity ed25519.PublicKey) (*QUIC, error) {
	return h.DialAt(ctx, endpoint, family, identity, netip.Addr{})
}

// DialAt pins a DNS answer while retaining the endpoint TLS hostname.
func (h *QUICHub) DialAt(ctx context.Context, endpoint model.Endpoint, family int, identity ed25519.PublicKey, target netip.Addr) (*QUIC, error) {
	if err := endpoint.Validate(); err != nil {
		return nil, err
	}
	if endpoint.Transport != model.QUIC || family != 4 && family != 6 {
		return nil, errors.New("invalid QUIC candidate")
	}
	u, _ := url.Parse(endpoint.URL)
	if _, err := EndpointDialAddress(endpoint, family, target); err != nil {
		return nil, err
	}
	addresses := []netip.Addr{target}
	var err error
	if !target.IsValid() {
		addresses, err = net.DefaultResolver.LookupNetIP(ctx, "ip"+strconv.Itoa(family), u.Hostname())
		if err != nil {
			return nil, err
		}
	}
	if len(addresses) == 0 {
		return nil, errors.New("endpoint has no address for permitted family")
	}
	if err := h.reserve(); err != nil {
		return nil, err
	}
	retained := false
	defer func() {
		if !retained {
			<-h.slots
		}
	}()
	port, _ := strconv.ParseUint(u.Port(), 10, 16)
	tlsConfig := PeerClientTLS(u.Hostname(), identity, nil)
	tlsConfig.NextProtos = []string{quicProtocol}
	for i, address := range addresses {
		attempt := ctx
		cancel := func() {}
		if deadline, ok := ctx.Deadline(); ok {
			attempt, cancel = context.WithTimeout(ctx, time.Until(deadline)/time.Duration(len(addresses)-i))
		}
		conn, dialErr := h.engine.Dial(attempt, net.UDPAddrFromAddrPort(netip.AddrPortFrom(address, uint16(port))), tlsConfig, quicConfig())
		cancel()
		if dialErr != nil {
			err = dialErr
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		if !conn.ConnectionState().SupportsDatagrams.Remote {
			conn.CloseWithError(1, "datagram support required")
			return nil, errors.New("peer did not negotiate QUIC datagrams")
		}
		retained = true
		context.AfterFunc(conn.Context(), func() { <-h.slots })
		return newQUIC(conn), nil
	}
	return nil, err
}

// QUIC preserves unreliable whole messages over RFC 9221 datagrams. Streams are
// disabled. A frame larger than the conservative path budget is fragmented;
// incomplete messages expire instead of acquiring retransmission semantics.
type QUIC struct {
	conn          *quic.Conn
	sequence      atomic.Uint64
	send, receive chan struct{}
	reassembly    quicReassembler
	once          sync.Once
}

func newQUIC(conn *quic.Conn) *QUIC {
	return &QUIC{conn: conn, send: make(chan struct{}, 1), receive: make(chan struct{}, 1)}
}
func (q *QUIC) LocalAddr() net.Addr  { return q.conn.LocalAddr() }
func (q *QUIC) RemoteAddr() net.Addr { return q.conn.RemoteAddr() }
func (q *QUIC) Unreliable() bool     { return true }
func (q *QUIC) Close() error {
	q.once.Do(func() { q.conn.CloseWithError(0, "peer session closed") })
	return nil
}
func (q *QUIC) Send(ctx context.Context, raw []byte) error {
	if len(raw) == 0 || len(raw) > MaxMessage {
		return errors.New("invalid QUIC message size")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case q.send <- struct{}{}:
		defer func() { <-q.send }()
	case <-ctx.Done():
		return ctx.Err()
	case <-q.conn.Context().Done():
		return net.ErrClosed
	}
	id := q.sequence.Add(1)
	if id == 0 {
		q.Close()
		return errors.New("QUIC message ID exhausted")
	}
	finished := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { q.Close(); close(finished) })
	defer func() {
		if !stop() {
			<-finished
		}
	}()
	for offset := 0; offset < len(raw); offset += quicFragmentPayload {
		size := min(quicFragmentPayload, len(raw)-offset)
		fragment := make([]byte, quicFragmentHeader+size)
		fragment[0] = 1
		binary.BigEndian.PutUint64(fragment[1:9], id)
		binary.BigEndian.PutUint16(fragment[9:11], uint16(len(raw)))
		binary.BigEndian.PutUint16(fragment[11:13], uint16(offset))
		copy(fragment[quicFragmentHeader:], raw[offset:offset+size])
		if err := q.conn.SendDatagram(fragment); err != nil {
			return ctxError(ctx, err)
		}
	}
	return ctx.Err()
}
func (q *QUIC) Receive(ctx context.Context) ([]byte, error) {
	select {
	case q.receive <- struct{}{}:
		defer func() { <-q.receive }()
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-q.conn.Context().Done():
		return nil, net.ErrClosed
	}
	for {
		fragment, err := q.conn.ReceiveDatagram(ctx)
		if err != nil {
			return nil, err
		}
		if raw := q.reassembly.receive(fragment, time.Now()); raw != nil {
			return raw, nil
		}
	}
}
