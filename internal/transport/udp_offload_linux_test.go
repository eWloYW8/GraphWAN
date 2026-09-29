//go:build linux

package transport

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"
)

func joinedMessage(m ipv6.Message) []byte {
	var out []byte
	for _, part := range m.Buffers {
		out = append(out, part...)
	}
	return out
}
func messageSegments(t *testing.T, m ipv6.Message) [][]byte {
	t.Helper()
	control := bytes.Clone(m.OOB)
	// The receive parser can read the same native segment size after changing
	// the cmsg type from the send option to the receive option.
	for offset := 0; offset < len(control); {
		h, _, rest, err := unix.ParseOneSocketControlMessage(control[offset:])
		if err != nil {
			t.Fatal(err)
		}
		if h.Level == unix.SOL_UDP && h.Type == unix.UDP_SEGMENT {
			(*unix.Cmsghdr)(unsafe.Pointer(&control[offset])).Type = unix.UDP_GRO
		}
		offset = len(control) - len(rest)
	}
	size, err := udpGROSize(control)
	if err != nil {
		t.Fatal(err)
	}
	raw := joinedMessage(m)
	if size == 0 {
		return [][]byte{raw}
	}
	var out [][]byte
	for len(raw) > 0 {
		n := min(size, len(raw))
		out = append(out, bytes.Clone(raw[:n]))
		raw = raw[n:]
	}
	return out
}
func TestUDPGSOPreparePreservesDatagrams(t *testing.T) {
	for _, sizes := range [][]int{
		{1200, 1200, 50, 1200, 600, 900, 900},
		make([]int, 128),
		{16384, 16384, 16384, 16384, 16384},
	} {
		for _, gso := range []bool{false, true} {
			payloads := make([][]byte, len(sizes))
			var expected [][]byte
			b := &udpBatchSend{}
			copy(b.header[:], []byte("GWD\x01unique-peer-token"))
			for i, size := range sizes {
				if size == 0 {
					size = 1000
				}
				payloads[i] = bytes.Repeat([]byte{byte(i)}, size)
				expected = append(expected, append(bytes.Clone(b.header[:]), payloads[i]...))
			}
			reply := (&ipv4.ControlMessage{Src: net.IPv4(127, 0, 0, 2)}).Marshal()
			var actual [][]byte
			for _, m := range b.prepare(payloads, reply, gso) {
				if !bytes.HasPrefix(m.OOB, reply) {
					t.Fatal("reply source lost")
				}
				if len(joinedMessage(m)) > maxUDPSuperPacket {
					t.Fatal("oversized super-packet")
				}
				parts := messageSegments(t, m)
				if len(parts) > 64 {
					t.Fatal("too many segments")
				}
				actual = append(actual, parts...)
			}
			if len(actual) != len(expected) {
				t.Fatalf("got %d datagrams, want %d", len(actual), len(expected))
			}
			for i := range actual {
				if !bytes.Equal(actual[i], expected[i]) {
					t.Fatalf("datagram %d corrupted (GSO=%v)", i, gso)
				}
			}
		}
	}
}

type failingGSO struct {
	t            *testing.T
	calls        int
	packets      [][]byte
	remainingGSO bool
}

