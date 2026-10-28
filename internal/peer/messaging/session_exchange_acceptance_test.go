package messaging_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

type channelFrames struct {
	ctx      context.Context
	incoming <-chan []byte
	outgoing chan<- []byte
	failKind byte
}

func (f channelFrames) Send(b []byte) error {
	kind, _ := messaging.Kind(b)
	if kind == f.failKind {
		return io.ErrUnexpectedEOF
	}
	select {
	case f.outgoing <- append([]byte{}, b...):
		return nil
	case <-f.ctx.Done():
		return f.ctx.Err()
	}
}
func (f channelFrames) Recv() ([]byte, error) {
	select {
	case b := <-f.incoming:
		return b, nil
	case <-f.ctx.Done():
		return nil, f.ctx.Err()
	}
}

type sessionOutcome struct {
	result messaging.Result
	err    error
}

func sessionExchange(t *testing.T, left, right messaging.Store, failRight byte) []sessionOutcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ab, ba := make(chan []byte, 1), make(chan []byte, 1)
	done := make(chan sessionOutcome, 2)
	go func() {
		r, err := sessionSide(channelFrames{ctx, ba, ab, 0}, left).Dial(ctx)
		if err != nil {
			cancel()
		}
		done <- sessionOutcome{r, err}
	}()
	go func() {
		r, err := sessionSide(channelFrames{ctx, ab, ba, failRight}, right).Serve(ctx)
		if err != nil {
			cancel()
		}
		done <- sessionOutcome{r, err}
	}()
	return []sessionOutcome{<-done, <-done}
}
func sessionSuccess(t *testing.T, outcomes []sessionOutcome) {
	t.Helper()
	for _, outcome := range outcomes {
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
	}
}

func TestMessageSessionAcceptanceBothDirectionsOverOneDial(t *testing.T) {
	left, _ := sessionFixture(t)
	right, _ := sessionFixture(t)
	ctx := context.Background()
	for _, side := range []struct {
		store messaging.Store
		id    string
	}{{left.PeerMessages(), "left"}, {right.PeerMessages(), "right"}} {
		if _, err := side.store.Queue(ctx, sessionContact, side.id, "hello", sessionAt); err != nil {
			t.Fatal(err)
		}
	}
	outcomes := sessionExchange(t, left.PeerMessages(), right.PeerMessages(), 0)
	sessionSuccess(t, outcomes)
	for _, outcome := range outcomes {
		if outcome.result.Sent != 1 || outcome.result.Received != 1 {
			t.Fatalf("not bidirectional: %+v", outcome.result)
		}
	}
	for _, s := range []messaging.Store{left.PeerMessages(), right.PeerMessages()} {
		rows, _, err := s.Page(ctx, sessionContact, 0, 64)
		if err != nil || len(rows) != 2 {
			t.Fatalf("rows %+v, %v", rows, err)
		}
		for _, row := range rows {
			if row.Direction == messaging.DirOut && row.State != messaging.StateDelivered {
				t.Fatalf("outbound not delivered: %+v", row)
			}
		}
	}
}

func TestMessageSessionAcceptanceLostReadStoredRetriesAfterRestart(t *testing.T) {
	left, path := sessionFixture(t)
	right, _ := sessionFixture(t)
	ctx := context.Background()
	if _, err := right.PeerMessages().Queue(ctx, sessionContact, "right", "hello", sessionAt); err != nil {
		t.Fatal(err)
	}
	sessionSuccess(t, sessionExchange(t, left.PeerMessages(), right.PeerMessages(), 0))
	if _, err := left.PeerMessages().MarkRead(ctx, sessionContact, []string{"right"}, sessionAt); err != nil {
		t.Fatal(err)
	}
	outcomes := sessionExchange(t, left.PeerMessages(), right.PeerMessages(), messaging.FrameReadStored)
	found := false
	for _, outcome := range outcomes {
		found = found || errors.Is(outcome.err, io.ErrUnexpectedEOF)
	}
	if !found {
		t.Fatalf("lost ReadStored not injected: %+v", outcomes)
	}
	if err := left.Close(); err != nil {
		t.Fatal(err)
	}
	left = sessionDB(t, path)
	pending, err := left.PeerMessages().PendingReads(ctx, sessionContact, 16)
	if err != nil || len(pending) != 1 {
		t.Fatalf("lost ack erased pending receipt: %v %v", pending, err)
	}
	sessionSuccess(t, sessionExchange(t, left.PeerMessages(), right.PeerMessages(), 0))
	pending, err = left.PeerMessages().PendingReads(ctx, sessionContact, 16)
	if err != nil || len(pending) != 0 {
		t.Fatalf("confirmed receipt still pending: %v %v", pending, err)
	}
	rows, _, err := right.PeerMessages().Page(ctx, sessionContact, 0, 64)
	if err != nil || len(rows) != 1 || rows[0].State != messaging.StateRead {
		t.Fatalf("read state lost/duplicated: %+v %v", rows, err)
	}
}
