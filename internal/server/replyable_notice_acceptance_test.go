//go:build linux

package server

import (
	"context"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestReplyableNoticeAcceptanceRequestNoticeQueuesChatWithoutLifecycleChange(t *testing.T) {
	f := newNoticeGuidanceServer(t)
	before := f.snapshot()
	response := f.reply(f.notice.MessageID, f.key)
	operatorEqual(t, "synthetic request notice can be answered", response.Stage, protocol.StageAccepted)
	after := f.snapshot()
	for _, field := range []string{"review", "outcome payloads", "review_outcomes_out", "review_outcome_seq"} {
		operatorEqual(t, "chat reply preserves "+field, after[field], before[field])
	}
	row := replyableNoticeRow(t, f.redeemOperatorFixture, f.key, "attempted-reply")
	operatorEqual(t, "wire parent is original ask", row.ReplyTo.ID, "guidance-ask")
	operatorEqual(t, "reviewer answers receiver-side ask", row.ReplyTo.Author, messaging.ReplyByReceiver)
}

func TestReplyableNoticeAcceptanceRequestRetryIsIdempotent(t *testing.T) {
	f := newNoticeGuidanceServer(t)
	first := f.reply(f.notice.MessageID, f.key)
	operatorEqual(t, "first reply accepted", first.Stage, protocol.StageAccepted)
	again := f.reply(f.notice.MessageID, f.key)
	operatorEqual(t, "retry accepted", again.Stage, protocol.StageAccepted)
	operatorEqual(t, "retry marked repeated", again.Repeated, true)
	operatorEqual(t, "retry preserves original sequence", again.Seq, first.Seq)
	operatorEqual(t, "only one queued chat reply", f.count("messages"), 2)
}

func TestReplyableNoticeAcceptanceOutcomeUsesOriginalAskOnWire(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		f := newOutcomeNoticeServer(t)
		workspace := ""
		if explicit {
			workspace = f.key
		}
		before := f.snapshot()
		f.agent.write(outcomeNoticeReply(f.notice.MessageID, workspace))
		operatorEqual(t, "outcome chat reply accepted", f.agent.read().Stage, protocol.StageAccepted)
		row := replyableNoticeRow(t, f.redeemOperatorFixture, f.key, "outcome-reply")
		operatorEqual(t, "wire parent is ask, never outcome digest", row.ReplyTo.ID, "ask")
		operatorEqual(t, "requester answers sender-side ask", row.ReplyTo.Author, messaging.ReplyBySender)
		operatorEqual(t, "reply addressed only to reviewer", len(row.To), 1)
		operatorEqual(t, "reviewer identity preserved", row.To[0], "target")
		operatorEqual(t, "chat reply leaves outcome state unchanged", f.snapshot()["outcome state"], before["outcome state"])
	}
}

func TestReplyableNoticeAcceptanceActualRequestRecipientGuardCannotQueue(t *testing.T) {
	f := newNoticeGuidanceServer(t)
	intruder := f.peer("intruder", protocol.Version)
	before := f.snapshot()
	intruder.write(noticeGuidanceReply(f.notice.MessageID, f.key))
	response := intruder.read()
	operatorEqual(t, "unauthorized notice reply rejected", response.Code, protocol.ErrMalformedFrame)
	noticeGuidanceUnchanged(f, before)
}

func TestReplyableNoticeAcceptanceCanonicalReviewImportStillWorks(t *testing.T) {
	f := newNoticeGuidanceServer(t)
	response := f.reply(f.notice.ReviewID, f.key)
	operatorEqual(t, "existing canonical reply target works", response.Stage, protocol.StageAccepted)
	row := replyableNoticeRow(t, f.redeemOperatorFixture, f.key, "attempted-reply")
	operatorEqual(t, "canonical import retains original ask", row.ReplyTo.ID, "guidance-ask")
}

func replyableNoticeRow(t *testing.T, f *redeemOperatorFixture, contact, id string) messaging.Row {
	t.Helper()
	rows, _, err := f.st.PeerMessages().Page(context.Background(), contact, 0, 20)
	operatorNoError(t, err)
	for _, row := range rows {
		if row.Direction == "out" && row.ID == id {
			return row
		}
	}
	t.Fatalf("queued reply %s not found", id)
	return messaging.Row{}
}
