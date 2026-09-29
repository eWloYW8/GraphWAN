package transport

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/testutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestResolvedEndpointPreservesTLSAndHTTPIdentity(t *testing.T) {
	for _, kind := range []model.Transport{model.WSS, model.GRPC} {
		t.Run(string(kind), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			rpc := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
				if err := stream.SendHeader(metadata.Pairs("graphwan-protocol", GRPCProtocol)); err != nil {
					return err
				}
				message := new(wrapperspb.BytesValue)
				if err := stream.RecvMsg(message); err != nil {
					return err
				}
				return stream.SendMsg(message)
			}))
			defer rpc.Stop()
			identity := make(chan [3]string, 8)
			frontend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				identity <- [3]string{r.Host, r.TLS.ServerName, r.URL.Path}
				if kind == model.GRPC {
					rpc.ServeHTTP(w, r)
					return
				}
				conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{WebSocketProtocol(kind)}})
				if err != nil {
					return
				}
				defer conn.CloseNow()
				messageKind, message, err := conn.Read(ctx)
				if err == nil {
					_ = conn.Write(ctx, messageKind, message)
				}
			}))
			frontend.EnableHTTP2 = true
			frontend.StartTLS()
			defer frontend.Close()
			u, _ := url.Parse(frontend.URL)
			host := frontend.Certificate().DNSNames[0]
			endpoint := model.Endpoint{ID: testutil.ID(1), Source: model.Manual, Transport: kind, URL: string(kind) + "://" + net.JoinHostPort(host, u.Port()) + "/overlay"}
			roots := x509.NewCertPool()
			roots.AddCert(frontend.Certificate())
			pub, _, _ := ed25519.GenerateKey(rand.Reader) // Deliberately differs from the frontend key.
			var conn Conn
			var err error
			if kind == model.WSS {
				conn, err = DialWebSocketAt(ctx, endpoint, 4, pub, roots, netip.MustParseAddr("127.0.0.1"))
			} else {
				conn, err = DialGRPCAt(ctx, endpoint, 4, pub, roots, netip.MustParseAddr("127.0.0.1"))
			}
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.Send(ctx, []byte("pinned DNS address")); err != nil {
				t.Fatal(err)
			}
			if got, err := conn.Receive(ctx); err != nil || string(got) != "pinned DNS address" {
				t.Fatalf("round trip: %q %v", got, err)
			}
			select {
			case got := <-identity:
				path := "/overlay"
				if kind == model.GRPC {
					path += "/graphwan.v1.Peer/Connect"
				}
				if got != [3]string{net.JoinHostPort(host, u.Port()), host, path} {
					t.Fatalf("HTTP authority/SNI/path changed: %v", got)
				}
			case <-ctx.Done():
				t.Fatal("HTTP identity not observed")
			}
		})
	}
}

func TestEndpointTargetPolicy(t *testing.T) {
	endpoint := model.Endpoint{ID: testutil.ID(1), Source: model.Manual, Transport: model.TCP, URL: "tcp://peer.example.test:24752"}
	for _, test := range []struct {
		address string
		family  int
		valid   bool
	}{
		{"127.0.0.1", 4, true}, {"::1", 6, true}, {"127.0.0.1", 6, false}, {"::1", 4, false},
		{"0.0.0.0", 4, false}, {"ff02::1", 6, false}, {"fe80::1%lo", 6, true}, {"2001:db8::1%lo", 6, false}, {"::ffff:127.0.0.1", 4, false},
	} {
		_, err := EndpointDialAddress(endpoint, test.family, netip.MustParseAddr(test.address))
		if (err == nil) != test.valid {
			t.Fatalf("target %s/%d: %v", test.address, test.family, err)
		}
	}
	endpoint.URL = "tcp://127.0.0.1:24752"
	if _, err := EndpointDialAddress(endpoint, 4, netip.MustParseAddr("127.0.0.2")); err == nil {
		t.Fatal("literal endpoint overridden")
	}
}

func TestLiteralLinkLocalZoneTranslation(t *testing.T) {
	endpoint := model.Endpoint{ID: testutil.ID(1), Source: model.Manual, Transport: model.WSS, URL: "wss://[fe80::1%25remote]:24752/path"}
	got, err := EndpointDialAddress(endpoint, 6, netip.MustParseAddr("fe80::1%local"))
	if err != nil || got != "[fe80::1%local]:24752" {
		t.Fatalf("translated socket address: %s %v", got, err)
	}
	if _, err := EndpointDialAddress(endpoint, 6, netip.MustParseAddr("fe80::2%local")); err == nil {
		t.Fatal("scope translation changed endpoint IP")
	}
}
