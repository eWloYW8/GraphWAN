package tunnel

import (
	"errors"
	"fmt"
	"net"
)

func tunOwnershipMarker(token string, index int) string {
	return fmt.Sprintf("graphwan:%s:%d", token, index)
}

// publishTunOwnership runs only after an exclusive SIOCIFCREATE succeeded.
// Until the marker is installed, failure cleanup requires the observed interface
// index. A failed lookup is not permission to destroy whichever interface now
// occupies the requested name. Nothing configures an address/route at this stage.
// The kernel does not make creation, index lookup and marking atomic; privileged
// interface changes must be coordinated with the owner.
func publishTunOwnership(name, token string, lookup func(string) (*net.Interface, error), describe func(string, string) (string, error), destroy func(string, int) error) (int, error) {
	iface, err := lookup(name)
	if err == nil && (iface == nil || iface.Name != name || iface.Index <= 0) {
		err = errors.New("interface identity was not established")
	}
	if err != nil {
		return 0, fmt.Errorf("created TUN %s could not be identified; leaving interface untouched: %w", name, err)
	}
	if _, err := describe(name, tunOwnershipMarker(token, iface.Index)); err != nil {
		return 0, errors.Join(fmt.Errorf("mark created TUN %s: %w", name, err), destroy(name, iface.Index))
	}
	return iface.Index, nil
}
