package messaging_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

type countingSessionStore struct {
	messaging.Store
	accepted, reads int
}

func (s *countingSessionStore) Accept(context.Context, string, messaging.Message, time.Time) (messaging.Answer, error) {
	s.accepted++
	return messaging.Answer{Stored: true}, nil
}
func (*countingSessionStore) Offer(context.Context, string, time.Time, int, int) ([]messaging.Row, error) {
	return nil, nil
}
func (*countingSessionStore) PendingReads(context.Context, string, int) ([]string, error) {
	return nil, nil
}
func (s *countingSessionStore) PeerRead(context.Context, string, string, time.Time) error {
	s.reads++
	return nil
}

func TestMessageSessionAcceptanceSeventeenthMessageNeverReachesStore(t *testing.T) {
	frames := &scriptFrames{input: [][]byte{sessionHello(t)}}
	for i := 1; i <= 17; i++ {
		frames.input = append(frames.input, sessionMessage(t, messaging.Message{ID: fmt.Sprint(i), N: uint64(i), CreatedAt: sessionAt, Text: "text"}))
	}
	s := &countingSessionStore{}
	_, err := sessionSide(frames, s).Serve(context.Background())
	if !errors.Is(err, messaging.ErrProtocol) {
		t.Fatalf("session limit not enforced: %v", err)
	}
	if s.accepted != 16 {
		t.Fatalf("accepted %d messages", s.accepted)
	}
}

func TestMessageSessionAcceptanceSeventeenthReadNeverReachesStore(t *testing.T) {
	end, err := messaging.EncodeEnd()
	if err != nil {
		t.Fatal(err)
	}
	frames := &scriptFrames{input: [][]byte{sessionHello(t), end}}
	for i := 1; i <= 17; i++ {
		frame, err := messaging.EncodeRead(messaging.Read{ID: fmt.Sprint(i)})
		if err != nil {
			t.Fatal(err)
		}
		frames.input = append(frames.input, frame)
	}
	s := &countingSessionStore{}
	_, err = sessionSide(frames, s).Serve(context.Background())
	if !errors.Is(err, messaging.ErrProtocol) {
		t.Fatalf("receipt limit not enforced: %v", err)
	}
	if s.reads != 16 {
		t.Fatalf("stored %d read receipts", s.reads)
	}
}

func TestMessageSessionAcceptanceUnexpectedPhaseCannotBecomeMessage(t *testing.T) {
	receipt, err := messaging.EncodeRead(messaging.Read{ID: "wrong-phase"})
	if err != nil {
		t.Fatal(err)
	}
	frames := &scriptFrames{input: [][]byte{sessionHello(t), receipt}}
	s := &countingSessionStore{}
	_, err = sessionSide(frames, s).Serve(context.Background())
	if !errors.Is(err, messaging.ErrProtocol) || s.accepted != 0 || s.reads != 0 {
		t.Fatalf("unexpected phase acted: %+v %v", s, err)
	}
}
