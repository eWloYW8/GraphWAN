package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/eWloYW8/GraphWAN/internal/controltransport"
	"github.com/eWloYW8/GraphWAN/internal/model"
)

type Options struct {
	Server          string
	ServerTransport string
	Name            string
	EnrollmentToken string
	// Roots authenticates the server before any enrollment token is sent. Nil
	// uses system roots; callers must explicitly add a self-hosted controller CA.
	Roots   *x509.CertPool
	Version string
	Logger  *slog.Logger
}

type Client struct {
	cache            *Cache
	reconcile        *Reconciler
	options          Options
	server           string
	lastTarget       string
	failedTargets    map[string]bool
	http             *http.Client
	transport        *http.Transport
	mu               sync.Mutex
	endpoints        []model.Endpoint
	endpointsSet     bool
	endpointsChanged chan struct{}
}

func serverURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.Path != "" && u.Path != "/" {
		return "", errors.New("controller must be an HTTPS origin without credentials, path, query or fragment")
	}
	u.Path = ""
	return u.String(), nil
}
func NewClient(cache *Cache, runtime Runtime, options Options) (*Client, error) {
	if cache == nil || runtime == nil {
		return nil, errors.New("agent requires cache and runtime")
	}
	existing, err := cache.Registration()
	if err != nil {
		return nil, err
	}
	if existing != nil {
		options.Server = existing.Server
		options.ServerTransport = existing.Transport
		if _, err := cache.TLSCertificate(*existing); err != nil {
			return nil, err
		}
		if err := validateDirectory(*existing, existing.Directory); err != nil {
			return nil, err
		}
		if len(existing.CA) > 0 {
			roots := x509.NewCertPool()
			if !roots.AppendCertsFromPEM(existing.CA) {
				return nil, errors.New("invalid cached CA")
			}
			options.Roots = roots
		}
	}
	server, err := serverURL(options.Server)
	if err != nil {
		return nil, err
	}
	if err := controltransport.Validate(options.ServerTransport); err != nil {
		return nil, err
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if len(options.Version) > 128 {
		return nil, errors.New("version too long")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: options.Roots, MinVersion: tls.VersionTLS13}, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, MaxIdleConnsPerHost: 2}
	transport.DialContext = controltransport.DialContext(options.ServerTransport, options.Roots)
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	last, _ := cache.lastServer()
	return &Client{lastTarget: last, failedTargets: map[string]bool{}, cache: cache, reconcile: NewReconciler(cache, runtime), options: options, server: server, http: client, transport: transport, endpointsChanged: make(chan struct{}, 1)}, nil
}
func (c *Client) Close()                    { c.transport.CloseIdleConnections() }
func (c *Client) Report() model.AgentReport { return c.reconcile.Report(c.options.Version) }

