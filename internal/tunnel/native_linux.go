//go:build linux

package tunnel

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sync"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

type native struct {
	file       *os.File
	config     Config
	once       sync.Once
	closeError error
}

func Open(config Config) (Device, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	config = config.withName()
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open TUN: %w", err)
	}
	var file *os.File
	success := false
	defer func() {
		if !success {
			if file != nil {
				file.Close()
			} else {
				unix.Close(fd)
			}
		}
	}()
	request, err := unix.NewIfreq(config.Name)
	if err != nil {
		return nil, err
	}
	// EXCL prevents attaching to another process's pre-existing interface. Without
	// PERSIST, closing this owned descriptor removes its interface and routes.
	request.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI | unix.IFF_TUN_EXCL)
	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, request); err != nil {
		return nil, fmt.Errorf("create TUN %s (requires CAP_NET_ADMIN): %w", config.Name, err)
	}
	// An unattached TUN descriptor cannot be registered with epoll. Construct
	// the Go file only after TUNSETIFF, so idle reads can wait and be canceled.
	file = os.NewFile(uintptr(fd), "/dev/net/tun")
	device := &native{file: file, config: config}
	link, err := netlink.LinkByName(config.Name)
	if err != nil {
		return nil, err
	}
	if err := netlink.LinkSetMTU(link, config.MTU); err != nil {
		return nil, err
	}
	bits := 128
	if config.Address.Addr().Is4() {
		bits = 32
	}
	address := &netlink.Addr{IPNet: &net.IPNet{IP: net.IP(config.Address.Addr().AsSlice()), Mask: net.CIDRMask(config.Address.Bits(), bits)}}
	if bits == 128 {
		address.Flags = unix.IFA_F_NODAD
	}
	if err := netlink.AddrAdd(link, address); err != nil {
		return nil, fmt.Errorf("configure TUN address: %w", err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return nil, fmt.Errorf("activate TUN: %w", err)
	}
	success = true
	return device, nil
}
func (d *native) Name() string                 { return d.config.Name }
func (d *native) Configuration() Config        { return d.config }
func (d *native) Read(raw []byte) (int, error) { return d.file.Read(raw) }
func (d *native) Write(raw []byte) (int, error) {
	n, err := d.file.Write(raw)
	if errors.Is(err, unix.ENODEV) || errors.Is(err, unix.EIO) {
		return n, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return n, err
}
func (d *native) Close() error {
	d.once.Do(func() { d.closeError = d.file.Close() })
	return d.closeError
}
