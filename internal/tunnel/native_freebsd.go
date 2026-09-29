//go:build freebsd

package tunnel

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"unsafe"

	"golang.org/x/sys/unix"
)

// FreeBSD sys/net/if_tun.h. TUNSTRANSIENT is available in FreeBSD 15.1;
// older kernels require explicit destruction after closing the descriptor.
const (
	tunSetMode      = 0x8004745e
	tunSetHead      = 0x80047460
	tunSetTransient = 0x80047462
	tunGetInfo      = 0x4008745c
	tunSetInfo      = 0x8008745b
	tunGetName      = 0x4020745d
)

type freeBSDIfreq struct {
	Name [unix.IFNAMSIZ]byte
	Data [16]byte
}

func interfaceIOCTL(fd int, op uintptr, req *freeBSDIfreq) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), op, uintptr(unsafe.Pointer(req)))
	if errno != 0 {
		return errno
	}
	return nil
}

func destroyFreeBSDInterface(name string, index int) error {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil
	} // Already removed externally.
	if iface.Index != index {
		return nil
	} // Name was reused by another interface.
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var req freeBSDIfreq
	copy(req.Name[:], name)
	return interfaceIOCTL(fd, unix.SIOCIFDESTROY, &req)
}

func Open(config Config) (Device, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	config = config.withName()
	control, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open interface control socket: %w", err)
	}
	defer unix.Close(control)
	// SIOCIFCREATE allocates a new clone atomically, never an existing idle TUN.
	var req freeBSDIfreq
	copy(req.Name[:], "tun")
	if err := interfaceIOCTL(control, unix.SIOCIFCREATE, &req); err != nil {
		return nil, fmt.Errorf("create TUN (requires root): %w", err)
	}
	name := unix.ByteSliceToString(req.Name[:])
	index, err := createdTunIndex(name, net.InterfaceByName)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open("/dev/"+name, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		_ = destroyFreeBSDInterface(name, index)
		return nil, fmt.Errorf("open cloned TUN: %w", err)
	}
	device := &framedDevice{file: os.NewFile(uintptr(fd), "/dev/"+name), config: config, ipv6Family: unix.AF_INET6}
	transient := unix.IoctlSetPointerInt(fd, tunSetTransient, 1)
	if transient != nil {
		device.cleanup = func() error { return destroyFreeBSDInterface(name, index) }
	}
	success := false
	defer func() {
		if !success {
			_ = device.Close()
		}
	}()
	if transient != nil && !errors.Is(transient, unix.ENOTTY) {
		return nil, fmt.Errorf("set TUN lifetime: %w", transient)
	}
	if err := unix.IoctlSetPointerInt(fd, tunSetHead, 1); err != nil {
		return nil, err
	}
	if err := unix.IoctlSetPointerInt(fd, tunSetMode, unix.IFF_BROADCAST|unix.IFF_MULTICAST); err != nil {
		return nil, err
	}
	if name != config.Name {
		if err := interfaceCommand("/sbin/ifconfig", name, "name", config.Name); err != nil {
			return nil, err
		}
		name = config.Name
	}
	if err := interfaceCommand("/sbin/ifconfig", name, "mtu", strconv.Itoa(config.MTU)); err != nil {
		return nil, err
	}
	// Address uniqueness belongs to the controller; DAD and automatic link-local
	// traffic have no role on this routed, non-Ethernet virtual interface.
	if err := interfaceCommand("/sbin/ifconfig", name, "inet6", "-auto_linklocal", "no_dad"); err != nil {
		return nil, err
	}
	family := "inet"
	if config.Address.Addr().Is6() {
		family = "inet6"
	}
	if err := interfaceCommand("/sbin/ifconfig", name, family, config.Address.String(), "alias", "up"); err != nil {
		return nil, err
	}
	success = true
	return device, nil
}

func (d *framedDevice) SetMTU(mtu int) error {
	d.configMu.Lock()
	defer d.configMu.Unlock()
	config := d.config
	config.MTU = mtu
	return d.reconfigureLocked(config)
}

func (d *framedDevice) Reconfigure(config Config) error {
	d.configMu.Lock()
	defer d.configMu.Unlock()
	return d.reconfigureLocked(config)
}

func (d *framedDevice) reconfigureLocked(config Config) error {
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
		return errors.New("cannot rename an owned TUN during reconfiguration")
	}
	if config == d.config {
		return nil
	}
	// Resolve the name from the owned descriptor before invoking name-based
	// address operations. Never act on a different device that reused our name.
	raw, err := d.file.SyscallConn()
	if err != nil {
		return err
	}
	var req freeBSDIfreq
	var ioctlError error
	if err := raw.Control(func(fd uintptr) { ioctlError = interfaceIOCTL(int(fd), tunGetName, &req) }); err != nil {
		return err
	}
	if ioctlError != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, ioctlError)
	}
	if unix.ByteSliceToString(req.Name[:]) != d.config.Name {
		return fmt.Errorf("%w: owned TUN was renamed", ErrUnavailable)
	}
	ops := configOperations{
		setMTU: d.setMTULocked,
		addresses: func() ([]netip.Prefix, error) {
			iface, err := net.InterfaceByName(d.config.Name)
			if err != nil {
				return nil, err
			}
			addresses, err := iface.Addrs()
			if err != nil {
				return nil, err
			}
			var prefixes []netip.Prefix
			for _, address := range addresses {
				prefix, err := netip.ParsePrefix(address.String())
				if err != nil {
					return nil, err
				}
				prefixes = append(prefixes, prefix)
			}
			return prefixes, nil
		},
		add:    func(address netip.Prefix) error { return freeBSDAddress(d.config.Name, address, "alias") },
		remove: func(address netip.Prefix) error { return freeBSDAddress(d.config.Name, address, "delete") },
	}
	if err := changeConfig(d.config, config, ops); err != nil {
		return err
	}
	d.config.Address, d.config.MTU = config.Address, config.MTU
	return nil
}

func freeBSDAddress(name string, address netip.Prefix, operation string) error {
	family := "inet"
	if address.Addr().Is6() {
		family = "inet6"
	}
	return interfaceCommand("/sbin/ifconfig", name, family, address.String(), operation)
}

// Use a single kernel operation so a failed MTU update cannot partially apply.
// The caller holds configMu and publishes the config only after all edits succeed.
func (d *framedDevice) setMTULocked(mtu int) error {
	raw, err := d.file.SyscallConn()
	if err != nil {
		return err
	}
	// Address the owned descriptor, not its mutable interface name. A renamed
	// device or a reused name must never redirect this operation to another TUN.
	var ioctlError error
	err = raw.Control(func(fd uintptr) {
		var info struct {
			Baudrate      int32
			MTU           uint16
			Type, Padding uint8
		}
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, tunGetInfo, uintptr(unsafe.Pointer(&info)))
		if errno != 0 {
			ioctlError = errno
			return
		}
		info.MTU = uint16(mtu)
		_, _, errno = unix.Syscall(unix.SYS_IOCTL, fd, tunSetInfo, uintptr(unsafe.Pointer(&info)))
		if errno != 0 {
			ioctlError = errno
		}
	})
	if err != nil {
		return err
	}
	if ioctlError != nil {
		return ioctlError
	}
	return nil
}
