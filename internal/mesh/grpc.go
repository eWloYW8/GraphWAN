package mesh

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/transport"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	grpcpeer "google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// gRPC owns an HTTP/2 connection after classification; cap even idle transports.
// Its five-second connection timeout handles an incomplete preface/settings.
func (m *Mesh) handoffGRPC(ctx context.Context, conn net.Conn) bool {
	select {
	case m.grpcSlots <- struct{}{}:
	default:
		return false
	}
	// grpc.Server.Stop waits for its preface readers before closing registered
	// transports. Cancel these sockets ourselves, including ones not registered
	// yet, so an incomplete HTTP/2 preface cannot delay Agent shutdown.
	stop := context.AfterFunc(m.ctx, func() { conn.Close() })
	release := sync.OnceFunc(func() { stop(); <-m.grpcSlots })
	conn.SetReadDeadline(time.Time{})
	select {
	case m.grpcListener.pending <- &ingressConn{Conn: conn, release: release}:
		return true
	case <-ctx.Done():
	case <-m.grpcListener.done:
	}
	release()
	return false
}

func (m *Mesh) startGRPC() {
	m.grpcListener = &connIngress{address: m.listener.Addr(), pending: make(chan net.Conn), done: make(chan struct{})}
	m.grpcServer = grpc.NewServer(
		grpc.UnknownServiceHandler(m.acceptGRPC),
		grpc.MaxRecvMsgSize(transport.GRPCMaxMessage), grpc.MaxSendMsgSize(transport.GRPCMaxMessage),
		grpc.MaxConcurrentStreams(64), grpc.MaxHeaderListSize(8192),
		grpc.ConnectionTimeout(5*time.Second),
		grpc.KeepaliveParams(keepalive.ServerParameters{MaxConnectionIdle: 10 * time.Second}))
	m.wg.Add(1)
	go func() { defer m.wg.Done(); m.grpcServer.Serve(m.grpcListener) }()
}

func (m *Mesh) acceptGRPC(_ any, stream grpc.ServerStream) error {
	method, ok := grpc.MethodFromServerStream(stream)
	if !ok || !m.allowsEndpoint(model.GRPC, method) {
		return status.Error(codes.PermissionDenied, "unconfigured gRPC endpoint")
	}
	md, _ := metadata.FromIncomingContext(stream.Context())
	if len(md.Get("origin")) != 0 {
		return status.Error(codes.PermissionDenied, "Agent protocol required")
	}
	p, ok := grpcpeer.FromContext(stream.Context())
	if !ok || p.Addr == nil || p.LocalAddr == nil {
		return status.Error(codes.Internal, "peer address unavailable")
	}
	conn := transport.NewGRPC(stream, p.LocalAddr, p.Addr, method, nil)
	defer conn.Close()
	if err := stream.SendHeader(metadata.Pairs("graphwan-protocol", transport.GRPCProtocol)); err != nil {
		return err
	}
	m.accept(conn, model.GRPC)
	// Returning tears down this RPC and interrupts any blocked SendMsg/RecvMsg.
	// The Link's receive loop lives independently of the gRPC handler goroutine.
	select {
	case <-conn.Done():
		return status.Error(codes.Canceled, "peer session closed")
	case <-stream.Context().Done():
		return stream.Context().Err()
	case <-m.ctx.Done():
		return context.Canceled
	}
}
