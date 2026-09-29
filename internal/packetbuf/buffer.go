// Package packetbuf provides explicitly owned, size-classed packet storage.
package packetbuf

import "sync"

// BatchSize bounds immediately available bursts without waiting for more packets.
const BatchSize = 128

// Buffer has one owner. Data may be resliced (for example after in-place
// decryption); Release always returns the original allocation. Neither the
// Buffer nor any view of its Data may be used after ownership is transferred or
// Release is called. Release must be called exactly once, including on drops.
type Buffer struct {
	Data     []byte
	storage  []byte
	pool     *sync.Pool
	headroom int
}

var small, large sync.Pool

func init() {
	small.New = func() any { return &Buffer{storage: make([]byte, 2048), pool: &small} }
	large.New = func() any { return &Buffer{storage: make([]byte, 16384), pool: &large} }
}

func Get(size int) *Buffer {
	if size < 0 {
		panic("negative packet size")
	}
	var b *Buffer
	switch {
	case size <= 2048:
		b = small.Get().(*Buffer)
	case size <= 16384:
		b = large.Get().(*Buffer)
	default:
		data := make([]byte, size)
		return &Buffer{Data: data, storage: data} // Do not retain exceptional jumbo allocations.
	}
	b.Data = b.storage[:size]
	b.headroom = 0
	return b
}

// GetHeadroom reserves prefix space for a later framing layer. The payload has
// the requested size, and the reservation is valid until Data is resliced.
func GetHeadroom(size, headroom int) *Buffer {
	if size < 0 || headroom < 0 || size+headroom < size {
		panic("invalid packet headroom")
	}
	b := Get(size + headroom)
	b.Data = b.Data[headroom:]
	b.headroom = headroom
	return b
}

// PrependByte uses reserved space only while Data is still the original view.
// Other buffers can use the caller's ordinary copy fallback.
func (b *Buffer) PrependByte(value byte) bool {
	if b.headroom == 0 || b.headroom >= len(b.storage) || len(b.Data) == 0 || &b.Data[0] != &b.storage[b.headroom] {
		return false
	}
	end := b.headroom + len(b.Data)
	b.headroom--
	b.Data = b.storage[b.headroom:end]
	b.Data[0] = value
	return true
}

// Wrap transfers ownership of an existing allocation, without copying it. Such
// allocations are garbage collected rather than inserted into a size-class pool.
func Wrap(data []byte) *Buffer { return &Buffer{Data: data} }
func (b *Buffer) Release() {
	if b == nil {
		return
	}
	b.Data = nil
	if b.pool != nil {
		b.pool.Put(b)
	}
}
func ReleaseAll(buffers []*Buffer) {
	for _, b := range buffers {
		b.Release()
	}
}
