//go:build openbsd

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

// OpenBSD sys/net/if_tun.h: unlike FreeBSD, tuninfo has a 32-bit MTU
// and 16-bit type/flags. The four-byte packet family header is always enabled.
const (
	openBSDTunGetInfo = 0x400c745c
	openBSDTunSetInfo = 0x800c745b
)

type openBSDTunInfo struct {
	MTU         uint32
	Type, Flags uint16
	Baudrate    uint32
}

type openBSDIfreq struct {
	Name [unix.IFNAMSIZ]byte
	Data [16]byte
}

type openBSDDevice struct {
	*framedDevice
	index int
	lease *openBSDLease
}

func openBSDInterfaceIOCTL(fd int, operation uintptr, name string) error {
	var request openBSDIfreq
	copy(request.Name[:], name)
	// Go routes SYS_IOCTL through libc on modern OpenBSD; RawSyscall does not.
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), operation, uintptr(unsafe.Pointer(&request)))
	if errno != 0 {
		return errno
	}
	return nil
}

func destroyOpenBSDInterface(name string, index int) error {
	interfaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	owned := false
	for _, iface := range interfaces {
		owned = owned || (iface.Name == name && iface.Index == index)
	}
	if !owned {
		return nil // Removed externally, or the name belongs to a replacement.
	}
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return openBSDInterfaceIOCTL(fd, unix.SIOCIFDESTROY, name)
}

func Open(config Config) (_ Device, resultError error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	rtable, err := unix.Getrtable()
	if err != nil {
		return nil, fmt.Errorf("read routing table: %w", err)
	}
	if rtable != 0 {
		return nil, errors.New("OpenBSD TUN configuration currently requires routing table 0")
	}
	if config.Name != "" {
		if _, err := openBSDUnit(config.Name); err != nil {
			return nil, err
		}
	}
	// Use the installed driver's device number, never a guessed major number.
	var driver unix.Stat_t
	if err := unix.Lstat("/dev/tun0", &driver); err != nil {
		return nil, fmt.Errorf("locate TUN driver device /dev/tun0: %w", err)
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
	var lease *openBSDLease
	for attempt := 0; attempt < 16; attempt++ {
		if config.Name == "" {
			var raw [2]byte
			rand.Read(raw[:])
			name = "tun" + strconv.Itoa(int(binary.BigEndian.Uint16(raw[:])))
		}
		lease, err = newOpenBSDLease(name)
		if err != nil {
			return nil, err
		}
		err = openBSDInterfaceIOCTL(control, unix.SIOCIFCREATE, name)
		if err == nil {
			break
		}
		cleanupError := lease.close()
		if cleanupError != nil {
			return nil, errors.Join(err, cleanupError)
		}
		if config.Name != "" || !errors.Is(err, unix.EEXIST) {
			return nil, fmt.Errorf("create owned TUN (requires root): %w", err)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("allocate unused TUN: %w", err)
	}
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, errors.Join(err, openBSDInterfaceIOCTL(control, unix.SIOCIFDESTROY, name), lease.close())
	}
	var device *openBSDDevice
	success := false
	defer func() {
		if !success {
			if device != nil {
				resultError = errors.Join(resultError, device.Close())
			} else {
				resultError = errors.Join(resultError, destroyOpenBSDInterface(name, iface.Index), lease.close())
			}
		}
	}()
	if _, err := openBSDDescription(name, openBSDMarker(lease.token, iface.Index)); err != nil {
		return nil, err
	}
	// Only a few /dev/tunN nodes exist by default. Create a private device node
	// for the atomically allocated unit; /tmp can be mounted nodev. Remove the
	// node and private directory after opening, without touching /dev/tunN.
	directory := filepath.Join("/dev", "graphwan-"+lease.token)
	if err := os.Mkdir(directory, 0700); err != nil {
		return nil, err
	}
	nodeDirectory := directory
	defer func() {
		if nodeDirectory != "" {
			resultError = errors.Join(resultError, os.RemoveAll(nodeDirectory))
		}
	}()
	unit, _ := openBSDUnit(name)
	path := filepath.Join(directory, "tun")
	if err := unix.Mknod(path, unix.S_IFCHR|0600, int(unix.Mkdev(unix.Major(uint64(driver.Rdev)), unit))); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open owned TUN: %w", err)
	}
	config.Name = name
	device = &openBSDDevice{framedDevice: &framedDevice{
		file: os.NewFile(uintptr(fd), path), config: config, ipv6Family: unix.AF_INET6,
		cleanup: lease.close,
	}, index: iface.Index, lease: lease}
	if err := device.setMTU(config.MTU); err != nil {
		return nil, err
	}
	if err := openBSDAddress(name, config.Address, true); err != nil {
		return nil, err
	}
	if err := device.routes().ensure(config.Address); err != nil {
		return nil, err
	}
	if err := os.RemoveAll(directory); err != nil {
		return nil, err
	}
	nodeDirectory = ""
	success = true
	return device, nil
}

