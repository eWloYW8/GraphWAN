//go:build openbsd || netbsd

package discovery

import (
	"fmt"
	"net"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

func physicalInterfaces(interfaces []net.Interface) (map[int]bool, error) {
	raw, err := route.FetchRIB(unix.AF_UNSPEC, route.RIBTypeInterface, 0)
	if err != nil {
		return nil, fmt.Errorf("interface RIB: %w", err)
	}
	messages, err := route.ParseRIB(route.RIBTypeInterface, raw)
	if err != nil {
		return nil, fmt.Errorf("parse interface RIB: %w", err)
	}
	byIndex := map[int]*route.InterfaceMessage{}
	for _, message := range messages {
		if iface, ok := message.(*route.InterfaceMessage); ok {
			byIndex[iface.Index] = iface
		}
	}
	cloners, err := bsdCloners()
	if err != nil {
		return nil, fmt.Errorf("interface cloners: %w", err)
	}
	result := make(map[int]bool, len(interfaces))
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		message := byIndex[iface.Index]
		if message == nil || message.Name != iface.Name {
			return nil, fmt.Errorf("interface %s changed during discovery", iface.Name)
		}
		kind := 0
		for _, metric := range message.Sys() {
			if metric, ok := metric.(*route.InterfaceMetrics); ok {
				kind = metric.Type
			}
		}
		result[iface.Index] = namedBSDPhysical(kind, message.Name, cloners)
	}
	return result, nil
}
