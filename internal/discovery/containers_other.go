//go:build !linux

package discovery

import "net"

// Other adapters already filter virtual interfaces with platform metadata.
// Additional container-veth filtering is currently specific to Linux.
func excludeContainerInterfaces(_ []net.Interface, _ map[int]bool) error { return nil }
