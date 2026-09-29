package transport

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/pion/stun/v3"
)

func readTCPBinding(t *testing.T, conn net.Conn) *stun.Message {
	t.Helper()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	var header [20]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 20+int(binary.BigEndian.Uint16(header[2:4])))
	copy(raw, header[:])
	if _, err := io.ReadFull(conn, raw[20:]); err != nil {
		t.Fatal(err)
	}
	request := &stun.Message{Raw: raw}
	if request.Decode() != nil || request.Type != stun.BindingRequest || stun.Fingerprint.Check(request) != nil {
		t.Fatal("invalid request")
	}
	return request
}

func TestTCPSTUNPersistentDataPortBothFamilies(t *testing.T) {
	testTCPSTUNDataPort(t, false)
}
func TestTCPSTUNWildcardDataPortBothFamilies(t *testing.T) {
	testTCPSTUNDataPort(t, true)
}
func testTCPSTUNDataPort(t *testing.T, wildcard bool) {
	for _, host := range []string{"127.0.0.1:0", "[::1]:0"} {
		t.Run(host, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			server, err := net.Listen("tcp", host)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			bind := host
			if wildcard {
				bind = ":0"
			}
			hub, err := ListenTCP(ctx, bind)
			if err != nil {
				t.Fatal(err)
			}
			defer hub.Close()
			type result struct {
				address netip.AddrPort
				err     error
			}
			done := make(chan result, 1)
			query := func() { a, e := hub.STUNBinding(ctx, server.Addr().(*net.TCPAddr).AddrPort()); done <- result{a, e} }
			go query()
			server.(*net.TCPListener).SetDeadline(time.Now().Add(3 * time.Second))
			conn, err := server.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			source := conn.RemoteAddr().(*net.TCPAddr)
			if source.Port != hub.Addr().(*net.TCPAddr).Port || (!wildcard && source.String() != hub.Addr().String()) {
				t.Fatal("observation did not use the peer data port")
			}
			var previous [12]byte
			for i := range 3 {
				if i > 0 {
					go query()
				}
				request := readTCPBinding(t, conn)
				if request.TransactionID == previous {
					t.Fatal("reused transaction ID")
				}
				previous = request.TransactionID
				response := stun.MustBuild(stun.NewTransactionIDSetter(request.TransactionID), stun.BindingSuccess, &stun.XORMappedAddress{IP: source.IP, Port: source.Port}, stun.Fingerprint)
				// TCP framing must tolerate a response split anywhere in its header/body.
				for _, part := range [][]byte{response.Raw[:7], response.Raw[7:23], response.Raw[23:]} {
					if err := writeFull(conn, part); err != nil {
						t.Fatal(err)
					}
				}
				got := <-done
				if got.err != nil || got.address != source.AddrPort() {
					t.Fatal(got)
				}
			}
			// All three observations were read on this same persistent connection.
		})
	}
}

func TestTCPSTUNRejectsMalformedResponsesAndReleasesConnection(t *testing.T) {
	for _, kind := range []string{"transaction", "oversize", "length", "fingerprint", "error", "eof"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			server, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			hub, err := ListenTCP(ctx, "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer hub.Close()
			done := make(chan error, 1)
			go func() { _, err := hub.STUNBinding(ctx, server.Addr().(*net.TCPAddr).AddrPort()); done <- err }()
			server.(*net.TCPListener).SetDeadline(time.Now().Add(3 * time.Second))
			conn, err := server.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			request := readTCPBinding(t, conn)
			response := stun.MustBuild(stun.NewTransactionIDSetter(request.TransactionID), stun.BindingSuccess, &stun.XORMappedAddress{IP: net.IPv4(192, 0, 2, 1), Port: 24752}, stun.Fingerprint)
			switch kind {
			case "transaction":
				response.Raw[8] ^= 1
			case "oversize":
				binary.BigEndian.PutUint16(response.Raw[2:4], 2048)
			case "length":
				binary.BigEndian.PutUint16(response.Raw[2:4], 1)
			case "fingerprint":
				response.Raw[len(response.Raw)-1] ^= 1
			case "error":
				response = stun.MustBuild(stun.NewTransactionIDSetter(request.TransactionID), stun.BindingError, stun.CodeUnauthorized)
			case "eof":
				response.Raw = response.Raw[:8]
			}
			writeFull(conn, response.Raw)
			conn.Close()
			if err := <-done; err == nil {
				t.Fatal("invalid response accepted")
			}
			hub.mu.Lock()
			remaining := len(hub.bindings)
			hub.mu.Unlock()
			if remaining != 0 {
				t.Fatal("failed observation leaked a connection")
			}
		})
	}
}

func TestTCPSTUNCancellationBusyAndShutdown(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		server, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { server.Close() })
		hub, err := ListenTCP(ctx, "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { hub.Close() })
		t.Cleanup(cancel)
		done := make(chan error, 1)
		address := server.Addr().(*net.TCPAddr).AddrPort()
		go func() { _, err := hub.STUNBinding(ctx, address); done <- err }()
		server.(*net.TCPListener).SetDeadline(time.Now().Add(3 * time.Second))
		conn, err := server.Accept()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		readTCPBinding(t, conn)
		if _, err := hub.STUNBinding(ctx, address); err == nil {
			t.Fatal("concurrent transaction on same connection accepted")
		}
		if shutdown {
			hub.Close()
		} else {
			cancel()
		}
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("interrupted observation succeeded")
			}
		case <-time.After(time.Second):
			t.Fatal("interrupted observation blocked")
		}
		hub.mu.Lock()
		remaining := len(hub.bindings)
		hub.mu.Unlock()
		if remaining != 0 {
			t.Fatal("interrupted observation leaked")
		}
	}
}

func TestTCPSTUNConnectionLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hub, err := ListenTCP(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	done := make(chan error, maxSTUNTransactions)
	for range maxSTUNTransactions {
		server, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer server.Close()
		go func() { _, err := hub.STUNBinding(ctx, server.Addr().(*net.TCPAddr).AddrPort()); done <- err }()
		server.(*net.TCPListener).SetDeadline(time.Now().Add(time.Second))
		conn, err := server.Accept()
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		readTCPBinding(t, conn)
	}
	unused, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer unused.Close()
	if _, err := hub.STUNBinding(ctx, unused.Addr().(*net.TCPAddr).AddrPort()); err == nil || err.Error() != "TCP STUN connection limit reached" {
		t.Fatal("connection bound ignored:", err)
	}
	hub.Close()
	for range maxSTUNTransactions {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("unfinished request succeeded")
			}
		case <-time.After(time.Second):
			t.Fatal("shutdown leaked a binding operation")
		}
	}
}
