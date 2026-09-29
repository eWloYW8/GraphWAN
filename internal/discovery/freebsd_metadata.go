package discovery

import "strings"

// FreeBSD's IFDATA_DRIVERNAME describes the kernel driver and unit, independently
// of the administrator-editable alias and interface groups. Cloner names come
// from SIOCIFGCLONERS. Wi-Fi VAPs and jail epairs are usable underlay interfaces.
func freeBSDPhysical(kind int, original string, cloners map[string]bool) bool {
	if original == "" || strings.ContainsRune(original, '\x00') {
		return false
	}
	// IANA hardware link types: Ethernet variants, Token Ring, FDDI, Wi-Fi,
	// InfiniBand, WiMAX and mobile broadband. Software point-to-point, loopback,
	// tunnel, VLAN and bridge types are not physical interfaces.
	switch kind {
	case 6, 7, 9, 15, 62, 69, 71, 117, 199, 237, 243, 244:
	default:
		return false
	}
	driver := strings.TrimRight(original, "0123456789")
	if driver == "wlan" || driver == "epair" {
		return true
	}
	// Netgraph eiface is not an if_clone driver. Also reject the core overlay
	// drivers even if their cloner is not visible in the current VNET jail.
	switch driver {
	case "ngeth", "tap", "tun", "vmnet", "bridge", "vlan", "lagg", "vxlan", "geneve", "wg", "ovpn", "gif", "gre", "gretap", "ipsec":
		return false
	}
	return !cloners[driver]
}
