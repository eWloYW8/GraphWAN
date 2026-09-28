package transport

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/graphwan/graphwan/internal/model"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	grpcpeer "google.golang.org/grpc/peer"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const GRPCProtocol = "graphwan-peer-v1"

// The BytesValue envelope adds one field tag and up to three length bytes.
const GRPCMaxMessage = MaxMessage + 4

// GRPCMethod treats the manual URL path as a proxy/service prefix. The standard
// protobuf service method remains intact, including for a single-segment prefix.
func GRPCMethod(u *url.URL) string {
	return strings.TrimRight(u.EscapedPath(), "/") + "/graphwan.v1.Peer/Connect"
}

type grpcStream interface {
	SendMsg(any) error
	RecvMsg(any) error
}

// GRPC owns one bidi RPC. closeStream must interrupt both SendMsg and RecvMsg:
// clients cancel the RPC; servers return from the handler when Done closes.
type GRPC struct {
	stream        grpcStream
	local, remote net.Addr
	path          string
	closeStream   func()
	once          sync.Once
	done          chan struct{}
	send, receive chan struct{}
}

func NewGRPC(stream grpcStream, local, remote net.Addr, path string, closeStream func()) *GRPC {
	return &GRPC{stream: stream, local: local, remote: remote, path: path, closeStream: closeStream, done: make(chan struct{}), send: make(chan struct{}, 1), receive: make(chan struct{}, 1)}
}
func (g *GRPC) LocalAddr() net.Addr   { return g.local }
func (g *GRPC) RemoteAddr() net.Addr  { return g.remote }
func (g *GRPC) EndpointPath() string  { return g.path }
func (g *GRPC) Done() <-chan struct{} { return g.done }
func (g *GRPC) Close() error {
	g.once.Do(func() {
		close(g.done)
		if g.closeStream != nil {
			g.closeStream()
		}
	})
	return nil
}
func (g *GRPC) operation(ctx context.Context, gate chan struct{}, run func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return ctx.Err()
	case <-g.done:
		return net.ErrClosed
	}
	finished := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { g.Close(); close(finished) })
	err := run()
	if !stop() {
		<-finished
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		g.Close()
	}
	return err
}
func (g *GRPC) Send(ctx context.Context, raw []byte) error {
	if len(raw) == 0 || len(raw) > MaxMessage {
		return errors.New("invalid gRPC message size")
	}
	return g.operation(ctx, g.send, func() error { return g.stream.SendMsg(&wrapperspb.BytesValue{Value: raw}) })
}
func (g *GRPC) Receive(ctx context.Context) ([]byte, error) {
	var message wrapperspb.BytesValue
	err := g.operation(ctx, g.receive, func() error {
		if err := g.stream.RecvMsg(&message); err != nil {
			return err
		}
		if len(message.Value) == 0 || len(message.Value) > MaxMessage {
			return errors.New("invalid gRPC message size")
		}
		return nil
	})
	return message.Value, err
}

// DialGRPC uses TLS even though the endpoint scheme is grpc. A direct Agent is
// pinned; a TLS proxy must pass ordinary PKI validation. The dial context bounds
// establishment only; Close and per-operation contexts own the established RPC.
func DialGRPC(ctx context.Context, endpoint model.Endpoint, family int, identity ed25519.PublicKey, roots *x509.CertPool) (*GRPC, error) {
	if err := endpoint.Validate(); err != nil {
		return nil, err
	}
	if endpoint.Transport != model.GRPC || family != 4 && family != 6 {
		return nil, errors.New("invalid gRPC candidate")
	}
	parsed, _ := url.Parse(endpoint.URL)
	tlsConfig := PeerClientTLS(parsed.Hostname(), identity, roots)
	tlsConfig.NextProtos = []string{"h2"}
	dialer := &net.Dialer{}
	client, err := grpc.NewClient("passthrough:///"+parsed.Host,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
		grpc.WithContextDialer(func(ctx context.Context, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp"+strconv.Itoa(family), address)
		}),
		grpc.WithNoProxy(), grpc.WithDisableRetry(), grpc.WithDisableServiceConfig(),
		grpc.WithMaxHeaderListSize(8192),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(GRPCMaxMessage), grpc.MaxCallSendMsgSize(GRPCMaxMessage)))
	if err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	closeClient := func() { cancel(); client.Close() }
	finished := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { closeClient(); close(finished) })
	success := false
	defer func() {
		if !stop() {
			<-finished
		}
		if !success {
			closeClient()
		}
	}()
	method := GRPCMethod(parsed)
	stream, err := client.NewStream(lifetime, &grpc.StreamDesc{ClientStreams: true, ServerStreams: true}, method, grpc.WaitForReady(true))
	if err != nil {
		return nil, ctxError(ctx, err)
	}
	header, err := stream.Header()
	if err != nil {
		return nil, ctxError(ctx, err)
	}
	if values := header.Get("graphwan-protocol"); len(values) != 1 || values[0] != GRPCProtocol {
		return nil, ctxError(ctx, errors.New("gRPC peer protocol not acknowledged"))
	}
	p, ok := grpcpeer.FromContext(stream.Context())
	if !ok || p.Addr == nil || p.LocalAddr == nil {
		return nil, errors.New("gRPC peer address unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	success = true
	return NewGRPC(stream, p.LocalAddr, p.Addr, method, closeClient), nil
}
