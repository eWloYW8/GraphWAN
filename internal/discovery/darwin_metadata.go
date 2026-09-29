package discovery

import "strings"

// XNU if_private.h and kpi_interface.h. Unlike functional type, the concrete
// family does not classify a tunnel by its delegated physical interface.
type darwinInterfaceMetadata struct {
	kind, family, subfamily uint32
	extendedFlags           uint64
}

func (m darwinInterfaceMetadata) physical(name string) bool {
	// if_clone-created Ethernet interfaces (including feth) still report the
	// Ethernet family. AWDL is a software peer-to-peer service of a Wi-Fi NIC.
	if m.extendedFlags&(0x00010000|0x00100000) != 0 {
		return false
	}
	// Legacy tuntaposx TAPs predate if_clone and register as plain Ethernet.
	// These are BSD driver names, not System Settings' editable service labels.
	switch strings.TrimRight(name, "0123456789") {
	case "tap", "tun", "utun":
		return false
	}
	// Hardware transports: unspecified, USB, Bluetooth, Wi-Fi, Thunderbolt.
	// Exclude coprocessor, relay, VMNET, simulated cellular and redirect devices.
	if m.subfamily > 4 {
		return false
	}
	switch m.family {
	case 2: // IFNET_FAMILY_ETHERNET; Wi-Fi is represented as Ethernet on macOS.
		return m.kind == 6 || m.kind == 71
	case 13: // IFNET_FAMILY_FIREWIRE.
		return m.kind == 144
	case 15: // IFNET_FAMILY_CELLULAR / IFT_CELLULAR.
		return m.kind == 255
	default:
		return false
	}
}
