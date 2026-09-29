package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

const adminMaxResponse = 64 << 20

type adminClient struct {
	http   *http.Client
	origin string
	csrf   string
}

func newAdminClient(origin, caFile string, allowHTTP bool) (*adminClient, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("--server requires an HTTPS origin without credentials, path, query or fragment")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || !allowHTTP || ip == nil || !ip.IsLoopback() {
			return nil, errors.New("--server requires HTTPS; --http permits only literal loopback HTTP")
		}
	}
	if u.Port() != "" {
		port, err := strconv.ParseUint(u.Port(), 10, 16)
		if err != nil || port == 0 {
			return nil, errors.New("invalid controller port")
		}
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read --ca: %w", err)
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("--ca file contains no certificates")
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}
	transport.MaxResponseHeaderBytes = 32 << 10
	jar, _ := cookiejar.New(nil)
	return &adminClient{origin: strings.TrimSuffix(origin, "/"), http: &http.Client{
		Transport: transport, Jar: jar,
		// Never replay credentials or a mutation at a redirect destination.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *adminClient) login(ctx context.Context, password string) error {
	var result struct {
		CSRF string `json:"csrf_token"`
	}
	if err := c.request(ctx, "POST", "/api/v1/login", map[string]string{"password": password}, nil, 200, &result); err != nil {
		return err
	}
	if result.CSRF == "" {
		return errors.New("login response is missing its CSRF token")
	}
	c.csrf = result.CSRF
	return nil
}

func (c *adminClient) logout() {
	// A failed logout must not turn a committed mutation into a reported failure.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.request(ctx, "POST", "/api/v1/logout", nil, nil, 204, nil)
}

func (c *adminClient) change(ctx context.Context, method, path string, body any, revision uint64, status int) (model.State, error) {
	var committed model.State
	if err := c.request(ctx, method, path, body, &revision, status, &committed); err != nil {
		return committed, err
	}
	if err := committed.Validate(); err != nil {
		return committed, fmt.Errorf("invalid mutation response; inspect current state before retrying: %w", err)
	}
	if committed.Revision <= revision || committed.Revision != revision+1 {
		return committed, errors.New("unexpected mutation revision; inspect current state before retrying")
	}
	return committed, nil
}

func (c *adminClient) request(ctx context.Context, method, path string, body any, revision *uint64, status int, result any) error {
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			return err
		}
		if len(raw) > 8<<20 {
			return errors.New("request exceeds the controller's 8 MiB body limit")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.origin+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	if revision != nil {
		req.Header.Set("If-Match", fmt.Sprintf("\"%d\"", *revision))
	}
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, adminMaxResponse+1))
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > adminMaxResponse {
		return errors.New("controller response exceeds 64 MiB")
	}
	if response.StatusCode != status {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &failure)
		if len(failure.Error) > 1024 {
			failure.Error = failure.Error[:1024]
		}
		return fmt.Errorf("%s %s: HTTP %d (%s), detail %q", method, path, response.StatusCode, http.StatusText(response.StatusCode), failure.Error)
	}
	if result != nil {
		if err := json.Unmarshal(data, result); err != nil {
			return fmt.Errorf("decode %s response: %w", path, err)
		}
	}
	return nil
}
