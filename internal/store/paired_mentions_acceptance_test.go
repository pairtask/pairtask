package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

type pairedStoreFixture struct {
	*operatorContactFixture
	notice protocol.Message
	root   string
	now    time.Time
}

func pairedStore(t *testing.T) *pairedStoreFixture {
	t.Helper()
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_, err := f.db.ClaimPendingFor(ctx, protocol.Recipient{AgentID: "home-a", SessionID: "session-home-a"}, protocol.Version)
	pairOK(t, err)
	_, err = f.db.OperatorContactSend(ctx, store.ContactSend{OperationID: "paired-root", Contact: f.contact,
		Text: "shared question", To: []string{"remote-a"}, Here: []string{"home-a"},
		AuditID: f.audit(t, contactAuditInput{"send", "paired-root"}), Now: now})
	pairOK(t, err)
	items, err := f.db.ClaimPendingFor(ctx, protocol.Recipient{AgentID: "home-a", SessionID: "session-home-a"}, protocol.Version)
	pairOK(t, err)
	pairEqual(t, len(items), 1)
	notice := items[0]
	binding, found, err := f.db.PairedThread(ctx, notice.MessageID)
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, binding.Contact, f.contact)
	pairEqual(t, binding.PeerID, "paired-root")
	routingSameAgents(t, binding.Audience.Agents, []string{"remote-a"})
	pairEqual(t, binding.Audience.Operator, false)
	return &pairedStoreFixture{f, notice, binding.RootLocalID, now}
}

func (f *pairedStoreFixture) reply(id, parent string) store.PairedReply {
	return store.PairedReply{Session: "session-home-a", Now: f.now.Add(time.Second), Msg: protocol.Message{
		Type: protocol.TypeMsg, From: "home-a", To: protocol.OperatorID, MessageID: id, InReplyTo: parent,
		Payload: json.RawMessage(`{"text":"home answer"}`), Durable: true, AckRequested: true,
	}}
}

func pairedStoreCounts(t *testing.T, f *pairedStoreFixture) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, table := range []string{"messages", "durable_messages", "durable_inbox", "peer_messages", "paired_threads", "paired_reply_outcomes"} {
		var exists int
		pairOK(t, f.raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&exists))
		if exists != 0 {
			var n int
			pairOK(t, f.raw.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&n))
			out[table] = n
		}
	}
	return out
}

func pairedStorePause(t *testing.T, f *pairedStoreFixture) {
	t.Helper()
	_, err := f.db.SetContactPaused(context.Background(), store.ContactPause{OperationID: "pause-paired", Contact: f.contact,
		Paused: true, AuditID: f.audit(t, contactAuditInput{"pause", "pause-paired"}), At: f.now.Add(2 * time.Second)})
	pairOK(t, err)
}

func TestPairedMentionsStoreAcceptanceBindingAndAliasReplaySurviveRestart(t *testing.T) {
	f := pairedStore(t)
	ctx := context.Background()
	request := f.reply("answer", f.notice.MessageID)
	first, err := f.db.PairedReply(ctx, request)
	pairOK(t, err)
	pairEqual(t, first.PeerDelivery.State, "queued")
	pairEqual(t, first.Repeated, false)
	before := pairedStoreCounts(t, f)
	pairedStorePause(t, f)
	pairOK(t, f.db.Close())
	f.db = pairSQLite(t, f.path)
	request.Msg.InReplyTo = f.root // delivery ID and canonical root identify one conversation
	request.Now = request.Now.Add(time.Hour)
	again, err := f.db.PairedReply(ctx, request)
	pairOK(t, err)
	pairEqual(t, again.Repeated, true)
	pairEqual(t, again.Seq, first.Seq)
	pairEqual(t, again.PeerDelivery, first.PeerDelivery)
	if after := pairedStoreCounts(t, f); !reflect.DeepEqual(before, after) {
		t.Fatalf("replay changed persisted delivery: before=%v after=%v", before, after)
	}
	for _, alias := range []string{f.root, f.notice.MessageID} {
		binding, found, err := f.db.PairedThread(ctx, alias)
		pairOK(t, err)
		pairEqual(t, found, true)
		pairEqual(t, binding.RootLocalID, f.root)
	}
}

