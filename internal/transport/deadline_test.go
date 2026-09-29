package transport

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// Models an elapsed deadline whose context timer callback has not run yet.
type pendingDeadlineContext struct {
	context.Context
	at time.Time
}

func (c pendingDeadlineContext) Deadline() (time.Time, bool) { return c.at, true }

func TestSocketDeadlineBeforeContextTimer(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	stream := NewStream(right)
	defer stream.Close()
	ctx := pendingDeadlineContext{Context: context.Background(), at: time.Now().Add(-time.Second)}
	if _, err := stream.ReceiveBatch(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	networkError := errors.New("non-timeout I/O failure")
	if ctxError(ctx, networkError) != networkError {
		t.Fatal("unrelated I/O error was replaced")
	}
}
