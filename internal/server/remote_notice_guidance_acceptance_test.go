//go:build linux

package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

const noticeReplyGuidance = "a synthetic review notice is not a chat reply target; use its review_id with accept_review, decline_review or submit_review; review outcomes are queued to the requester"

type noticeGuidanceServer struct {
	*redeemOperatorFixture
	key    string
	agent  *operatorPeer
	notice protocol.Message
}

func newNoticeGuidanceServer(t *testing.T) *noticeGuidanceServer {
	t.Helper()
	f := newRedeemOperator(t, redeemConfig(), "")
	id := messageIdentity(t)
	messageConfirm(f, id, 1)
	key := id.Fingerprint()
	agent := f.peer("reviewer", protocol.Version)
	grant := f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpReviewGrant, ContactKey: key, ContactAgents: []string{"reviewer"}, ContactReviewGrant: &protocol.ContactReviewGrant{Mode: "auto"}, MessageID: "grant"})
	operatorEqual(t, "review grant accepted", grant.Type, protocol.TypeOperatorContactResult)
	m := messaging.Message{ID: "guidance-ask", N: 1, Text: "review this change", From: "requester", To: []string{"reviewer"}, CreatedAt: time.Now(), Review: &messaging.ReviewAsk{Forge: "github", Repository: "pairtask/pairtask", Change: "259", Base: strings.Repeat("1", 40), Head: strings.Repeat("2", 40)}}
	a, err := f.st.PeerMessages().Accept(context.Background(), key, m, time.Now())
	operatorNoError(t, err)
	operatorEqual(t, "incoming review stored", a.Stored, true)
	shared := f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpShare, ContactKey: key, ContactAgents: []string{"reviewer"}, MessageID: "share"})
	operatorEqual(t, "reviewer shared", shared.Type, protocol.TypeOperatorContactResult)
	notice := agent.read()
	operatorEqual(t, "synthetic review notice delivered", notice.ReviewID != "", true)
	acknowledgeRemoteAgent(t, agent, notice.MessageID)
	return &noticeGuidanceServer{f, key, agent, notice}
}

func (f *noticeGuidanceServer) reply(parent, workspace string) protocol.Message {
	f.t.Helper()
	f.agent.write(noticeGuidanceReply(parent, workspace))
	return f.agent.read()
}

func noticeGuidanceReply(parent, workspace string) protocol.Message {
	return protocol.Message{Type: protocol.TypeMsg, MessageID: "attempted-reply", Workspace: workspace, To: "requester", InReplyTo: parent, Payload: json.RawMessage(`{"text":"chat reply is not a lifecycle update"}`), Durable: true, AckRequested: true}
}

func (f *noticeGuidanceServer) snapshot() map[string]string {
	f.t.Helper()
	values := map[string]string{}
	for _, table := range []string{"peer_messages", "remote_imports", "messages", "durable_inbox", "review_outcomes_out", "review_outcome_seq"} {
		values[table] = sessionJSON(f.count(table))
	}
	outcomes, err := f.st.PeerMessages().PendingOutcomes(context.Background(), f.key, 10)
	operatorNoError(f.t, err)
	values["outcome payloads"] = sessionJSON(outcomes)
	rec, found, err := f.st.GetReview(context.Background(), f.notice.ReviewID)
	operatorNoError(f.t, err)
	operatorEqual(f.t, "review exists", found, true)
	values["review"] = sessionJSON(rec)
	var outbox int
	operatorNoError(f.t, f.db.QueryRow(`SELECT COUNT(*) FROM peer_messages WHERE direction='out'`).Scan(&outbox))
	values["outbox"] = sessionJSON(outbox)
	return values
}

func noticeGuidanceUnchanged(f *noticeGuidanceServer, before map[string]string) {
	f.t.Helper()
	after := f.snapshot()
	for field, value := range before {
		operatorEqual(f.t, "refusal preserves "+field, after[field], value)
	}
}

func TestRemoteNoticeGuidanceAcceptancePrefixNeverOverridesRecipientPermission(t *testing.T) {
	f := newNoticeGuidanceServer(t)
	// Make the prefix name an actual import, so the lookup must decide first.
	_, err := f.db.Exec(`UPDATE remote_imports SET local_id=? WHERE contact=? AND remote_id='guidance-ask'`, f.notice.MessageID, f.key)
	operatorNoError(t, err)
	intruder := f.peer("intruder", protocol.Version)
	before := f.snapshot()
	intruder.write(noticeGuidanceReply(f.notice.MessageID, f.key))
	response := intruder.read()
	operatorEqual(t, "wrong recipient denied", response.Code, protocol.ErrMalformedFrame)
	operatorEqual(t, "recipient reason preserved", response.Detail, "a reply to a paired workspace's message comes from one of its recipients and goes to its sender")
	noticeGuidanceUnchanged(f, before)
}

