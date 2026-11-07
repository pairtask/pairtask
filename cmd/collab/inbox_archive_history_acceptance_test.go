package main

import (
	"context"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func TestInboxArchiveAcceptanceHistoryAudienceReceiptsAndReplyAuthorityRemain(t *testing.T) {
	f := newOfflineInbox(t)
	original := f.seed(t, offlineSeed{id: "broadcast", targets: []string{"operator", "reader"}})
	f.archive(t)
	db := f.open(t)
	page, err := db.ReadOperatorHistory(context.Background(), store.HistoryQuery{Page: protocol.PageRequest{Limit: 32}, Budget: protocol.MaxFrameBytes - 512})
	offlineOK(t, err)
	assertStatusEqual(t, "operator audience retained", len(page.Entries), 1)
	entry := page.Entries[0]
	assertStatusEqual(t, "sequence retained", entry.Message.Seq, original.Seq)
	assertStatusEqual(t, "sender retained", entry.Message.From, original.From)
	assertStatusEqual(t, "recipient retained", entry.Message.To, original.To)
	assertStatusEqual(t, "still acknowledged", entry.OperatorStage, protocol.StageAgentAcknowledged)
	if !strings.Contains(strings.ToLower(string(entry.Message.Payload)), "archived") {
		t.Fatal("history omitted the archived payload marker")
	}
	offlineOK(t, db.ValidatePublicReply(context.Background(), "reader", original.MessageID))
	if db.ValidatePublicReply(context.Background(), "unrelated", original.MessageID) == nil {
		t.Fatal("archive broadened public reply authority")
	}
}

func TestInboxArchiveAcceptanceRepeatedAcksAndReconnectNeverResurrectMessages(t *testing.T) {
	f := newOfflineInbox(t)
	f.seed(t, offlineSeed{id: "acked", targets: []string{"operator", "reader"}})
	f.archive(t)
	db := f.open(t)
	before := offlineReceipts(t, f)
	_, err := db.Acknowledge(context.Background(), store.Acknowledgment{MessageID: "acked", Recipient: protocol.Recipient{AgentID: "reader", SessionID: "new-owner"}, ProtocolVersion: 25, Stage: protocol.StageAgentAcknowledged})
	offlineOK(t, err)
	transition, err := db.AcknowledgeOperator(context.Background(), "acked", store.OperatorAudit{Action: "ack", Result: store.AuditAccepted})
	offlineOK(t, err)
	assertStatusEqual(t, "operator ack already committed", transition == nil, true)
	offlineNoReplay(t, db)
	assertStatusEqual(t, "receipt state unchanged", offlineReceipts(t, f), before)
}

func offlineReceipts(t *testing.T, f *offlineInboxFixture) string {
	t.Helper()
	db := offlineSQL(t, f.database, true)
	return strings.Join(offlineTableRows(t, db, "durable_inbox"), "\n") + strings.Join(offlineTableRows(t, db, "message_receipts"), "\n")
}

func offlineNoReplay(t *testing.T, db *store.Store) {
	t.Helper()
	ctx := context.Background()
	page, err := db.ReadOperatorPage(ctx, protocol.PageRequest{Limit: 32})
	offlineOK(t, err)
	assertStatusEqual(t, "archived rows absent from receive", len(page.Messages), 0)
	for _, agent := range []string{"operator", "reader"} {
		rows, err := db.ClaimPendingFor(ctx, protocol.Recipient{AgentID: agent, SessionID: agent + "-session"}, 25)
		offlineOK(t, err)
		assertStatusEqual(t, "archived rows absent from reconnect", len(rows), 0)
		unacked, err := db.UnacknowledgedForSession(ctx, agent+"-session", 0)
		offlineOK(t, err)
		assertStatusEqual(t, "archived rows absent from legacy resend", len(unacked), 0)
	}
}
