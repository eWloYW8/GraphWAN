package transport

import (
	"net"
	"sync"
)

// At most one accepted connection per listener waits for the caller. Close
// interrupts every Accept and closes connections that have not been delivered.
type listenerSet struct {
	listeners []net.Listener
	accepted  chan net.Conn
	done      chan struct{}
	once      sync.Once
	wg        sync.WaitGroup
}

func mergeListeners(listeners []net.Listener) net.Listener {
	if len(listeners) == 1 {
		return listeners[0]
	}
	s := &listenerSet{listeners: listeners, accepted: make(chan net.Conn), done: make(chan struct{})}
	s.wg.Add(len(listeners))
	for _, listener := range listeners {
		go func() {
			defer s.wg.Done()
			for {
				conn, err := listener.Accept()
				if err != nil {
					s.stop()
					return
				}
				select {
				case s.accepted <- conn:
				case <-s.done:
					conn.Close()
					return
				}
			}
		}()
	}
	return s
}

func (s *listenerSet) Addr() net.Addr { return s.listeners[0].Addr() }
func (s *listenerSet) Accept() (net.Conn, error) {
	select {
	case <-s.done:
		return nil, net.ErrClosed
	case conn := <-s.accepted:
		select {
		case <-s.done:
			conn.Close()
			return nil, net.ErrClosed
		default:
			return conn, nil
		}
	}
}
func (s *listenerSet) stop() {
	s.once.Do(func() {
		close(s.done)
		for _, listener := range s.listeners {
			listener.Close()
		}
	})
}
func (s *listenerSet) Close() error { s.stop(); s.wg.Wait(); return nil }
