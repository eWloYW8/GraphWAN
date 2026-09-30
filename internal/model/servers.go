package model

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"time"
)

const MaxServers = 64

type ServerEndpoint struct {
	ID        ID             `json:"id"`
	Transport string         `json:"transport"`
	URL       string         `json:"url"`
	Source    EndpointSource `json:"source"`
	ExpiresAt time.Time      `json:"expires_at,omitempty"`
}

func (e ServerEndpoint) Validate() error {
	if err := e.ID.Validate(); err != nil {
		return err
	}
	if len(e.URL) > 2048 {
		return fmt.Errorf("server endpoint URL too long")
	}
	if e.Source != Manual && e.Transport != "tcp" {
		return fmt.Errorf("automatic server endpoints must use TCP")
	}
	want := map[string]string{"tcp": "tcp", "websocket": "ws", "grpc": "grpc", "wss": "wss"}[e.Transport]
	u, err := url.Parse(e.URL)
	if want == "" || err != nil || u.Scheme != want || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("invalid server endpoint")
	}
	address, addressErr := netip.ParseAddr(u.Hostname())
	if addressErr == nil {
		if address.IsUnspecified() || address.IsMulticast() || address.Is4In6() {
			return fmt.Errorf("server endpoint must be unicast")
		}
	} else if e.Source != Manual || !validHostname(u.Hostname()) {
		return fmt.Errorf("invalid server endpoint hostname")
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil || port == 0 {
		return fmt.Errorf("server endpoint needs a port")
	}
	if e.Source != Interface && e.Source != Observed && e.Source != Manual {
		return fmt.Errorf("invalid server endpoint source")
	}
	if e.Source == Observed && e.ExpiresAt.IsZero() {
		return fmt.Errorf("observed server endpoint needs an expiry")
	}
	return nil
}
func (e ServerEndpoint) Origin() string {
	u, err := url.Parse(e.URL)
	if err != nil {
		return ""
	}
	return "https://" + u.Host
}
func (e ServerEndpoint) Address() string {
	u, err := url.Parse(e.URL)
	if err != nil {
		return ""
	}
	return net.JoinHostPort(u.Hostname(), u.Port())
}

type Server struct {
	ID          ID               `json:"id"`
	Name        string           `json:"name"`
	PublicKey   []byte           `json:"public_key"`
	Endpoints   []ServerEndpoint `json:"endpoints"`
	STUNServers []string         `json:"stun_servers,omitempty"`
	Revoked     bool             `json:"revoked,omitempty"`
}

func (s Server) TLSName() string { return "server-" + string(s.ID) + ".graphwan" }
func (s Server) Validate() error {
	if err := s.ID.Validate(); err != nil {
		return err
	}
	if err := validateName(s.Name); err != nil {
		return err
	}
	if len(s.PublicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid server identity")
	}
	if len(s.Endpoints) > MaxEndpoints {
		return fmt.Errorf("too many server endpoints")
	}
	seen := map[ID]bool{}
	urls := map[string]bool{}
	for _, e := range s.Endpoints {
		if err := e.Validate(); err != nil {
			return err
		}
		if seen[e.ID] || urls[e.URL] {
			return fmt.Errorf("duplicate server endpoint")
		}
		seen[e.ID] = true
		urls[e.URL] = true
	}
	return ValidateSTUNServers(s.STUNServers)
}

type ServerDirectory struct {
	ClusterID ID       `json:"cluster_id"`
	Revision  uint64   `json:"revision"`
	CA        []byte   `json:"ca"`
	Servers   []Server `json:"servers"`
}

func (d ServerDirectory) Validate() error {
	if err := d.ClusterID.Validate(); err != nil {
		return err
	}
	block, rest := pem.Decode(d.CA)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return fmt.Errorf("invalid cluster CA")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.IsCA {
		return fmt.Errorf("invalid cluster CA certificate")
	}
	if len(d.Servers) == 0 || len(d.Servers) > MaxServers {
		return fmt.Errorf("invalid server count")
	}
	ids := map[ID]bool{}
	keys := map[string]bool{}
	for _, s := range d.Servers {
		if err := s.Validate(); err != nil {
			return err
		}
		if ids[s.ID] || keys[string(s.PublicKey)] {
			return fmt.Errorf("duplicate server identity")
		}
		ids[s.ID] = true
		keys[string(s.PublicKey)] = true
	}
	return nil
}
func CloneServers(in []Server) []Server {
	if in == nil {
		return nil
	}
	out := append([]Server{}, in...)
	for i := range out {
		out[i].PublicKey = bytes.Clone(out[i].PublicKey)
		out[i].Endpoints = append([]ServerEndpoint{}, out[i].Endpoints...)
		out[i].STUNServers = append([]string(nil), out[i].STUNServers...)
	}
	return out
}
func (d *ServerDirectory) Clone() *ServerDirectory {
	if d == nil {
		return nil
	}
	out := *d
	out.CA = bytes.Clone(d.CA)
	out.Servers = CloneServers(d.Servers)
	return &out
}
func (s State) ServerDirectory() *ServerDirectory {
	if s.ClusterID == "" {
		return nil
	}
	return &ServerDirectory{ClusterID: s.ClusterID, Revision: s.Revision, CA: bytes.Clone(s.ClusterCA), Servers: CloneServers(s.Servers)}
}
