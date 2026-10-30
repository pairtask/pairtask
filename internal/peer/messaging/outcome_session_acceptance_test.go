package messaging_test

import (
	"context"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

type outcomeProbe struct {
	*routingSessionProbe
	pending []messaging.ReviewOutcome
	replies []messaging.OutcomeReply
	reads   int
}

func (p *outcomeProbe) PendingOutcomes(context.Context, string, int) ([]messaging.ReviewOutcome, error) {
	p.reads++
	return p.pending, nil
}
func (p *outcomeProbe) OutcomeAnswered(_ context.Context, _ string, r messaging.OutcomeReply) error {
	p.replies = append(p.replies, r)
	p.pending = nil
	return nil
}
func (p *outcomeProbe) AcceptOutcome(context.Context, string, messaging.ReviewOutcome, time.Time) (messaging.OutcomeAnswer, error) {
	return messaging.OutcomeAnswer{Stored: true}, nil
}

func outcomePhasePrefix(t *testing.T, v byte) [][]byte {
	t.Helper()
	directory, err := messaging.EncodeDirectory(nil)
	return [][]byte{routingSessionHello(t, v), routingSessionEnd(t), routingSessionEnd(t), routingSessionEnd(t), routingSessionEnd(t), sessionFrame(t, directory, err)}
}

func TestOutcomeSessionAcceptanceOldPeerThenUpgrade(t *testing.T) {
	db, _ := sessionFixture(t)
	o := messaging.ReviewOutcome{AskID: "ask", Reviewer: "reviewer", Seq: 1, State: "declined"}
	p := &outcomeProbe{routingSessionProbe: &routingSessionProbe{Store: db.PeerMessages()}, pending: []messaging.ReviewOutcome{o}}
	old := &scriptFrames{input: outcomePhasePrefix(t, 4)}
	_, v, err := sessionSide(old, p).OpenDial(context.Background())
	sessionOK(t, err)
	sessionEqual(t, v, byte(4))
	sessionEqual(t, p.reads, 0)
	sessionEqual(t, len(p.pending), 1)
	ack, err := messaging.EncodeOutcomeReply(messaging.OutcomeReply{AskID: o.AskID, Seq: o.Seq})
	input := append(outcomePhasePrefix(t, 5), sessionFrame(t, ack, err), routingSessionEnd(t))
	upgraded := &scriptFrames{input: input}
	result, v, err := sessionSide(upgraded, p).OpenDial(context.Background())
	sessionOK(t, err)
	sessionEqual(t, v, byte(5))
	sessionEqual(t, len(p.replies), 1)
	sessionEqual(t, len(p.pending), 0)
	sessionEqual(t, result.Moved(), true)
}

func TestOutcomeSessionAcceptanceWrongAnswerCannotSettle(t *testing.T) {
	db, _ := sessionFixture(t)
	o := messaging.ReviewOutcome{AskID: "ask", Reviewer: "reviewer", Seq: 1, State: "declined"}
	p := &outcomeProbe{routingSessionProbe: &routingSessionProbe{Store: db.PeerMessages()}, pending: []messaging.ReviewOutcome{o}}
	ack, err := messaging.EncodeOutcomeReply(messaging.OutcomeReply{AskID: o.AskID, Seq: 2})
	f := &scriptFrames{input: append(outcomePhasePrefix(t, 5), sessionFrame(t, ack, err))}
	_, _, err = sessionSide(f, p).OpenDial(context.Background())
	sessionErrorIs(t, err, messaging.ErrProtocol)
	sessionEqual(t, len(p.replies), 0)
	sessionEqual(t, len(p.pending), 1)
}
