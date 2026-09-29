//go:build !linux && !windows

package discovery

import (
	"net"
	"strings"
)

func physical(iface net.Interface) bool {
	if len(iface.HardwareAddr) == 0 {
		return false
	}
	name := strings.ToLower(iface.Name)
	for _, prefix := range []string{"tun", "tap", "utun", "bridge", "veth", "docker", "wg", "graphwan"} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	return true
}

func physicalInterfaces(interfaces []net.Interface) (map[int]bool, error) {
	result := make(map[int]bool, len(interfaces))
	for _, iface := range interfaces {
		result[iface.Index] = physical(iface)
	}
	return result, nil
}
