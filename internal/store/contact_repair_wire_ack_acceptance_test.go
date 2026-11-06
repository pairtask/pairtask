package store_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/store"
)

func TestContactRepairWireAcceptanceOperatorAckIgnoresRetiredReusedID(t *testing.T) {
	f := namespaceFixture(t)
	namespaceAccept(t, f, messageWire("same-ack", 1))
	namespaceRepair(t, f)
	old := namespaceEpochRows(t, f, "peer_messages", 1)
	namespaceAccept(t, f, messageWire("same-ack", 1))
	summary, err := f.db.PeerMessages().AckSummary(context.Background(), f.key)
	pairOK(t, err)
	pairEqual(t, summary.Unacked, 1)
	n, after, err := f.db.PeerMessages().Ack(context.Background(), f.key, store.AckSelector{IDs: []string{"same-ack"}}, messageAt)
	pairOK(t, err)
	pairEqual(t, n, 1)
	pairEqual(t, after.Unacked, 0)
	if !reflect.DeepEqual(old, namespaceEpochRows(t, f, "peer_messages", 1)) {
		t.Fatal("current operator ACK acknowledged retired message")
	}
}

func TestContactRepairWireAcceptanceAckSummaryIgnoresHigherRetiredFloor(t *testing.T) {
	f := namespaceFixture(t)
	namespaceAccept(t, f, messageWire("old-floor", 1))
	_, err := f.raw.Exec(`INSERT INTO peer_ack_floors(contact,pairing_epoch,floor,expired) VALUES (?,1,100000,7)
 ON CONFLICT(contact,pairing_epoch) DO UPDATE SET floor=100000,expired=7`, f.key)
	pairOK(t, err)
	namespaceRepair(t, f)
	old := namespaceEpochRows(t, f, "peer_ack_floors", 1)
	namespaceAccept(t, f, messageWire("new-ack", 1))
	summary, err := f.db.PeerMessages().AckSummary(context.Background(), f.key)
	pairOK(t, err)
	pairEqual(t, summary.Unacked, 1)
	pairEqual(t, summary.AckExpired, 0)
	summaries, err := f.db.PeerMessages().AckSummaries(context.Background())
	pairOK(t, err)
	pairEqual(t, summaries[f.key], summary)
	n, after, err := f.db.PeerMessages().Ack(context.Background(), f.key, store.AckSelector{Upto: summary.AckUpto}, messageAt)
	pairOK(t, err)
	pairEqual(t, n, 1)
	pairEqual(t, after.Unacked, 0)
	if !reflect.DeepEqual(old, namespaceEpochRows(t, f, "peer_ack_floors", 1)) {
		t.Fatal("new ACK changed retired floor or expiry count")
	}
}
