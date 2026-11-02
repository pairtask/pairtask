package messaging_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

type reviewIDReceivingProbe struct {
	*outcomeProbe
	accepted []messaging.ReviewOutcome
	target   messaging.OutcomeStore
}

func (p *reviewIDReceivingProbe) AcceptOutcome(ctx context.Context, key string, o messaging.ReviewOutcome, at time.Time) (messaging.OutcomeAnswer, error) {
	p.accepted = append(p.accepted, o)
	if p.target != nil {
		return p.target.AcceptOutcome(ctx, key, o, at)
	}
	return messaging.OutcomeAnswer{Stored: true}, nil
}

func reviewIDSentOutcome(t *testing.T, frames *scriptFrames, version byte) messaging.ReviewOutcome {
	t.Helper()
	for _, frame := range frames.sent {
		kind, err := messaging.Kind(frame)
		sessionOK(t, err)
		if kind != messaging.FrameReviewOutcome {
			continue
		}
		o, err := messaging.DecodeOutcomeFor(frame, version)
		sessionOK(t, err)
		return o
	}
	t.Fatal("session sent no outcome")
	return messaging.ReviewOutcome{}
}

func TestReviewIDsSessionAcceptanceNegotiatedVersionControlsOutboundShape(t *testing.T) {
	for _, version := range []byte{5, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			db, _ := sessionFixture(t)
			o := reviewIDOutcome()
			probe := &outcomeProbe{routingSessionProbe: &routingSessionProbe{Store: db.PeerMessages()}, pending: []messaging.ReviewOutcome{o}}
			ack, err := messaging.EncodeOutcomeReply(messaging.OutcomeReply{AskID: o.AskID, Seq: o.Seq})
			sessionOK(t, err)
			frames := &scriptFrames{input: append(outcomePhasePrefix(t, version), ack, routingSessionEnd(t))}
			_, negotiated, err := sessionSide(frames, probe).OpenDial(context.Background())
			sessionOK(t, err)
			sessionEqual(t, negotiated, version)
			got := reviewIDSentOutcome(t, frames, version)
			want := ""
			if version == 6 {
				want = o.ReviewID
			}
			sessionEqual(t, got.ReviewID, want)
			sessionEqual(t, len(probe.replies), 1)
		})
	}
}

func TestReviewIDsSessionAcceptanceNegotiatedVersionControlsInboundShape(t *testing.T) {
	for _, version := range []byte{5, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			db, _ := sessionFixture(t)
			probe := &reviewIDReceivingProbe{outcomeProbe: &outcomeProbe{routingSessionProbe: &routingSessionProbe{Store: db.PeerMessages()}}}
			o := reviewIDOutcome()
			wire, err := messaging.EncodeOutcomeFor(o, version)
			sessionOK(t, err)
			frames := &scriptFrames{input: append(outcomePhasePrefix(t, version), wire, routingSessionEnd(t))}
			_, _, err = sessionSide(frames, probe).OpenDial(context.Background())
			sessionOK(t, err)
			sessionEqual(t, len(probe.accepted), 1)
			want := ""
			if version == 6 {
				want = o.ReviewID
			}
			sessionEqual(t, probe.accepted[0].ReviewID, want)
		})
	}
}

func reviewIDReceiver(t *testing.T) *reviewIDReceivingProbe {
	t.Helper()
	db, _ := sessionFixture(t)
	s := db.PeerMessages()
	_, err := s.Queue(context.Background(), messaging.Outgoing{Contact: sessionContact, ID: "ask", Text: "review", To: []string{"reviewer"}, Review: &messaging.ReviewAsk{Forge: "github", Repository: "pairtask/pairtask", Change: "259", Base: "base", Head: "head"}, Now: sessionAt})
	sessionOK(t, err)
	return &reviewIDReceivingProbe{outcomeProbe: &outcomeProbe{routingSessionProbe: &routingSessionProbe{Store: s}}, target: s}
}

func TestReviewIDsSessionAcceptanceLostReceiptThenUpgradeReplaysWithoutConflict(t *testing.T) {
	probe := reviewIDReceiver(t)
	o := messaging.ReviewOutcome{AskID: "ask", Reviewer: "reviewer", Seq: 1, State: "declined", ReviewID: "reviewer-local"}
	legacy, err := messaging.EncodeOutcomeFor(o, 5)
	sessionOK(t, err)
	lost := &scriptFrames{input: append(outcomePhasePrefix(t, 5), legacy, routingSessionEnd(t)), failKind: messaging.FrameOutcomeStored}
	_, _, err = sessionSide(lost, probe).OpenDial(context.Background())
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("lost receipt not injected: %v", err)
	}
	modern, err := messaging.EncodeOutcomeFor(o, 6)
	sessionOK(t, err)
	replay := &scriptFrames{input: append(outcomePhasePrefix(t, 6), modern, routingSessionEnd(t))}
	result, _, err := sessionSide(replay, probe).OpenDial(context.Background())
	sessionOK(t, err)
	sessionEqual(t, result.OutcomesStored, 1)
	sessionEqual(t, len(probe.accepted), 2)
	sessionEqual(t, probe.accepted[0].ReviewID, "")
	sessionEqual(t, probe.accepted[1].ReviewID, o.ReviewID)
}

func TestReviewIDsSessionAcceptanceConflictHasRefusalInsteadOfStoredReceipt(t *testing.T) {
	probe := reviewIDReceiver(t)
	o := messaging.ReviewOutcome{AskID: "ask", Reviewer: "reviewer", Seq: 1, State: "declined", ReviewID: "first-id"}
	a, err := probe.target.AcceptOutcome(context.Background(), sessionContact, o, sessionAt)
	sessionOK(t, err)
	sessionEqual(t, a.Stored, true)
	o.ReviewID = "different-id"
	wire, err := messaging.EncodeOutcomeFor(o, 6)
	sessionOK(t, err)
	frames := &scriptFrames{input: append(outcomePhasePrefix(t, 6), wire, routingSessionEnd(t))}
	result, _, err := sessionSide(frames, probe).OpenDial(context.Background())
	sessionOK(t, err)
	sessionEqual(t, result.OutcomesStored, 0)
	refused := 0
	for _, frame := range frames.sent {
		kind, _ := messaging.Kind(frame)
		if kind == messaging.FrameOutcomeStored {
			t.Fatal("conflicting review ID received a Stored receipt")
		}
		if kind == messaging.FrameOutcomeRefused {
			refused++
			reply, err := messaging.DecodeOutcomeReply(frame)
			sessionOK(t, err)
			sessionEqual(t, reply.Reason, messaging.ReasonConflict)
		}
	}
	sessionEqual(t, refused, 1)
}
