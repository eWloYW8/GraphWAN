package packet_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	"github.com/eWloYW8/GraphWAN/internal/packet"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
)

func example() packet.Packet {
	return packet.Packet{Header: packet.Header{Network: testutil.ID(1), Source: testutil.ID(2), Destination: testutil.ID(3), HopLimit: 32, Epoch: 123, Flow: 456, Sequence: 789}, Payload: []byte{0x45, 0, 1, 2, 3}}
}
func TestRoundTripAndForward(t *testing.T) {
	p := example()
	raw, err := p.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	got, err := packet.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p, got) {
		t.Fatalf("roundtrip=%+v", got)
	}
	for range 31 {
		if err := got.Forward(); err != nil {
			t.Fatal(err)
		}
	}
	if err := got.Forward(); !errors.Is(err, packet.ErrHopLimit) {
		t.Fatal("hop limit did not expire")
	}
	var buf bytes.Buffer
	if err := packet.WriteFrame(&buf, raw); err != nil {
		t.Fatal(err)
	}
	frame, err := packet.ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(frame, raw) {
		t.Fatal("stream framing corrupted packet")
	}
}
func TestRejectMalformed(t *testing.T) {
	raw, _ := example().MarshalBinary()
	for name, change := range map[string]func([]byte) []byte{
		"short":            func(b []byte) []byte { return b[:20] },
		"version":          func(b []byte) []byte { b[2] = 2; return b },
		"reserved":         func(b []byte) []byte { b[6] = 1; return b },
		"hop limit":        func(b []byte) []byte { b[3] = 0; return b },
		"length":           func(b []byte) []byte { b[5]++; return b },
		"zero ID":          func(b []byte) []byte { clear(b[8:24]); return b },
		"zero source":      func(b []byte) []byte { clear(b[24:40]); return b },
		"zero destination": func(b []byte) []byte { clear(b[40:56]); return b },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := packet.Parse(change(bytes.Clone(raw))); err == nil {
				t.Fatal("accepted malformed frame")
			}
		})
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], 0xffffffff)
	if _, err := packet.ReadFrame(bytes.NewReader(prefix[:])); err == nil {
		t.Fatal("accepted unbounded allocation")
	}
}
