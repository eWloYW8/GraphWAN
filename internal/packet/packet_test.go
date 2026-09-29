package packet_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
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

type stalledWriter struct{}

func (stalledWriter) Write([]byte) (int, error) { return 0, nil }
func TestStalledWriter(t *testing.T) {
	raw, _ := example().MarshalBinary()
	if err := packet.WriteFrame(stalledWriter{}, raw); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(err)
	}
}

func FuzzParse(f *testing.F) {
	raw, _ := example().MarshalBinary()
	f.Add(raw)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		p, err := packet.Parse(b)
		if err != nil {
			return
		}
		out, err := p.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(b, out) {
			t.Fatal("noncanonical accepted frame")
		}
	})
}
func TestIPInspection(t *testing.T) {
	ip := make([]byte, 28)
	ip[0] = 0x45
	ip[9] = 17
	binary.BigEndian.PutUint16(ip[2:4], 28)
	copy(ip[12:20], []byte{10, 0, 0, 1, 10, 0, 0, 2})
	copy(ip[20:24], []byte{0, 80, 1, 187})
	first, err := packet.InspectIP(ip)
	if err != nil {
		t.Fatal(err)
	}
	if first.Source.String() != "10.0.0.1" || first.Destination.String() != "10.0.0.2" {
		t.Fatal(first)
	}
	ip[27] = 1
	second, err := packet.InspectIP(ip)
	if err != nil || first.Flow != second.Flow {
		t.Fatal("payload changed flow hash")
	}
	ip[20]++
	third, _ := packet.InspectIP(ip)
	if first.Flow == third.Flow {
		t.Fatal("ports did not change flow hash")
	}
	ip[0] = 0x4f
	if _, err := packet.InspectIP(ip); err == nil {
		t.Fatal("accepted invalid IHL")
	}
	v6 := make([]byte, 40)
	v6[0] = 0x60
	v6[6] = 59
	v6[23] = 1
	v6[39] = 2
	info, err := packet.InspectIP(v6)
	if err != nil || info.Destination.String() != "::2" {
		t.Fatalf("IPv6: %+v %v", info, err)
	}
}
func FuzzInspectIP(f *testing.F) {
	f.Add([]byte{0x45})
	f.Add(make([]byte, 40))
	f.Fuzz(func(t *testing.T, b []byte) {
		full, fullErr := packet.InspectIP(b)
		addresses, addressErr := packet.InspectAddresses(b)
		if (fullErr == nil) != (addressErr == nil) {
			t.Fatal("address-only validation differs")
		}
		if fullErr == nil && (full.Source != addresses.Source || full.Destination != addresses.Destination) {
			t.Fatal("address-only parse differs")
		}
	})
}
