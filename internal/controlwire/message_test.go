package controlwire

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/eWloYW8/GraphWAN/internal/model"
)

func TestHeartbeatCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name           string
		client, server bool
	}{
		{"compact", true, true},
		{"old server", true, false},
		{"old agent", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			protocols := func(enabled bool) []string {
				if enabled {
					return []string{Subprotocol}
				}
				return nil
			}
			compact := tc.client && tc.server
			check := func(conn *websocket.Conn) error {
				kind, raw, err := conn.Read(ctx)
				if err != nil {
					return err
				}
				if compact && (kind != websocket.MessageBinary || len(raw) != 1 || raw[0] != 0) {
					return fmt.Errorf("expected one-byte heartbeat, got %d %q", kind, raw)
				}
				if !compact && kind != websocket.MessageText {
					return fmt.Errorf("legacy connection received binary heartbeat")
				}
				var message model.ControlMessage
				if err := decode(kind, raw, compact, &message); err != nil {
					return err
				}
				if message.Type != "heartbeat" {
					return fmt.Errorf("unexpected message %q", message.Type)
				}
				return nil
			}
			done := make(chan error, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: protocols(tc.server)})
				if err != nil {
					done <- err
					return
				}
				defer conn.CloseNow()
				if err = Write(ctx, conn, model.ControlMessage{Type: "heartbeat"}); err == nil {
					err = check(conn)
				}
				done <- err
			}))
			defer srv.Close()
			conn, _, err := websocket.Dial(ctx, srv.URL, &websocket.DialOptions{Subprotocols: protocols(tc.client)})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			if Compact(conn) != compact {
				t.Fatal("incorrect subprotocol negotiation")
			}
			if err = check(conn); err != nil {
				t.Fatal(err)
			}
			if err = Write(ctx, conn, model.ControlMessage{Type: "heartbeat"}); err != nil {
				t.Fatal(err)
			}
			if err = <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
	var message model.ControlMessage
	if err := decode(websocket.MessageText, []byte(`{"type":"ack","report":{"applied_revision":7}}`), true, &message); err != nil || message.Report == nil || message.Report.AppliedRevision != 7 {
		t.Fatal("compact protocol did not preserve JSON reports")
	}
	if err := decode(websocket.MessageBinary, []byte{0}, true, &message); err != nil || message.Report != nil || message.Type != "heartbeat" {
		t.Fatal("heartbeat retained report/ACK fields")
	}
	for _, raw := range [][]byte{nil, {1}, {0, 0}} {
		if err := decode(websocket.MessageBinary, raw, true, &message); err == nil {
			t.Fatalf("accepted malformed heartbeat %x", raw)
		}
	}
	if err := decode(websocket.MessageBinary, []byte{0}, false, &message); err == nil {
		t.Fatal("accepted unnegotiated compact heartbeat")
	}
}
