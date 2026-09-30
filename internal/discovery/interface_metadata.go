package discovery

// Windows reports these properties independently of an adapter's display name.
// Keep the classification decision independent of platform APIs.
type interfaceMetadata struct {
	kind                       uint32
	hardware, filter, endpoint bool
}

func (m interfaceMetadata) physical() bool {
	if !m.hardware || m.filter || m.endpoint {
		return false
	}
	switch m.kind {
	case 24, 53, 131, 209: // IANA softwareLoopback, propVirtual, tunnel, bridge.
		return false
	}
	return true
}
