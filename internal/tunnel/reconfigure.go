package tunnel

import (
	"errors"
	"fmt"
	"net/netip"
)

// Address commands can fail after changing kernel state (for example, when a
// utility is killed at its deadline). Rollback reads the actual addresses and
// restores only the two addresses involved, leaving unrelated aliases alone.
type configOperations struct {
	setMTU      func(int) error // Atomic update or an all-or-restore transaction.
	addresses   func() ([]netip.Prefix, error)
	add, remove func(netip.Prefix) error
}

func changeConfig(before, after Config, ops configOperations) error {
	mtuChanged := before.MTU != after.MTU
	if mtuChanged {
		if err := ops.setMTU(after.MTU); err != nil {
			return err
		}
	}
	if before.Address == after.Address {
		return nil
	}
	var err error
	if before.Address.Addr() == after.Address.Addr() {
		// IPv6 kernels reject changing an existing address's prefix length.
		err = ops.remove(before.Address)
		if err == nil {
			err = ops.add(after.Address)
		}
	} else {
		// A different host address can be prepared before retiring the old one.
		err = ops.add(after.Address)
		if err == nil {
			err = ops.remove(before.Address)
		}
	}
	if err == nil {
		return nil
	}
	rollback := restoreAddresses(before.Address, after.Address, ops)
	if mtuChanged {
		rollback = errors.Join(rollback, ops.setMTU(before.MTU))
	}
	if rollback != nil {
		return errors.Join(err, fmt.Errorf("%w: restore TUN configuration: %w", ErrUnavailable, rollback))
	}
	return err
}

func restoreAddresses(before, after netip.Prefix, ops configOperations) error {
	addresses, err := ops.addresses()
	if err != nil {
		return err
	}
	found := false
	var failures []error
	for _, address := range addresses {
		if address == before {
			found = true
			continue
		}
		if address.Addr() == before.Addr() || address.Addr() == after.Addr() {
			if err := ops.remove(address); err != nil {
				failures = append(failures, err)
			}
		}
	}
	if !found {
		if err := ops.add(before); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
