package transport

import (
	"errors"
	"net"
	"net/netip"
	"net/url"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

// EndpointDialAddress pins a DNS answer at the socket layer. Callers must keep
// the original URL for HTTP authority, TLS SNI and certificate verification.
// An invalid target retains ordinary hostname resolution for standalone callers.
func EndpointDialAddress(endpoint model.Endpoint, family int, target netip.Addr) (string, error) {
	if err := endpoint.Validate(); err != nil {
		return "", err
	}
	if family != 4 && family != 6 {
		return "", errors.New("invalid endpoint family")
	}
	u, _ := url.Parse(endpoint.URL)
	if !target.IsValid() {
		return u.Host, nil
	}
	if target.Is4In6() || (target.Zone() != "" && !target.IsLinkLocalUnicast()) || target.IsUnspecified() || target.IsMulticast() || target.Is4() != (family == 4) {
		return "", errors.New("invalid endpoint target address")
	}
	if literal, err := netip.ParseAddr(u.Hostname()); err == nil && literal.WithZone("") != target.WithZone("") {
		return "", errors.New("cannot override a literal endpoint")
	}
	return net.JoinHostPort(target.String(), u.Port()), nil
}
