//go:build freebsd || darwin || openbsd

package tunnel

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/graphwan/graphwan/internal/model"
	"golang.org/x/sys/unix"
)

// BSD TUN and Darwin utun prepend a network-order address family to each packet.
// Read has one caller; writes are serialized without holding a lock across Close.
type framedDevice struct {
	file        *os.File
	config      Config
	ipv6Family  uint32
	readBuffer  [model.MaxMTU + 4]byte
	writeBuffer [model.MaxMTU + 4]byte
	writeMu     sync.Mutex
	once        sync.Once
	closeError  error
	cleanup     func() error
	configMu    sync.Mutex
	closed      bool
}

func (d *framedDevice) Name() string { return d.config.Name }
func (d *framedDevice) Configuration() Config {
	d.configMu.Lock()
	defer d.configMu.Unlock()
	return d.config
}
func (d *framedDevice) Read(raw []byte) (int, error) {
	n, err := d.file.Read(d.readBuffer[:])
	if err != nil {
		return 0, err
	}
	if n < 5 {
		return 0, fmt.Errorf("invalid TUN frame length %d", n)
	}
	family := binary.BigEndian.Uint32(d.readBuffer[:4])
	version := d.readBuffer[4] >> 4
	if (family != unix.AF_INET || version != 4) && (family != d.ipv6Family || version != 6) {
		return 0, errors.New("invalid TUN address family")
	}
	if n-4 > len(raw) {
		return 0, io.ErrShortBuffer
	}
	return copy(raw, d.readBuffer[4:n]), nil
}
func (d *framedDevice) Write(raw []byte) (int, error) {
	if len(raw) == 0 || len(raw) > model.MaxMTU {
		return 0, errors.New("invalid TUN packet length")
	}
	family := uint32(unix.AF_INET)
	switch raw[0] >> 4 {
	case 4:
	case 6:
		family = d.ipv6Family
	default:
		return 0, errors.New("invalid TUN IP version")
	}
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	binary.BigEndian.PutUint32(d.writeBuffer[:4], family)
	copy(d.writeBuffer[4:], raw)
	n, err := d.file.Write(d.writeBuffer[:len(raw)+4])
	if errors.Is(err, unix.ENXIO) || errors.Is(err, unix.ENODEV) || errors.Is(err, unix.EIO) {
		err = fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if err == nil && n != len(raw)+4 {
		err = io.ErrShortWrite
	}
	return max(0, n-4), err
}
func (d *framedDevice) Close() error {
	d.once.Do(func() {
		d.configMu.Lock()
		defer d.configMu.Unlock()
		d.closed = true
		d.closeError = d.file.Close()
		if d.cleanup != nil {
			d.closeError = errors.Join(d.closeError, d.cleanup())
		}
	})
	return d.closeError
}

// Fixed absolute executables and individual arguments avoid PATH/shell injection.
// Both runtime and diagnostic output are bounded, including on a broken utility.
func interfaceCommand(path string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = time.Second
	var output limitedOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %v: %w: %s", path, args, err, output.String())
	}
	return nil
}

type limitedOutput struct{ strings.Builder }

func (b *limitedOutput) Write(raw []byte) (int, error) {
	n := len(raw)
	_, _ = b.Builder.Write(raw[:min(n, 4096-b.Len())])
	return n, nil
}
