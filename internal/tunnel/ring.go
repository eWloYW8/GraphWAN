package tunnel

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

var errRingEmpty = errors.New("receive ring empty")

// The backend owns packet storage until release/send. wait must also observe a
// separate persistent close signal: reusing the driver's read event can lose a
// close wakeup when ReceivePacket resets that event on an empty ring.
type packetRing interface {
	receive() ([]byte, error)
	release([]byte)
	allocate(int) ([]byte, error)
	send([]byte)
	wait() error
	wake() error
	close() error
}

type ringDevice struct {
	ring       packetRing
	config     Config
	configMu   sync.Mutex
	writeMu    sync.Mutex
	lifeMu     sync.Mutex
	closed     atomic.Bool
	active     sync.WaitGroup
	once       sync.Once
	closeError error
}

func (d *ringDevice) Name() string { return d.config.Name }
func (d *ringDevice) Configuration() Config {
	d.configMu.Lock()
	defer d.configMu.Unlock()
	return d.config
}
func (d *ringDevice) begin() bool {
	d.lifeMu.Lock()
	defer d.lifeMu.Unlock()
	if d.closed.Load() {
		return false
	}
	d.active.Add(1)
	return true
}
func (d *ringDevice) Read(raw []byte) (int, error) {
	if !d.begin() {
		return 0, os.ErrClosed
	}
	defer d.active.Done()
	for {
		if d.closed.Load() {
			return 0, os.ErrClosed
		}
		packet, err := d.ring.receive()
		if errors.Is(err, errRingEmpty) {
			if err := d.ring.wait(); err != nil {
				return 0, err
			}
			continue
		}
		if err != nil {
			return 0, err
		}
		if len(packet) == 0 {
			return 0, fmt.Errorf("%w: empty receive ring packet", ErrUnavailable)
		}
		if len(packet) > len(raw) {
			d.ring.release(packet)
			return 0, io.ErrShortBuffer
		}
		n := copy(raw, packet)
		d.ring.release(packet)
		return n, nil
	}
}
func (d *ringDevice) Write(raw []byte) (int, error) {
	if len(raw) == 0 || len(raw) > model.MaxMTU || (raw[0]>>4 != 4 && raw[0]>>4 != 6) {
		return 0, errors.New("invalid TUN IP packet")
	}
	if !d.begin() {
		return 0, os.ErrClosed
	}
	defer d.active.Done()
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	if d.closed.Load() {
		return 0, os.ErrClosed
	}
	packet, err := d.ring.allocate(len(raw))
	if err != nil {
		return 0, err
	}
	copy(packet, raw)
	d.ring.send(packet)
	return len(raw), nil
}
func (d *ringDevice) Close() error {
	d.once.Do(func() {
		d.configMu.Lock()
		defer d.configMu.Unlock()
		d.lifeMu.Lock()
		d.closed.Store(true)
		d.lifeMu.Unlock()
		wakeError := d.ring.wake()
		// begin cannot add a new operation after closed is published. Existing
		// operations finish before any mapped packet memory or handle is freed.
		d.active.Wait()
		d.closeError = errors.Join(wakeError, d.ring.close())
	})
	return d.closeError
}
