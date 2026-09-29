// Package packet implements the bounded, transport-independent overlay wire format.
package packet

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/graphwan/graphwan/internal/model"
)

const (
	Version         = 1
	HeaderSize      = 80
	MaxPayload      = model.MaxMTU
	MaxFrame        = HeaderSize + MaxPayload
	DefaultHopLimit = 32
)

var ErrHopLimit = errors.New("overlay hop limit exceeded")

// Header is authenticated together with the payload by the peer crypto layer.
// Sequence belongs to an encrypted session, not a virtual IP flow.
type Header struct {
	Network     model.ID
	Source      model.ID
	Destination model.ID
	HopLimit    uint8
	Epoch       uint64
	Flow        uint64
	Sequence    uint64
}
type Packet struct {
	Header  Header
	Payload []byte
}

func (p Packet) MarshalBinary() ([]byte, error) { return p.AppendBinary(nil) }

// AppendBinary appends the wire frame to caller-owned storage. dst must not
// overlap Payload. Callers can reuse capacity without changing the wire format.
func (p Packet) AppendBinary(dst []byte) ([]byte, error) {
	if len(p.Payload) == 0 || len(p.Payload) > MaxPayload {
		return nil, errors.New("invalid payload length")
	}
	if p.Header.HopLimit == 0 {
		return nil, ErrHopLimit
	}
	offset := len(dst)
	dst = append(dst, make([]byte, HeaderSize+len(p.Payload))...)
	out := dst[offset:]
	out[0], out[1] = 'G', 'W'
	out[2] = Version
	out[3] = p.Header.HopLimit
	binary.BigEndian.PutUint16(out[4:6], uint16(len(p.Payload)))
	// Bytes 6:8 are reserved and must remain zero.
	for i, id := range []model.ID{p.Header.Network, p.Header.Source, p.Header.Destination} {
		if err := id.Validate(); err != nil {
			return nil, err
		}
		if _, err := hex.Decode(out[8+i*16:24+i*16], []byte(id)); err != nil {
			return nil, err
		}
	}
	binary.BigEndian.PutUint64(out[56:64], p.Header.Epoch)
	binary.BigEndian.PutUint64(out[64:72], p.Header.Flow)
	binary.BigEndian.PutUint64(out[72:80], p.Header.Sequence)
	copy(out[HeaderSize:], p.Payload)
	return dst, nil
}

// Parse references the caller's frame; callers must copy before reusing buffers.
func Parse(frame []byte) (Packet, error) {
	var p Packet
	if len(frame) < HeaderSize || len(frame) > MaxFrame {
		return p, errors.New("invalid frame length")
	}
	if frame[0] != 'G' || frame[1] != 'W' || frame[2] != Version || frame[6] != 0 || frame[7] != 0 {
		return p, errors.New("unsupported packet header")
	}
	if frame[3] == 0 {
		return p, ErrHopLimit
	}
	size := int(binary.BigEndian.Uint16(frame[4:6]))
	if size == 0 || size != len(frame)-HeaderSize {
		return p, errors.New("payload length mismatch")
	}
	ids := []*model.ID{&p.Header.Network, &p.Header.Source, &p.Header.Destination}
	for i, dest := range ids {
		raw := frame[8+i*16 : 24+i*16]
		// Fixed-width binary IDs encode to canonical lowercase hex by construction.
		// Only the reserved zero value needs validation on this receive path.
		if [16]byte(raw) == [16]byte{} {
			return Packet{}, errors.New("zero ID is reserved")
		}
		*dest = model.ID(hex.EncodeToString(raw))
	}
	p.Header.HopLimit = frame[3]
	p.Header.Epoch = binary.BigEndian.Uint64(frame[56:64])
	p.Header.Flow = binary.BigEndian.Uint64(frame[64:72])
	p.Header.Sequence = binary.BigEndian.Uint64(frame[72:80])
	p.Payload = frame[HeaderSize:]
	return p, nil
}

func (p *Packet) Forward() error {
	if p.Header.HopLimit <= 1 {
		return ErrHopLimit
	}
	p.Header.HopLimit--
	return nil
}

// ReadFrame checks the size before allocation, including for malicious peers.
func ReadFrame(r io.Reader) ([]byte, error) {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n <= HeaderSize || n > MaxFrame {
		return nil, fmt.Errorf("invalid stream frame length %d", n)
	}
	frame := make([]byte, n)
	_, err := io.ReadFull(r, frame)
	return frame, err
}
func WriteFrame(w io.Writer, frame []byte) error {
	if len(frame) <= HeaderSize || len(frame) > MaxFrame {
		return errors.New("invalid stream frame length")
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(frame)))
	if err := writeAll(w, size[:]); err != nil {
		return err
	}
	return writeAll(w, frame)
}
func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if n < 0 || n > len(data) {
			return io.ErrShortWrite
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
