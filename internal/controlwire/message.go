// Package controlwire implements the negotiated inner control WebSocket format.
package controlwire

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/eWloYW8/GraphWAN/internal/model"
)

// Subprotocol preserves JSON configuration/report messages but uses a one-byte
// binary heartbeat in both directions. A peer that declines it uses legacy JSON.
const Subprotocol = "graphwan.control.compact.v1"

func Compact(conn *websocket.Conn) bool { return conn.Subprotocol() == Subprotocol }

func Read(ctx context.Context, conn *websocket.Conn, message *model.ControlMessage) error {
	kind, raw, err := conn.Read(ctx)
	if err != nil {
		return err
	}
	return decode(kind, raw, Compact(conn), message)
}

func decode(kind websocket.MessageType, raw []byte, compact bool, message *model.ControlMessage) error {
	if compact && kind == websocket.MessageBinary {
		if len(raw) != 1 || raw[0] != 0 {
			return errors.New("invalid compact control heartbeat")
		}
		*message = model.ControlMessage{Type: "heartbeat"}
		return nil
	}
	return json.Unmarshal(raw, message)
}

func Write(ctx context.Context, conn *websocket.Conn, message model.ControlMessage) error {
	if Compact(conn) && message.Type == "heartbeat" {
		return conn.Write(ctx, websocket.MessageBinary, []byte{0})
	}
	return wsjson.Write(ctx, conn, message)
}
