package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
)

func websocketServer(t *testing.T, kind model.Transport, tlsConfig *tls.Config) (*httptest.Server, <-chan *WebSocket) {
	t.Helper()
	accepted := make(chan *WebSocket, 8)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/custom/overlay" {
			http.NotFound(w, r)
			return
		}
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{WebSocketProtocol(kind)}, CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return
		}
		remote, _ := net.ResolveTCPAddr("tcp", r.RemoteAddr)
		accepted <- NewWebSocket(c, r.Context().Value(http.LocalAddrContextKey).(net.Addr), remote, r.URL.EscapedPath())
	}))
	if tlsConfig != nil {
		tlsConfig = tlsConfig.Clone()
		cert, err := tlsConfig.GetCertificate(nil)
		if err != nil {
			t.Fatal(err)
		}
		// httptest otherwise inserts its unrelated default certificate for IP SNI.
		tlsConfig.Certificates = []tls.Certificate{*cert}
	}
	server.TLS = tlsConfig
	if kind == model.WSS && tlsConfig != nil {
		server.StartTLS()
	} else {
		server.Start()
	}
	t.Cleanup(server.Close)
	return server, accepted
}
func TestWSSWithTrustedTLSProxy(t *testing.T) {
	backend, accepted := websocketServer(t, model.WSS, nil)
	target, _ := url.Parse(backend.URL)
	proxy := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(target))
	defer proxy.Close()
	roots := proxy.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	endpoint := model.Endpoint{ID: testutil.ID(1), Source: model.Manual, Transport: model.WSS, URL: "wss" + strings.TrimPrefix(proxy.URL, "https") + "/custom/overlay"}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, err := DialWebSocket(ctx, endpoint, 4, pub, roots)
	if err != nil {
		t.Fatal("trusted proxy with a different key was rejected:", err)
	}
	defer client.Close()
	var server *WebSocket
	select {
	case server = <-accepted:
	case <-ctx.Done():
		t.Fatal("TLS proxy did not reach plaintext Agent backend")
	}
	defer server.Close()
	frame := bytes.Repeat([]byte{42}, 1400)
	if err := client.Send(ctx, frame); err != nil {
		t.Fatal(err)
	}
	got, err := server.Receive(ctx)
	if err != nil || !bytes.Equal(got, frame) {
		t.Fatal("proxy corrupted binary message:", err)
	}
}
func TestWebSocketBinaryBoundariesCancellationAndTLS(t *testing.T) {
	for _, kind := range []model.Transport{model.WS, model.WSS} {
		t.Run(string(kind), func(t *testing.T) {
			pub, key, _ := ed25519.GenerateKey(rand.Reader)
			tlsConfig, err := PeerServerTLS(key)
			if err != nil {
				t.Fatal(err)
			}
			server, accepted := websocketServer(t, kind, tlsConfig)
			endpoint := model.Endpoint{ID: testutil.ID(1), Source: model.Manual, Transport: kind, URL: string(kind) + strings.TrimPrefix(strings.TrimPrefix(server.URL, "https"), "http") + "/custom/overlay"}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			a, err := DialWebSocket(ctx, endpoint, 4, pub, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			b := <-accepted
			defer b.Close()
			for _, size := range []int{1, 80, 4096, MaxMessage} {
				sent := bytes.Repeat([]byte{byte(size)}, size)
				if err := a.Send(ctx, sent); err != nil {
					t.Fatal(err)
				}
				got, err := b.Receive(ctx)
				if err != nil || !bytes.Equal(sent, got) {
					t.Fatalf("binary boundary size %d: %v", size, err)
				}
				if err := b.Send(ctx, got); err != nil {
					t.Fatal(err)
				}
				got, err = a.Receive(ctx)
				if err != nil || !bytes.Equal(sent, got) {
					t.Fatal("reverse delivery", err)
				}
			}
			if err := a.Send(ctx, make([]byte, MaxMessage+1)); err == nil {
				t.Fatal("oversized send accepted")
			}
			stopped, stop := context.WithTimeout(ctx, 20*time.Millisecond)
			defer stop()
			if _, err := b.Receive(stopped); err == nil {
				t.Fatal("idle receive ignored cancellation")
			}
		})
	}
}
func TestWebSocketRejectsTextOversizeWrongProtocolAndRedirect(t *testing.T) {
	for _, bad := range []string{"text", "oversize", "empty"} {
		t.Run(bad, func(t *testing.T) {
			server, accepted := websocketServer(t, model.WS, nil)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/custom/overlay", &websocket.DialOptions{Subprotocols: []string{WebSocketProtocol(model.WS)}})
			if err != nil {
				t.Fatal(err)
			}
			defer c.CloseNow()
			b := <-accepted
			defer b.Close()
			kind, raw := websocket.MessageBinary, []byte{}
			if bad == "text" {
				kind, raw = websocket.MessageText, []byte("bad")
			}
			if bad == "oversize" {
				raw = make([]byte, MaxMessage+1)
			}
			go c.Write(ctx, kind, raw)
			if _, err := b.Receive(ctx); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
	for _, redirect := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if redirect {
				http.Redirect(w, r, "http://127.0.0.1:1/redirect", http.StatusFound)
				return
			}
			c, err := websocket.Accept(w, r, nil)
			if err == nil {
				c.CloseNow()
			}
		}))
		endpoint := model.Endpoint{ID: testutil.ID(1), Source: model.Manual, Transport: model.WS, URL: "ws" + strings.TrimPrefix(server.URL, "http")}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		c, err := DialWebSocket(ctx, endpoint, 4, nil, nil)
		if err == nil {
			c.Close()
			t.Fatal("missing subprotocol or redirect accepted")
		}
		cancel()
		server.Close()
	}
}
func TestPeerTLSIdentityAndPublicCAAdmission(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	config, err := PeerServerTLS(key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := config.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	state := tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate.Leaf}}
	if err := PeerClientTLS("unrelated-host.invalid", pub, nil).VerifyConnection(state); err != nil {
		t.Fatal("identity pin rejected", err)
	}
	if err := PeerClientTLS("unrelated-host.invalid", other, x509.NewCertPool()).VerifyConnection(state); err == nil {
		t.Fatal("wrong identity accepted")
	}
	expired := *certificate.Leaf
	expired.NotAfter = time.Now().Add(-time.Second)
	if err := PeerClientTLS("example.com", pub, nil).VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{&expired}}); err == nil {
		t.Fatal("expired pinned certificate accepted")
	}
	clientOnly := *certificate.Leaf
	clientOnly.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	if err := PeerClientTLS("example.com", pub, nil).VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{&clientOnly}}); err == nil {
		t.Fatal("client-only pinned certificate accepted")
	}
	// The TLS proxy has a different key, but a valid CA and hostname. Noise is
	// responsible for authenticating the configured Agent behind that proxy.
	proxyPub, proxyKey, _ := ed25519.GenerateKey(rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	caDER, _ := x509.CreateCertificate(rand.Reader, ca, ca, proxyPub, proxyKey)
	caCert, _ := x509.ParseCertificate(caDER)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"proxy.example.test"}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter}
	der, _ := x509.CreateCertificate(rand.Reader, leaf, caCert, proxyPub, proxyKey)
	cert, _ := x509.ParseCertificate(der)
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	state.PeerCertificates = []*x509.Certificate{cert}
	if err := PeerClientTLS("proxy.example.test", pub, roots).VerifyConnection(state); err != nil {
		t.Fatal("valid TLS proxy rejected", err)
	}
	if err := PeerClientTLS("wrong.example.test", pub, roots).VerifyConnection(state); err == nil {
		t.Fatal("TLS proxy hostname mismatch accepted")
	}
	if err := PeerClientTLS("proxy.example.test", pub, x509.NewCertPool()).VerifyConnection(state); err == nil {
		t.Fatal("untrusted TLS proxy accepted")
	}
}
