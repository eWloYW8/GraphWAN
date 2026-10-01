package wgaccess

import (
	"context"
	"net"
	"os"
	"sync"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/packetbuf"
	"golang.zx2c4.com/wireguard/tun"
)

type memoryTUN struct {
	mtu      int
	network  model.ID
	receive  func(model.ID, []byte)
	outgoing *packetbuf.Queue
	events   chan tun.Event
	once     sync.Once
}

func newTUN(mtu int, network model.ID, receive func(model.ID, []byte)) *memoryTUN {
	return &memoryTUN{mtu: mtu, network: network, receive: receive, outgoing: packetbuf.NewQueue(256), events: make(chan tun.Event)}
}
func (t *memoryTUN) File() *os.File           { return nil }
func (t *memoryTUN) Name() (string, error)    { return "graphwan-wireguard", nil }
func (t *memoryTUN) MTU() (int, error)        { return t.mtu, nil }
func (t *memoryTUN) BatchSize() int           { return 1 }
func (t *memoryTUN) Events() <-chan tun.Event { return t.events }
func (t *memoryTUN) Close() error {
	t.once.Do(func() { t.outgoing.Close(); close(t.events) })
	return nil
}
func (t *memoryTUN) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	var packets [1]*packetbuf.Buffer
	n, err := t.outgoing.Read(context.Background(), packets[:])
	if err != nil {
		return 0, err
	}
	for i := range n {
		sizes[i] = copy(bufs[i][offset:], packets[i].Data)
		packets[i].Release()
	}
	return n, nil
}
func (t *memoryTUN) Write(bufs [][]byte, offset int) (int, error) {
	for _, raw := range bufs {
		t.receive(t.network, raw[offset:])
	}
	return len(bufs), nil
}
func (t *memoryTUN) send(raw []byte) error {
	b := packetbuf.Get(len(raw))
	copy(b.Data, raw)
	if t.outgoing.Put([]*packetbuf.Buffer{b}) != 0 {
		return net.ErrClosed
	}
	return nil
}
