// Package pki manages controller-issued TLS identities. Agent private keys never
// leave the enrolling agent; enrollment accepts an Ed25519 certificate request.
package pki

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"time"

	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/store"
)

type Authority struct {
	cert *x509.Certificate
	key  ed25519.PrivateKey
	PEM  []byte
}
type savedCA struct {
	Certificate []byte `json:"certificate"`
	Key         []byte `json:"key"`
}

func serial() (*big.Int, error) { return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128)) }

func LoadOrCreate(db *store.Store) (*Authority, error) {
	var saved savedCA
	err := db.WriteRecords(func(r *store.Records) error {
		if raw := r.Get("ca"); raw != nil {
			return json.Unmarshal(raw, &saved)
		}
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		n, err := serial()
		if err != nil {
			return err
		}
		now := time.Now()
		template := &x509.Certificate{SerialNumber: n, Subject: pkix.Name{CommonName: "GraphWAN Controller CA"}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
		cert, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
		if err != nil {
			return err
		}
		saved = savedCA{Certificate: cert, Key: key}
		raw, err := json.Marshal(saved)
		if err != nil {
			return err
		}
		return r.Put("ca", raw)
	})
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(saved.Certificate)
	if err != nil {
		return nil, err
	}
	if len(saved.Key) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid CA private key")
	}
	key := ed25519.PrivateKey(saved.Key)
	pub, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok || !key.Public().(ed25519.PublicKey).Equal(pub) || !cert.IsCA {
		return nil, errors.New("invalid CA identity")
	}
	return &Authority{cert: cert, key: key, PEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})}, nil
}

func (a *Authority) IssueAgent(id model.ID, csrDER []byte) ([]byte, ed25519.PublicKey, error) {
	if err := id.Validate(); err != nil {
		return nil, nil, err
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, nil, err
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, nil, err
	}
	pub, ok := csr.PublicKey.(ed25519.PublicKey)
	if !ok {
		return nil, nil, errors.New("agent must use Ed25519")
	}
	n, err := serial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: n, Subject: pkix.Name{CommonName: string(id)}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, a.cert, pub, a.key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pub, nil
}

func (a *Authority) ServerTLS(hosts []string) (*tls.Config, error) {
	// Browsers do not universally offer Ed25519 for TLS CertificateVerify.
	// Keep the durable CA and Agent identities, but use P-256 for HTTPS.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	n, err := serial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: n, Subject: pkix.Name{CommonName: "GraphWAN Server"}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	for _, host := range hosts {
		if ip := net.ParseIP(host); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else if host != "" {
			template.DNSNames = append(template.DNSNames, host)
		}
	}
	if len(template.IPAddresses)+len(template.DNSNames) == 0 {
		return nil, errors.New("TLS server requires at least one hostname")
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.cert, &key.PublicKey, a.key)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(a.cert)
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{{Certificate: [][]byte{der, a.cert.Raw}, PrivateKey: key}}, ClientCAs: pool, ClientAuth: tls.VerifyClientCertIfGiven}, nil
}
