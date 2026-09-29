package transport

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/packetbuf"
)

// Stream adapts TCP, TLS and other net.Conn streams to complete messages. One
// reader and one writer may operate concurrently. Writes cannot interleave.
// An I/O error closes the stream because a partial frame cannot be resumed safely.
type Stream struct {
	conn        net.Conn
	sendMu      sync.Mutex
	readMu      sync.Mutex
	reader      *bufio.Reader
	writeBuffer []byte
}

func NewStream(conn net.Conn) *Stream {
	return &Stream{conn: conn, reader: bufio.NewReaderSize(conn, 64*1024)}
}
func (s *Stream) LocalAddr() net.Addr  { return s.conn.LocalAddr() }
func (s *Stream) RemoteAddr() net.Addr { return s.conn.RemoteAddr() }
func (s *Stream) Close() error         { return s.conn.Close() }

// SendBatch preserves the existing per-message wire framing while amortizing
// socket writes and cancellation bookkeeping over immediately available packets.
func (s *Stream) Send(ctx context.Context, data []byte) error {
	return s.SendBatch(ctx, [][]byte{data})
}
func (s *Stream) SendBatch(ctx context.Context, messages [][]byte) (err error) {
	if len(messages) == 0 || len(messages) > 128 {
		return errors.New("invalid transport batch size")
	}
	size := 0
	for _, data := range messages {
		if len(data) == 0 || len(data) > MaxMessage {
			return errors.New("invalid transport message size")
		}
		size += 4 + len(data)
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
	if cap(s.writeBuffer) < size {
		s.writeBuffer = make([]byte, size)
	}
	frame := s.writeBuffer[:size]
	offset := 0
	for _, data := range messages {
		binary.BigEndian.PutUint32(frame[offset:], uint32(len(data)))
		copy(frame[offset+4:], data)
		offset += 4 + len(data)
	}
	return ctxError(ctx, writeFull(s.conn, frame))
}
func (s *Stream) Receive(ctx context.Context) ([]byte, error) {
	messages, err := s.receive(ctx, 1)
	if err != nil {
		return nil, err
	}
	defer packetbuf.ReleaseAll(messages)
	return append([]byte(nil), messages[0].Data...), nil
}
func (s *Stream) ReceiveBatch(ctx context.Context) ([][]byte, error) {
	messages, err := s.ReceiveOwnedBatch(ctx)
	if err != nil {
		return nil, err
	}
	defer packetbuf.ReleaseAll(messages)
	out := make([][]byte, len(messages))
	for i, b := range messages {
		out[i] = append([]byte(nil), b.Data...)
	}
	return out, nil
}
func (s *Stream) ReceiveOwnedBatch(ctx context.Context) ([]*packetbuf.Buffer, error) {
	return s.receive(ctx, 32)
}
func (s *Stream) receive(ctx context.Context, limit int) (out []*packetbuf.Buffer, err error) {
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
	out = make([]*packetbuf.Buffer, 0, limit)
	// Use a separate owner list because explicit return nil replaces named out.
	owned := out
	defer func() {
		if err != nil {
			packetbuf.ReleaseAll(owned)
		}
	}()
	for len(out) < limit {
		// After the first frame, consume only complete frames already buffered.
		// A quiet connection never waits to fill a batch or a partial next frame.
		if len(out) > 0 {
			if s.reader.Buffered() < 4 {
				break
			}
			prefix, _ := s.reader.Peek(4)
			size := binary.BigEndian.Uint32(prefix)
			if size == 0 || size > MaxMessage {
				return nil, fmt.Errorf("invalid transport frame size %d", size)
			}
			if s.reader.Buffered() < 4+int(size) {
				break
			}
		}
		prefix, err := s.reader.Peek(4)
		if err != nil {
			return nil, ctxError(ctx, err)
		}
		size := binary.BigEndian.Uint32(prefix)
		s.reader.Discard(4)
		if size == 0 || size > MaxMessage {
			return nil, fmt.Errorf("invalid transport frame size %d", size)
		}
		data := packetbuf.Get(int(size))
		out = append(out, data)
		owned = out
		if _, err := io.ReadFull(s.reader, data.Data); err != nil {
			return nil, ctxError(ctx, err)
		}
	}
	return out, nil
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
	if err != nil {
		if cause := ctx.Err(); cause != nil {
			return cause
		}
		// The socket deadline can fire before the context's timer goroutine
		// runs. Preserve the context error contract in that scheduling window.
		var timeout net.Error
		if limit, ok := ctx.Deadline(); ok && !time.Now().Before(limit) && errors.As(err, &timeout) && timeout.Timeout() {
			return context.DeadlineExceeded
		}
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
