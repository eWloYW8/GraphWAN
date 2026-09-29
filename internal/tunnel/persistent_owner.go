package tunnel

import (
	"errors"
	"fmt"
	"net"
)

func tunOwnershipMarker(token string, index int) string {
	return fmt.Sprintf("graphwan:%s:%d", token, index)
}

// createdTunIndex runs only after an exclusive SIOCIFCREATE succeeded. A failed
// lookup is not permission to destroy whichever interface now occupies the name.
// Nothing configures an address/route at this stage. Creation and index lookup
// are not atomic; privileged interface changes must be coordinated with the owner.
func createdTunIndex(name string, lookup func(string) (*net.Interface, error)) (int, error) {
	iface, err := lookup(name)
	if err == nil && (iface == nil || iface.Name != name || iface.Index <= 0) {
		err = errors.New("interface identity was not established")
	}
	if err != nil {
		return 0, fmt.Errorf("created TUN %s could not be identified; leaving interface untouched: %w", name, err)
	}
	return iface.Index, nil
}

// publishTunOwnership adds the OpenBSD/NetBSD recovery marker. Failure cleanup
// requires the observed interface index until this marker has been installed.
func publishTunOwnership(name, token string, lookup func(string) (*net.Interface, error), describe func(string, string) (string, error), destroy func(string, int) error) (int, error) {
	index, err := createdTunIndex(name, lookup)
	if err != nil {
		return 0, err
	}
	if _, err := describe(name, tunOwnershipMarker(token, index)); err != nil {
		return 0, errors.Join(fmt.Errorf("mark created TUN %s: %w", name, err), destroy(name, index))
	}
	return index, nil
}
