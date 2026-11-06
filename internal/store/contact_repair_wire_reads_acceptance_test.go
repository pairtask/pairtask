package store_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func TestContactRepairWireAcceptanceMarkReadCannotReviveOldReceipt(t *testing.T) {
	f := namespaceFixture(t)
	namespaceAccept(t, f, messageWire("same-in", 1))
	namespaceRepair(t, f)
	old := namespaceEpochRows(t, f, "peer_messages", 1)
	next := messageWire("same-in", 1)
	next.Text = "new inbound"
	namespaceAccept(t, f, next)
	marked, err := f.db.PeerMessages().MarkRead(context.Background(), f.key, []string{next.ID}, messageAt)
	pairOK(t, err)
	pairEqual(t, marked, 1)
	if !reflect.DeepEqual(old, namespaceEpochRows(t, f, "peer_messages", 1)) {
		t.Error("reading current ID revived or marked a retired message")
	}
	bound := wireBound(t, f)
	pending, err := bound.PendingReads(context.Background(), f.key, 10)
	pairOK(t, err)
	pairEqual(t, len(pending), 1)
	pairEqual(t, pending[0], next.ID)
	pairOK(t, bound.ReadConfirmed(context.Background(), f.key, next.ID))
	pending, err = bound.PendingReads(context.Background(), f.key, 10)
	pairOK(t, err)
	pairEqual(t, len(pending), 0)
	if !reflect.DeepEqual(old, namespaceEpochRows(t, f, "peer_messages", 1)) {
		t.Fatal("receipt confirmation changed retired history")
	}
}

func TestContactRepairWireAcceptanceAgentReadQueueAndConfirmUseCurrentEpoch(t *testing.T) {
	f := namespaceFixture(t)
	oldImport := wireImported(t, f, "agent-message", "old body", 1)
	pairOK(t, f.db.PeerMessages().QueueAgentRead(context.Background(), oldImport.Msg.MessageID, "recipient", messageAt))
	namespaceRepair(t, f)
	old := namespaceEpochRows(t, f, "peer_agent_reads", 1)
	bound := wireBound(t, f)
	pending, err := bound.PendingAgentReads(context.Background(), f.key, 10)
	pairOK(t, err)
	if len(pending) != 0 {
		t.Errorf("new session offered retired agent reads: %+v", pending)
	}
	namespaceShare(t, f, "recipient")
	current := wireImported(t, f, "agent-message", "new body", 2)
	pairOK(t, f.db.PeerMessages().QueueAgentRead(context.Background(), current.Msg.MessageID, "recipient", messageAt))
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_agent_reads WHERE pairing_epoch=2 AND state='pending'`), 1)
	pairOK(t, bound.AgentReadConfirmed(context.Background(), f.key, messaging.AgentRead{ID: "agent-message", Agent: "recipient"}))
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_agent_reads WHERE pairing_epoch=2 AND state='confirmed'`), 1)
	if !reflect.DeepEqual(old, namespaceEpochRows(t, f, "peer_agent_reads", 1)) {
		t.Fatal("new agent read or its ACK changed retired evidence")
	}
}

func TestContactRepairWireAcceptancePeerAgentReadDoesNotUseOldOnlyMessage(t *testing.T) {
	f := namespaceFixture(t)
	wireQueue(t, f, "old-agent-out", "old body")
	namespaceRepair(t, f)
	before := wireMutationSnapshot(t, f)
	pairOK(t, wireBound(t, f).PeerAgentRead(context.Background(), f.key, messaging.AgentRead{ID: "old-agent-out", Agent: "target"}, messageAt))
	if !reflect.DeepEqual(before, wireMutationSnapshot(t, f)) {
		t.Fatal("agent read naming retired message changed new or old authority")
	}
}

func TestContactRepairWireAcceptancePeerAgentReadWritesNewEpoch(t *testing.T) {
	f := namespaceFixture(t)
	wireQueue(t, f, "same-agent-out", "old body")
	read := messaging.AgentRead{ID: "same-agent-out", Agent: "target"}
	pairOK(t, f.db.PeerMessages().PeerAgentRead(context.Background(), f.key, read, messageAt))
	namespaceRepair(t, f)
	old := namespaceEpochRows(t, f, "peer_agent_acks", 1)
	wireQueue(t, f, "same-agent-out", "new body")
	pairOK(t, wireBound(t, f).PeerAgentRead(context.Background(), f.key, read, messageAt))
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_agent_acks WHERE pairing_epoch=2`), 1)
	if !reflect.DeepEqual(old, namespaceEpochRows(t, f, "peer_agent_acks", 1)) {
		t.Fatal("new peer-agent read changed retired ACK")
	}
}
