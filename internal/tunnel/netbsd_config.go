package tunnel

import "errors"

// NetBSD 11 if_tun.h limits both interface MTU and injected packets to 1500.
const netBSDMaxMTU = 1500

func validateNetBSDConfig(config Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	if config.MTU > netBSDMaxMTU {
		return errors.New("NetBSD TUN supports MTU up to 1500")
	}
	if config.Name != "" {
		_, err := persistentTunUnit(config.Name)
		return err
	}
	return nil
}