func (d *openBSDDevice) SetMTU(mtu int) error {
	d.configMu.Lock()
	defer d.configMu.Unlock()
	config := d.config
	config.MTU = mtu
	return d.reconfigureLocked(config)
}

func (d *openBSDDevice) Reconfigure(config Config) error {
	d.configMu.Lock()
	defer d.configMu.Unlock()
	return d.reconfigureLocked(config)
}

func (d *openBSDDevice) reconfigureLocked(config Config) error {
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
		return errors.New("cannot rename an owned OpenBSD TUN")
	}
	if err := d.checkOwnership(); err != nil {
		return err
	}
	ops := configOperations{
		setMTU:    d.setMTU,
		addresses: func() ([]netip.Prefix, error) { return bsdInterfaceAddresses(d.config.Name) },
		add:       func(address netip.Prefix) error { return openBSDAddress(d.config.Name, address, true) },
		remove:    func(address netip.Prefix) error { return openBSDAddress(d.config.Name, address, false) },
	}
	if err := changeRoutedConfig(d.config, config, ops, d.routes()); err != nil {
		return err
	}
	d.config.Address, d.config.MTU = config.Address, config.MTU
	return nil
}

func (d *openBSDDevice) tunInfo(mtu int) error {
	raw, err := d.file.SyscallConn()
	if err != nil {
		return err
	}
	var ioctlError error
	err = raw.Control(func(fd uintptr) {
		var info openBSDTunInfo
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, openBSDTunGetInfo, uintptr(unsafe.Pointer(&info)))
		if errno == 0 && mtu != 0 {
			info.MTU = uint32(mtu)
			_, _, errno = unix.Syscall(unix.SYS_IOCTL, fd, openBSDTunSetInfo, uintptr(unsafe.Pointer(&info)))
		}
		if errno != 0 {
			ioctlError = errno
		}
	})
	return errors.Join(err, ioctlError)
}

func (d *openBSDDevice) setMTU(mtu int) error { return d.tunInfo(mtu) }

func (d *openBSDDevice) checkOwnership() error {
	if err := d.tunInfo(0); err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	iface, err := net.InterfaceByName(d.config.Name)
	if err != nil || iface.Index != d.index {
		return fmt.Errorf("%w: owned TUN identity changed", ErrUnavailable)
	}
	description, err := openBSDDescription(d.config.Name, "")
	if err != nil || description != openBSDMarker(d.lease.token, d.index) {
		return fmt.Errorf("%w: owned TUN marker changed: %v", ErrUnavailable, err)
	}
	return nil
}

func openBSDAddress(name string, address netip.Prefix, add bool) error {
	family := "inet"
	if address.Addr().Is6() {
		family = "inet6"
	}
	if !add {
		return interfaceCommand("/sbin/ifconfig", name, family, address.Addr().String(), "delete")
	}
	args := []string{name, family, address.String()}
	if address.Addr().Is4() {
		args = append(args, address.Addr().String())
	}
	if err := interfaceCommand("/sbin/ifconfig", append(args, "alias")...); err != nil {
		return err
	}
	if !address.Addr().Is6() {
		return nil
	}
	// OpenBSD performs DAD on a running TUN and has no per-interface no_dad
	// switch. Do not report an applied configuration before the address can be
	// used. A local bind checks readiness without emitting traffic or changing
	// global IPv6 settings; failure remains part of the configuration transaction.
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IP(address.Addr().AsSlice())})
		if err == nil {
			return conn.Close()
		}
		if !errors.Is(err, unix.EADDRNOTAVAIL) || time.Now().After(deadline) {
			return fmt.Errorf("wait for IPv6 address %s: %w", address, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (d *openBSDDevice) routes() subnetRoutes {
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