func TestRemoteNoticeGuidanceAcceptancePrefixNeverOverridesOtherContact(t *testing.T) {
	f := newNoticeGuidanceServer(t)
	_, err := f.db.Exec(`UPDATE remote_imports SET local_id=? WHERE contact=? AND remote_id='guidance-ask'`, f.notice.MessageID, f.key)
	operatorNoError(t, err)
	other := messageIdentity(t)
	messageConfirm(f.redeemOperatorFixture, other, 2)
	before := f.snapshot()
	response := f.reply(f.notice.MessageID, other.Fingerprint())
	operatorEqual(t, "wrong workspace denied", response.Code, protocol.ErrMalformedFrame)
	operatorEqual(t, "wrong-contact reason preserved", response.Detail, "with workspace, in_reply_to answers a message from that workspace")
	noticeGuidanceUnchanged(f, before)
}

func TestRemoteNoticeGuidanceAcceptanceLookupFailureIsNotMissingNotice(t *testing.T) {
	f := newNoticeGuidanceServer(t)
	before := f.snapshot()
	_, err := f.db.Exec(`ALTER TABLE remote_imports RENAME TO held_remote_imports`)
	operatorNoError(t, err)
	response := f.reply(f.notice.MessageID, f.key)
	_, err = f.db.Exec(`ALTER TABLE held_remote_imports RENAME TO remote_imports`)
	operatorNoError(t, err)
	operatorEqual(t, "database lookup failure denied", response.Code, protocol.ErrMalformedFrame)
	operatorEqual(t, "existing lookup-error detail preserved", response.Detail, "with workspace, in_reply_to answers a message from that workspace")
	noticeGuidanceUnchanged(f, before)
}

func TestRemoteNoticeGuidanceAcceptanceLocalReplyWithPrefixRemainsLocal(t *testing.T) {
	f := newNoticeGuidanceServer(t)
	local := f.peer("local-recipient", protocol.Version)
	before := f.snapshot()
	msg := noticeGuidanceReply(f.notice.MessageID, "")
	msg.To = "local-recipient"
	f.agent.write(msg)
	operatorEqual(t, "local reply accepted", f.agent.read().Stage, protocol.StageAccepted)
	got := local.read()
	operatorEqual(t, "local reply retains parent", got.InReplyTo, f.notice.MessageID)
	operatorEqual(t, "local origin remains local", got.Origin, protocol.OriginAgent)
	operatorEqual(t, "no remote routing provenance", got.Remote == nil, true)
	after := f.snapshot()
	for _, field := range []string{"peer_messages", "remote_imports", "outbox", "outcome payloads", "review"} {
		operatorEqual(t, "local reply changes no remote "+field, after[field], before[field])
	}
}

func TestRemoteNoticeGuidanceAcceptanceNonReviewMissingParentKeepsExistingReason(t *testing.T) {
	f := newNoticeGuidanceServer(t)
	before := f.snapshot()
	response := f.reply("ordinary-missing-message", f.key)
	operatorEqual(t, "missing ordinary reply rejected", response.Code, protocol.ErrMalformedFrame)
	operatorEqual(t, "ordinary error unchanged", response.Detail, "with workspace, in_reply_to answers a message from that workspace")
	noticeGuidanceUnchanged(f, before)
}

func TestRemoteNoticeGuidanceAcceptanceUnknownWorkspaceKeepsItsReason(t *testing.T) {
	f := newNoticeGuidanceServer(t)
	unknown := "sha256:" + strings.Repeat("f", 64)
	_, err := f.st.ResolveWorkspace(context.Background(), unknown)
	if err == nil {
		t.Fatal("fixture unexpectedly resolved unknown workspace")
	}
	before := f.snapshot()
	response := f.reply(f.notice.MessageID, unknown)
	operatorEqual(t, "unknown workspace rejected", response.Code, protocol.ErrRecipientUnavailable)
	operatorEqual(t, "workspace failure not hidden by notice prefix", response.Detail, err.Error())
	noticeGuidanceUnchanged(f, before)
}

func TestRemoteNoticeGuidanceAcceptanceNoticeReviewIDAcceptQueuesOutcome(t *testing.T) {
	f := newNoticeGuidanceServer(t)
	f.agent.write(protocol.Message{Type: protocol.TypeReviewPrepare, RequestID: "pin", ReviewID: f.notice.ReviewID, Revisions: &protocol.Revisions{Base: strings.Repeat("1", 40), Head: strings.Repeat("2", 40)}})
	operatorEqual(t, "notice review ID pins", f.agent.read().Type, protocol.TypeReviewResult)
	f.agent.write(protocol.Message{Type: protocol.TypeReviewAccept, RequestID: "accept", ReviewID: f.notice.ReviewID})
	operatorEqual(t, "notice review ID accepts", f.agent.read().Type, protocol.TypeReviewResult)
	outcomes, err := f.st.PeerMessages().PendingOutcomes(context.Background(), f.key, 10)
	operatorNoError(t, err)
	operatorEqual(t, "one automatic outcome queued", len(outcomes), 1)
	operatorEqual(t, "accepted state queued to requesting contact", outcomes[0].State, "accepted")
	operatorEqual(t, "outcome names original remote ask", outcomes[0].AskID, "guidance-ask")
}
