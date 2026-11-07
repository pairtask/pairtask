package messaging_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

// Frames has no buffering guarantee. Each successful send reaches the
// other side; a bounded context makes protocol deadlocks observable.
type controlDuplexFrames struct {
	ctx context.Context
	out chan<- []byte
	in  <-chan []byte
}

func (f controlDuplexFrames) Send(b []byte) error {
	select {
	case f.out <- append([]byte(nil), b...):
		return nil
	case <-f.ctx.Done():
		return f.ctx.Err()
	}
}
func (f controlDuplexFrames) Recv() ([]byte, error) {
	select {
	case b := <-f.in:
		return b, nil
	case <-f.ctx.Done():
		return nil, f.ctx.Err()
	}
}
func TestControlDuplexAcceptanceMismatchedIDsFinishOnBothSides(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a, b := make(chan []byte), make(chan []byte)
	dialStore := &controlProbe{id: bytes.Repeat([]byte{1}, 32)}
	serveStore := &controlProbe{id: bytes.Repeat([]byte{2}, 32)}
	done := make(chan error, 2)
	go func() {
		_, e := (messaging.ControlSession{Frames: controlDuplexFrames{ctx, a, b}, Store: dialStore}).Dial(ctx)
		done <- e
	}()
	go func() {
		_, e := (messaging.ControlSession{Frames: controlDuplexFrames{ctx, b, a}, Store: serveStore}).Serve(ctx)
		done <- e
	}()
	for range 2 {
		if err := <-done; !errors.Is(err, messaging.ErrOtherPairing) {
			t.Fatalf("mismatched duplex did not close cleanly: %v", err)
		}
	}
	sessionEqual(t, len(dialStore.calls), 1)
	sessionEqual(t, len(serveStore.calls), 1)
}
