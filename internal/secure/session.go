package secure

import (
	"encoding/binary"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/packet"
	"github.com/flynn/noise"
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
	id          string
	binding     []byte
	sendAD      []byte
	receiveAD   []byte
	created     time.Time
	sendMu      sync.Mutex
	receiveMu   sync.Mutex
	send        noise.Cipher
	receive     noise.Cipher
	sequence    uint64
	highest     uint64
	seen        [ReplayWindow]uint64
	batchAD     [4][]byte
	batchNonces [128]uint64
}

func newSession(id string, binding []byte, send, receive noise.Cipher) *Session {
	return &Session{id: id, binding: binding, sendAD: append(append([]byte{}, binding...), make([]byte, 8)...), receiveAD: append(append([]byte{}, binding...), make([]byte, 8)...), created: time.Now(), send: send, receive: receive}
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
	return s.SealAppend(nil, plaintext)
}

// SealAppend appends one encrypted message to dst. The caller owns dst and must
// not overlap it with plaintext. Nonces and associated data remain session-owned.
func (s *Session) SealAppend(dst, plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 || len(plaintext) > MaxPlaintext {
		return nil, errors.New("invalid encrypted message size")
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.sequence >= MaxMessages-1 || time.Since(s.created) >= MaxSessionAge {
		return nil, ErrRekey
	}
	s.sequence++
	offset := len(dst)
	dst = append(dst, make([]byte, 8)...)
	binary.BigEndian.PutUint64(dst[offset:], s.sequence)
	copy(s.sendAD[len(s.binding):], dst[offset:])
	return s.send.Encrypt(dst, s.sequence, s.sendAD, plaintext), nil
}

// Open accepts bounded out-of-order delivery. Replay state advances only after
// authentication succeeds, so forged high nonces cannot evict valid packets.
func (s *Session) Open(frame []byte) ([]byte, error) { return s.open(nil, frame) }

// OpenInPlace consumes an exclusively owned encrypted frame. The returned
// plaintext aliases frame; it must not be used as ciphertext again.
func (s *Session) OpenInPlace(frame []byte) ([]byte, error) {
	if len(frame) <= Overhead {
		return nil, errors.New("invalid encrypted frame size")
	}
	return s.open(frame[8:8], frame)
}
func (s *Session) open(dst, frame []byte) ([]byte, error) {
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
	copy(s.receiveAD[len(s.binding):], frame[:8])
	plaintext, err := s.receive.Decrypt(dst, nonce, s.receiveAD, frame[8:])
	if err != nil {
		return nil, err
	}
	s.seen[nonce%ReplayWindow] = nonce
	s.highest = max(s.highest, nonce)
	return plaintext, nil
}

// OpenBatchInPlace consumes distinct, exclusively owned ciphertext buffers and
// replaces successful entries with plaintext. Authentication can run in parallel,
// but replay-window commits are serialized in original receive order. A forged
// high nonce or a duplicate within the batch can never advance/evade the window.
func (s *Session) OpenBatchInPlace(frames [][]byte) []error {
	errs := make([]error, len(frames))
	if len(frames) > len(s.batchNonces) {
		for i := range errs {
			errs[i] = errors.New("encrypted batch too large")
		}
		return errs
	}
	s.receiveMu.Lock()
	defer s.receiveMu.Unlock()
	if time.Since(s.created) >= MaxSessionAge {
		for i := range errs {
			errs[i] = ErrRekey
		}
		return errs
	}
	bytes := 0
	for i, frame := range frames {
		if len(frame) <= Overhead || len(frame) > MaxPlaintext+Overhead {
			errs[i] = errors.New("invalid encrypted frame size")
			continue
		}
		nonce := binary.BigEndian.Uint64(frame[:8])
		s.batchNonces[i] = nonce
		if nonce == 0 || nonce >= MaxMessages || s.seen[nonce%ReplayWindow] == nonce || s.highest >= ReplayWindow && nonce <= s.highest-ReplayWindow {
			errs[i] = ErrReplay
			continue
		}
		bytes += len(frame)
	}
	decrypt := func(worker, start, end int) {
		if s.batchAD[worker] == nil {
			s.batchAD[worker] = make([]byte, len(s.binding)+8)
			copy(s.batchAD[worker], s.binding)
		}
		ad := s.batchAD[worker]
		for i := start; i < end; i++ {
			if errs[i] != nil {
				continue
			}
			frame := frames[i]
			copy(ad[len(s.binding):], frame[:8])
			frames[i], errs[i] = s.receive.Decrypt(frame[8:8], s.batchNonces[i], ad, frame[8:])
		}
	}
	// ARM64 currently uses the generic Poly1305 path. Smaller batches there
	// justify parallel workers; vectorized amd64 crypto needs a larger payload
	// to amortize scheduling. Small/control batches always stay synchronous.
	parallelBytes := 64 * 1024
	if runtime.GOARCH == "arm64" {
		parallelBytes = 8 * 1024
	}
	if len(frames) >= 8 && bytes >= parallelBytes && runtime.GOMAXPROCS(0) > 1 {
		workers := min(4, runtime.GOMAXPROCS(0), max(2, len(frames)/8))
		var next atomic.Int32
		work := func(worker int) {
			// Small chunks let faster cores take more work on heterogeneous CPUs.
			for {
				start := int(next.Add(4)) - 4
				if start >= len(frames) {
					return
				}
				decrypt(worker, start, min(start+4, len(frames)))
			}
		}
		var wg sync.WaitGroup
		for worker := 1; worker < workers; worker++ {
			wg.Add(1)
			go func() { defer wg.Done(); work(worker) }()
		}
		work(0)
		wg.Wait()
	} else {
		decrypt(0, 0, len(frames))
	}
	for i, nonce := range s.batchNonces[:len(frames)] {
		if errs[i] == nil {
			if s.seen[nonce%ReplayWindow] == nonce || s.highest >= ReplayWindow && nonce <= s.highest-ReplayWindow {
				errs[i] = ErrReplay
			} else {
				s.seen[nonce%ReplayWindow] = nonce
				s.highest = max(s.highest, nonce)
			}
		}
		if errs[i] != nil {
			frames[i] = nil
		}
	}
	return errs
}
