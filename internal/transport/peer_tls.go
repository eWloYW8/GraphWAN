package transport

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"slices"
	"sync"
	"time"
)

// PeerServerTLS presents an automatically renewed certificate bound to the
// Agent's durable Ed25519 identity. It is independent of controller availability.
func PeerServerTLS(identity ed25519.PrivateKey) (*tls.Config, error) {
	if len(identity) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid TLS identity")
	}
	identity = append(ed25519.PrivateKey{}, identity...)
	var mu sync.Mutex
	var cached *tls.Certificate
	var renew time.Time
	get := func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		mu.Lock()
		defer mu.Unlock()
		now := time.Now()
		if cached != nil && now.Before(renew) {
			return cached, nil
		}
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			return nil, err
		}
		template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "GraphWAN Agent"}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.Add(7 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, BasicConstraintsValid: true}
		der, err := x509.CreateCertificate(rand.Reader, template, template, identity.Public(), identity)
		if err != nil {
			return nil, err
		}
		leaf, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, err
		}
		cached = &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: identity, Leaf: leaf}
		renew = now.Add(24 * time.Hour)
		return cached, nil
	}
	if _, err := get(nil); err != nil {
		return nil, err
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}, GetCertificate: get, SessionTicketsDisabled: true}, nil
}

// PeerClientTLS accepts either a direct Agent identity pin or ordinary Web PKI
// for a TLS-terminating reverse proxy. Noise still authenticates the Agent behind
// that proxy. No certificate is accepted merely because TLS encryption succeeded.
func PeerClientTLS(host string, identity ed25519.PublicKey, roots *x509.CertPool) *tls.Config {
	pin := append(ed25519.PublicKey{}, identity...)
	return &tls.Config{MinVersion: tls.VersionTLS13, ServerName: host, NextProtos: []string{"http/1.1"},
		InsecureSkipVerify: true, // Verification is performed in full by VerifyConnection.
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("peer certificate missing")
			}
			leaf := state.PeerCertificates[0]
			now := time.Now()
			if pub, ok := leaf.PublicKey.(ed25519.PublicKey); ok && len(pin) == ed25519.PublicKeySize && pub.Equal(pin) {
				if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) || len(leaf.UnhandledCriticalExtensions) != 0 {
					return errors.New("invalid pinned certificate lifetime or extensions")
				}
				if leaf.KeyUsage != 0 && leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
					return errors.New("pinned certificate cannot sign")
				}
				if len(leaf.ExtKeyUsage) > 0 && !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth) && !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageAny) {
					return errors.New("pinned certificate is not a server certificate")
				}
				return nil
			}
			intermediates := x509.NewCertPool()
			for _, cert := range state.PeerCertificates[1:] {
				intermediates.AddCert(cert)
			}
			_, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: roots, Intermediates: intermediates, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
			return err
		},
	}
}
