package transport

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Stream adapts TCP, TLS and other net.Conn streams to complete messages. One
// reader and one writer may operate concurrently. Writes cannot interleave.
// An I/O error closes the stream because a partial frame cannot be resumed safely.
type Stream struct {
	conn   net.Conn
	sendMu sync.Mutex
	readMu sync.Mutex
}

func NewStream(conn net.Conn) *Stream  { return &Stream{conn: conn} }
func (s *Stream) LocalAddr() net.Addr  { return s.conn.LocalAddr() }
func (s *Stream) RemoteAddr() net.Addr { return s.conn.RemoteAddr() }
func (s *Stream) Close() error         { return s.conn.Close() }
func (s *Stream) Send(ctx context.Context, data []byte) (err error) {
	if len(data) == 0 || len(data) > MaxMessage {
		return errors.New("invalid transport message size")
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	cleanup, err := deadline(ctx, s.conn.SetWriteDeadline)
	if err != nil {
		return err
	}
	defer cleanup()
	defer func() {
		if err != nil {
			s.conn.Close()
		}
	}()
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(data)))
	if err := writeFull(s.conn, prefix[:]); err != nil {
		return ctxError(ctx, err)
	}
	return ctxError(ctx, writeFull(s.conn, data))
}
func (s *Stream) Receive(ctx context.Context) (out []byte, err error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	cleanup, err := deadline(ctx, s.conn.SetReadDeadline)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	defer func() {
		if err != nil {
			s.conn.Close()
		}
	}()
	var prefix [4]byte
	if _, err := io.ReadFull(s.conn, prefix[:]); err != nil {
		return nil, ctxError(ctx, err)
	}
	size := binary.BigEndian.Uint32(prefix[:])
	if size == 0 || size > MaxMessage {
		return nil, fmt.Errorf("invalid transport frame size %d", size)
	}
	data := make([]byte, size)
	_, err = io.ReadFull(s.conn, data)
	return data, ctxError(ctx, err)
}

func deadline(ctx context.Context, set func(time.Time) error) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit, _ := ctx.Deadline()
	if err := set(limit); err != nil {
		return nil, err
	}
	finished := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { set(time.Now()); close(finished) })
	return func() {
		if !stop() {
			<-finished
		}
		set(time.Time{})
	}, nil
}
func ctxError(ctx context.Context, err error) error {
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
func writeFull(w io.Writer, buf []byte) error {
	for len(buf) > 0 {
		n, err := w.Write(buf)
		if n < 0 || n > len(buf) {
			return io.ErrShortWrite
		}
		buf = buf[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
