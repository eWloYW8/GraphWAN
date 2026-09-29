//go:build dragonfly

package tunnel

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// DragonFly sys/net/tun/if_tun.h and sys/netinet6/{in6_var,nd6}.h.
// In particular TUNGIFNAME is request 98, not FreeBSD's request 93.
const (
	dragonFlyTunGetName = 0x40207462
	dragonFlyTunSetHead = 0x80047460
	dragonFlyTunSetMode = 0x8004745e
	dragonFlyGetND      = 0xc048696c
	dragonFlySetNDFlags = 0xc0486957
)

type dragonFlyDevice struct {
	*framedDevice
	index int
}

func dragonFlyIOCTL(fd uintptr, op uintptr, request unsafe.Pointer) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, op, uintptr(request))
	if errno != 0 {
		return errno
	}
	return nil
}

func dragonFlyName(fd uintptr) (string, error) {
	var request [32]byte // ifreq: IFNAMSIZ name and 16-byte union.
	if err := dragonFlyIOCTL(fd, dragonFlyTunGetName, unsafe.Pointer(&request)); err != nil {
		return "", err
	}
	name := unix.ByteSliceToString(request[:unix.IFNAMSIZ])
	if _, err := persistentTunUnit(name); err != nil {
		return "", fmt.Errorf("invalid or renamed DragonFly TUN: %q", name)
	}
	return name, nil
}

func Open(config Config) (Device, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if config.Name != "" {
		return nil, errors.New("DragonFly allocates TUN names; leave Name empty")
	}
	// /dev/tun autoclones exclusively and marks the new interface for destruction
	// on close, including process death. Opening a numbered device could adopt
	// somebody else's manually created interface and must not be used here.
	fd, err := unix.Open("/dev/tun", unix.O_RDWR|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open DragonFly TUN (requires root and if_tun): %w", err)
	}
	file := os.NewFile(uintptr(fd), "/dev/tun")
	success := false
	defer func() {
		if !success {
			_ = file.Close()
		}
	}()
	name, err := dragonFlyName(uintptr(fd))
	if err != nil {
		return nil, err
	}
	// tunclose destroys the canonical driver/unit name, so do not rename it.
	config.Name = name
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	d := &dragonFlyDevice{framedDevice: &framedDevice{file: file, config: config, ipv6Family: unix.AF_INET6}, index: iface.Index}
	if err := unix.IoctlSetPointerInt(fd, dragonFlyTunSetHead, 1); err != nil {
		return nil, err
	}
	if err := unix.IoctlSetPointerInt(fd, dragonFlyTunSetMode, unix.IFF_BROADCAST|unix.IFF_MULTICAST); err != nil {
		return nil, err
	}
	if err := d.setMTU(config.MTU); err != nil {
		return nil, err
	}
	if err := dragonFlyConfigureND(name); err != nil {
		return nil, err
	}
	if err := dragonFlyAddress(name, config.Address, true); err != nil {
		return nil, err
	}
	if err := d.routes().ensure(config.Address); err != nil {
		return nil, err
	}
	success = true
	return d, nil
}

func (d *dragonFlyDevice) SetMTU(mtu int) error {
	d.configMu.Lock()
	defer d.configMu.Unlock()
	config := d.config
	config.MTU = mtu
	return d.reconfigureLocked(config)
}

func (d *dragonFlyDevice) Reconfigure(config Config) error {
	d.configMu.Lock()
	defer d.configMu.Unlock()
	return d.reconfigureLocked(config)
}

func (d *dragonFlyDevice) reconfigureLocked(config Config) error {
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
		return errors.New("cannot rename an owned DragonFly TUN")
	}
	if err := d.checkOwnership(); err != nil {
		return err
	}
	ops := configOperations{
		setMTU:    d.setMTU,
		addresses: func() ([]netip.Prefix, error) { return bsdInterfaceAddresses(d.config.Name) },
		add:       func(address netip.Prefix) error { return dragonFlyAddress(d.config.Name, address, true) },
		remove:    func(address netip.Prefix) error { return dragonFlyAddress(d.config.Name, address, false) },
	}
	if err := changeRoutedConfig(d.config, config, ops, d.routes()); err != nil {
		return err
	}
	d.config.Address, d.config.MTU = config.Address, config.MTU
	return nil
}

func (d *dragonFlyDevice) checkOwnership() error {
	raw, err := d.file.SyscallConn()
	if err != nil {
		return err
	}
	var name string
	var ioctlError error
	if err := raw.Control(func(fd uintptr) { name, ioctlError = dragonFlyName(fd) }); err != nil {
		return err
	}
	if ioctlError != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, ioctlError)
	}
	iface, err := net.InterfaceByName(name)
	if err != nil || name != d.config.Name || iface.Index != d.index {
		return fmt.Errorf("%w: owned DragonFly TUN identity changed", ErrUnavailable)
	}
	return nil
}

func (d *dragonFlyDevice) setMTU(mtu int) error {
	if err := d.checkOwnership(); err != nil {
		return err
	}
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	// Use the interface ioctl, not the TUN descriptor's TUNSIFINFO: only the
	// former updates ND6's cached maxmtu through nd6_setmtu. Otherwise IPv6
	// remains capped at the original 1500 even after raising the link MTU.
	var request [32]byte
	copy(request[:16], d.config.Name)
	binary.NativeEndian.PutUint32(request[16:20], uint32(mtu))
	return dragonFlyIOCTL(uintptr(fd), unix.SIOCSIFMTU, unsafe.Pointer(&request))
}

func dragonFlyConfigureND(name string) error {
	fd, err := unix.Socket(unix.AF_INET6, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	// in6_ndireq: 16-byte name plus 56-byte nd_ifinfo, flags at offset 20.
	// Disable DAD, router advertisements and new link-local addresses only on
	// our new TUN. Overlay addresses are unique by controller validation.
	var request [72]byte
	copy(request[:16], name)
	if err := dragonFlyIOCTL(uintptr(fd), dragonFlyGetND, unsafe.Pointer(&request)); err != nil {
		return err
	}
	flags := binary.NativeEndian.Uint32(request[36:40])
	binary.NativeEndian.PutUint32(request[36:40], flags&^(uint32(2)|4)|8)
	return dragonFlyIOCTL(uintptr(fd), dragonFlySetNDFlags, unsafe.Pointer(&request))
}

func dragonFlyAddress(name string, address netip.Prefix, add bool) error {
	family := "inet"
	if address.Addr().Is6() {
		family = "inet6"
	}
	if !add {
		return interfaceCommand("/sbin/ifconfig", name, family, address.Addr().String(), "delete")
	}
	return interfaceCommand("/sbin/ifconfig", name, family, address.String(), "alias", "up")
}

func (d *dragonFlyDevice) routes() subnetRoutes {
	command := func(operation string, prefix netip.Prefix) error {
		family := "-inet"
		if prefix.Addr().Is6() {
			family = "-inet6"
		}
		return interfaceCommand("/sbin/route", "-n", operation, family, "-net", prefix.String(), "-link", "-iface", d.config.Name)
	}
	return subnetRoutes{index: d.index,
		list:   func(prefix netip.Prefix) ([]routeEntry, error) { return bsdRoutes(prefix, 0) },
		add:    func(prefix netip.Prefix) error { return command("add", prefix) },
		remove: func(prefix netip.Prefix) error { return command("delete", prefix) },
	}
}
