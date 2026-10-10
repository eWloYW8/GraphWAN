package mesh

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"testing"
)

func TestBufferedConnGatherPreservesPeekedBytes(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	reader := bufio.NewReader(a)
	conn := &bufferedConn{Conn: a, reader: reader}
	done := make(chan error, 1)
	go func() { _, err := b.Write([]byte("admission")); done <- err }()
	if peek, err := reader.Peek(1); err != nil || peek[0] != 'a' {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	go func() { _, err := conn.WriteBuffers(net.Buffers{[]byte("first"), []byte("last")}); done <- err }()
	got := make([]byte, len("firstlast"))
	if _, err := io.ReadFull(b, got); err != nil || !bytes.Equal(got, []byte("firstlast")) {
		t.Fatal(string(got), err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got = make([]byte, len("admission"))
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != "admission" {
		t.Fatal("gather write lost read-ahead ownership", string(got), err)
	}
}
