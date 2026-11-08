//go:build linux

package server

import (
	"context"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestStaleAskAcceptanceMatchingCheckDoesNotStartReview(t *testing.T) {
	f := staleReviewServer(t, 26)
	before := f.snapshot()
	response := staleCheck(f, stalePins())
	operatorEqual(t, "pin check supported", response.Type, protocol.TypeReviewResult)
	operatorEqual(t, "matching check leaves requested", response.Review.State, protocol.ReviewRequested)
	noticeGuidanceUnchanged(f, before)
}

func TestStaleAskAcceptanceMovedRequestedOrReadyDeclinesAndNotifies(t *testing.T) {
	for _, ready := range []bool{false, true} {
		f := staleReviewServer(t, 26)
		if ready {
			pins := stalePins()
			f.agent.write(protocol.Message{Type: protocol.TypeReviewPrepare, RequestID: "prepare", ReviewID: f.notice.ReviewID, Revisions: &pins})
			operatorEqual(t, "fixture ready", f.agent.read().Review.State, protocol.ReviewReady)
		}
		response := staleCheck(f, staleMoved())
		operatorEqual(t, "check returns review", response.Type, protocol.TypeReviewResult)
		operatorEqual(t, "moved review declined", response.Review.State, protocol.ReviewDeclined)
		operatorEqual(t, "no acceptance attempt", response.Review.Attempt, 0)
		operatorEqual(t, "original pins preserved", *response.Review.Pinned, stalePins())
		staleAssertMoved(t, response.Review, staleMoved())
		outcomes, err := f.st.PeerMessages().PendingOutcomes(context.Background(), f.key, 10)
		operatorNoError(t, err)
		operatorEqual(t, "one latest outcome", len(outcomes), 1)
		operatorEqual(t, "compatible declined outcome", outcomes[0].State, "declined")
		operatorEqual(t, "fixed moved reason", outcomes[0].Reason, "moved base="+staleMoved().Base+" head="+staleMoved().Head)
	}
}

func TestStaleAskAcceptanceRepeatedCheckDoesNotDuplicateOutcome(t *testing.T) {
	f := staleReviewServer(t, 26)
	operatorEqual(t, "initial moved check", staleCheck(f, staleMoved()).Type, protocol.TypeReviewResult)
	before := f.snapshot()
	response := staleCheck(f, staleMoved())
	operatorEqual(t, "repeated check returns declined view", response.Review.State, protocol.ReviewDeclined)
	noticeGuidanceUnchanged(f, before)
}

func TestStaleAskAcceptanceLegacyPrepareCommitsDeclineBeforeConflict(t *testing.T) {
	f := staleReviewServer(t, 25)
	revisions := staleMoved()
	f.agent.write(protocol.Message{Type: protocol.TypeReviewPrepare, RequestID: "legacy-prepare", ReviewID: f.notice.ReviewID, Revisions: &revisions})
	operatorEqual(t, "legacy gets conflict", f.agent.read().Code, protocol.ErrReviewConflict)
	record, found, err := f.st.GetReview(context.Background(), f.notice.ReviewID)
	operatorNoError(t, err)
	operatorEqual(t, "review retained", found, true)
	operatorEqual(t, "decline committed despite error response", record.View.State, protocol.ReviewDeclined)
	outcomes, err := f.st.PeerMessages().PendingOutcomes(context.Background(), f.key, 10)
	operatorNoError(t, err)
	operatorEqual(t, "requester gets declined outcome", outcomes[0].State, "declined")
}

func TestStaleAskAcceptanceIntruderCannotCheckSomeoneElsesPins(t *testing.T) {
	f := staleReviewServer(t, 26)
	intruder := f.peer("intruder", 26)
	before := f.snapshot()
	revisions := staleMoved()
	intruder.write(protocol.Message{Type: "review_check_pins", RequestID: "intruder-check", ReviewID: f.notice.ReviewID, Revisions: &revisions})
	operatorEqual(t, "intruder denied", intruder.read().Type, protocol.TypeError)
	noticeGuidanceUnchanged(f, before)
}
