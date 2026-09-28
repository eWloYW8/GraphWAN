// Package discovery maintains automatically advertised interface endpoints.
package discovery

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"

	"github.com/graphwan/graphwan/internal/model"
)

func Interfaces(identity []byte, port uint16) ([]model.Endpoint, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	result := []model.Endpoint{}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || !physical(iface) {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, raw := range addresses {
			prefix, err := netip.ParsePrefix(raw.String())
			if err != nil {
				continue
			}
			ip := prefix.Addr().Unmap()
			if !ip.IsGlobalUnicast() {
				continue
			}
			for _, kind := range []model.Transport{model.UDP, model.TCP} {
				key := append(append([]byte{}, identity...), []byte("/"+iface.Name+"/"+ip.String()+"/"+string(kind))...)
				sum := sha256.Sum256(key)
				endpointURL := url.URL{Scheme: string(kind), Host: net.JoinHostPort(ip.String(), strconv.Itoa(int(port)))}
				result = append(result, model.Endpoint{ID: model.ID(hex.EncodeToString(sum[:16])), Source: model.Interface, Transport: kind, URL: endpointURL.String()})
			}
		}
	}
	slices.SortFunc(result, func(a, b model.Endpoint) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return result, nil
}
