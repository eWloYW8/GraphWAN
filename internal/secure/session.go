package secure

import (
	"encoding/binary"
	"errors"
	"sync"
	"time"

	"github.com/flynn/noise"
	"github.com/graphwan/graphwan/internal/packet"
)

const (
	ReplayWindow = 1024
	// A fresh handshake must replace the session before either limit is reached.
	MaxMessages   = uint64(1) << 32
	MaxSessionAge = time.Hour
	// Plaintext includes a message type and, for data, a complete overlay frame.
	MaxPlaintext = packet.MaxFrame + 1
	Overhead     = 8 + 16
)

var ErrReplay = errors.New("replayed or stale encrypted packet")
var ErrRekey = errors.New("session requires a fresh handshake")

type Session struct {
	id        string
	binding   []byte
	created   time.Time
	sendMu    sync.Mutex
	receiveMu sync.Mutex
	send      noise.Cipher
	receive   noise.Cipher
	sequence  uint64
	highest   uint64
	seen      [ReplayWindow]uint64
}

func newSession(id string, binding []byte, send, receive noise.Cipher) *Session {
	return &Session{id: id, binding: binding, created: time.Now(), send: send, receive: receive}
}
func (s *Session) ID() string         { return s.id }
func (s *Session) Created() time.Time { return s.created }
func (s *Session) NeedsRekey() bool {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.sequence >= MaxMessages-1 || time.Since(s.created) >= MaxSessionAge
}

// Seal uses a fresh monotonic nonce even when a transport send subsequently
// fails. Nonces, keys and replay windows are never persisted or reused.
func (s *Session) Seal(plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 || len(plaintext) > MaxPlaintext {
		return nil, errors.New("invalid encrypted message size")
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.sequence >= MaxMessages-1 || time.Since(s.created) >= MaxSessionAge {
		return nil, ErrRekey
	}
	s.sequence++
	prefix := make([]byte, 8)
	binary.BigEndian.PutUint64(prefix, s.sequence)
	ad := make([]byte, 0, len(s.binding)+8)
	ad = append(ad, s.binding...)
	ad = append(ad, prefix...)
	return s.send.Encrypt(prefix, s.sequence, ad, plaintext), nil
}

// Open accepts bounded out-of-order delivery. Replay state advances only after
// authentication succeeds, so forged high nonces cannot evict valid packets.
func (s *Session) Open(frame []byte) ([]byte, error) {
	if len(frame) <= Overhead || len(frame) > MaxPlaintext+Overhead {
		return nil, errors.New("invalid encrypted frame size")
	}
	if time.Since(s.created) >= MaxSessionAge {
		return nil, ErrRekey
	}
	nonce := binary.BigEndian.Uint64(frame[:8])
	if nonce == 0 || nonce >= MaxMessages {
		return nil, ErrReplay
	}
	s.receiveMu.Lock()
	defer s.receiveMu.Unlock()
	if s.seen[nonce%ReplayWindow] == nonce || s.highest >= ReplayWindow && nonce <= s.highest-ReplayWindow {
		return nil, ErrReplay
	}
	ad := make([]byte, 0, len(s.binding)+8)
	ad = append(ad, s.binding...)
	ad = append(ad, frame[:8]...)
	plaintext, err := s.receive.Decrypt(nil, nonce, ad, frame[8:])
	if err != nil {
		return nil, err
	}
	s.seen[nonce%ReplayWindow] = nonce
	s.highest = max(s.highest, nonce)
	return plaintext, nil
}
