// Package peer authenticates a configured graph edge over a message transport.
package peer

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/graphwan/graphwan/internal/model"
	"github.com/graphwan/graphwan/internal/packetbuf"
	"github.com/graphwan/graphwan/internal/secure"
	"github.com/graphwan/graphwan/internal/transport"
)

const (
	helloKind          = 1
	replyKind          = 2
	finishKind         = 3
	readyKind          = 4
	dataKind           = 5
	helloHeader        = 67
	handshakeTimeout   = 10 * time.Second
	retransmitInterval = 250 * time.Millisecond
)

type Hello struct {
	Network, Edge, Initiator, Responder model.ID
	Transport                           model.Transport
}

var transports = []model.Transport{model.UDP, model.TCP, model.QUIC, model.WS, model.WSS, model.GRPC}

func (h Hello) marshal(noise []byte) ([]byte, error) {
	out := make([]byte, helloHeader, helloHeader+len(noise))
	out[0], out[1] = helloKind, 1
	for i, id := range []model.ID{h.Network, h.Edge, h.Initiator, h.Responder} {
		if err := id.Validate(); err != nil {
			return nil, err
		}
		hex.Decode(out[2+i*16:18+i*16], []byte(id))
	}
	found := false
	for i, t := range transports {
		if h.Transport == t {
			out[66] = byte(i)
			found = true
			break
		}
	}
	if !found {
		return nil, errors.New("unknown handshake transport")
	}
	return append(out, noise...), nil
}
func parseHello(raw []byte) (Hello, error) {
	var h Hello
	if len(raw) <= helloHeader || len(raw) > helloHeader+secure.MaxHandshake || raw[0] != helloKind || raw[1] != 1 || int(raw[66]) >= len(transports) {
		return h, errors.New("invalid peer hello")
	}
	for i, dest := range []*model.ID{&h.Network, &h.Edge, &h.Initiator, &h.Responder} {
		*dest = model.ID(hex.EncodeToString(raw[2+i*16 : 18+i*16]))
		if err := dest.Validate(); err != nil {
			return Hello{}, err
		}
	}
	h.Transport = transports[raw[66]]
	return h, nil
}

// Channel carries authenticated plaintext messages after its handshake. It owns
// the transport connection. A single Receive loop must remain active to service
// retransmitted UDP handshake messages as well as application traffic.
type Channel struct {
	conn           transport.Conn
	session        *secure.Session
	sendMu         sync.Mutex
	sendBuffer     []byte
	unreliable     bool
	repeatRequest  []byte
	repeatResponse []byte
	ignore         [][]byte
}

func (c *Channel) ID() string           { return c.session.ID() }
func (c *Channel) NeedsRekey() bool     { return c.session.NeedsRekey() }
func (c *Channel) Created() time.Time   { return c.session.Created() }
func (c *Channel) Close() error         { return c.conn.Close() }
func (c *Channel) LocalAddr() net.Addr  { return c.conn.LocalAddr() }
func (c *Channel) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }

// Resolve authorizes the exact incoming Network/Edge/Node tuple and returns its
// configured identities. Never construct that policy solely from the Hello.
type Resolve func(Hello) (secure.Config, error)

