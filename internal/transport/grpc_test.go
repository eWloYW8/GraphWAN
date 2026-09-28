package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"net"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/testutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	grpcpeer "google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func grpcPairServer(t *testing.T, acknowledge bool) (model.Endpoint, ed25519.PublicKey, <-chan *GRPC) {
	t.Helper()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	config, err := PeerServerTLS(key)
	if err != nil {
		t.Fatal(err)
	}
	config.NextProtos = []string{"h2"}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := model.Endpoint{ID: testutil.ID(1), Transport: model.GRPC, Source: model.Manual, URL: "grpc://" + listener.Addr().String() + "/custom"}
	parsed, _ := url.Parse(endpoint.URL)
	accepted := make(chan *GRPC, 8)
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(config)), grpc.MaxRecvMsgSize(GRPCMaxMessage), grpc.MaxSendMsgSize(GRPCMaxMessage), grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		method, _ := grpc.MethodFromServerStream(stream)
		if method != GRPCMethod(parsed) {
			return status.Error(codes.PermissionDenied, "wrong path")
		}
		if !acknowledge {
			<-stream.Context().Done()
			return stream.Context().Err()
		}
		p, _ := grpcpeer.FromContext(stream.Context())
		conn := NewGRPC(stream, p.LocalAddr, p.Addr, method, nil)
		defer conn.Close()
		if err := stream.SendHeader(metadata.Pairs("graphwan-protocol", GRPCProtocol)); err != nil {
			return err
		}
		accepted <- conn
		select {
		case <-conn.Done():
			return status.Error(codes.Canceled, "closed")
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}))
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	return endpoint, pub, accepted
}

func TestGRPCBinaryBoundariesAndIndependentLifetime(t *testing.T) {
	endpoint, pub, accepted := grpcPairServer(t, true)
	dialCtx, cancelDial := context.WithTimeout(context.Background(), time.Second)
	defer cancelDial()
	a, err := DialGRPC(dialCtx, endpoint, 4, pub, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	cancelDial() // Successful establishment must not tie a Link to the dial timer.
	b := <-accepted
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, size := range []int{1, 80, 1400, MaxMessage} {
		frame := bytes.Repeat([]byte{byte(size)}, size)
		for _, pair := range [][2]*GRPC{{a, b}, {b, a}} {
			if err := pair[0].Send(ctx, frame); err != nil {
				t.Fatal(err)
			}
			got, err := pair[1].Receive(ctx)
			if err != nil || !bytes.Equal(frame, got) {
				t.Fatalf("frame size %d: %v", size, err)
			}
		}
	}
	if a.LocalAddr() == nil || a.RemoteAddr() == nil || a.EndpointPath() != "/custom/graphwan.v1.Peer/Connect" {
		t.Fatal("missing connection metadata")
	}
	if err := a.Send(ctx, make([]byte, MaxMessage+1)); err == nil {
		t.Fatal("oversized send accepted")
	}
	readCtx, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	if _, err := b.Receive(readCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("server receive cancellation: %v", err)
	}
	if _, err := a.Receive(ctx); err == nil {
		t.Fatal("server RPC closure did not reach client")
	}
}

func TestGRPCCancellationUnderFlowControl(t *testing.T) {
	endpoint, pub, accepted := grpcPairServer(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	a, err := DialGRPC(ctx, endpoint, 4, pub, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b := <-accepted
	defer b.Close()
	// The remote side does not read: filling HTTP/2 flow-control windows must
	// remain cancellable, without an unbounded application queue or pump.
	blocked, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	frame := make([]byte, MaxMessage)
	for {
		if err := a.Send(blocked, frame); err != nil {
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			break
		}
	}
	select {
	case <-a.Done():
	case <-ctx.Done():
		t.Fatal("flow-controlled RPC did not close")
	}
}

func TestGRPCRejectsEmptyMessagesAndUnknownPaths(t *testing.T) {
	endpoint, pub, accepted := grpcPairServer(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	a, err := DialGRPC(ctx, endpoint, 4, pub, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b := <-accepted
	defer b.Close()
	if err := a.stream.SendMsg(&wrapperspb.BytesValue{}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Receive(ctx); err == nil {
		t.Fatal("empty message accepted")
	}
	endpoint.URL += "/unknown"
	if conn, err := DialGRPC(ctx, endpoint, 4, pub, nil); err == nil {
		conn.Close()
		t.Fatal("unknown method accepted")
	}
}

func TestGRPCDialDeadlineBoundsUnacknowledgedStream(t *testing.T) {
	endpoint, pub, _ := grpcPairServer(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if conn, err := DialGRPC(ctx, endpoint, 4, pub, nil); err == nil {
		conn.Close()
		t.Fatal("unacknowledged stream accepted")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestGRPCRejectsOversizedWireMessages(t *testing.T) {
	endpoint, pub, accepted := grpcPairServer(t, true)
	u, _ := url.Parse(endpoint.URL)
	config := PeerClientTLS(u.Hostname(), pub, nil)
	config.NextProtos = []string{"h2"}
	client, err := grpc.NewClient("passthrough:///"+u.Host, grpc.WithTransportCredentials(credentials.NewTLS(config)), grpc.WithNoProxy())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Bypass the adapter's local size check to exercise the remote wire bound.
	stream, err := client.NewStream(ctx, &grpc.StreamDesc{ClientStreams: true, ServerStreams: true}, GRPCMethod(u), grpc.WaitForReady(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Header(); err != nil {
		t.Fatal(err)
	}
	var remote *GRPC
	select {
	case remote = <-accepted:
	case <-ctx.Done():
		t.Fatal("server did not accept RPC")
	}
	defer remote.Close()
	if err := stream.SendMsg(&wrapperspb.BytesValue{Value: make([]byte, MaxMessage+1)}); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Receive(ctx); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("wire limit not enforced: %v", err)
	}
}

func TestGRPCWithIndependentTrustedTLSFrontend(t *testing.T) {
	// HTTP/2 TLS terminates in net/http with a different key. This exercises the
	// CA branch of the real gRPC client's TLS credentials, not just its callback.
	rpc := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		if err := stream.SendHeader(metadata.Pairs("graphwan-protocol", GRPCProtocol)); err != nil {
			return err
		}
		for {
			message := new(wrapperspb.BytesValue)
			if err := stream.RecvMsg(message); err != nil {
				return err
			}
			if err := stream.SendMsg(message); err != nil {
				return err
			}
		}
	}))
	defer rpc.Stop()
	frontend := httptest.NewUnstartedServer(rpc)
	frontend.EnableHTTP2 = true
	frontend.StartTLS()
	defer frontend.Close()
	u, _ := url.Parse(frontend.URL)
	endpoint := model.Endpoint{ID: testutil.ID(1), Source: model.Manual, Transport: model.GRPC, URL: "grpc://" + u.Host}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	roots := x509.NewCertPool()
	roots.AddCert(frontend.Certificate())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := DialGRPC(ctx, endpoint, 4, pub, roots)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Send(ctx, []byte("independent TLS frontend")); err != nil {
		t.Fatal(err)
	}
	got, err := conn.Receive(ctx)
	if err != nil || string(got) != "independent TLS frontend" {
		t.Fatal("TLS frontend round trip:", err)
	}
}