func (w *failingGSO) WriteBatch(messages []ipv6.Message, _ int) (int, error) {
	w.calls++
	if w.calls == 1 {
		if len(messages) < 2 {
			w.t.Fatal("test requires multiple super-packets")
		}
		w.packets = append(w.packets, messageSegments(w.t, messages[0])...)
		return 1, nil
	}
	if w.calls == 2 {
		return -1, unix.EMSGSIZE // x/net's result when GSO exceeds the path MTU.
	}
	for _, m := range messages {
		parts := messageSegments(w.t, m)
		if len(parts) != 1 {
			w.remainingGSO = true
		}
		w.packets = append(w.packets, parts...)
	}
	return len(messages), nil
}
func TestUDPGSOFallbackDoesNotRepeatSentPrefix(t *testing.T) {
	hub, err := ListenUDP("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	peer, err := hub.Dial(hub.LocalAddr().(*net.UDPAddr).AddrPort())
	if err != nil {
		t.Fatal(err)
	}
	writer := &failingGSO{t: t}
	hub.batchSockets[hub.sockets[0]] = &udpBatchSocket{conn: writer, gso: true}
	payloads := [][]byte{bytes.Repeat([]byte{1}, 100), bytes.Repeat([]byte{2}, 100), bytes.Repeat([]byte{3}, 200), bytes.Repeat([]byte{4}, 200)}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := peer.SendBatch(ctx, payloads); err != nil {
		t.Fatal(err)
	}
	if err := peer.SendBatch(ctx, payloads); err != nil {
		t.Fatal(err)
	}
	if writer.remainingGSO {
		t.Fatal("GSO was not disabled after fallback")
	}
	if len(writer.packets) != 2*len(payloads) {
		t.Fatalf("duplicate or missing datagrams: %d", len(writer.packets))
	}
	for i, p := range writer.packets {
		if !bytes.Equal(p[udpHeaderSize:], payloads[i%len(payloads)]) {
			t.Fatalf("packet %d corrupted", i)
		}
	}
}

func TestUDPGSORealWireCompatibility(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:0", "[::1]:0"} {
		t.Run(addr, func(t *testing.T) {
			server, err := net.ListenPacket("udp", addr)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			hub, err := ListenUDP(addr)
			if err != nil {
				t.Fatal(err)
			}
			defer hub.Close()
			peer, err := hub.Dial(server.LocalAddr().(*net.UDPAddr).AddrPort())
			if err != nil {
				t.Fatal(err)
			}
			payloads := make([][]byte, 32)
			for i := range payloads {
				payloads[i] = bytes.Repeat([]byte{byte(i)}, 1200)
			}
			payloads[31] = []byte("short final segment")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := peer.SendBatch(ctx, payloads); err != nil {
				t.Fatal(err)
			}
			server.SetReadDeadline(time.Now().Add(3 * time.Second))
			raw := make([]byte, 65535)
			for i, want := range payloads {
				n, _, err := server.ReadFrom(raw)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(raw[:4], udpMagic[:]) || !bytes.Equal(raw[udpHeaderSize:n], want) {
					t.Fatalf("packet %d boundaries corrupted", i)
				}
			}
		})
	}
}
func TestUDPGROSplitAndTruncation(t *testing.T) {
	control := appendUDPSegment(nil, 100)
	(*unix.Cmsghdr)(unsafe.Pointer(&control[0])).Type = unix.UDP_GRO
	for _, flags := range []int{0, unix.MSG_TRUNC, unix.MSG_CTRUNC} {
		raw := bytes.Repeat([]byte{9}, 250)
		r := &udpPacketReader{count: 1}
		r.messages[0] = ipv6.Message{Buffers: [][]byte{raw}, N: len(raw), OOB: control, NN: len(control), Flags: flags, Addr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234}}
		if flags != 0 {
			_, _, got, _, err := r.read()
			if err != nil || got != flags || len(r.segment) != 0 {
				t.Fatal("truncated super-packet was split")
			}
			continue
		}
		for _, size := range []int{100, 100, 50} {
			packet, oob, got, remote, err := r.read()
			if err != nil || got != 0 || len(packet) != size || remote.Port() != 1234 || !bytes.Equal(oob, control) {
				t.Fatal("bad GRO split")
			}
		}
		if len(r.segment) != 0 {
			t.Fatal("extra segment retained")
		}
	}
	for _, err := range []error{unix.EIO, unix.EMSGSIZE, unix.ENOPROTOOPT, unix.EINVAL, unix.EOPNOTSUPP} {
		if !udpGSOUnsupported(err) {
			t.Fatal(err)
		}
	}
	if udpGSOUnsupported(errors.New("other")) || udpGSOUnsupported(context.Canceled) {
		t.Fatal("unexpected retry")
	}
}
