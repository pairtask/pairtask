//go:build linux

package server

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestReviewOutcomeE2EDeclineBothDirections(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%v", reverse), func(t *testing.T) {
			p := newInstantPair(t)
			sides := p.sides()
			if reverse {
				sides[0], sides[1] = sides[1], sides[0]
			}
			outcomeRoundTrip(t, sides, false)
		})
	}
}

func outcomeRoundTrip(t *testing.T, sides []instantSide, complete bool) {
	t.Helper()
	sender, receiver := sides[0], sides[1]
	a, b := sender.f.peer("asker", 15), receiver.f.peer("reviewer", 15)
	sender.shareRemoteAgents(t, "asker")
	g := receiver.f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpReviewGrant, ContactKey: receiver.key, ContactAgents: []string{"reviewer"}, ContactReviewGrant: &protocol.ContactReviewGrant{Mode: "auto"}, MessageID: "grant"})
	operatorEqual(t, "grant", g.Type, protocol.TypeOperatorContactResult)
	ask := &protocol.ReviewAsk{ReviewTarget: protocol.ReviewTarget{Forge: "github", Repository: "pairtask/pairtask", Change: "235"}, Base: strings.Repeat("1", 40), Head: strings.Repeat("2", 40)}
	a.write(protocol.Message{Type: protocol.TypeMsg, Workspace: sender.key, To: "reviewer", MessageID: "ask", ReviewAsk: ask, Durable: true, AckRequested: true})
	operatorEqual(t, "queued", a.read().Stage, protocol.StageAccepted)
	notice := b.read()
	if notice.ReviewID == "" {
		t.Fatal("review notice missing ID")
	}
	acknowledgeRemoteAgent(t, b, notice.MessageID)
	final := "declined"
	if complete {
		completeOutcomeReview(t, b, notice.ReviewID)
		final = "completed"
	} else {
		b.write(protocol.Message{Type: protocol.TypeReviewDecline, RequestID: "decline", ReviewID: notice.ReviewID, ReviewReason: "not available"})
		operatorEqual(t, "declined", b.read().Type, protocol.TypeReviewResult)
	}
	got := awaitOutcomeState(t, a, final)
	requireWorkspaceAgent(t, got, sender.key, "reviewer")
	operatorEqual(t, "original ask parent", got.InReplyTo, "ask")
	operatorEqual(t, "outcome ask", got.ReviewOutcome.AskID, "ask")
	if complete {
		operatorEqual(t, "verdict", got.ReviewOutcome.Verdict, "comment")
		operatorEqual(t, "summary", got.ReviewOutcome.Summary, "review done")
		operatorEqual(t, "no publication", got.ReviewOutcome.Intent, uint32(0))
	} else {
		operatorEqual(t, "reason", got.ReviewOutcome.Reason, "not available")
	}
}

func awaitOutcomeState(t *testing.T, p *operatorPeer, state string) protocol.Message {
	t.Helper()
	for i := 0; i < 12; i++ {
		m := p.read()
		if m.ReviewOutcome != nil {
			if m.ReviewOutcome.State == state {
				return m
			}
			p.write(protocol.Message{Type: protocol.TypeAck, MessageID: m.MessageID, Stage: protocol.StageAgentAcknowledged})
		}
	}
	t.Fatal("expected outcome never arrived")
	return protocol.Message{}
}

func completeOutcomeReview(t *testing.T, p *operatorPeer, id string) {
	t.Helper()
	for _, msg := range []protocol.Message{
		{Type: protocol.TypeReviewPrepare, Revisions: &protocol.Revisions{Base: strings.Repeat("1", 40), Head: strings.Repeat("2", 40)}},
		{Type: protocol.TypeReviewAccept},
		{Type: protocol.TypeReviewSubmit, Attempt: 1, Verdict: &protocol.Verdict{Outcome: "comment", Summary: "review done"}},
	} {
		msg.RequestID, msg.ReviewID = msg.Type, id
		p.write(msg)
		operatorEqual(t, msg.Type+" result", p.read().Type, protocol.TypeReviewResult)
	}
}

func TestReviewOutcomeE2ECompleteBothDirections(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%v", reverse), func(t *testing.T) {
			p := newInstantPair(t)
			sides := p.sides()
			if reverse {
				sides[0], sides[1] = sides[1], sides[0]
			}
			outcomeRoundTrip(t, sides, true)
		})
	}
}
