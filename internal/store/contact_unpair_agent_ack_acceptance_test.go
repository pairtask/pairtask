package store_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func TestContactUnpairAgentAckAcceptanceLocalAckDoesNotQueueRetiredRead(t *testing.T) {
	f := namespaceFixture(t)
	imported := f.wireImported(t, "local-ack-after-unpair", "old body", 1)
	owner := protocol.Recipient{AgentID: "recipient", SessionID: "local-ack-session"}
	claimed, err := f.db.ClaimPendingFor(context.Background(), owner, protocol.Version)
	pairOK(t, err)
	pairEqual(t, len(claimed), 1)
	pairEqual(t, claimed[0].MessageID, imported.Msg.MessageID)
	_, err = f.db.AcknowledgeReport(context.Background(), store.Acknowledgment{MessageID: imported.Msg.MessageID, Recipient: owner, Stage: protocol.StageAdapterReceived, ProtocolVersion: protocol.Version})
	pairOK(t, err)
	op := &operatorContactFixture{db: f.db, raw: f.raw, contact: f.key}
	_, _, err = f.db.UnpairContact(context.Background(), unpairRequest(t, op, "ack-unpair"))
	pairOK(t, err)
	reads := epochRows(t, f.raw, "peer_agent_reads", epochColumns(t, f.raw, "peer_agent_reads"))
	report, err := f.db.AcknowledgeReport(context.Background(), store.Acknowledgment{MessageID: imported.Msg.MessageID, Recipient: owner, Stage: protocol.StageAgentAcknowledged, ProtocolVersion: protocol.Version})
	pairOK(t, err)
	if report.RemoteContact != "" {
		t.Errorf("local acknowledgment queued work for retired contact: %s", report.RemoteContact)
	}
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM durable_inbox WHERE message_id=? AND agent_id='recipient' AND acknowledged_at IS NOT NULL`, imported.Msg.MessageID), 1)
	if !reflect.DeepEqual(reads, epochRows(t, f.raw, "peer_agent_reads", epochColumns(t, f.raw, "peer_agent_reads"))) {
		t.Fatal("local acknowledgment created a retired wire receipt after unpair")
	}
}
