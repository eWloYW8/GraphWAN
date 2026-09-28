//go:build !linux

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
