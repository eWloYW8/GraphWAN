package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

const punchALPN = "graphwan.punch.v1"

var punchMagic = [4]byte{'H', 'G', 'W', 1}

type PunchMux struct {
	session     *yamux.Session
	identity    ed25519.PublicKey
	streamMu    sync.Mutex
	streamLimit int
	streams     int
	socket      net.Conn
}

// NewPunchMux works for active/passive AND simultaneous-open TCP sockets. Both
// ends exchange their identity hint, authorize it against current topology, then
// prove possession with mutually pinned TLS 1.3. The smaller key is TLS/yamux
// client, independently of which kernel called accept. Every logical stream
// subsequently runs the ordinary per-Network/Edge Noise admission.
func NewPunchMux(ctx context.Context, conn net.Conn, identity ed25519.PublicKey, certificate *tls.Config, authorize func(ed25519.PublicKey) bool) (_ *PunchMux, err error) {
	defer func() {
		if err != nil {
			conn.Close()
		}
	}()
	cleanup, err := deadline(ctx, conn.SetDeadline)
	if err != nil {
		return nil, err
	}
	defer func() { cleanup() }()
	if len(identity) != ed25519.PublicKeySize {
		return nil, errors.New("invalid local punch identity")
	}
	var hello [36]byte
	copy(hello[:4], punchMagic[:])
	copy(hello[4:], identity)
	if err := writeFull(conn, hello[:]); err != nil {
		return nil, ctxError(ctx, err)
	}
	if _, err := io.ReadFull(conn, hello[:]); err != nil {
		return nil, ctxError(ctx, err)
	}
	remote := ed25519.PublicKey(bytes.Clone(hello[4:]))
	if [4]byte(hello[:4]) != punchMagic || remote.Equal(identity) || !authorize(remote) {
		return nil, errors.New("unconfigured TCP punch peer")
	}
	client := bytes.Compare(identity, remote) < 0
	config := certificate.Clone()
	config.NextProtos = []string{punchALPN}
	config.ClientAuth = tls.RequireAnyClientCert
	config.InsecureSkipVerify = true // VerifyConnection below pins BOTH directions, with no CA fallback.
	config.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return config.GetCertificate(nil) }
	config.VerifyConnection = func(state tls.ConnectionState) error {
		if state.NegotiatedProtocol != punchALPN || len(state.PeerCertificates) != 1 {
			return errors.New("invalid punch TLS negotiation")
		}
		leaf := state.PeerCertificates[0]
		pub, ok := leaf.PublicKey.(ed25519.PublicKey)
		if !ok || !pub.Equal(remote) {
			return errors.New("TCP punch identity mismatch")
		}
		now := time.Now()
		usage := x509.ExtKeyUsageClientAuth
		if client {
			usage = x509.ExtKeyUsageServerAuth
		}
		if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) || len(leaf.UnhandledCriticalExtensions) != 0 || leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 || !slices.Contains(leaf.ExtKeyUsage, usage) {
			return errors.New("invalid TCP punch certificate")
		}
		return nil
	}
	var secured *tls.Conn
	if client {
		secured = tls.Client(conn, config)
	} else {
		secured = tls.Server(conn, config)
	}
	if err := secured.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	// TLS 1.3 clients may finish before the server checks their certificate.
	// Publish the session only after both verifiers have accepted the peer.
	if err := writeFull(secured, punchMagic[:]); err != nil {
		return nil, ctxError(ctx, err)
	}
	var confirmed [4]byte
	if _, err := io.ReadFull(secured, confirmed[:]); err != nil {
		return nil, ctxError(ctx, err)
	}
	if confirmed != punchMagic {
		return nil, errors.New("missing authenticated TCP punch confirmation")
	}
	// Remove the establishment deadline before starting background mux reads.
	cleanup()
	cleanup = func() {}
	options := yamux.DefaultConfig()
	options.AcceptBacklog = 8
	options.KeepAliveInterval = 5 * time.Second
	options.ConnectionWriteTimeout = 2 * time.Second
	options.StreamOpenTimeout = 3 * time.Second
	options.StreamCloseTimeout = 3 * time.Second
	options.MaxStreamWindowSize = 256 * 1024
	options.LogOutput = io.Discard
	var session *yamux.Session
	if client {
		session, err = yamux.Client(secured, options)
	} else {
		session, err = yamux.Server(secured, options)
	}
	if err != nil {
		return nil, err
	}
	return &PunchMux{session: session, identity: remote, streamLimit: 32, socket: conn}, nil
}

func (p *PunchMux) Identity() ed25519.PublicKey { return bytes.Clone(p.identity) }
func (p *PunchMux) RemoteAddr() net.Addr        { return p.session.RemoteAddr() }
func (p *PunchMux) Done() <-chan struct{}       { return p.session.CloseChan() }
func (p *PunchMux) Close() error                { p.socket.Close(); return p.session.Close() }

// EnsureStreamCapacity increases the bounded stream allowance for controller-
// authorized topology. Only the owner may call it; peers cannot negotiate it.
// Keep the high-water mark for this physical session: reducing policy can leave
// closing streams and healthy expired-observation sessions awaiting renewal.
func (p *PunchMux) EnsureStreamCapacity(limit int) {
	p.streamMu.Lock()
	p.streamLimit = max(p.streamLimit, limit)
	p.streamMu.Unlock()
}

func (p *PunchMux) reserve(accept bool) error {
	p.streamMu.Lock()
	defer p.streamMu.Unlock()
	if p.session.IsClosed() {
		return net.ErrClosed
	}
	count := p.session.NumStreams()
	if p.streams >= p.streamLimit || count > p.streamLimit || !accept && count == p.streamLimit {
		return errors.New("TCP punch stream limit reached")
	}
	p.streams++
	return nil
}

func (p *PunchMux) release() {
	p.streamMu.Lock()
	p.streams--
	p.streamMu.Unlock()
}

func (p *PunchMux) Open(ctx context.Context) (*PunchStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := p.reserve(false); err != nil {
		return nil, err
	}
	type opened struct {
		stream *yamux.Stream
		err    error
	}
	result := make(chan opened)
	// OpenStream has a library-level timeout; retain the slot until it returns,
	// even when this caller cancels. Cancellation never closes unrelated streams.
	go func() {
		stream, err := p.session.OpenStream()
		select {
		case result <- opened{stream, err}:
		case <-ctx.Done():
			if stream != nil {
				stream.Close()
			}
			p.release()
		}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-result:
		if result.err != nil {
			p.release()
			return nil, result.err
		}
		return p.wrap(result.stream), nil
	}
}

func (p *PunchMux) Accept(ctx context.Context) (*PunchStream, error) {
	stream, err := p.session.AcceptStreamWithContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := p.reserve(true); err != nil {
		stream.Close()
		p.Close()
		return nil, err
	}
	return p.wrap(stream), nil
}

type punchConn struct {
	net.Conn
	release func()
}

func (c *punchConn) Close() error { err := c.Conn.Close(); c.release(); return err }

type PunchStream struct{ *Stream }

func (*PunchStream) TCPPunch() bool { return true }
func (p *PunchMux) wrap(stream *yamux.Stream) *PunchStream {
	return &PunchStream{NewStream(&punchConn{Conn: stream, release: sync.OnceFunc(p.release)})}
}
