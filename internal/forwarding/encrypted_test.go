package forwarding_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/eWloYW8/GraphWAN/internal/forwarding"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/peer"
	"github.com/eWloYW8/GraphWAN/internal/routing"
	"github.com/eWloYW8/GraphWAN/internal/secure"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
	"github.com/eWloYW8/GraphWAN/internal/transport"
)

func TestEncryptedThreeNodeForwarding(t *testing.T) {
	for _, suite := range []model.CipherSuite{model.AES128GCM, model.AES256GCM, model.ChaCha20Poly1305, model.XChaCha20Poly1305} {
		t.Run(string(suite), func(t *testing.T) { encryptedThreeNodeForwarding(t, suite) })
	}
}
func encryptedThreeNodeForwarding(t *testing.T, suite model.CipherSuite) {
	for _, kind := range []model.Transport{model.TCP, model.UDP} {
		t.Run(string(kind), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			state := testutil.Topology()
			state.Networks[0].Cipher = suite
			configs := map[model.ID]model.Snapshot{}
			keys := map[model.ID]ed25519.PrivateKey{}
			for i := range 3 {
				snapshot, err := routing.Compile(state, testutil.ID(10+i))
				if err != nil {
					t.Fatal(err)
				}
				node := testutil.ID(20 + i)
				configs[node] = snapshot
				seed := make([]byte, ed25519.SeedSize)
				seed[0] = byte(i + 1)
				keys[node] = ed25519.NewKeyFromSeed(seed)
			}
			type adjacency struct{ local, remote model.ID }
			channels := map[adjacency]*peer.Channel{}
			var cleanup []func()
			defer func() {
				for _, close := range cleanup {
					close()
				}
			}()
			for i := range 2 {
				a, b := testutil.ID(20+i), testutil.ID(21+i)
				policyA := secure.Config{Network: testutil.ID(1), Edge: testutil.ID(40 + i), Local: a, Peer: b, Transport: kind, Cipher: suite, Identity: keys[a], PeerIdentity: keys[b].Public().(ed25519.PublicKey)}
				policyB := policyA
				policyB.Local, policyB.Peer = b, a
				policyB.Identity = keys[b]
				policyB.PeerIdentity = keys[a].Public().(ed25519.PublicKey)
				var outbound transport.Conn
				var accept func() (transport.Conn, error)
				if kind == model.TCP {
					listener, err := net.Listen("tcp", "127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					cleanup = append(cleanup, func() { listener.Close() })
					conn, err := net.Dial("tcp", listener.Addr().String())
					if err != nil {
						t.Fatal(err)
					}
					outbound = transport.NewStream(conn)
					accept = func() (transport.Conn, error) {
						conn, err := listener.Accept()
						if err != nil {
							return nil, err
						}
						return transport.NewStream(conn), nil
					}
				} else {
					left, err := transport.ListenUDP("127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					right, err := transport.ListenUDP("127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					cleanup = append(cleanup, func() { left.Close(); right.Close() })
					outbound, err = left.Dial(right.LocalAddr().(*net.UDPAddr).AddrPort())
					if err != nil {
						t.Fatal(err)
					}
					accept = func() (transport.Conn, error) { return right.Accept(ctx) }
				}
				type accepted struct {
					channel *peer.Channel
					err     error
				}
				result := make(chan accepted, 1)
				go func() {
					raw, err := accept()
					if err != nil {
						result <- accepted{err: err}
						return
					}
					channel, err := peer.Accept(ctx, raw, kind, func(hello peer.Hello) (secure.Config, error) { return policyB, nil })
					result <- accepted{channel, err}
				}()
				channelA, err := peer.Dial(ctx, outbound, policyA)
				if err != nil {
					t.Fatal(err)
				}
				channelB := <-result
				if channelB.err != nil {
					t.Fatal(channelB.err)
				}
				channels[adjacency{a, b}], channels[adjacency{b, a}] = channelA, channelB.channel
				cleanup = append(cleanup, func() { channelA.Close(); channelB.channel.Close() })
			}
			type received struct {
				node   model.ID
				packet []byte
			}
			delivered := make(chan received, 4)
			routers := map[model.ID]*forwarding.Router{}
			for node, config := range configs {
				router, err := forwarding.New(config, func(ctx context.Context, network, next model.ID, frame []byte) error {
					channel := channels[adjacency{node, next}]
					if channel == nil {
						return fmt.Errorf("selected an unconnected edge %s -> %s", node, next)
					}
					return channel.Send(ctx, frame)
				}, func(ctx context.Context, network model.ID, raw []byte) error {
					delivered <- received{node: node, packet: bytes.Clone(raw)}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				routers[node] = router
			}
			var wg sync.WaitGroup
			failures := make(chan error, len(channels))
			for edge, channel := range channels {
				wg.Go(func() {
					for {
						raw, err := channel.Receive(ctx)
						if err != nil {
							if ctx.Err() == nil {
								failures <- err
							}
							return
						}
						if err := routers[edge.local].FromPeer(ctx, edge.remote, raw); err != nil {
							failures <- err
							return
						}
					}
				})
			}
			defer func() {
				cancel()
				for _, channel := range channels {
					channel.Close()
				}
				wg.Wait()
			}()
			for _, direction := range [][2]int{{0, 2}, {2, 0}} {
				raw := ipPacket(byte(direction[0]+1), byte(direction[1]+1))
				if err := routers[testutil.ID(20+direction[0])].FromTunnel(ctx, testutil.ID(1), raw); err != nil {
					t.Fatal(err)
				}
				select {
				case got := <-delivered:
					if got.node != testutil.ID(20+direction[1]) || !bytes.Equal(got.packet, raw) {
						t.Fatal("wrong packet or recipient")
					}
				case err := <-failures:
					t.Fatal(err)
				case <-ctx.Done():
					t.Fatal("multi-hop forwarding timed out")
				}
			}
		})
	}
}
