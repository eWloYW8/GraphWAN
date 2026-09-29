//go:build netbsd

package tunnel

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	netBSDTunSetMode  = 0x80047458
	netBSDTunSetHead  = 0x80047442
	netBSDTunGetHead  = 0x40047441
	netBSDSetNonblock = 0x8004667e
)

type netBSDIfreq struct {
	Name [unix.IFNAMSIZ]byte
	Data [128]byte
}
type netBSDDevice struct {
	*framedDevice
	index int
	lease *tunLease
}

func netBSDInterfaceIOCTL(fd int, operation uintptr, name string) error {
	var request netBSDIfreq
	copy(request.Name[:], name)
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), operation, uintptr(unsafe.Pointer(&request)))
	if errno != 0 {
		return errno
	}
	return nil
}
func destroyNetBSDInterface(name string, index int) error {
	interfaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	found := false
	for _, iface := range interfaces {
		found = found || iface.Name == name && iface.Index == index
	}
	if !found {
		return nil
	}
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return netBSDInterfaceIOCTL(fd, unix.SIOCIFDESTROY, name)
}

func Open(config Config) (_ Device, resultError error) {
	if err := validateNetBSDConfig(config); err != nil {
		return nil, err
	}
	var driver unix.Stat_t
	if err := unix.Lstat("/dev/tun0", &driver); err != nil {
		return nil, fmt.Errorf("locate TUN driver /dev/tun0: %w", err)
	}
	if driver.Mode&unix.S_IFMT != unix.S_IFCHR || unix.Minor(uint64(driver.Rdev)) != 0 {
		return nil, errors.New("/dev/tun0 is not the expected character device")
	}
	control, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(control)
	name := config.Name
	var lease *tunLease
	for attempt := 0; attempt < 16; attempt++ {
		if config.Name == "" {
			var raw [2]byte
			rand.Read(raw[:])
			name = "tun" + strconv.Itoa(int(binary.BigEndian.Uint16(raw[:])))
		}
		lease, err = newTunLease(name)
		if err != nil {
			return nil, err
		}
		err = netBSDInterfaceIOCTL(control, unix.SIOCIFCREATE, name)
		if err == nil {
			break
		}
		if cleanup := lease.close(); cleanup != nil {
			return nil, errors.Join(err, cleanup)
		}
		if config.Name != "" || !errors.Is(err, unix.EEXIST) {
			return nil, fmt.Errorf("create owned TUN (requires root): %w", err)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("allocate unused TUN: %w", err)
	}
	index, err := publishTunOwnership(name, lease.token, net.InterfaceByName, nativeInterfaceDescription, destroyNetBSDInterface)
	if err != nil {
		return nil, errors.Join(err, lease.close())
	}
	var device *netBSDDevice
	success := false
	defer func() {
		if !success {
			if device != nil {
				resultError = errors.Join(resultError, device.Close())
			} else {
				resultError = errors.Join(resultError, lease.close())
			}
		}
	}()
	directory := filepath.Join("/dev", "graphwan-"+lease.token)
	if err := os.Mkdir(directory, 0700); err != nil {
		return nil, err
	}
	defer func() {
		if !success {
			resultError = errors.Join(resultError, removeTunNode(lease.token))
		}
	}()
	unit, _ := persistentTunUnit(name)
	path := filepath.Join(directory, "tun")
	if err := unix.Mknod(path, unix.S_IFCHR|0600, int(unix.Mkdev(unix.Major(uint64(driver.Rdev)), unit))); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open owned TUN: %w", err)
	}
	config.Name = name
	device = &netBSDDevice{framedDevice: &framedDevice{file: os.NewFile(uintptr(fd), path), config: config, ipv6Family: unix.AF_INET6, cleanup: lease.close}, index: index, lease: lease}
	// tunread consults the driver's TUN_NBIO flag, not the descriptor's
	// O_NONBLOCK bit. Set FIONBIO explicitly before any concurrent reader.
	if err := unix.IoctlSetPointerInt(fd, netBSDSetNonblock, 1); err != nil {
		return nil, err
	}
	if err := unix.IoctlSetPointerInt(fd, netBSDTunSetHead, 1); err != nil {
		return nil, err
	}
	if err := unix.IoctlSetPointerInt(fd, netBSDTunSetMode, unix.IFF_BROADCAST|unix.IFF_MULTICAST); err != nil {
		return nil, err
	}
	if err := device.setMTU(config.MTU); err != nil {
		return nil, err
	}
	if err := netBSDAddress(name, config.Address, true); err != nil {
		return nil, err
	}
	if err := device.routes().ensure(config.Address); err != nil {
		return nil, err
	}
	if err := removeTunNode(lease.token); err != nil {
		return nil, err
	}
	success = true
	return device, nil
}

