package messaging_test

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func acceptanceLink(frames messaging.Frames, s messaging.Store) *messaging.Link {
	return &messaging.Link{Session: sessionSide(nil, s), Open: func(context.Context) messaging.Frames { return frames },
		Timing: messaging.LinkTiming{Round: time.Second, Grace: time.Second, Heartbeat: time.Second, Rotate: 1 << 20, MaxAge: time.Hour}}
}
func liveSignal(t *testing.T, kind byte) []byte {
	t.Helper()
	frame, err := messaging.EncodeSignal(kind)
	return sessionFrame(t, frame, err)
}

func TestLiveAcceptanceMalformedIdleSignalsAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   byte
		dialer bool
	}{
		{"ping", messaging.FramePing, false}, {"round", messaging.FrameRound, false},
		{"pong", messaging.FramePong, true}, {"pending", messaging.FramePending, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			malformed := append(liveSignal(t, tc.kind), 0)
			frames := &scriptFrames{input: [][]byte{malformed}}
			link := acceptanceLink(frames, &countingSessionStore{})
			var err error
			if tc.dialer {
				err = link.RunDialer(context.Background())
			} else {
				err = link.RunServer(context.Background())
			}
			if !errors.Is(err, messaging.ErrProtocol) {
				t.Fatalf("malformed %s was acted on: %v", tc.name, err)
			}
			if len(frames.sent) != 0 {
				t.Fatalf("malformed signal caused %d response frames", len(frames.sent))
			}
		})
	}
}

func TestLiveAcceptanceBusyRoundCannotReachStore(t *testing.T) {
	end, err := messaging.EncodeEnd()
	sessionOK(t, err)
	frames := &scriptFrames{input: [][]byte{liveSignal(t, messaging.FrameRound), sessionMessage(t, messaging.Message{
		ID: "busy", N: 1, CreatedAt: sessionAt, Text: "must not commit concurrently"}), end, end}}
	store := &countingSessionStore{}
	link := acceptanceLink(frames, store)
	link.Begin = func() (func(messaging.Result, error), bool) { return nil, false }
	_ = link.RunServer(context.Background())
	if store.accepted != 0 {
		t.Fatalf("busy round stored %d messages", store.accepted)
	}
	for _, frame := range frames.sent {
		kind, _ := messaging.Kind(frame)
		if kind == messaging.FrameRoundAck || kind == messaging.FrameStored {
			t.Fatalf("busy round was acknowledged: %d", kind)
		}
	}
}

func TestLiveAcceptanceLostStoredAckReplaysWithoutDuplicateInsertion(t *testing.T) {
	db, _ := sessionFixture(t)
	m := messaging.Message{ID: "lost-live-ack", N: 1, CreatedAt: sessionAt, Text: "commit once"}
	first := &scriptFrames{input: [][]byte{liveSignal(t, messaging.FrameRound), sessionMessage(t, m)}, failKind: messaging.FrameStored}
	err := acceptanceLink(first, db.PeerMessages()).RunServer(context.Background())
	sessionErrorIs(t, err, io.ErrUnexpectedEOF)
	end, err := messaging.EncodeEnd()
	sessionOK(t, err)
	replay := &scriptFrames{input: [][]byte{liveSignal(t, messaging.FrameRound), sessionMessage(t, m), end, end}}
	err = acceptanceLink(replay, db.PeerMessages()).RunServer(context.Background())
	sessionErrorIs(t, err, io.EOF)
	rows, _, err := db.PeerMessages().Page(context.Background(), sessionContact, 0, 64)
	sessionOK(t, err)
	sessionEqual(t, len(rows), 1)
	sessionEqual(t, rows[0].ID, m.ID)
	sessionEqual(t, rows[0].Text, m.Text)
	sessionEqual(t, countLiveFrames(replay.sent, messaging.FrameStored), 1)
}

func countLiveFrames(frames [][]byte, wanted byte) int {
	count := 0
	for _, frame := range frames {
		kind, _ := messaging.Kind(frame)
		if kind == wanted {
			count++
		}
	}
	return count
}

