package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/controltransport"
	"github.com/eWloYW8/GraphWAN/internal/model"
)

// Enroll installs identity only: no TUN, peer listener or background service.
func Enroll(ctx context.Context, cache *Cache, invite model.AgentInvitation, name string) (*Registration, error) {
	if err := invite.Validate(); err != nil {
		return nil, err
	}
	existing, err := cache.Registration()
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.Directory == nil || existing.Directory.ClusterID != invite.Directory.ClusterID || !bytes.Equal(existing.CA, invite.Directory.CA) {
			return nil, errors.New("agent is already registered with a different cluster; identity was not changed")
		}
		if _, err := cache.TLSCertificate(*existing); err != nil {
			return nil, err
		}
		return existing, nil
	}
	if !time.Now().Before(invite.ExpiresAt) {
		return nil, errors.New("agent invitation has expired")
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(invite.Directory.CA)
	var last error
	for _, target := range targets(Registration{Directory: &invite.Directory}) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: target.tlsName, MinVersion: tls.VersionTLS13},
			DialContext: controltransport.DialContext(target.transport, roots, target.tlsName), TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 5 * time.Second}
		c := &Client{cache: cache, server: target.origin, enrollmentDirectory: &invite.Directory,
			options: Options{Name: name, EnrollmentToken: invite.Token, ServerTransport: target.transport, Roots: roots, Logger: slog.Default()},
			http:    &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, transport: tr}
		attempt, cancel := context.WithTimeout(ctx, 8*time.Second)
		result, err := c.registration(attempt)
		cancel()
		tr.CloseIdleConnections()
		if err == nil {
			return result, nil
		}
		last = err
	}
	if last == nil {
		last = errors.New("no usable server endpoints in invitation")
	}
	return nil, last
}