// SetEndpoints replaces the discovered set. Manual endpoints come exclusively
// from controller snapshots and must not be included here.
func (c *Client) SetEndpoints(endpoints []model.Endpoint) error {
	if len(endpoints) > model.MaxEndpoints {
		return errors.New("too many endpoints")
	}
	for _, e := range endpoints {
		if e.Source != model.Interface && e.Source != model.Observed {
			return errors.New("discovery cannot publish manual endpoints")
		}
		if err := e.Validate(); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.endpoints = append([]model.Endpoint{}, endpoints...)
	c.endpointsSet = true
	c.mu.Unlock()
	select {
	case c.endpointsChanged <- struct{}{}:
	default:
	}
	return nil
}

func (c *Client) registration(ctx context.Context) (*Registration, error) {
	reg, err := c.cache.Registration()
	if err != nil {
		return nil, err
	}
	if reg != nil {
		return reg, nil
	}
	if c.options.EnrollmentToken == "" {
		return nil, errors.New("new agent requires an enrollment token")
	}
	csr, err := c.cache.CSR()
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{"name": c.options.Name, "csr": csr})
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, "POST", c.server+"/api/v1/enroll", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	requestID := sha256.Sum256(body)
	req.Header.Set("Idempotency-Key", hex.EncodeToString(requestID[:]))
	req.Header.Set("Authorization", "Bearer "+c.options.EnrollmentToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("enroll: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		return nil, fmt.Errorf("enrollment rejected: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSnapshotBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxSnapshotBytes {
		return nil, errors.New("enrollment response too large")
	}
	var result struct {
		CA          []byte                 `json:"ca_certificate"`
		Directory   *model.ServerDirectory `json:"servers"`
		AgentID     model.ID               `json:"agent_id"`
		Certificate []byte                 `json:"certificate"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	reg = &Registration{CA: result.CA, Transport: c.options.ServerTransport, Directory: result.Directory, AgentID: result.AgentID, Server: c.server, Certificate: result.Certificate}
	cert, err := c.cache.TLSCertificate(*reg)
	if err != nil {
		return nil, err
	}
	if _, err := cert.Leaf.Verify(x509.VerifyOptions{Roots: c.options.Roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return nil, fmt.Errorf("verify issued agent certificate: %w", err)
	}
	if err := validateDirectory(*reg, reg.Directory); err != nil {
		return nil, err
	}
	if len(reg.CA) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(reg.CA) {
			return nil, errors.New("invalid enrolled CA")
		}
		if _, err := cert.Leaf.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
			return nil, err
		}
	}
	if err := c.cache.SaveRegistration(*reg); err != nil {
		return nil, err
	}
	c.options.Logger.Info("agent enrolled", "agent_id", reg.AgentID, "server", reg.Server)
	// Do not retain the bearer secret once its one-time purpose has succeeded.
	c.options.EnrollmentToken = ""
	return reg, nil
}

// Run restores local runtime first and maintains control synchronization until
// canceled. Reconnect attempts never tear down working runtime resources.
// A Client instance must have at most one concurrent Run call.
func (c *Client) Run(ctx context.Context) error {
	if err := c.reconcile.Restore(ctx); err != nil {
		var apply *ApplyError
		if !errors.As(err, &apply) {
			return err
		}
		c.options.Logger.Error("cached configuration failed", "error", err)
	}
	reg, err := c.registration(ctx)
	if err != nil {
		return err
	}
	runtimeCtx, stopRuntime := context.WithCancel(ctx)
	updates := make(chan model.Snapshot, 1)
	acks := make(chan struct{}, 1)
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); c.applyLoop(runtimeCtx, updates, acks) }()
	defer func() { stopRuntime(); <-workerDone }()
	delay := 250 * time.Millisecond
	for ctx.Err() == nil {
		start := time.Now()
		err := c.connect(ctx, reg.AgentID, updates, acks)
		if ctx.Err() != nil {
			break
		}
		c.options.Logger.Warn("controller disconnected; retaining local configuration", "server", c.server, "error", err)
		if time.Since(start) > time.Minute {
			delay = 250 * time.Millisecond
		}
		wait := delay/2 + time.Duration(rand.Int64N(int64(delay/2)+1))
		if saved, e := c.cache.Registration(); e == nil && saved != nil && len(c.failedTargets) < len(targets(*saved)) {
			wait = 50 * time.Millisecond
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
		delay = min(delay*2, 30*time.Second)
	}
	return ctx.Err()
}

func (c *Client) connect(parent context.Context, id model.ID, updates chan model.Snapshot, acks chan struct{}) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	target, err := c.selectTarget()
	if err != nil {
		return err
	}
	defer func() { c.failedTargets[target.key] = true }()
	dialCtx, stop := context.WithTimeout(ctx, 4*time.Second)
	conn, resp, err := websocket.Dial(dialCtx, c.server+"/api/v1/agent/control", &websocket.DialOptions{HTTPClient: c.http, CompressionMode: websocket.CompressionDisabled})
	stop()
	if err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		return err
	}
	c.lastTarget = target.key
	clear(c.failedTargets)
	if err := c.cache.saveLastServer(target.key); err != nil {
		conn.CloseNow()
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxSnapshotBytes)
	readerDone := make(chan error, 1)
	go func() { readerDone <- c.readControl(ctx, conn, id, updates, acks) }()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	// Publish discovery once per connection, including an intentionally empty set.
	if err := c.sendEndpoints(ctx, conn); err != nil {
		conn.CloseNow()
		<-readerDone
		return err
	}
	for {
		select {
		case err := <-readerDone:
			return err
		case <-ctx.Done():
			conn.CloseNow()
			<-readerDone
			return ctx.Err()
		case <-c.endpointsChanged:
			if err := c.sendEndpoints(ctx, conn); err != nil {
				conn.CloseNow()
				<-readerDone
				return err
			}
			continue
		case <-acks:
		case <-ticker.C:
		}
		report := c.Report()
		writeCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		err := wsjson.Write(writeCtx, conn, model.ControlMessage{Type: "ack", Report: &report})
		stop()
		if err != nil {
			conn.CloseNow()
			<-readerDone
			return err
		}
	}
}
func (c *Client) sendEndpoints(ctx context.Context, conn *websocket.Conn) error {
	c.mu.Lock()
	endpoints := append([]model.Endpoint{}, c.endpoints...)
	set := c.endpointsSet
	c.mu.Unlock()
	if !set {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return wsjson.Write(ctx, conn, model.ControlMessage{Type: "endpoints", Endpoints: endpoints})
}
func (c *Client) readControl(ctx context.Context, conn *websocket.Conn, id model.ID, updates chan model.Snapshot, acks chan struct{}) error {
	for {
		var message model.ControlMessage
		readCtx, stop := context.WithTimeout(ctx, 45*time.Second)
		err := wsjson.Read(readCtx, conn, &message)
		stop()
		if err != nil {
			return err
		}
		switch message.Type {
		case "config":
			if message.Snapshot == nil {
				return errors.New("controller omitted snapshot")
			}
			if err := message.Snapshot.Validate(id); err != nil {
				return fmt.Errorf("invalid controller snapshot: %w", err)
			}
			if err := c.cache.SaveDirectory(message.Snapshot.Servers, c.options.Roots); err != nil {
				return err
			}
			// Coalesce queued revisions, never the currently applying revision.
			select {
			case <-updates:
			default:
			}
			select {
			case updates <- *message.Snapshot:
			case <-ctx.Done():
				return ctx.Err()
			}
		case "heartbeat":
			select {
			case acks <- struct{}{}:
			default:
			}
		case "error":
			return fmt.Errorf("controller error: %.4096s", strings.TrimSpace(message.Error))
		default:
			return fmt.Errorf("unknown controller message %q", message.Type)
		}
	}
}

// Configuration application has the lifetime of Run, not a control connection.
// A controller outage cannot cancel a staged local update or its retry timer.
func (c *Client) applyLoop(ctx context.Context, updates chan model.Snapshot, acks chan struct{}) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		var snapshot model.Snapshot
		select {
		case <-ctx.Done():
			return
		case snapshot = <-updates:
		case <-ticker.C:
			desired, _, err := c.cache.Snapshots()
			if err != nil {
				c.options.Logger.Error("read desired configuration", "error", err)
				continue
			}
			report := c.Report()
			if desired == nil || report.AppliedRevision == desired.Revision && report.ConfigError == "" {
				continue
			}
			snapshot = *desired
		}
		if err := c.reconcile.Accept(ctx, snapshot); err != nil {
			c.options.Logger.Error("configuration rejected", "revision", snapshot.Revision, "error", err)
		}
		select {
		case acks <- struct{}{}:
		default:
		}
	}
}

func (c *Client) selectTarget() (controlTarget, error) {
	reg, err := c.cache.Registration()
	if err != nil {
		return controlTarget{}, err
	}
	if reg == nil {
		return controlTarget{}, errors.New("missing registration")
	}
	list := targets(*reg)
	if len(list) == 0 {
		return controlTarget{}, errors.New("no controller endpoints in local directory")
	}
	var selected *controlTarget
	for i := range list {
		if list[i].key == c.lastTarget && !c.failedTargets[list[i].key] {
			selected = &list[i]
			break
		}
	}
	if selected == nil {
		for i := range list {
			if !c.failedTargets[list[i].key] {
				selected = &list[i]
				break
			}
		}
	}
	if selected == nil {
		clear(c.failedTargets)
		selected = &list[0]
	}
	cert, err := c.cache.TLSCertificate(*reg)
	if err != nil {
		return controlTarget{}, err
	}
	roots := c.options.Roots
	if len(reg.CA) > 0 {
		roots = x509.NewCertPool()
		roots.AppendCertsFromPEM(reg.CA)
	}
	c.transport.CloseIdleConnections()
	c.transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{cert}, ServerName: selected.tlsName, MinVersion: tls.VersionTLS13}, DialContext: controltransport.DialContext(selected.transport, roots, selected.tlsName), TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 4 * time.Second, MaxIdleConnsPerHost: 2}
	c.http.Transport = c.transport
	c.server = selected.origin
	return *selected, nil
}
