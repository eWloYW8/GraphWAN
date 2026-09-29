//go:build darwin

package tunnel

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	utunControl     = "com.apple.net.utun_control"
	sysProtoControl = 2
	utunOptName     = 2
)

type darwinDevice struct {
	*framedDevice
	index int
}

func darwinSocket(family, kind, protocol int) (int, error) {
	// Darwin does not support SOCK_CLOEXEC. Coordinate with Go's fork/exec path
	// so concurrent configuration commands cannot inherit an unmarked descriptor.
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	fd, err := unix.Socket(family, kind, protocol)
	if err == nil {
		unix.CloseOnExec(fd)
	}
	return fd, err
}

func Open(config Config) (Device, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	unit, err := utunUnit(config.Name)
	if err != nil {
		return nil, err
	}
	fd, err := darwinSocket(unix.AF_SYSTEM, unix.SOCK_DGRAM, sysProtoControl)
	if err != nil {
		return nil, fmt.Errorf("open utun control socket: %w", err)
	}
	var file *os.File
	success := false
	defer func() {
		if !success {
			if file != nil {
				_ = file.Close()
			} else {
				_ = unix.Close(fd)
			}
		}
	}()
	var info unix.CtlInfo
	copy(info.Name[:], utunControl)
	if err := unix.IoctlCtlInfo(fd, &info); err != nil {
		return nil, fmt.Errorf("find utun kernel control: %w", err)
	}
	if err := unix.Connect(fd, &unix.SockaddrCtl{ID: info.Id, Unit: unit}); err != nil {
		return nil, fmt.Errorf("create utun (requires root): %w", err)
	}
	name, err := unix.GetsockoptString(fd, sysProtoControl, utunOptName)
	if err != nil {
		return nil, err
	}
	if _, err := utunUnit(name); err != nil || name == "" {
		return nil, fmt.Errorf("kernel returned invalid utun name %q", name)
	}
	config.Name = name
	if err := unix.SetNonblock(fd, true); err != nil {
		return nil, err
	}
	file = os.NewFile(uintptr(fd), name)
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	d := &darwinDevice{framedDevice: &framedDevice{file: file, config: config, ipv6Family: unix.AF_INET6}, index: iface.Index}
	if err := d.setMTU(config.MTU); err != nil {
		return nil, err
	}
	// Overlay addresses are assigned uniquely by the controller. Disable DAD
	// only on this utun; no host-wide IPv6 setting is changed.
	if err := interfaceCommand("/sbin/ifconfig", name, "inet6", "-dad"); err != nil {
		return nil, err
	}
	if err := darwinAddress(name, config.Address, true); err != nil {
		return nil, err
	}
	if err := interfaceCommand("/sbin/ifconfig", name, "up"); err != nil {
		return nil, err
	}
	if err := d.routes().ensure(config.Address); err != nil {
		return nil, err
	}
	success = true
	return d, nil
}

func (d *darwinDevice) SetMTU(mtu int) error {
	d.configMu.Lock()
	defer d.configMu.Unlock()
	config := d.config
	config.MTU = mtu
	return d.reconfigureLocked(config)
}
func (d *darwinDevice) Reconfigure(config Config) error {
	d.configMu.Lock()
	defer d.configMu.Unlock()
	return d.reconfigureLocked(config)
}
func (d *darwinDevice) reconfigureLocked(config Config) error {
	if d.closed {
		return os.ErrClosed
	}
	if config.Name == "" {
		config.Name = d.config.Name
	}
	if err := config.Validate(); err != nil {
		return err
	}
	if config.Name != d.config.Name {
		return errors.New("cannot rename an owned utun")
	}
	if err := d.checkOwnership(); err != nil {
		return err
	}
	if err := changeRoutedConfig(d.config, config, d.configOperations(), d.routes()); err != nil {
		return err
	}
	d.config.Address, d.config.MTU = config.Address, config.MTU
	return nil
}

func (d *darwinDevice) checkOwnership() error {
	raw, err := d.file.SyscallConn()
	if err != nil {
		return err
	}
	var name string
	var socketError error
	if err := raw.Control(func(fd uintptr) { name, socketError = unix.GetsockoptString(int(fd), sysProtoControl, utunOptName) }); err != nil {
		return err
	}
	if socketError != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, socketError)
	}
	iface, err := net.InterfaceByName(name)
	if err != nil || name != d.config.Name || iface.Index != d.index {
		return fmt.Errorf("%w: utun identity changed", ErrUnavailable)
	}
	return nil
}

func (d *darwinDevice) setMTU(mtu int) error {
	fd, err := darwinSocket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var req unix.IfreqMTU
	copy(req.Name[:], d.config.Name)
	req.MTU = int32(mtu)
	return unix.IoctlSetIfreqMTU(fd, &req)
}

func darwinAddress(name string, address netip.Prefix, add bool) error {
	family := "inet"
	if address.Addr().Is6() {
		family = "inet6"
	}
	if !add {
		return interfaceCommand("/sbin/ifconfig", name, family, address.Addr().String(), "-alias")
	}
	args := []string{name, family, address.String()}
	if address.Addr().Is4() {
		args = append(args, address.Addr().String())
	} // Point-to-point peer.
	return interfaceCommand("/sbin/ifconfig", append(args, "alias")...)
}

func (d *darwinDevice) configOperations() configOperations {
	return configOperations{
		setMTU:    d.setMTU,
		addresses: func() ([]netip.Prefix, error) { return bsdInterfaceAddresses(d.config.Name) },
		add:       func(address netip.Prefix) error { return darwinAddress(d.config.Name, address, true) },
		remove:    func(address netip.Prefix) error { return darwinAddress(d.config.Name, address, false) },
	}
}

func (d *darwinDevice) routes() subnetRoutes {
	command := func(operation string, prefix netip.Prefix) error {
		family := "-inet"
		if prefix.Addr().Is6() {
			family = "-inet6"
		}
		return interfaceCommand("/sbin/route", "-n", operation, family, "-net", prefix.String(), "-interface", d.config.Name)
	}
	return subnetRoutes{index: d.index, list: darwinRoutes,
		add:    func(prefix netip.Prefix) error { return command("add", prefix) },
		remove: func(prefix netip.Prefix) error { return command("delete", prefix) },
	}
}

func darwinRoutes(prefix netip.Prefix) ([]routeEntry, error) {
	return bsdRoutes(prefix, unix.RTF_IFSCOPE)
}
