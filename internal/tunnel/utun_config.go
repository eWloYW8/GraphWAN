package tunnel

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Unit zero asks the kernel to allocate an unused utun; explicit names use n+1.
func utunUnit(name string) (uint32, error) {
	if name == "" {
		return 0, nil
	}
	if !strings.HasPrefix(name, "utun") {
		return 0, errors.New("macOS TUN name must be utun followed by a decimal index")
	}
	suffix := strings.TrimPrefix(name, "utun")
	n, err := strconv.ParseUint(suffix, 10, 32)
	if err != nil || n >= 1<<32-1 || strconv.FormatUint(n, 10) != suffix {
		return 0, errors.New("invalid utun interface index")
	}
	return uint32(n) + 1, nil
}

type routeEntry struct {
	prefix         netip.Prefix
	index          int
	scoped, usable bool
}

type subnetRoutes struct {
	index       int
	list        func(netip.Prefix) ([]routeEntry, error)
	add, remove func(netip.Prefix) error
}

func (r subnetRoutes) ensure(prefix netip.Prefix) error {
	prefix = prefix.Masked()
	// A single-address network is already delivered locally by the kernel.
	if prefix.Bits() == prefix.Addr().BitLen() {
		return nil
	}
	present, err := r.owned(prefix, true)
	if err != nil || present {
		return err
	}
	addError := r.add(prefix)
	present, err = r.owned(prefix, true)
	if err != nil {
		return errors.Join(addError, err)
	}
	if present {
		return nil
	} // Includes commands that timed out after succeeding.
	return errors.Join(addError, fmt.Errorf("subnet route %s was not installed", prefix))
}

func (r subnetRoutes) discard(prefix netip.Prefix) error {
	prefix = prefix.Masked()
	if prefix.Bits() == prefix.Addr().BitLen() {
		return nil
	}
	present, err := r.owned(prefix, false)
	if err != nil || !present {
		return err
	}
	removeError := r.remove(prefix)
	present, err = r.owned(prefix, false)
	if err != nil {
		return errors.Join(removeError, err)
	}
	if !present {
		return nil
	}
	return errors.Join(removeError, fmt.Errorf("owned subnet route %s was not removed", prefix))
}

func (r subnetRoutes) owned(prefix netip.Prefix, requireUsable bool) (bool, error) {
	entries, err := r.list(prefix)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.prefix != prefix || entry.scoped {
			continue
		}
		if entry.index == r.index && (!requireUsable || entry.usable) {
			return true, nil
		}
		if requireUsable {
			return false, fmt.Errorf("subnet route %s conflicts with an existing route on interface %d", prefix, entry.index)
		}
	}
	return false, nil
}

// A point-to-point utun needs an explicit subnet route. Treat address changes and
// their route changes as one transaction; recovery must restore both or retire
// the device. Route deletion always rechecks the current interface ownership.
func changeRoutedConfig(before, after Config, ops configOperations, routes subnetRoutes) error {
	err := changeConfig(before, after, ops)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			return err
		}
		if restore := routes.ensure(before.Address); restore != nil {
			return errors.Join(err, fmt.Errorf("%w: restore old route: %w", ErrUnavailable, restore))
		}
		return err
	}
	err = routes.ensure(after.Address)
	if err == nil && before.Address.Masked() != after.Address.Masked() {
		err = routes.discard(before.Address)
	}
	if err == nil {
		return nil
	}
	rollback := changeConfig(after, before, ops)
	rollback = errors.Join(rollback, routes.ensure(before.Address))
	if before.Address.Masked() != after.Address.Masked() {
		rollback = errors.Join(rollback, routes.discard(after.Address))
	}
	if rollback != nil {
		return errors.Join(err, fmt.Errorf("%w: restore routed TUN: %w", ErrUnavailable, rollback))
	}
	return err
}
