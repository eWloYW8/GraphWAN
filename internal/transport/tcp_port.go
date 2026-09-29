package transport

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"time"
)

// ListenTCP retains owned listeners while allowing outgoing TCP connections
// from its source port. This is required for TCP mapping discovery and punching.
func ListenTCP(ctx context.Context, address string) (*TCP, error) {
	config := net.ListenConfig{Control: reuseTCPPort}
	listeners, err := listenIP(address, func(family, address string) (net.Listener, net.Addr, error) {
		listener, err := config.Listen(ctx, "tcp"+family, address)
		if err != nil {
			return nil, nil, err
		}
		return listener, listener.Addr(), nil
	})
	if err != nil {
		return nil, err
	}
	t := &TCP{Listener: mergeListeners(listeners), bindings: map[netip.AddrPort]*tcpBinding{}, done: make(chan struct{})}
	t.wg.Add(1)
	go t.expireBindings()
	return t, nil
}

type TCP struct {
	net.Listener
	mu       sync.Mutex
	bindings map[netip.AddrPort]*tcpBinding
	done     chan struct{}
	once     sync.Once
	wg       sync.WaitGroup
}

func (t *TCP) Close() error {
	t.once.Do(func() {
		close(t.done)
		t.Listener.Close()
		t.mu.Lock()
		for _, binding := range t.bindings {
			binding.cancel()
			if binding.conn != nil {
				binding.conn.Close()
			}
		}
		clear(t.bindings)
		t.mu.Unlock()
	})
	t.wg.Wait()
	return nil
}

func (t *TCP) expireBindings() {
	defer t.wg.Done()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-t.done:
			return
		case now := <-ticker.C:
			t.mu.Lock()
			for address, binding := range t.bindings {
				if now.Sub(binding.lastUsed) > time.Minute {
					binding.cancel()
					if binding.conn != nil {
						binding.conn.Close()
					}
					delete(t.bindings, address)
				}
			}
			t.mu.Unlock()
		}
	}
}

func DialTCPPort(ctx context.Context, local *net.TCPAddr, remote netip.AddrPort) (net.Conn, error) {
	family := "tcp6"
	if remote.Addr().Unmap().Is4() {
		family = "tcp4"
	}
	bind := &net.TCPAddr{IP: local.IP, Port: local.Port, Zone: local.Zone}
	if bind.IP.IsUnspecified() {
		bind.IP = nil
		bind.Zone = ""
	}
	dialer := net.Dialer{LocalAddr: bind, Control: reuseTCPPort}
	return dialer.DialContext(ctx, family, remote.String())
}
