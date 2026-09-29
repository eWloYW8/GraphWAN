package discovery

import "strings"

// OpenBSD interface names are assigned by their drivers, not editable aliases.
// Match the complete driver name against the kernel cloner list: virtual
// Ethernet devices cannot masquerade as hardware by changing groups or labels.
func openBSDPhysical(kind int, name string, cloners map[string]bool) bool {
	if name == "" || strings.ContainsRune(name, '\x00') {
		return false
	}
	switch kind {
	case 6, 7, 9, 15, 26, 55, 62, 69, 71, 117, 144, 199, 250:
		// OpenBSD IFT_MBIM is 250, unlike the IANA mobile broadband link types.
	default:
		return false
	}
	driver := strings.TrimRight(name, "0123456789")
	return driver != "" && driver != name && !cloners[driver]
}
