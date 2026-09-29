package link

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/netip"
	"net/url"

	"github.com/graphwan/graphwan/internal/model"
)

// Address is the remote address in its owner's scope. Target is reserved for
// unscoped DNS answers; socket-local zone translation never changes that answer.
func (c Candidate) Address() netip.Addr {
	if c.Target.IsValid() {
		return c.Target
	}
	u, err := url.Parse(c.Endpoint.URL)
	if err != nil {
		return netip.Addr{}
	}
	address, _ := netip.ParseAddr(u.Hostname())
	return address
}

func (c Candidate) NeedsScope() bool {
	a := c.Address()
	return a.Is6() && a.IsLinkLocalUnicast()
}

// ScopeIdentity binds a dialing interface to its current advertised endpoint.
// It is independent of process/interface enumeration order and live sessions,
// but changes on endpoint replacement so stale handshakes cannot reinstall it.
func ScopeIdentity(endpoint model.Endpoint) string {
	if endpoint.ID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(string(endpoint.ID) + "/" + string(endpoint.Source) + "/" + string(endpoint.Transport) + "/" + endpoint.URL))
	return hex.EncodeToString(sum[:16])
}

// ScopeCandidate binds one remote link-local address to an initiator interface.
// Automatic discovery advertises a UDP endpoint for every eligible address; its
// zone supplies the egress interface for all transports, including manual ones.
// The caller must obtain scope from the initiator's configured endpoint list.
func ScopeCandidate(base Candidate, scope model.Endpoint) (Candidate, error) {
	if !base.NeedsScope() || base.Family != 6 || base.Scope.ID != "" || scope.Validate() != nil || scope.Source != model.Interface || scope.Transport != model.UDP {
		return Candidate{}, errors.New("invalid link-local scope")
	}
	u, _ := url.Parse(scope.URL)
	address, err := netip.ParseAddr(u.Hostname())
	if err != nil || !address.Is6() || !address.IsLinkLocalUnicast() || address.Zone() == "" {
		return Candidate{}, errors.New("scope requires a link-local interface endpoint")
	}
	sum := sha256.Sum256([]byte(base.ID + "/scope/" + ScopeIdentity(scope)))
	base.ID, base.Scope = hex.EncodeToString(sum[:16]), scope
	return base, nil
}

// DialTarget translates the owner's zone into the initiator's zone. Never pass
// the returned address to the responder as a DNS answer or use it there as a
// local socket address: only the dialing node owns this interface namespace.
func (c Candidate) DialTarget() (netip.Addr, error) {
	if !c.NeedsScope() {
		if c.Scope.ID != "" {
			return netip.Addr{}, errors.New("unexpected interface scope")
		}
		return c.Target, nil
	}
	if c.Scope.ID == "" {
		return netip.Addr{}, errors.New("link-local candidate has no local scope")
	}
	base := c
	base.Scope = model.Endpoint{}
	if _, err := ScopeCandidate(base, c.Scope); err != nil {
		return netip.Addr{}, err
	}
	u, _ := url.Parse(c.Scope.URL)
	local, _ := netip.ParseAddr(u.Hostname())
	return c.Address().WithZone(local.Zone()), nil
}
