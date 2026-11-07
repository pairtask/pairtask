//go:build linux

package server

import (
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestStaleAskPairAcceptanceRequesterReceivesMovedPins(t *testing.T) {
	f := newReplyableNoticePair(t, false)
	moved := staleMoved()
	f.reviewer.write(protocol.Message{Type: "review_check_pins", RequestID: "pair-check", ReviewID: f.notice.ReviewID, Revisions: &moved})
	response := f.reviewer.read()
	operatorEqual(t, "reviewer declined", response.Type, protocol.TypeReviewResult)
	operatorEqual(t, "reviewer state", response.Review.State, protocol.ReviewDeclined)
	notice := awaitOutcomeState(t, f.asker, "declined")
	if notice.ReviewOutcome == nil {
		t.Fatal("requester outcome lost typed metadata")
	}
	staleAssertMoved(t, notice.ReviewOutcome, moved)
	operatorEqual(t, "request identity", notice.InReplyTo, "replyable-ask")
	acknowledgeRemoteAgent(t, f.asker, notice.MessageID)
}

func TestStaleAskAcceptanceBaseOnlyMoveIsDetected(t *testing.T) {
	f := staleReviewServer(t, 26)
	moved := stalePins()
	moved.Base = staleMoved().Head
	response := staleCheck(f, moved)
	operatorEqual(t, "supported check", response.Type, protocol.TypeReviewResult)
	operatorEqual(t, "base-only change declined", response.Review.State, protocol.ReviewDeclined)
	staleAssertMoved(t, response.Review, moved)
}

func TestStaleAskAcceptanceInvalidPinsCannotChangeReview(t *testing.T) {
	for _, head := range []string{"short", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "1234567890123456789012345678901234567890 trailing"} {
		t.Run(head, func(t *testing.T) {
			f := staleReviewServer(t, 26)
			before := f.snapshot()
			moved := staleMoved()
			moved.Head = head
			operatorEqual(t, "invalid pins refused", staleCheck(f, moved).Type, protocol.TypeError)
			noticeGuidanceUnchanged(f, before)
		})
	}
}
