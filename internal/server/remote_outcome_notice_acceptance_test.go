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

const outcomeReplyGuidance = "a review outcome notice is not a chat reply target; send a new message to the reviewer in that workspace"

type outcomeNoticeServer struct {
	*redeemOperatorFixture
	key    string
	agent  *operatorPeer
	notice protocol.Message
}

func (f *outcomeNoticeServer) rescan(id string) {
	f.t.Helper()
	r := f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpShare, ContactKey: f.key, ContactAgents: []string{"sender"}, MessageID: id})
	operatorEqual(f.t, "sender shared", r.Type, protocol.TypeOperatorContactResult)
}

func newOutcomeNoticeServer(t *testing.T) *outcomeNoticeServer {
	t.Helper()
	f := newRedeemOperator(t, redeemConfig(), "")
	id := messageIdentity(t)
	messageConfirm(f, id, 1)
	n := &outcomeNoticeServer{redeemOperatorFixture: f, key: id.Fingerprint(), agent: f.peer("sender", protocol.Version)}
	n.rescan("share")
	n.agent.write(protocol.Message{Type: protocol.TypeMsg, MessageID: "ask", Workspace: n.key, To: "target", Payload: json.RawMessage(`{"text":"review this change"}`), ReviewAsk: &protocol.ReviewAsk{ReviewTarget: protocol.ReviewTarget{Forge: "github", Repository: "pairtask/pairtask", Change: "259"}, Base: strings.Repeat("1", 40), Head: strings.Repeat("2", 40)}, Durable: true, AckRequested: true})
	operatorEqual(t, "remote review ask queued", n.agent.read().Stage, protocol.StageAccepted)
	a, err := f.st.PeerMessages().AcceptOutcome(context.Background(), n.key, messaging.ReviewOutcome{AskID: "ask", Reviewer: "target", Seq: 1, State: "requested"}, time.Now())
	operatorNoError(t, err)
	operatorEqual(t, "outcome stored", a.Stored, true)
	n.rescan("import-outcome")
	n.notice = n.agent.read()
	operatorEqual(t, "outcome notice delivered", n.notice.ReviewOutcome != nil, true)
	acknowledgeRemoteAgent(t, n.agent, n.notice.MessageID)
	return n
}

func outcomeNoticeReply(parent, workspace string) protocol.Message {
	return protocol.Message{Type: protocol.TypeMsg, MessageID: "outcome-reply", To: "target", Workspace: workspace, InReplyTo: parent, Payload: json.RawMessage(`{"text":"follow up"}`), Durable: true, AckRequested: true}
}

func (f *outcomeNoticeServer) snapshot() map[string]string {
	f.t.Helper()
	result := map[string]string{}
	for _, table := range []string{"peer_messages", "messages", "durable_inbox", "remote_imports", "review_outcomes_out", "review_outcome_seq"} {
		result[table] = sessionJSON(f.count(table))
	}
	a, found, err := f.st.PeerMessages().AskOutFor(context.Background(), f.key, "ask")
	operatorNoError(f.t, err)
	operatorEqual(f.t, "original ask exists", found, true)
	result["outcome state"] = sessionJSON(a)
	var outgoing int
	operatorNoError(f.t, f.db.QueryRow(`SELECT COUNT(*) FROM peer_messages WHERE direction='out'`).Scan(&outgoing))
	result["outbox"] = sessionJSON(outgoing)
	return result
}

func outcomeNoticeUnchanged(f *outcomeNoticeServer, before map[string]string) {
	f.t.Helper()
	after := f.snapshot()
	for field, value := range before {
		operatorEqual(f.t, "outcome refusal preserves "+field, after[field], value)
	}
}

func TestRemoteOutcomeNoticeAcceptanceExplicitAndImplicitRepliesCannotMutate(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "implicit", true: "explicit"}[explicit], func(t *testing.T) {
			f := newOutcomeNoticeServer(t)
			workspace := ""
			if explicit {
				workspace = f.key
			}
			before := f.snapshot()
			f.agent.write(outcomeNoticeReply(f.notice.MessageID, workspace))
			response := f.agent.read()
			operatorEqual(t, "outcome reply rejected", response.Code, protocol.ErrMalformedFrame)
			operatorEqual(t, "dedicated follow-up guidance", response.Detail, outcomeReplyGuidance)
			if strings.Contains(response.Detail, "request_review") {
				t.Fatal("outcome refusal suggests requesting another review")
			}
			outcomeNoticeUnchanged(f, before)
		})
	}
}

func TestRemoteOutcomeNoticeAcceptanceUnauthorizedSenderKeepsPermissionReason(t *testing.T) {
	f := newOutcomeNoticeServer(t)
	intruder := f.peer("intruder", protocol.Version)
	before := f.snapshot()
	intruder.write(outcomeNoticeReply(f.notice.MessageID, f.key))
	response := intruder.read()
	operatorEqual(t, "unauthorized sender rejected", response.Code, protocol.ErrMalformedFrame)
	operatorEqual(t, "permission checked before outcome kind", response.Detail, "a reply to a paired workspace's message comes from one of its recipients and goes to its sender")
	outcomeNoticeUnchanged(f, before)
}

func TestRemoteOutcomeNoticeAcceptanceWrongWorkspaceKeepsContactReason(t *testing.T) {
	f := newOutcomeNoticeServer(t)
	other := messageIdentity(t)
	messageConfirm(f.redeemOperatorFixture, other, 2)
	before := f.snapshot()
	f.agent.write(outcomeNoticeReply(f.notice.MessageID, other.Fingerprint()))
	response := f.agent.read()
	operatorEqual(t, "different contact rejected", response.Code, protocol.ErrMalformedFrame)
	operatorEqual(t, "contact checked before outcome kind", response.Detail, "with workspace, in_reply_to answers a message from that workspace")
	outcomeNoticeUnchanged(f, before)
}

func TestRemoteOutcomeNoticeAcceptanceOrdinaryImportedRepliesStillQueue(t *testing.T) {
	f := newOutcomeNoticeServer(t)
	m := messaging.Message{ID: "ordinary", N: 1, From: "target", To: []string{"sender"}, Text: "ordinary question", CreatedAt: time.Now()}
	a, err := f.st.PeerMessages().Accept(context.Background(), f.key, m, time.Now())
	operatorNoError(t, err)
	operatorEqual(t, "ordinary import stored", a.Stored, true)
	f.rescan("import-ordinary")
	notice := f.agent.read()
	operatorEqual(t, "ordinary message has no outcome", notice.ReviewOutcome == nil, true)
	acknowledgeRemoteAgent(t, f.agent, notice.MessageID)
	f.agent.write(outcomeNoticeReply(notice.MessageID, f.key))
	operatorEqual(t, "normal reply accepted", f.agent.read().Stage, protocol.StageAccepted)
	rows, _, err := f.st.PeerMessages().Page(context.Background(), f.key, 0, 20)
	operatorNoError(t, err)
	for _, row := range rows {
		if row.Direction == "out" && row.ID == "outcome-reply" {
			operatorEqual(t, "ordinary remote parent retained", row.ReplyTo.ID, "ordinary")
			return
		}
	}
	t.Fatal("normal imported reply did not reach outbox")
}
