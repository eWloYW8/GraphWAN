package link

import (
	"context"
	"errors"
	"github.com/eWloYW8/GraphWAN/internal/model"
	"github.com/eWloYW8/GraphWAN/internal/packetbuf"
	"testing"
)

func TestTCPBacklogOwnershipAndDrain(t *testing.T) {
	for _, kind := range []model.Transport{model.TCP, model.WS, model.WSS, model.GRPC, model.UDP, model.QUIC} {
		t.Run(string(kind), func(t *testing.T) {
			l := &Link{ctx: context.Background(), info: Info{Transport: kind}, dataReady: make(chan struct{}, 1)}
			l.dataGeneration.Store(1)
			// Exceptional unpooled owners make release observable without pool reuse.
			owners := make([]*packetbuf.Buffer, 128)
			for i := range owners {
				owners[i] = packetbuf.Wrap(make([]byte, 4096))
			}
			err := l.SendOwnedBatch(context.Background(), owners)
			want := 128
			if l.tcpBacked() {
				want = tcpQueueBytes / 4097
				if !errors.Is(err, ErrQueueFull) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if l.dataCount != want || l.dataBytes != want*4097 {
				t.Fatalf("count=%d bytes=%d", l.dataCount, l.dataBytes)
			}
			for _, b := range owners {
				if b.Data != nil {
					t.Fatal("input owner not released after copy/drop")
				}
			}
			for l.dataCount > 0 {
				l.takeData().buffer.Release()
			}
			if l.dataBytes != 0 {
				t.Fatal("drain leaked byte budget")
			}
			if err := l.Send(context.Background(), make([]byte, 4096)); err != nil {
				t.Fatal(err)
			}
			l.takeData().buffer.Release()
			if l.dataBytes != 0 {
				t.Fatal("refill leaked budget")
			}
		})
	}
}
