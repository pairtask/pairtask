//go:build linux

package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

func contactAckSummary(t *testing.T, response sessionWire) protocol.ContactAckSummary {
	t.Helper()
	return sessionDecode[protocol.ContactAckSummary](t, response, "contact_acks")
}

func TestContactAckBulkAcceptanceFrozenBoundCoversUnloadedRows(t *testing.T) {
	f := newContactAckFixture(t)
	for i := 1; i <= 80; i++ {
		f.receive(fmt.Sprint("incoming-", i), uint64(i))
	}
	response := f.call(map[string]any{"contact_op": "list"})
	page := sessionDecode[protocol.ContactMessagePage](t, response, "contact_page")
	if page.Acks == nil {
		t.Fatal("contact page omits ACK summary")
	}
	operatorEqual(t, "page is partial", len(page.Messages) < 80, true)
	operatorEqual(t, "summary counts unloaded rows", page.Acks.Unacked, 80)
	f.receive("after-snapshot", 81)
	fields := map[string]any{"contact_op": "ack", "message_id": "frozen-bulk", "contact_ack_upto": page.Acks.AckUpto}
	response = f.call(fields)
	operatorEqual(t, "all frozen rows acknowledged", sessionDecode[int](t, response, "contact_acked"), 80)
	operatorEqual(t, "new arrival remains", contactAckSummary(t, response).Unacked, 1)
	response = f.call(fields)
	operatorEqual(t, "replay does not widen selection", contactAckSummary(t, response).Unacked, 1)
	var newAcked int
	operatorNoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM peer_messages WHERE message_id='after-snapshot' AND acked_at IS NOT NULL`).Scan(&newAcked))
	operatorEqual(t, "new arrival not accidentally acknowledged", newAcked, 0)
}

func TestContactAckBulkAcceptanceOutboundCursorCannotExtendInboundBound(t *testing.T) {
	f := newContactAckFixture(t)
	f.receive("in", 1)
	out, err := f.st.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: f.key, ID: "out", Text: "later outbound", Now: time.Now()})
	operatorNoError(t, err)
	response := f.call(map[string]any{"contact_op": "ack", "message_id": "bad-bound", "contact_ack_upto": out.Cursor})
	operatorEqual(t, "outbound cursor is not an inbound bound", response.text("code"), "malformed_frame")
	summary, err := f.st.PeerMessages().AckSummary(context.Background(), f.key)
	operatorNoError(t, err)
	operatorEqual(t, "rejected bound makes no change", summary.Unacked, 1)
}

func TestContactAckBulkAcceptanceUnknownIDsSkipAndZeroResultIsPresent(t *testing.T) {
	f := newContactAckFixture(t)
	f.receive("in", 1)
	response := f.call(map[string]any{"contact_op": "ack", "message_id": "unknown-only", "contact_ack": []string{"unknown"}})
	operatorEqual(t, "valid empty outcome", response.text("type"), "operator_contact_result")
	if _, ok := response["contact_acked"]; !ok {
		t.Fatal("zero contact_acked omitted")
	}
	operatorEqual(t, "nothing newly acknowledged", sessionDecode[int](t, response, "contact_acked"), 0)
	operatorEqual(t, "unknown ID leaves pending count", contactAckSummary(t, response).Unacked, 1)
	response = f.call(map[string]any{"contact_op": "ack", "message_id": "dedup-ids", "contact_ack": []string{"in", "in", "unknown"}})
	operatorEqual(t, "duplicates counted once", sessionDecode[int](t, response, "contact_acked"), 1)
	operatorEqual(t, "post-write count", contactAckSummary(t, response).Unacked, 0)
}

func TestContactAckBulkAcceptanceLegacyReadNeverAcknowledges(t *testing.T) {
	f := newContactAckFixture(t)
	f.receive("in", 1)
	response := f.call(map[string]any{"contact_op": "read", "message_id": "display-read", "contact_read": []string{"in"}})
	operatorEqual(t, "legacy read accepted", response.text("type"), "operator_contact_result")
	response = f.call(map[string]any{"contact_op": "list"})
	page := sessionDecode[protocol.ContactMessagePage](t, response, "contact_page")
	if page.Acks == nil {
		t.Fatal("page has no ACK summary")
	}
	operatorEqual(t, "shown remains unacked", page.Acks.Unacked, 1)
	operatorEqual(t, "row explicitly needs ACK", page.Messages[0].AckState, "needs_ack")
	operatorEqual(t, "display does not fabricate human timestamp", page.Messages[0].AckedAt == nil, true)
}
