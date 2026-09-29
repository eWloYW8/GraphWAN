package pki_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/pki"
	"github.com/eWloYW8/GraphWAN/internal/store"
)

func TestBrowserCertificateAndExistingAgentIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "controller.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	original, err := pki.LoadOrCreate(db)
	if err != nil {
		t.Fatal(err)
	}
	_, identity, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "existing Agent"}}, identity)
	if err != nil {
		t.Fatal(err)
	}
	issued, _, err := original.IssueAgent(model.NewID(), csr)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := pki.LoadOrCreate(db)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original.PEM, reloaded.PEM) {
		t.Fatal("restarting the controller changed its trusted CA")
	}
	config, err := reloaded.ServerTLS([]string{"localhost", "127.0.0.1", "::1"})
	if err != nil {
		t.Fatal(err)
	}
	// A browser can support TLS 1.3 without offering Ed25519 CertificateVerify.
	hello := &tls.ClientHelloInfo{
		ServerName: "localhost", SupportedVersions: []uint16{tls.VersionTLS13},
		CipherSuites:     []uint16{tls.TLS_AES_128_GCM_SHA256},
		SupportedCurves:  []tls.CurveID{tls.X25519, tls.CurveP256},
		SignatureSchemes: []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256, tls.PSSWithSHA256},
	}
	if err := hello.SupportsCertificate(&config.Certificates[0]); err != nil {
		t.Fatalf("browser cannot negotiate the HTTPS certificate: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.VerifiedChains) > 0 {
			io.WriteString(w, "authenticated Agent")
		} else {
			io.WriteString(w, "browser")
		}
	}))
	server.TLS = config
	server.StartTLS()
	defer server.Close()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(original.PEM) {
		t.Fatal("cannot load original trusted CA")
	}
	block, _ := pem.Decode(issued)
	if block == nil {
		t.Fatal("missing issued Agent certificate")
	}
	for _, authenticated := range []bool{false, true} {
		clientTLS := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}
		want := "browser"
		if authenticated {
			clientTLS.Certificates = []tls.Certificate{{Certificate: [][]byte{block.Bytes}, PrivateKey: identity}}
			want = "authenticated Agent"
		}
		transport := &http.Transport{TLSClientConfig: clientTLS}
		t.Cleanup(transport.CloseIdleConnections)
		client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
		response, err := client.Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK || string(body) != want || response.TLS.Version != tls.VersionTLS13 {
			t.Fatalf("want %q over TLS 1.3: status=%d body=%q err=%v", want, response.StatusCode, body, err)
		}
	}
}
