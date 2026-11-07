//go:build linux

package server

import (
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestReplyableNoticeAcceptanceBothSidesThreadToOriginalAskAndReview(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		f := newReplyableNoticePair(t, reverse)
		f.reviewer.write(replyableChat("request-follow-up", f.notice.MessageID, f.receiver.key, "asker"))
		operatorEqual(t, "reviewer can answer request notice", f.reviewer.read().Stage, protocol.StageAccepted)
		requestReply := replyableDelivered(t, f.asker, "reviewer")
		operatorEqual(t, "reviewer chat threads to original local ask", requestReply.InReplyTo, "replyable-ask")
		requireWorkspaceAgent(t, requestReply, f.sender.key, "reviewer")
		acknowledgeRemoteAgent(t, f.asker, requestReply.MessageID)
		f.asker.write(replyableChat("outcome-follow-up", f.outcome.MessageID, f.sender.key, "reviewer"))
		operatorEqual(t, "requester can answer outcome notice", f.asker.read().Stage, protocol.StageAccepted)
		outcomeReply := replyableDelivered(t, f.reviewer, "asker")
		operatorEqual(t, "outcome chat threads to imported review", outcomeReply.InReplyTo, f.notice.ReviewID)
		requireWorkspaceAgent(t, outcomeReply, f.receiver.key, "asker")
	}
}
