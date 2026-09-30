package link

import (
	"encoding/binary"
	"time"
)

func encodePing(nonce uint64, compact bool) []byte {
	// Never wrap a shortened nonce: a delayed authenticated reply must not
	// acknowledge a later probe. Normal sessions rekey well before this limit.
	if compact && nonce <= 0xffff {
		return binary.BigEndian.AppendUint16([]byte{compactPingMessage}, uint16(nonce))
	}
	return binary.BigEndian.AppendUint64([]byte{pingMessage}, nonce)
}

func (l *Link) handleProbe(raw []byte, now time.Time) bool {
	if len(raw) == 0 {
		return false
	}
	var nonce uint64
	switch raw[0] {
	case pingMessage, pongMessage:
		if len(raw) != 9 {
			return false
		}
		nonce = binary.BigEndian.Uint64(raw[1:])
	case compactPingMessage, compactPongMessage:
		if len(raw) != 3 {
			return false
		}
		nonce = uint64(binary.BigEndian.Uint16(raw[1:]))
	default:
		return false
	}
	if raw[0] == pongMessage || raw[0] == compactPongMessage {
		l.recordPong(nonce, now)
		return true
	}
	if raw[0] == compactPingMessage {
		l.mu.Lock()
		l.compactProbes = true
		l.mu.Unlock()
	}
	response := append([]byte(nil), raw...)
	response[0]++ // Both ping encodings have the corresponding pong immediately after.
	select {
	case l.control <- response:
	case <-l.ctx.Done():
		return false
	default:
	}
	return true
}
