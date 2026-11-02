//go:build linux

package server

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

type contactAckFixture struct {
	*redeemOperatorFixture
	owner *sessionProbe
	key   string
}

func newContactAckFixture(t *testing.T) *contactAckFixture {
	t.Helper()
	f := newRedeemOperator(t, redeemConfig(), "")
	identity := messageIdentity(t)
	messageConfirm(f, identity, 1)
	owner := sessionOpen(f.operatorFixture)
	owner.snapshot()
	return &contactAckFixture{f, owner, identity.Fingerprint()}
}

func (f *contactAckFixture) call(fields map[string]any) sessionWire {
	f.t.Helper()
	if _, provided := fields["contact_key"]; !provided {
		fields["contact_key"] = f.key
	}
	response, _ := f.owner.request("operator_contact", fields)
	return response
}

func (f *contactAckFixture) receive(id string, sequence uint64) {
	f.t.Helper()
	result, err := f.st.PeerMessages().Accept(context.Background(), f.key,
		messaging.Message{ID: id, N: sequence, Text: "incoming test message", CreatedAt: time.Now()}, time.Now())
	operatorNoError(f.t, err)
	operatorEqual(f.t, "received fixture", result.Stored, true)
}

func TestContactAckAcceptanceExplicitAckIsPrivateAndPreservesTransportRead(t *testing.T) {
	f := newContactAckFixture(t)
	f.receive("incoming", 1)
	ctx := context.Background()
	marked, err := f.st.PeerMessages().MarkRead(ctx, f.key, []string{"incoming"}, time.Now())
	operatorNoError(t, err)
	operatorEqual(t, "legacy display read still works", marked, 1)
	before, err := f.st.PeerMessages().PendingReads(ctx, f.key, 64)
	operatorNoError(t, err)
	response := f.call(map[string]any{"contact_op": "ack", "message_id": "explicit-human-ack", "contact_ack": []string{"incoming"}})
	operatorEqual(t, "explicit operator ACK accepted: "+sessionJSON(response), response.text("type"), "operator_contact_result")
	operatorEqual(t, "one human acknowledgment", sessionDecode[int](t, response, "contact_acked"), 1)
	after, err := f.st.PeerMessages().PendingReads(ctx, f.key, 64)
	operatorNoError(t, err)
	if !slices.Equal(before, after) {
		t.Fatalf("private human ACK changed peer read receipts: %q -> %q", before, after)
	}
	for _, table := range []string{"messages", "durable_inbox", "remote_imports"} {
		operatorEqual(t, "private ACK creates no "+table, f.count(table), 0)
	}
}

func contactAckCounts(f *contactAckFixture) map[string]int {
	counts := map[string]int{}
	for _, table := range []string{"peer_messages", "peer_ack_floors", "operator_audit", "operator_contact_requests", "messages", "durable_inbox", "remote_imports"} {
		counts[table] = f.count(table)
	}
	return counts
}