func (d *netBSDDevice) SetMTU(mtu int) error {
	d.configMu.Lock()
	defer d.configMu.Unlock()
	config := d.config
	config.MTU = mtu
	return d.reconfigureLocked(config)
}
func (d *netBSDDevice) Reconfigure(config Config) error {
	d.configMu.Lock()
	defer d.configMu.Unlock()
	return d.reconfigureLocked(config)
}
func (d *netBSDDevice) reconfigureLocked(config Config) error {
	if d.closed {
		return os.ErrClosed
	}
	if config.Name == "" {
		config.Name = d.config.Name
	}
	if err := validateNetBSDConfig(config); err != nil {
		return err
	}
	if config.Name != d.config.Name {
		return errors.New("cannot rename an owned NetBSD TUN")
	}
	if err := d.checkOwnership(); err != nil {
		return err
	}
	ops := configOperations{
		setMTU:    d.setMTU,
		addresses: func() ([]netip.Prefix, error) { return bsdInterfaceAddresses(d.config.Name) },
		add:       func(address netip.Prefix) error { return netBSDAddress(d.config.Name, address, true) },
		remove:    func(address netip.Prefix) error { return netBSDAddress(d.config.Name, address, false) },
	}
	if err := changeRoutedConfig(d.config, config, ops, d.routes()); err != nil {
		return err
	}
	d.config.Address, d.config.MTU = config.Address, config.MTU
	return nil
}
func (d *netBSDDevice) checkOwnership() error {
	raw, err := d.file.SyscallConn()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	var ioctlError error
	err = raw.Control(func(fd uintptr) { _, ioctlError = unix.IoctlGetInt(int(fd), netBSDTunGetHead) })
	if err != nil || ioctlError != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, errors.Join(err, ioctlError))
	}
	iface, err := net.InterfaceByName(d.config.Name)
	if err != nil || iface.Index != d.index {
		return fmt.Errorf("%w: owned TUN identity changed", ErrUnavailable)
	}
	description, err := nativeInterfaceDescription(d.config.Name, "")
	if err != nil || description != tunOwnershipMarker(d.lease.token, d.index) {
		return fmt.Errorf("%w: owned TUN marker changed: %v", ErrUnavailable, err)
	}
	return nil
}
func (d *netBSDDevice) setMTU(mtu int) error {
	return interfaceCommand("/sbin/ifconfig", d.config.Name, "mtu", strconv.Itoa(mtu))
}
func netBSDAddress(name string, address netip.Prefix, add bool) error {
	family := "inet"
	if address.Addr().Is6() {
		family = "inet6"
	}
	if !add {
		return interfaceCommand("/sbin/ifconfig", name, family, address.Addr().String(), "delete")
	}
	if err := interfaceCommand("/sbin/ifconfig", name, family, address.String(), "alias", "up"); err != nil {
		return err
	}
	if !address.Addr().Is6() {
		return nil
	}
	// NetBSD permits bind(2) to tentative IPv6 addresses, so a successful
	// UDP bind does not prove that the kernel can send or receive yet.
	fd, err := unix.Socket(unix.AF_INET6, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	until := time.Now().Add(5 * time.Second)
	for {
		var request netBSDIPv6Request
		copy(request.Name[:], name)
		request.Data[0], request.Data[1] = 28, unix.AF_INET6
		ip := address.Addr().As16()
		copy(request.Data[8:24], ip[:])
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), netBSDGetIPv6Flags, uintptr(unsafe.Pointer(&request)))
		if errno != 0 {
			return errno
		}
		flags := binary.NativeEndian.Uint32(request.Data[:4])
		if flags&0x04 != 0 {
			return fmt.Errorf("duplicate IPv6 address %s", address)
		}
		if flags&(0x02|0x08) == 0 {
			return nil
		}
		if time.Now().After(until) {
			return fmt.Errorf("IPv6 address %s is not ready (flags %#x)", address, flags)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
func (d *netBSDDevice) routes() subnetRoutes {
	command := func(operation string, prefix netip.Prefix) error {
		family := "-inet"
		if prefix.Addr().Is6() {
			family = "-inet6"
		}
		return interfaceCommand("/sbin/route", "-n", operation, family, "-net", prefix.String(), "-link", "-iface", d.config.Name)
	}
	return subnetRoutes{index: d.index, list: func(prefix netip.Prefix) ([]routeEntry, error) { return bsdRoutes(prefix, 0) }, add: func(prefix netip.Prefix) error { return command("add", prefix) }, remove: func(prefix netip.Prefix) error { return command("delete", prefix) }}
}

// in6_ifreq's largest union member is icmp6_ifstat: 34 64-bit counters.
const netBSDGetIPv6Flags = 0xc1206949

type netBSDIPv6Request struct {
	Name [unix.IFNAMSIZ]byte
	Data [272]byte
}
