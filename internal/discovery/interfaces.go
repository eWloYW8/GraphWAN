// Package discovery maintains automatically advertised interface endpoints.
package discovery

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

type InterfaceOptions struct {
	ExcludeContainerIPs bool
}

func Interfaces(identity []byte, port uint16) ([]model.Endpoint, error) {
	return InterfacesWithOptions(identity, port, InterfaceOptions{})
}

func InterfacesWithOptions(identity []byte, port uint16, options InterfaceOptions) ([]model.Endpoint, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	physical, err := physicalInterfaces(interfaces)
	if err != nil {
		return nil, err
	}
	if options.ExcludeContainerIPs {
		if err := excludeContainerInterfaces(interfaces, physical); err != nil {
			return nil, err
		}
	}
	return interfaceEndpoints(identity, port, interfaces, physical, func(iface net.Interface) ([]net.Addr, error) { return iface.Addrs() })
}

func interfaceEndpoints(identity []byte, port uint16, interfaces []net.Interface, physical map[int]bool, addressesFor func(net.Interface) ([]net.Addr, error)) ([]model.Endpoint, error) {
	if port == 0 {
		return nil, errors.New("interface discovery requires a nonzero listen port")
	}
	interfaces = slices.Clone(interfaces)
	slices.SortFunc(interfaces, func(a, b net.Interface) int { return strings.Compare(a.Name, b.Name) })
	result := []model.Endpoint{}
	seen := map[string]bool{}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || !physical[iface.Index] {
			continue
		}
		addresses, err := addressesFor(iface)
		if err != nil {
			return nil, fmt.Errorf("addresses for interface %s: %w", iface.Name, err)
		}
		for _, raw := range addresses {
			prefix, err := netip.ParsePrefix(raw.String())
			if err != nil {
				continue
			}
			ip := prefix.Addr().Unmap()
			if !ip.IsGlobalUnicast() && !ip.IsLinkLocalUnicast() {
				continue
			}
			// This is the owner's zone. Mesh translates it into each dialing
			// node's local zone; duplicate addresses on different NICs stay distinct.
			if ip.Is6() && ip.IsLinkLocalUnicast() {
				ip = ip.WithZone(iface.Name)
			}

			for _, kind := range []model.Transport{model.UDP, model.TCP} {
				key := append(append([]byte{}, identity...), []byte("/"+iface.Name+"/"+ip.String()+"/"+string(kind))...)
				sum := sha256.Sum256(key)
				endpointURL := url.URL{Scheme: string(kind), Host: net.JoinHostPort(ip.String(), strconv.Itoa(int(port)))}
				if seen[endpointURL.String()] {
					continue
				}
				seen[endpointURL.String()] = true
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
