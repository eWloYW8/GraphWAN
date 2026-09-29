package transport

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/pion/stun/v3"
)

func TestSTUNUsesSharedDataSocketAndRetries(t *testing.T) { testSTUNDataSocket(t, false) }
func TestSTUNWildcardBothFamilies(t *testing.T)           { testSTUNDataSocket(t, true) }
func testSTUNDataSocket(t *testing.T, wildcard bool) {
	for _, family := range []string{"udp4", "udp6"} {
		t.Run(family, func(t *testing.T) {
			host := "127.0.0.1:0"
			if family == "udp6" {
				host = "[::1]:0"
			}
			server, err := net.ListenPacket(family, host)
			if err != nil {
				if family == "udp6" {
					t.Skip(err)
				}
				t.Fatal(err)
			}
			defer server.Close()
			bind := host
			if wildcard {
				bind = ":0"
			}
			udp, _, _ := quicHubsAt(t, bind)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			type reply struct {
				address netip.AddrPort
				err     error
			}
			done := make(chan reply, 1)
			go func() {
				a, err := udp[0].STUNBinding(ctx, server.LocalAddr().(*net.UDPAddr).AddrPort())
				done <- reply{a, err}
			}()
			server.SetDeadline(time.Now().Add(3 * time.Second))
			buffer := make([]byte, 2048)
			n, source, err := server.ReadFrom(buffer)
			if err != nil {
				t.Fatal(err)
			}
			first := bytes.Clone(buffer[:n])
			if source.(*net.UDPAddr).Port != udp[0].LocalAddr().(*net.UDPAddr).Port || (!wildcard && source.String() != udp[0].LocalAddr().String()) {
				t.Fatal("STUN used another socket")
			}
			request := &stun.Message{Raw: bytes.Clone(first)}
			if request.Decode() != nil || request.Type != stun.BindingRequest || stun.Fingerprint.Check(request) != nil {
				t.Fatal("invalid binding request")
			}
			response := stun.MustBuild(stun.NewTransactionIDSetter(request.TransactionID), stun.BindingSuccess,
				&stun.XORMappedAddress{IP: source.(*net.UDPAddr).IP, Port: source.(*net.UDPAddr).Port}, stun.Fingerprint)
			// A matching ID from another source must not complete the transaction.
			spoof, err := net.ListenPacket(family, host)
			if err != nil {
				t.Fatal(err)
			}
			defer spoof.Close()
			spoof.WriteTo(response.Raw, source)
			// The correct source with an unrelated transaction must also be ignored.
			wrong := stun.MustBuild(stun.TransactionID, stun.BindingSuccess,
				&stun.XORMappedAddress{IP: source.(*net.UDPAddr).IP, Port: 1}, stun.Fingerprint)
			server.WriteTo(wrong.Raw, source)
			// Dropping the valid reply forces a retransmission of the same request.
			n, _, err = server.ReadFrom(buffer)
			if err != nil || !bytes.Equal(buffer[:n], first) {
				t.Fatal("missing exact retransmission:", err)
			}
			select {
			case got := <-done:
				t.Fatal("unsolicited response accepted:", got)
			default:
			}
			server.WriteTo(response.Raw, source)
			got := <-done
			if got.err != nil || got.address != source.(*net.UDPAddr).AddrPort() {
				t.Fatal(got)
			}
			if len(udp[0].peers) != 0 || len(udp[0].stunPending) != 0 {
				t.Fatal("STUN leaked a peer or transaction")
			}
			// Native peer traffic continues through the same multiplexed reader.
			target := udp[1].LocalAddr().(*net.UDPAddr).AddrPort()
			if wildcard {
				target = netip.AddrPortFrom(server.LocalAddr().(*net.UDPAddr).AddrPort().Addr(), target.Port())
			}
			a, err := udp[0].Dial(target)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			if err := a.Send(ctx, []byte("peer packet")); err != nil {
				t.Fatal(err)
			}
			b, err := udp[1].Accept(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			if raw, err := b.Receive(ctx); err != nil || string(raw) != "peer packet" {
				t.Fatal(err)
			}
		})
	}
}

func TestSTUNCancellationClosureAndBounds(t *testing.T) {
	hub, err := ListenUDP("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	server := netip.MustParseAddrPort("127.0.0.1:1")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := hub.STUNBinding(ctx, server); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if len(hub.stunPending) != 0 {
		t.Fatal("cancellation leaked a transaction")
	}
	hub.stunMu.Lock()
	for i := range maxSTUNTransactions {
		var id [12]byte
		id[0] = byte(i)
		hub.stunPending[id] = &stunTransaction{}
	}
	hub.stunMu.Unlock()
	if _, err := hub.STUNBinding(context.Background(), server); err == nil {
		t.Fatal("transaction limit ignored")
	}
	hub.stunMu.Lock()
	clear(hub.stunPending)
	hub.stunMu.Unlock()
	done := make(chan error, 1)
	go func() { _, err := hub.STUNBinding(context.Background(), server); done <- err }()
	hub.Close()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("binding blocked after close")
	}
}

func TestSTUNResponseValidation(t *testing.T) {
	// Independent IPv4 response vector from RFC 5769 section 2.2.
	vector, _ := hex.DecodeString("0101003c2112a442b7e7a701bc34d686fa87dfae8022000b7465737420766563746f7220002000080001a147e112a643000800142b91f599fd9e90c38c7489f92af9ba53f06be7d780280004c07d4c96")
	result, ok := parseSTUNResponse(vector, true)
	if !ok || result.err != nil || result.address.String() != "192.0.2.1:32853" {
		t.Fatal(result, ok)
	}
	vector[len(vector)-1] ^= 1
	if _, ok := parseSTUNResponse(vector, true); ok {
		t.Fatal("bad fingerprint accepted")
	}
	for _, ip := range []net.IP{net.IPv4zero, net.IPv4bcast, net.ParseIP("224.0.0.1"), net.ParseIP("::1")} {
		m := stun.MustBuild(stun.TransactionID, stun.BindingSuccess, &stun.XORMappedAddress{IP: ip, Port: 24752})
		if _, ok := parseSTUNResponse(m.Raw, true); ok {
			t.Fatal("invalid mapped address accepted:", ip)
		}
	}
	m := stun.MustBuild(stun.TransactionID, stun.BindingSuccess, &stun.XORMappedAddress{IP: net.IPv4(192, 0, 2, 1), Port: 24752}, stun.RawAttribute{Type: 0x1234, Value: []byte{}})
	if result, ok := parseSTUNResponse(m.Raw, true); !ok || result.err == nil {
		t.Fatal("unknown required attribute accepted")
	}
	m = stun.MustBuild(stun.TransactionID, stun.BindingError, stun.CodeUnauthorized)
	if result, ok := parseSTUNResponse(m.Raw, true); !ok || result.err == nil {
		t.Fatal("error response accepted")
	}
}

func FuzzSTUNResponse(f *testing.F) {
	m := stun.MustBuild(stun.TransactionID, stun.BindingSuccess, &stun.XORMappedAddress{IP: net.IPv4(192, 0, 2, 1), Port: 3478}, stun.Fingerprint)
	f.Add(m.Raw)
	f.Add([]byte("not STUN"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 2048 {
			return
		}
		result, ok := parseSTUNResponse(raw, true)
		if ok && result.err == nil && (!result.address.IsValid() || result.address.Port() == 0) {
			t.Fatal("invalid observation")
		}
	})
}