func Dial(parent context.Context, conn transport.Conn, config secure.Config) (channel *Channel, err error) {
	defer func() {
		if err != nil {
			conn.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(parent, handshakeTimeout)
	defer cancel()
	config.Initiator = true
	handshake, err := secure.NewHandshake(config)
	if err != nil {
		return nil, err
	}
	first, _, err := handshake.Write()
	if err != nil {
		return nil, err
	}
	hello := Hello{Network: config.Network, Edge: config.Edge, Initiator: config.Local, Responder: config.Peer, Transport: config.Transport}
	request, err := hello.marshal(first)
	if err != nil {
		return nil, err
	}
	unreliable := isDatagram(conn)
	reply, err := exchange(ctx, conn, unreliable, request, replyKind, nil)
	if err != nil {
		return nil, err
	}
	if _, err := handshake.Read(reply[1:]); err != nil {
		return nil, err
	}
	finish, session, err := handshake.Write()
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, errors.New("incomplete peer handshake")
	}
	finish = append([]byte{finishKind}, finish...)
	ready, err := exchange(ctx, conn, unreliable, finish, readyKind, reply)
	if err != nil {
		return nil, err
	}
	confirmation, err := session.Open(ready[1:])
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(confirmation, []byte{0}) {
		return nil, errors.New("invalid handshake confirmation")
	}
	markAuthenticated(conn)
	return &Channel{conn: conn, session: session, unreliable: unreliable, repeatRequest: reply, repeatResponse: finish, ignore: [][]byte{ready}}, nil
}

func Accept(parent context.Context, conn transport.Conn, kind model.Transport, resolve Resolve) (channel *Channel, err error) {
	defer func() {
		if err != nil {
			conn.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(parent, handshakeTimeout)
	defer cancel()
	first, err := conn.Receive(ctx)
	if err != nil {
		return nil, err
	}
	hello, err := parseHello(first)
	if err != nil {
		return nil, err
	}
	if hello.Transport != kind {
		return nil, errors.New("hello transport does not match listener")
	}
	config, err := resolve(hello)
	if err != nil {
		return nil, err
	}
	if config.Network != hello.Network || config.Edge != hello.Edge || config.Local != hello.Responder || config.Peer != hello.Initiator || config.Transport != kind {
		return nil, errors.New("resolved policy does not match hello")
	}
	config.Initiator = false
	handshake, err := secure.NewHandshake(config)
	if err != nil {
		return nil, err
	}
	if _, err := handshake.Read(first[helloHeader:]); err != nil {
		return nil, err
	}
	reply, _, err := handshake.Write()
	if err != nil {
		return nil, err
	}
	reply = append([]byte{replyKind}, reply...)
	unreliable := isDatagram(conn)
	finish, err := exchange(ctx, conn, unreliable, reply, finishKind, first)
	if err != nil {
		return nil, err
	}
	session, err := handshake.Read(finish[1:])
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, errors.New("incomplete peer handshake")
	}
	confirmation, err := session.Seal([]byte{0})
	if err != nil {
		return nil, err
	}
	ready := append([]byte{readyKind}, confirmation...)
	if err := conn.Send(ctx, ready); err != nil {
		return nil, err
	}
	markAuthenticated(conn)
	return &Channel{conn: conn, session: session, unreliable: unreliable, repeatRequest: finish, repeatResponse: ready, ignore: [][]byte{first}}, nil
}

func exchange(ctx context.Context, conn transport.Conn, unreliable bool, out []byte, want byte, duplicate []byte) ([]byte, error) {
	if err := conn.Send(ctx, out); err != nil {
		return nil, err
	}
	for {
		readCtx := ctx
		cancel := func() {}
		if unreliable {
			readCtx, cancel = context.WithTimeout(ctx, retransmitInterval)
		}
		raw, err := conn.Receive(readCtx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if unreliable && errors.Is(err, context.DeadlineExceeded) {
				if err := conn.Send(ctx, out); err != nil {
					return nil, err
				}
				continue
			}
			return nil, err
		}
		if len(raw) > 1 && len(raw) <= secure.MaxHandshake+1 && raw[0] == want {
			return raw, nil
		}
		if unreliable && bytes.Equal(raw, duplicate) {
			if err := conn.Send(ctx, out); err != nil {
				return nil, err
			}
			continue
		}
		if unreliable {
			continue
		}
		return nil, errors.New("unexpected peer handshake message")
	}
}
func isDatagram(conn transport.Conn) bool {
	d, ok := conn.(interface{ Unreliable() bool })
	return ok && d.Unreliable()
}
func markAuthenticated(conn transport.Conn) {
	if d, ok := conn.(interface{ Authenticated() }); ok {
		d.Authenticated()
	}
}
func markAlive(conn transport.Conn) {
	if d, ok := conn.(interface{ Alive() }); ok {
		d.Alive()
	}
}

func (c *Channel) Send(ctx context.Context, payload []byte) error {
	return c.SendBatch(ctx, [][]byte{payload})
}

// SendBatch encrypts each message with its own nonce, retaining compatibility
// with peers that read one message at a time. Transports without batching fall
// back to individual sends under the same bounded operation context.
func (c *Channel) SendBatch(ctx context.Context, payloads [][]byte) error {
	if len(payloads) == 0 || len(payloads) > 128 {
		return errors.New("invalid peer batch size")
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	needed := 0
	for _, payload := range payloads {
		if len(payload) == 0 || len(payload) > secure.MaxPlaintext {
			return errors.New("invalid peer message size")
		}
		needed += 1 + secure.Overhead + len(payload)
	}
	if cap(c.sendBuffer) < needed {
		c.sendBuffer = make([]byte, needed)
	}
	c.sendBuffer = c.sendBuffer[:0]
	var messages [128][]byte
	for i, payload := range payloads {
		offset := len(c.sendBuffer)
		c.sendBuffer = append(c.sendBuffer, dataKind)
		var err error
		c.sendBuffer, err = c.session.SealAppend(c.sendBuffer, payload)
		if err != nil {
			return err
		}
		messages[i] = c.sendBuffer[offset:]
	}
	if batch, ok := c.conn.(interface {
		SendBatch(context.Context, [][]byte) error
	}); ok {
		return batch.SendBatch(ctx, messages[:len(payloads)])
	}
	for _, message := range messages[:len(payloads)] {
		if err := c.conn.Send(ctx, message); err != nil {
			return err
		}
	}
	return nil
}
func (c *Channel) Receive(ctx context.Context) ([]byte, error) {
	for {
		raw, err := c.conn.Receive(ctx)
		if err != nil {
			return nil, err
		}
		plain, err := c.decode(ctx, raw)
		if err != nil || plain != nil {
			return plain, err
		}
	}
}
func (c *Channel) ReceiveBatch(ctx context.Context) ([][]byte, error) {
	batch, ok := c.conn.(interface {
		ReceiveBatch(context.Context) ([][]byte, error)
	})
	if !ok {
		raw, err := c.Receive(ctx)
		if err != nil {
			return nil, err
		}
		return [][]byte{raw}, nil
	}
	for {
		messages, err := batch.ReceiveBatch(ctx)
		if err != nil {
			return nil, err
		}
		allEncrypted := len(messages) > 1
		for _, raw := range messages {
			if len(raw) < 2 || raw[0] != dataKind {
				allEncrypted = false
				break
			}
		}
		if allEncrypted {
			for i := range messages {
				messages[i] = messages[i][1:]
			}
			errs := c.session.OpenBatchInPlace(messages)
			count := 0
			for i, err := range errs {
				if err != nil {
					if errors.Is(err, secure.ErrReplay) || c.unreliable && !errors.Is(err, secure.ErrRekey) {
						continue
					}
					return nil, err
				}
				messages[count] = messages[i]
				count++
			}
			clear(messages[count:])
			if count > 0 {
				markAlive(c.conn)
				return messages[:count], nil
			}
			continue
		}
		count := 0
		for _, raw := range messages {
			plain, err := c.decode(ctx, raw)
			if err != nil {
				return nil, err
			}
			if plain != nil {
				messages[count] = plain
				count++
			}
		}
		clear(messages[count:])
		if count > 0 {
			return messages[:count], nil
		}
	}
}
func (c *Channel) decode(ctx context.Context, raw []byte) ([]byte, error) {
	if len(raw) > 1 && raw[0] == dataKind {
		plaintext, err := c.session.OpenInPlace(raw[1:])
		if err != nil {
			if errors.Is(err, secure.ErrReplay) || c.unreliable && !errors.Is(err, secure.ErrRekey) {
				return nil, nil
			}
			return nil, err
		}
		markAlive(c.conn)
		return plaintext, nil
	}
	if c.unreliable {
		if bytes.Equal(raw, c.repeatRequest) {
			if err := c.conn.Send(ctx, c.repeatResponse); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	for _, message := range c.ignore {
		if bytes.Equal(raw, message) {
			return nil, nil
		}
	}
	return nil, fmt.Errorf("unexpected peer channel message")
}

// ReceiveOwnedBatch carries storage ownership through authentication and into
// forwarding. Rejected/control frames are released here; accepted buffers remain
// owned by the caller until delivery (or queue drop) completes.
func (c *Channel) ReceiveOwnedBatch(ctx context.Context) ([]*packetbuf.Buffer, error) {
	for {
		var buffers []*packetbuf.Buffer
		var err error
		if receiver, ok := c.conn.(interface {
			ReceiveOwnedBatch(context.Context) ([]*packetbuf.Buffer, error)
		}); ok {
			buffers, err = receiver.ReceiveOwnedBatch(ctx)
		} else {
			var raw []byte
			raw, err = c.conn.Receive(ctx)
			if err == nil {
				buffers = []*packetbuf.Buffer{packetbuf.Wrap(raw)}
			}
		}
		if err != nil {
			packetbuf.ReleaseAll(buffers)
			return nil, err
		}
		allEncrypted := len(buffers) > 1
		for _, b := range buffers {
			if len(b.Data) < 2 || b.Data[0] != dataKind {
				allEncrypted = false
				break
			}
		}
		if allEncrypted {
			frames := make([][]byte, len(buffers))
			for i, b := range buffers {
				frames[i] = b.Data[1:]
			}
			errs := c.session.OpenBatchInPlace(frames)
			for i, decodeErr := range errs {
				buffers[i].Data = frames[i]
				if decodeErr != nil && !errors.Is(decodeErr, secure.ErrReplay) && !(c.unreliable && !errors.Is(decodeErr, secure.ErrRekey)) {
					err = decodeErr
				}
			}
		} else {
			for _, b := range buffers {
				b.Data, err = c.decode(ctx, b.Data)
				if err != nil {
					break
				}
			}
		}
		if err != nil {
			packetbuf.ReleaseAll(buffers)
			return nil, err
		}
		count := 0
		for _, b := range buffers {
			if b.Data == nil {
				b.Release()
				continue
			}
			buffers[count] = b
			count++
		}
		clear(buffers[count:])
		if count > 0 {
			markAlive(c.conn)
			return buffers[:count], nil
		}
	}
}