func TestPairedMentionsStoreAcceptanceConflictsNeverWriteEitherCopy(t *testing.T) {
	for _, mutation := range []string{"workspace", "audience", "broadcast", "room", "body-replay", "sender-replay"} {
		t.Run(mutation, func(t *testing.T) {
			f := pairedStore(t)
			ctx := context.Background()
			request := f.reply("answer", f.notice.MessageID)
			if mutation == "body-replay" || mutation == "sender-replay" {
				_, err := f.db.PairedReply(ctx, request)
				pairOK(t, err)
			}
			switch mutation {
			case "workspace":
				request.Msg.Workspace = messageContact(t, f.db, 2)
			case "audience":
				request.Msg.Workspace, request.Msg.To = f.contact, "remote-outsider"
			case "broadcast":
				request.Msg.To = "*"
			case "room":
				request.Msg.Room = "unrelated"
			case "body-replay":
				request.Msg.Payload = json.RawMessage(`{"text":"changed answer"}`)
			case "sender-replay":
				request.Msg.From, request.Session = "other-home", "session-other-home"
			}
			before := pairedStoreCounts(t, f)
			_, err := f.db.PairedReply(ctx, request)
			if !errors.Is(err, store.ErrConversationConflict) {
				t.Fatalf("%s conflict was not explicit: %v", mutation, err)
			}
			if after := pairedStoreCounts(t, f); !reflect.DeepEqual(before, after) {
				t.Fatalf("%s wrote partial conversation: before=%v after=%v", mutation, before, after)
			}
		})
	}
}

func TestPairedMentionsStoreAcceptanceRevokedDeliveryIsLocalAndReplayable(t *testing.T) {
	for _, tc := range []struct{ name, reason string }{{"paused", "paused"}, {"contact_removed", "contact_removed"}, {"released", "not_shared"}, {"done", "not_shared"}, {"self-claimed-other-task", "not_shared"}} {
		t.Run(tc.name, func(t *testing.T) {
			f := pairedStore(t)
			ctx := context.Background()
			switch tc.name {
			case "paused":
				pairedStorePause(t, f)
			case "contact_removed":
				// Simulate authoritative pin removal, as in the contact collector fixtures.
				_, err := f.raw.Exec(`DELETE FROM peer_contacts WHERE identity_key=?`, f.contact)
				pairOK(t, err)
			case "released", "self-claimed-other-task":
				_, err := f.db.ReleaseClaim(ctx, store.ClaimRequest{MessageID: f.root, Agent: "home-a", Task: "reply", Role: protocol.RoleOwner, At: f.now.Add(time.Second)})
				pairOK(t, err)
				if tc.name == "self-claimed-other-task" {
					_, err = f.db.SetClaim(ctx, store.ClaimRequest{MessageID: f.root, Agent: "home-a", Task: "unrelated-work", Role: protocol.RoleOwner, At: f.now.Add(2 * time.Second)})
					pairOK(t, err)
				}
			case "done":
				_, err := f.db.FinishClaim(ctx, store.ClaimRequest{MessageID: f.root, Agent: "home-a", Task: "reply", At: f.now.Add(time.Second)})
				pairOK(t, err)
			}
			request := f.reply("local-only-answer", f.notice.MessageID)
			first, err := f.db.PairedReply(ctx, request)
			pairOK(t, err)
			pairEqual(t, first.PeerDelivery.State, "not_sent")
			pairEqual(t, first.PeerDelivery.Reason, tc.reason)
			pairEqual(t, first.Seq > 0, true)
			var sender, payload, parent string
			pairOK(t, f.raw.QueryRow(`SELECT m.sender,m.payload,d.in_reply_to FROM messages m JOIN message_metadata d ON d.seq=m.seq WHERE m.seq=?`, first.Seq).Scan(&sender, &payload, &parent))
			pairEqual(t, sender, "home-a")
			pairEqual(t, parent, f.root)
			var text struct {
				Text string `json:"text"`
			}
			pairOK(t, json.Unmarshal([]byte(payload), &text))
			pairEqual(t, text.Text, "home answer")
			var peerReplies, outcomes int
			pairOK(t, f.raw.QueryRow(`SELECT COUNT(*) FROM peer_messages WHERE from_agent='home-a'`).Scan(&peerReplies))
			pairEqual(t, peerReplies, 0)
			pairOK(t, f.raw.QueryRow(`SELECT COUNT(*) FROM paired_reply_outcomes WHERE message_id=?`, request.Msg.MessageID).Scan(&outcomes))
			pairEqual(t, outcomes, 1)
			before := pairedStoreCounts(t, f)
			again, err := f.db.PairedReply(ctx, request)
			pairOK(t, err)
			pairEqual(t, again.Repeated, true)
			pairEqual(t, again.Seq, first.Seq)
			pairEqual(t, again.PeerDelivery, first.PeerDelivery)
			if after := pairedStoreCounts(t, f); !reflect.DeepEqual(before, after) {
				t.Fatalf("local-only retry duplicated writes: before=%v after=%v", before, after)
			}
			if tc.reason == "contact_removed" {
				_, found, err := f.db.Contact(ctx, f.contact)
				pairOK(t, err)
				pairEqual(t, found, false)
			}
		})
	}
}

