package forwarding_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"testing"

	"github.com/eWloYW8/GraphWAN/internal/forwarding"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/packet"
	"github.com/eWloYW8/GraphWAN/internal/routing"
	"github.com/eWloYW8/GraphWAN/internal/testutil"
)

func sizedIP(source, destination netip.Addr, size int) []byte {
	raw := bytes.Repeat([]byte{42}, size)
	if source.Is4() {
		clear(raw[:20])
		raw[0], raw[8], raw[9] = 0x45, 64, 253 // Experimental upper-layer protocol.
		binary.BigEndian.PutUint16(raw[2:4], uint16(size))
		copy(raw[12:16], source.AsSlice())
		copy(raw[16:20], destination.AsSlice())
	} else {
		clear(raw[:40])
		raw[0], raw[6], raw[7] = 0x60, 253, 64
		binary.BigEndian.PutUint16(raw[4:6], uint16(size-40))
		copy(raw[8:24], source.AsSlice())
		copy(raw[24:40], destination.AsSlice())
	}
	return raw
}

func TestForwardingMTUBoundaries(t *testing.T) {
	for _, family := range []int{4, 6} {
		for _, mtu := range []int{1280, 1500, 9000} {
			t.Run(fmt.Sprintf("IPv%d/MTU%d", family, mtu), func(t *testing.T) {
				state := testutil.Topology()
				network := &state.Networks[0]
				network.MTU = mtu
				if family == 6 {
					network.CIDR = netip.MustParsePrefix("fd42::/64")
					for i := range network.Nodes {
						network.Nodes[i].Address = netip.MustParseAddr(fmt.Sprintf("fd42::%d", i+1))
					}
				}
				a, b, c := network.Nodes[0], network.Nodes[1], network.Nodes[2]
				config, err := routing.Compile(state, b.AgentID)
				if err != nil {
					t.Fatal(err)
				}
				var sent, delivered []byte
				router, err := forwarding.New(config, func(_ context.Context, _, next model.ID, raw []byte) error {
					if next != c.ID {
						t.Fatal("unexpected next hop", next)
					}
					sent = bytes.Clone(raw)
					return nil
				}, func(_ context.Context, _ model.ID, raw []byte) error {
					delivered = bytes.Clone(raw)
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				for _, origin := range []string{"local", "peer"} {
					for _, destination := range []model.Node{b, c} {
						for _, size := range []int{mtu, mtu + 1, mtu} {
							sent, delivered = nil, nil
							source := b
							if origin == "peer" {
								source = a
							}
							raw := sizedIP(source.Address, destination.Address, size)
							if origin == "local" {
								err = router.FromTunnel(t.Context(), network.ID, raw)
							} else {
								p := packet.Packet{Header: packet.Header{Network: network.ID, Source: a.ID, Destination: destination.ID, HopLimit: 32}, Payload: raw[:min(size, packet.MaxPayload)]}
								frame, frameErr := p.MarshalBinary()
								if frameErr != nil {
									t.Fatal(frameErr)
								}
								// Deliberately exceed the global wire limit too. An
								// authenticated peer need not use our local encoder.
								if size > packet.MaxPayload {
									frame = append(frame, raw[packet.MaxPayload:]...)
									binary.BigEndian.PutUint16(frame[4:6], uint16(size))
								}
								err = router.FromPeer(t.Context(), a.ID, frame)
							}
							if size > mtu {
								if err == nil || sent != nil || delivered != nil {
									t.Fatalf("%s oversized packet escaped admission: %v", origin, err)
								}
								if (origin == "local" || size <= packet.MaxPayload) && !errors.Is(err, forwarding.ErrMTU) {
									t.Fatalf("wrong MTU rejection: %v", err)
								}
								continue
							}
							if err != nil {
								t.Fatal(err)
							}
							if destination.ID == b.ID {
								if !bytes.Equal(delivered, raw) || sent != nil {
									t.Fatal("MTU-sized local delivery was corrupted or forwarded")
								}
							} else {
								p, parseErr := packet.Parse(sent)
								if parseErr != nil || !bytes.Equal(p.Payload, raw) || delivered != nil {
									t.Fatal("MTU-sized transit was corrupted or locally delivered", parseErr)
								}
							}
						}
					}
				}
			})
		}
	}
}

func TestMixedRoutingEpochLoopExhaustsHopLimit(t *testing.T) {
	state := testutil.Topology()
	network := &state.Networks[0]
	network.Edges = nil
	// A/B/C form a triangle; each can reach D. E originates traffic through A.
	for i, pair := range [][2]int{{0, 1}, {1, 2}, {2, 0}, {0, 3}, {1, 3}, {2, 3}, {0, 4}} {
		network.Edges = append(network.Edges, model.Edge{ID: testutil.ID(40 + i), A: network.Nodes[pair[0]].ID, B: network.Nodes[pair[1]].ID, Enabled: true, Weight: 100, Transports: []model.Transport{model.TCP}, Methods: model.ConnectionMethods{IPv4Direct: true}})
	}
	routers := map[model.ID]*forwarding.Router{}
	hops := 0
	for i, agent := range state.Agents {
		view := state.Clone()
		view.Revision += uint64(i)
		// Each individually valid graph has a shortest path to D, but their
		// asynchronous A->B, B->C and C->A views produce a three-hop cycle.
		cheap := [][]int{{0, 1, 5}, {1, 2, 3}, {2, 0, 4}, {}, {}}[i]
		for _, edge := range cheap {
			view.Networks[0].Edges[edge].Weight = 1
		}
		config, err := routing.Compile(view, agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		self := network.Nodes[i].ID
		router, err := forwarding.New(config, func(ctx context.Context, _, next model.ID, raw []byte) error {
			p, err := packet.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			if int(p.Header.HopLimit) != packet.DefaultHopLimit-hops || p.Header.Epoch != state.Revision+4 {
				t.Fatal("forwarding reset the hop limit or routing epoch", p.Header)
			}
			hops++
			if hops > packet.DefaultHopLimit {
				t.Fatal("routing loop exceeded its hop budget")
			}
			return routers[next].FromPeer(ctx, self, raw)
		}, func(context.Context, model.ID, []byte) error {
			t.Fatal("mixed routing fixture escaped its cycle")
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		routers[self] = router
	}
	err := routers[network.Nodes[4].ID].FromTunnel(t.Context(), network.ID, ipPacket(5, 4))
	if !errors.Is(err, packet.ErrHopLimit) || hops != packet.DefaultHopLimit {
		t.Fatalf("loop termination: hops=%d error=%v", hops, err)
	}
}
