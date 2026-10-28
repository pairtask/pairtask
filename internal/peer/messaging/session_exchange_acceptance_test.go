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

func sessionQueue(t *testing.T, s messaging.Store, id string) {
	t.Helper()
	_, err := s.Queue(context.Background(), messaging.Outgoing{Contact: sessionContact, ID: id, Text: "hello", Now: sessionAt})
	sessionOK(t, err)
}

func sessionRows(t *testing.T, s messaging.Store, want int) []messaging.Row {
	t.Helper()
	rows, _, err := s.Page(context.Background(), sessionContact, 0, 64)
	sessionOK(t, err)
	sessionEqual(t, len(rows), want)
	return rows
}

func sessionDelivered(t *testing.T, s messaging.Store) {
	t.Helper()
	for _, row := range sessionRows(t, s, 2) {
		if row.Direction == messaging.DirOut {
			sessionEqual(t, row.State, messaging.StateDelivered)
		}
	}
}

func sessionPending(t *testing.T, s messaging.Store, want int) {
	t.Helper()
	pending, err := s.PendingReads(context.Background(), sessionContact, 16)
	sessionOK(t, err)
	sessionEqual(t, len(pending), want)
}

func sessionLostReadAck(t *testing.T, outcomes []sessionOutcome) {
	t.Helper()
	for _, outcome := range outcomes {
		if errors.Is(outcome.err, io.ErrUnexpectedEOF) {
			return
		}
	}
	t.Fatalf("lost ReadStored not injected: %+v", outcomes)
}

func TestMessageSessionAcceptanceBothDirectionsOverOneDial(t *testing.T) {
	left, _ := sessionFixture(t)
	right, _ := sessionFixture(t)
	sessionQueue(t, left.PeerMessages(), "left")
	sessionQueue(t, right.PeerMessages(), "right")
	outcomes := sessionExchange(t, left.PeerMessages(), right.PeerMessages(), 0)
	sessionSuccess(t, outcomes)
	for _, outcome := range outcomes {
		sessionEqual(t, outcome.result.Sent, 1)
		sessionEqual(t, outcome.result.Received, 1)
	}
	sessionDelivered(t, left.PeerMessages())
	sessionDelivered(t, right.PeerMessages())
}

func TestMessageSessionAcceptanceLostReadStoredRetriesAfterRestart(t *testing.T) {
	left, path := sessionFixture(t)
	right, _ := sessionFixture(t)
	sessionQueue(t, right.PeerMessages(), "right")
	sessionSuccess(t, sessionExchange(t, left.PeerMessages(), right.PeerMessages(), 0))
	_, err := left.PeerMessages().MarkRead(context.Background(), sessionContact, []string{"right"}, sessionAt)
	sessionOK(t, err)
	sessionLostReadAck(t, sessionExchange(t, left.PeerMessages(), right.PeerMessages(), messaging.FrameReadStored))
	sessionOK(t, left.Close())
	left = sessionDB(t, path)
	sessionPending(t, left.PeerMessages(), 1)
	sessionSuccess(t, sessionExchange(t, left.PeerMessages(), right.PeerMessages(), 0))
	sessionPending(t, left.PeerMessages(), 0)
	sessionEqual(t, sessionRows(t, right.PeerMessages(), 1)[0].State, messaging.StateRead)
}
