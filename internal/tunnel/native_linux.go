//go:build linux

package tunnel

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	wgtun "golang.zx2c4.com/wireguard/tun"
)

type native struct {
	device              wgtun.Device
	readBuffers         [][]byte
	readSizes           []int
	readNext, readCount int
	writeMu             sync.Mutex
	writeBuffers        [32][]byte
	config              Config
	once                sync.Once
	closeError          error
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
	request.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI | unix.IFF_TUN_EXCL | unix.IFF_VNET_HDR)
	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, request); err != nil {
		return nil, fmt.Errorf("create TUN %s (requires CAP_NET_ADMIN): %w", config.Name, err)
	}
	// An unattached TUN descriptor cannot be registered with epoll. Construct
	// the Go file only after TUNSETIFF, so idle reads can wait and be canceled.
	file = os.NewFile(uintptr(fd), "/dev/net/tun")
	device := &native{config: config}
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
	device.device, err = wgtun.CreateTUNFromFile(file, config.MTU)
	if err != nil {
		return nil, fmt.Errorf("initialize TUN offloads: %w", err)
	}
	// Link changes are owned by our reconciler; drain the adapter's notifications.
	go func() {
		for range device.device.Events() {
		}
	}()
	success = true
	return device, nil
}
func (d *native) Name() string          { return d.config.Name }
func (d *native) Configuration() Config { return d.config }

// Read retains compatibility with single-packet users while draining every
// segment of a kernel GSO packet. The agent uses ReadBatch directly.
func (d *native) Read(raw []byte) (int, error) {
	if d.readBuffers == nil {
		d.readBuffers = make([][]byte, d.BatchSize())
		d.readSizes = make([]int, d.BatchSize())
		for i := range d.readBuffers {
			d.readBuffers[i] = make([]byte, model.MaxMTU+1)
		}
	}
	if d.readNext == d.readCount {
		var err error
		d.readCount, err = d.ReadBatch(d.readBuffers, d.readSizes)
		d.readNext = 0
		if err != nil {
			return 0, err
		}
		if d.readCount == 0 {
			return 0, io.ErrNoProgress
		}
	}
	i := d.readNext
	d.readNext++
	if len(raw) < d.readSizes[i] {
		return 0, io.ErrShortBuffer
	}
	return copy(raw, d.readBuffers[i][:d.readSizes[i]]), nil
}
func (d *native) BatchSize() int { return d.device.BatchSize() }
func (d *native) ReadBatch(buffers [][]byte, sizes []int) (int, error) {
	return d.device.Read(buffers, sizes, 0)
}
func (d *native) Write(raw []byte) (int, error) {
	if err := d.WriteBatch([][]byte{raw}); err != nil {
		return 0, err
	}
	return len(raw), nil
}
func (d *native) WriteBatch(packets [][]byte) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	// GRO mutates its inputs and needs headroom for the virtio header and room
	// to append adjacent TCP segments. Copy only at this ownership boundary.
	for len(packets) > 0 {
		count := min(len(packets), len(d.writeBuffers))
		for i, raw := range packets[:count] {
			if len(raw) == 0 || len(raw) > 65535 {
				return errors.New("invalid TUN packet size")
			}
			if d.writeBuffers[i] == nil {
				d.writeBuffers[i] = make([]byte, 65535+10)
			}
			d.writeBuffers[i] = d.writeBuffers[i][:len(raw)+10]
			copy(d.writeBuffers[i][10:], raw)
		}
		_, err := d.device.Write(d.writeBuffers[:count], 10)
		if errors.Is(err, unix.ENODEV) || errors.Is(err, unix.EIO) {
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		if err != nil {
			return err
		}
		packets = packets[count:]
	}
	return nil
}
func (d *native) Close() error {
	d.once.Do(func() { d.closeError = d.device.Close() })
	return d.closeError
}
