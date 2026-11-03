//go:build linux

package server

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

func statusFeedbackAsked(t *testing.T, sender instantSide, owner *sessionProbe, want string) {
	t.Helper()
	r := sender.f.send(protocol.Message{Type: protocol.TypeOperatorReview, ReviewOp: protocol.ReviewOpAsked})
	if r.ReviewAsked == nil || len(r.ReviewAsked.Items) != 1 {
		t.Fatalf("requester lost its review row: %+v", r)
	}
	item := r.ReviewAsked.Items[0]
	operatorEqual(t, "requester state", item.State, want)
	operatorEqual(t, "requester original ask", item.AskID, "notification-lifecycle")
	operatorEqual(t, "settled only after cancellation", item.Settled, want == "cancelled")
	card := sender.awaitRemoteView(t, func(m protocol.ContactMessageView) bool {
		return m.ID == item.AskID && m.ReviewOutcome != nil && m.ReviewOutcome.State == want
	})
	operatorEqual(t, "paired card carries same foreign ID", card.ReviewOutcome.ReviewID, item.ReviewID)
	operatorEqual(t, "paired card retains requester ID", card.ReviewOutcome.AskID, item.AskID)
	active := 1
	if want == "cancelled" {
		active = 0
	}
	statusFeedbackCounts(t, sender, active)
	statusFeedbackChanged(t, owner, sender.key)
}

func statusFeedbackCounts(t *testing.T, sender instantSide, active int) {
	t.Helper()
	counts := sender.f.send(protocol.Message{Type: protocol.TypeOperatorReview, ReviewOp: protocol.ReviewOpCounts})
	if counts.ReviewCounts == nil {
		t.Fatal("requester status has no authoritative counts")
	}
	operatorEqual(t, "requester active count tracks transition", counts.ReviewCounts.AskedOpen, active)
}

func statusFeedbackChanged(t *testing.T, owner *sessionProbe, contact string) {
	t.Helper()
	changed := false
	for _, frame := range owner.barrier() {
		if frame.text("type") != protocol.TypeEvent {
			continue
		}
		event := sessionDecode[protocol.SessionEvent](t, frame, "event")
		if event.Kind == protocol.EventContactMessagesChanged && event.ContactKey == contact {
			changed = true
		}
	}
	operatorEqual(t, "remote transition pushes requester UI refresh", changed, true)
}

func TestReviewStatusFeedbackAcceptanceFailedViewMatchesSnapshotAndDoesNotMutate(t *testing.T) {
	f := newReviewSignalFixture(t)
	m := reviewSignalAsk("failed-publication", 1, time.Now())
	_, err := f.st.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: f.key, ID: m.ID, Text: m.Text, To: m.To, Review: m.Review, Now: m.CreatedAt})
	operatorNoError(t, err)
	a, err := f.st.PeerMessages().AcceptOutcome(context.Background(), f.key, messaging.ReviewOutcome{AskID: m.ID, Reviewer: "reviewer", Seq: 1, Attempt: 1, Intent: 1, State: "publication_failed", Verdict: "comment", Reason: "forge unavailable"}, time.Now())
	operatorNoError(t, err)
	operatorEqual(t, "failure outcome saved", a.Stored, true)
	before := reviewCountsSnapshot(t, f.owner)
	operatorEqual(t, "one failure in baseline", string(before["failed"]), "1")
	operatorEqual(t, "failure is not active", string(before["asked_active"]), "0")
	response := reviewPendingPage(f, map[string]any{"review_op": protocol.ReviewOpFailed})
	operatorEqual(t, "failure view result", response.text("type"), protocol.TypeOperatorReviewResult)
	page := sessionDecode[protocol.ReviewFailedPage](t, response, "review_failed")
	operatorEqual(t, "failure page total", page.Total, 1)
	operatorEqual(t, "failure page item count", len(page.Items), 1)
	operatorEqual(t, "failure token matches baseline", fmt.Sprintf("%q", page.Token), string(before["failed_token"]))
	operatorEqual(t, "failure view retains original ask", page.Items[0].ID, m.ID)
	operatorEqual(t, "failure view retains remote identity", page.Items[0].Contact, f.key)
	operatorEqual(t, "failure view explains problem", page.Items[0].Reason, "forge unavailable")
	after := reviewCountsSnapshot(t, f.owner)
	operatorEqual(t, "failure read is not a resolution", string(after["failed_token"]), string(before["failed_token"]))
}

func TestReviewStatusFeedbackAcceptanceRemoteLifecycleReachesRequesterBothDirections(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			p := newInstantPair(t)
			sides := p.sides()
			if reverse {
				sides[0], sides[1] = sides[1], sides[0]
			}
			sender, receiver := sides[0], sides[1]
			asker, reviewer := sender.f.peer("asker", protocol.Version), receiver.f.peer("reviewer", protocol.Version)
			sender.shareRemoteAgents(t, "asker")
			grant := receiver.f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpReviewGrant, ContactKey: receiver.key, ContactAgents: []string{"reviewer"}, ContactReviewGrant: &protocol.ContactReviewGrant{Mode: "auto"}, MessageID: "grant"})
			operatorEqual(t, "review grant", grant.Type, protocol.TypeOperatorContactResult)
			owner := sessionOpen(sender.f.operatorFixture)
			owner.snapshot()
			pins := &protocol.Revisions{Base: strings.Repeat("1", 40), Head: strings.Repeat("2", 40)}
			asker.write(protocol.Message{Type: protocol.TypeMsg, MessageID: "notification-lifecycle", Workspace: sender.key, To: "reviewer", ReviewAsk: &protocol.ReviewAsk{ReviewTarget: protocol.ReviewTarget{Forge: "github", Repository: "pairtask/pairtask", Change: "298"}, Base: pins.Base, Head: pins.Head}, Durable: true, AckRequested: true})
			operatorEqual(t, "request queued", asker.read().Stage, protocol.StageAccepted)
			notice := reviewer.read()
			if notice.ReviewID == "" {
				t.Fatal("reviewer notice has no actionable review ID")
			}
			acknowledgeRemoteAgent(t, reviewer, notice.MessageID)
			awaitOutcomeState(t, asker, "requested")
			statusFeedbackAsked(t, sender, owner, "requested")
			for _, msg := range []protocol.Message{
				{Type: protocol.TypeReviewPrepare, Revisions: pins},
				{Type: protocol.TypeReviewAccept},
			} {
				msg.RequestID, msg.ReviewID = msg.Type, notice.ReviewID
				reviewer.write(msg)
				operatorEqual(t, "reviewer lifecycle action", reviewer.read().Type, protocol.TypeReviewResult)
			}
			awaitOutcomeState(t, asker, "accepted")
			statusFeedbackAsked(t, sender, owner, "accepted")
			cancel := receiver.f.send(protocol.Message{Type: protocol.TypeOperatorReview, ReviewOp: protocol.ReviewOpCancel, MessageID: "cancel-test", ReviewID: notice.ReviewID, ReviewReason: "notification test complete"})
			operatorEqual(t, "cancelled remotely", cancel.Type, protocol.TypeOperatorReviewResult)
			awaitOutcomeState(t, asker, "cancelled")
			statusFeedbackAsked(t, sender, owner, "cancelled")
			p.assertOneConnection(t)
		})
	}
}
