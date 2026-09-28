package transport

import (
	"encoding/binary"
	"time"
)

const (
	quicFragmentHeader   = 13 // version, message ID, total message size, offset
	quicFragmentPayload  = 1024
	quicReassemblies     = 32
	quicAssemblyLifetime = 2 * time.Second
)

type quicAssembly struct {
	raw     []byte
	seen    uint16
	count   int
	expires time.Time
}
type quicReassembler struct{ pending map[uint64]*quicAssembly }

// Datagram reassembly neither acknowledges nor retransmits fragments. A missing
// fragment drops the whole message. Limits also apply before peer Noise admission.
func (r *quicReassembler) receive(fragment []byte, now time.Time) []byte {
	for id, record := range r.pending {
		if !now.Before(record.expires) {
			delete(r.pending, id)
		}
	}
	if len(fragment) <= quicFragmentHeader || fragment[0] != 1 {
		return nil
	}
	id := binary.BigEndian.Uint64(fragment[1:9])
	total := int(binary.BigEndian.Uint16(fragment[9:11]))
	offset := int(binary.BigEndian.Uint16(fragment[11:13]))
	if id == 0 || total == 0 || total > MaxMessage || offset >= total || offset%quicFragmentPayload != 0 || len(fragment)-quicFragmentHeader != min(quicFragmentPayload, total-offset) {
		return nil
	}
	if total <= quicFragmentPayload {
		return fragment[quicFragmentHeader:]
	}
	if r.pending == nil {
		r.pending = make(map[uint64]*quicAssembly)
	}
	record := r.pending[id]
	if record == nil {
		if len(r.pending) >= quicReassemblies {
			return nil
		}
		record = &quicAssembly{raw: make([]byte, total), expires: now.Add(quicAssemblyLifetime)}
		r.pending[id] = record
	}
	if len(record.raw) != total {
		delete(r.pending, id)
		return nil
	}
	mask := uint16(1) << (offset / quicFragmentPayload)
	if record.seen&mask != 0 {
		return nil
	}
	copy(record.raw[offset:], fragment[quicFragmentHeader:])
	record.seen |= mask
	record.count++
	if record.count != (total+quicFragmentPayload-1)/quicFragmentPayload {
		return nil
	}
	delete(r.pending, id)
	return record.raw
}