func TestContactAckAcceptanceInvalidSelectorsAndAliasesNeverWrite(t *testing.T) {
	tooMany := make([]string, protocol.ContactReadMax+1)
	for i := range tooMany {
		tooMany[i] = "incoming"
	}
	for _, tc := range []struct {
		name   string
		fields map[string]any
	}{
		{"neither", map[string]any{}},
		{"empty-list", map[string]any{"contact_ack": []string{}}},
		{"oversized-list", map[string]any{"contact_ack": tooMany}},
		{"both", map[string]any{"contact_ack": []string{"incoming"}, "contact_ack_upto": 1}},
		{"zero-bound", map[string]any{"contact_ack_upto": 0}},
		{"negative-bound", map[string]any{"contact_ack_upto": -1}},
		{"future-bound", map[string]any{"contact_ack_upto": 999999}},
		{"alias", map[string]any{"contact_ack": []string{"incoming"}, "contact_key": "friendly name"}},
		{"noncanonical-key", map[string]any{"contact_ack": []string{"incoming"}, "contact_key": "sha256:" + strings.Repeat("z", 64)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newContactAckFixture(t)
			f.receive("incoming", 1)
			before := contactAckCounts(f)
			tc.fields["contact_op"], tc.fields["message_id"] = "ack", "invalid-"+tc.name
			response := f.call(tc.fields)
			operatorEqual(t, "bad selector refused: "+sessionJSON(response), response.text("code"), "malformed_frame")
			for table, count := range before {
				operatorEqual(t, "invalid request leaves "+table+" unchanged", f.count(table), count)
			}
			var marked int
			operatorNoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM peer_messages WHERE acked_at IS NOT NULL`).Scan(&marked))
			operatorEqual(t, "invalid request cannot acknowledge", marked, 0)
		})
	}
}

func TestContactAckAcceptanceAgentCannotAcknowledgeForHuman(t *testing.T) {
	f := newContactAckFixture(t)
	f.receive("incoming", 1)
	before := contactAckCounts(f)
	agent, _ := roomPeer(f.operatorFixture, "agent", 17)
	agent.send(map[string]any{"type": "operator_contact", "contact_op": "ack", "contact_key": f.key,
		"message_id": "forged-ack", "contact_ack": []string{"incoming"}})
	operatorEqual(t, "agent cannot act as operator", agent.frame().text("code"), "forbidden")
	for table, count := range before {
		operatorEqual(t, "forgery leaves "+table+" unchanged", f.count(table), count)
	}
}

func TestContactAckAcceptanceAuditFailureCannotCommitAck(t *testing.T) {
	f := newContactAckFixture(t)
	f.receive("in", 1)
	_, err := f.db.Exec(`CREATE TRIGGER fail_ack_audit BEFORE UPDATE ON operator_audit WHEN NEW.result='accepted' BEGIN SELECT RAISE(ABORT,'injected audit failure'); END`)
	operatorNoError(t, err)
	fields := map[string]any{"contact_op": "ack", "message_id": "audit-retry", "contact_ack": []string{"in"}}
	response := f.call(fields)
	operatorEqual(t, "failed audit is not success", response.text("type"), "error")
	var acked int
	operatorNoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM peer_messages WHERE acked_at IS NOT NULL`).Scan(&acked))
	operatorEqual(t, "no unaudited acknowledgment", acked, 0)
	_, err = f.db.Exec(`DROP TRIGGER fail_ack_audit`)
	operatorNoError(t, err)
	response = f.call(fields)
	operatorEqual(t, "same intent recoverable after audit failure", response.text("type"), "operator_contact_result")
	operatorEqual(t, "one committed acknowledgment", sessionDecode[int](t, response, "contact_acked"), 1)
}

func TestContactAckAcceptanceCannotAcceptAwaitingReview(t *testing.T) {
	f := newContactAckFixture(t)
	f.peer("reviewer", protocol.Version)
	grant := f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpReviewGrant, ContactKey: f.key, ContactAgents: []string{"reviewer"}, ContactReviewGrant: &protocol.ContactReviewGrant{Mode: "ask"}, MessageID: "grant-ask"})
	operatorEqual(t, "grant accepted", grant.Type, protocol.TypeOperatorContactResult)
	m := messaging.Message{ID: "review-ask", N: 1, Text: "please review", CreatedAt: time.Now(), To: []string{"reviewer"}, From: "requester", Review: &messaging.ReviewAsk{Forge: "github", Repository: "pairtask/pairtask", Change: "12", Base: strings.Repeat("1", 40), Head: strings.Repeat("2", 40)}}
	a, err := f.st.PeerMessages().Accept(context.Background(), f.key, m, time.Now())
	operatorNoError(t, err)
	operatorEqual(t, "review staged", a.Stored, true)
	var before string
	operatorNoError(t, f.db.QueryRow(`SELECT state FROM remote_imports WHERE remote_id='review-ask'`).Scan(&before))
	operatorEqual(t, "review awaits human decision", before, "awaiting")
	response := f.call(map[string]any{"contact_op": "ack", "message_id": "human-ack-review", "contact_ack": []string{"review-ask"}})
	operatorEqual(t, "ACK accepted without review decision", response.text("type"), "operator_contact_result")
	operatorEqual(t, "routed review is not human ACK eligible", sessionDecode[int](t, response, "contact_acked"), 0)
	var after string
	operatorNoError(t, f.db.QueryRow(`SELECT state FROM remote_imports WHERE remote_id='review-ask'`).Scan(&after))
	operatorEqual(t, "ACK cannot approve review", after, before)
	operatorEqual(t, "ACK cannot dispatch review", f.count("messages"), 0)
}