func TestPairedMentionsStoreAcceptancePeerWriteFailureRollsBackLocalReply(t *testing.T) {
	f := pairedStore(t)
	_, err := f.raw.Exec(`CREATE TRIGGER deny_paired_reply BEFORE INSERT ON peer_messages WHEN NEW.from_agent='home-a' BEGIN SELECT RAISE(ABORT,'peer write unavailable'); END`)
	pairOK(t, err)
	before := pairedStoreCounts(t, f)
	request := f.reply("atomic-answer", f.notice.MessageID)
	_, err = f.db.PairedReply(context.Background(), request)
	if err == nil {
		t.Fatal("paired reply survived peer write failure")
	}
	if after := pairedStoreCounts(t, f); !reflect.DeepEqual(before, after) {
		t.Fatalf("peer failure left local reply/outcome: before=%v after=%v", before, after)
	}
	_, err = f.raw.Exec(`DROP TRIGGER deny_paired_reply`)
	pairOK(t, err)
	result, err := f.db.PairedReply(context.Background(), request)
	pairOK(t, err)
	pairEqual(t, result.Repeated, false)
	pairEqual(t, result.PeerDelivery.State, "queued")
}

func TestPairedMentionsStoreAcceptancePeerCapacityFailureLeavesNoLocalAcceptance(t *testing.T) {
	f := pairedStore(t)
	ctx := context.Background()
	for i := 1; i < messaging.OutboxMessages; i++ { // original operator copy occupies the first slot
		_, err := f.db.PeerMessages().Queue(ctx, messaging.Outgoing{Contact: f.contact, ID: fmt.Sprintf("fill-%d", i), Text: "fill", Now: f.now})
		pairOK(t, err)
	}
	request := f.reply("capacity-answer", f.notice.MessageID)
	before := pairedStoreCounts(t, f)
	_, err := f.db.PairedReply(ctx, request)
	if !errors.Is(err, messaging.ErrOutboxFull) {
		t.Fatalf("full peer outbox must be a retryable capacity failure: %v", err)
	}
	if after := pairedStoreCounts(t, f); !reflect.DeepEqual(before, after) {
		t.Fatalf("capacity refusal persisted local acceptance: before=%v after=%v", before, after)
	}
	pairOK(t, f.db.PeerMessages().Delivered(ctx, f.contact, "paired-root", f.now.Add(time.Second)))
	result, err := f.db.PairedReply(ctx, request)
	pairOK(t, err)
	pairEqual(t, result.Repeated, false)
	pairEqual(t, result.PeerDelivery.State, "queued")
}

func TestPairedMentionsStoreAcceptanceRemoteTagTextCannotCreateOperatorAuthority(t *testing.T) {
	f := pairedStore(t)
	ctx := context.Background()
	_, err := f.db.PeerMessages().Share(ctx, f.contact, []string{"home-a"}, f.now)
	pairOK(t, err)
	var assignments, bindings int
	pairOK(t, f.raw.QueryRow(`SELECT COUNT(*) FROM assignments`).Scan(&assignments))
	pairOK(t, f.raw.QueryRow(`SELECT COUNT(*) FROM paired_threads`).Scan(&bindings))
	m := messageWire("remote-tag-text", 1)
	m.From, m.To, m.Text = "remote-a", []string{"home-a"}, "@home-a@here @operator remain ordinary remote text"
	answer, err := f.db.PeerMessages().Accept(ctx, f.contact, m, f.now)
	pairOK(t, err)
	pairEqual(t, answer.Stored, true)
	imports, err := f.db.PendingImports(ctx, 10)
	pairOK(t, err)
	pairEqual(t, len(imports), 1)
	result, err := f.db.ImportRemote(ctx, imports[0], 100, f.now)
	pairOK(t, err)
	pairEqual(t, result.State, store.ImportImported)
	var afterAssignments, afterBindings int
	pairOK(t, f.raw.QueryRow(`SELECT COUNT(*) FROM assignments`).Scan(&afterAssignments))
	pairOK(t, f.raw.QueryRow(`SELECT COUNT(*) FROM paired_threads`).Scan(&afterBindings))
	pairEqual(t, afterAssignments, assignments)
	pairEqual(t, afterBindings, bindings)
	items, err := f.db.ClaimPendingFor(ctx, protocol.Recipient{AgentID: "home-a", SessionID: "session-home-a"}, protocol.Version)
	pairOK(t, err)
	for _, item := range items {
		if item.Origin != protocol.OriginRemote {
			continue
		}
		pairEqual(t, item.Assignment == nil, true)
		pairEqual(t, item.Conversation == nil, true)
		return
	}
	t.Fatal("ordinary remote message disappeared")
}