// A peer that stops reading must not trap the state-machine writer and
// prevent its heartbeat timer from ever running again.
type stalledLiveFrames struct {
	ctx   context.Context
	input <-chan []byte
}

func (f stalledLiveFrames) Send([]byte) error { <-f.ctx.Done(); return f.ctx.Err() }
func (f stalledLiveFrames) Recv() ([]byte, error) {
	select {
	case b := <-f.input:
		return b, nil
	case <-f.ctx.Done():
		return nil, f.ctx.Err()
	}
}
func TestLiveAcceptanceBlockedIdleWriteHasOwnDeadline(t *testing.T) {
	for _, dialer := range []bool{true, false} {
		name := "server"
		if dialer {
			name = "dialer"
		}
		t.Run(name, func(t *testing.T) {
			input := make(chan []byte, 1)
			if !dialer {
				input <- liveSignal(t, messaging.FramePing)
			}
			link := acceptanceLink(nil, &countingSessionStore{})
			link.Open = func(ctx context.Context) messaging.Frames { return stalledLiveFrames{ctx, input} }
			link.Timing.Round = 50 * time.Millisecond
			link.Timing.Heartbeat = 20 * time.Millisecond
			parent, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var err error
			if dialer {
				err = link.RunDialer(parent)
			} else {
				err = link.RunServer(parent)
			}
			if err == nil {
				t.Fatal("blocked idle write succeeded")
			}
			if parent.Err() != nil {
				t.Fatalf("idle write only stopped at parent shutdown: %v", err)
			}
		})
	}
}

type refuseFirstLiveStore struct {
	messaging.Store
	attempts atomic.Int32
}

func (s *refuseFirstLiveStore) Accept(ctx context.Context, contact string, m messaging.Message, now time.Time) (messaging.Answer, error) {
	if s.attempts.Add(1) == 1 {
		return messaging.Answer{Refusal: messaging.Refuse(messaging.ReasonBusy)}, nil
	}
	return s.Store.Accept(ctx, contact, m, now)
}

func TestLiveAcceptanceServerBacklogRetriesAfterTransientRefusal(t *testing.T) {
	receiver, _ := sessionFixture(t)
	sender, _ := sessionFixture(t)
	sessionQueue(t, sender.PeerMessages(), "retry-server-side")
	receiveStore := &refuseFirstLiveStore{Store: receiver.PeerMessages()}
	ab, ba := make(chan []byte, 1), make(chan []byte, 1)
	dialer := acceptanceLink(nil, receiveStore)
	server := acceptanceLink(nil, sender.PeerMessages())
	dialer.Open = func(ctx context.Context) messaging.Frames { return channelFrames{ctx, ba, ab, 0} }
	server.Open = func(ctx context.Context) messaging.Frames { return channelFrames{ctx, ab, ba, 0} }
	dialer.Timing.Heartbeat = 100 * time.Millisecond
	server.Timing.Heartbeat = 100 * time.Millisecond
	server.More = func() bool {
		rows, err := sender.PeerMessages().Offer(context.Background(), sessionContact, sessionAt, messaging.SessionLimit)
		return err == nil && len(rows) > 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	done := make(chan error, 2)
	go func() { done <- dialer.RunDialer(ctx) }()
	go func() { done <- server.RunServer(ctx) }()
	defer func() { cancel(); <-done; <-done }()
	for {
		rows, _, err := sender.PeerMessages().Page(context.Background(), sessionContact, 0, 64)
		sessionOK(t, err)
		if len(rows) == 1 && rows[0].State == messaging.StateDelivered {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("server backlog never retried after refusal; attempts=%d", receiveStore.attempts.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}
	if receiveStore.attempts.Load() != 2 {
		t.Fatalf("unexpected retry count: %d", receiveStore.attempts.Load())
	}
	sessionRows(t, receiver.PeerMessages(), 1)
}
