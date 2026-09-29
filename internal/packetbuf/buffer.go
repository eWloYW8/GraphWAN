// Package packetbuf provides explicitly owned, size-classed packet storage.
package packetbuf

import "sync"

// Buffer has one owner. Data may be resliced (for example after in-place
// decryption); Release always returns the original allocation. Neither the
// Buffer nor any view of its Data may be used after ownership is transferred or
// Release is called. Release must be called exactly once, including on drops.
type Buffer struct {
	Data    []byte
	storage []byte
	pool    *sync.Pool
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
		return &Buffer{Data: make([]byte, size)} // Do not retain exceptional jumbo allocations.
	}
	b.Data = b.storage[:size]
	return b
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
