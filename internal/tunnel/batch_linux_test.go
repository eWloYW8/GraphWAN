//go:build linux && integration

package tunnel_test

import (
	"bytes"
	"io"
	"net"
	"net/netip"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/graphwan/graphwan/internal/tunnel"
)

// Reflect a TCP connection through the real TUN: exchanging source/destination
// IPs preserves both the IP and transport checksums. Each socket sees the other
// endpoint at the virtual remote address. Large writes exercise kernel GSO,
// userspace segmentation, GRO and real kernel checksum validation in both ways.
func TestNativeLinuxBatchTCP(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("GRAPHWAN_TEST_NETNS") != "1" {
		t.Skip("requires isolated network namespace")
	}
	own, _ := os.Readlink("/proc/self/ns/net")
	init, _ := os.Readlink("/proc/1/ns/net")
	if own == init {
		t.Fatal("requires isolated network namespace")
	}
	for _, addresses := range [][2]string{{"10.240.32.1/24", "10.240.32.2"}, {"fd42:6778::1/64", "fd42:6778::2"}} {
		t.Run(addresses[0], func(t *testing.T) {
			device, err := tunnel.Open(tunnel.Config{Name: "gw-batch", Address: netip.MustParsePrefix(addresses[0]), MTU: 1280})
			if err != nil {
				t.Fatal(err)
			}
			defer device.Close()
			batch := device.(tunnel.BatchDevice)
			var maxBatch atomic.Int32
			stopped := make(chan error, 1)
			go func() {
				bufs := make([][]byte, batch.BatchSize())
				sizes := make([]int, len(bufs))
				for i := range bufs {
					bufs[i] = make([]byte, 9001)
				}
				for {
					n, err := batch.ReadBatch(bufs, sizes)
					if err != nil {
						stopped <- err
						return
					}
					for {
						old := maxBatch.Load()
						if old >= int32(n) || maxBatch.CompareAndSwap(old, int32(n)) {
							break
						}
					}
					packets := make([][]byte, 0, n)
					for i := range n {
						raw := bufs[i][:sizes[i]]
						if raw[0]>>4 == 4 && raw[9] == 6 {
							var src [4]byte
							copy(src[:], raw[12:16])
							copy(raw[12:16], raw[16:20])
							copy(raw[16:20], src[:])
						} else if raw[0]>>4 == 6 && raw[6] == 6 {
							var src [16]byte
							copy(src[:], raw[8:24])
							copy(raw[8:24], raw[24:40])
							copy(raw[24:40], src[:])
						} else {
							continue
						}
						packets = append(packets, raw)
					}
					if err := batch.WriteBatch(packets); err != nil {
						stopped <- err
						return
					}
				}
			}()
			listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IP(device.Configuration().Address.Addr().AsSlice())})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			payload := bytes.Repeat([]byte("batch-checksum-and-order"), 200000)
			server := make(chan error, 1)
			listener.SetDeadline(time.Now().Add(15 * time.Second))
			go func() {
				conn, err := listener.AcceptTCP()
				if err != nil {
					server <- err
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(15 * time.Second))
				_, err = io.Copy(conn, conn)
				server <- err
			}()
			conn, err := net.DialTimeout("tcp", net.JoinHostPort(addresses[1], strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)), 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(15 * time.Second))
			written := make(chan error, 1)
			go func() {
				_, err := conn.Write(payload)
				if err == nil {
					err = conn.(*net.TCPConn).CloseWrite()
				}
				written <- err
			}()
			got := make([]byte, len(payload))
			_, err = io.ReadFull(conn, got)
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatal("TCP payload/checksum corruption", err)
			}
			if err := <-written; err != nil {
				t.Fatal(err)
			}
			if err := <-server; err != nil {
				t.Fatal(err)
			}
			if maxBatch.Load() < 2 {
				t.Fatal("kernel GSO was not exercised")
			}
			device.Close()
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("batch read ignored Close")
			}
		})
	}
}
