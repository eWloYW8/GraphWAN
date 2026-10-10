package peer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/secure"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
	"github.com/eWloYW8/GraphWAN/internal/transport"
)

// The receiver exposes only the original single-message transport API.
type legacyChannelTransport struct{ transport.Conn }

type framedChannelTransport struct {
	*transport.Stream
	framed atomic.Int32
}

func (c *framedChannelTransport) SendFramedBatch(ctx context.Context, data []byte) error {
	c.framed.Add(1)
	return c.Stream.SendFramedBatch(ctx, data)
}

func TestPacketChannelBatchWithLegacyReceiver(t *testing.T) {
	for _, cipher := range []model.CipherSuite{model.AES128GCM, model.AES256GCM, model.ChaCha20Poly1305, model.XChaCha20Poly1305} {
		for _, contiguous := range []bool{false, true} {
			name := string(cipher) + "/legacy"
			if contiguous {
				name = string(cipher) + "/contiguous"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				seedA, seedB := make([]byte, ed25519.SeedSize), make([]byte, ed25519.SeedSize)
				seedA[0], seedB[0] = 1, 2
				keyA, keyB := ed25519.NewKeyFromSeed(seedA), ed25519.NewKeyFromSeed(seedB)
				a := secure.Config{Network: testutil.ID(1), Edge: testutil.ID(2), Local: testutil.ID(3), Peer: testutil.ID(4), Transport: model.TCP, Cipher: cipher, Identity: keyA, PeerIdentity: keyB.Public().(ed25519.PublicKey)}
				b := a
				b.Local, b.Peer, b.Identity, b.PeerIdentity = a.Peer, a.Local, keyB, keyA.Public().(ed25519.PublicKey)
				x, y := net.Pipe()
				defer x.Close()
				defer y.Close()
				framed := &framedChannelTransport{Stream: transport.NewStream(x)}
				var outgoing transport.Conn = framed
				if !contiguous {
					outgoing = legacyChannelTransport{Conn: framed}
				}
				type result struct {
					channel *Channel
					err     error
				}
				accepted := make(chan result, 1)
				go func() {
					channel, err := Accept(ctx, legacyChannelTransport{Conn: transport.NewStream(y)}, model.TCP, func(Hello) (secure.Config, error) { return b, nil })
					accepted <- result{channel, err}
				}()
				sender, err := Dial(ctx, outgoing, a)
				if err != nil {
					t.Fatal(err)
				}
				defer sender.Close()
				incoming := <-accepted
				if incoming.err != nil {
					t.Fatal(incoming.err)
				}
				defer incoming.channel.Close()
				framed.framed.Store(0)
				for _, count := range []int{1, 32, 128} {
					payloads := make([][]byte, count)
					originals := make([][]byte, count)
					for i := range payloads {
						size := 1337
						if i == 0 {
							size = 9 // Control messages share the packet channel.
						} else if i == count-1 {
							size = secure.MaxPlaintext
						}
						payloads[i] = bytes.Repeat([]byte{byte(i + 1)}, size)
						originals[i] = bytes.Clone(payloads[i])
					}
					done := make(chan error, 1)
					go func() { done <- sender.SendBatch(ctx, payloads) }()
					for _, want := range originals {
						got, err := incoming.channel.Receive(ctx)
						if err != nil || !bytes.Equal(got, want) {
							t.Fatal("legacy receiver lost an independently authenticated message", len(got), err)
						}
					}
					if err := <-done; err != nil {
						t.Fatal(err)
					}
					for i, want := range originals {
						if !bytes.Equal(payloads[i], want) {
							t.Fatal("batch send changed caller-owned plaintext")
						}
					}
				}
				wantCalls := int32(0)
				if contiguous {
					wantCalls = 3
				}
				if framed.framed.Load() != wantCalls {
					t.Fatal("incorrect framed transport selection", framed.framed.Load())
				}
			})
		}
	}
}
