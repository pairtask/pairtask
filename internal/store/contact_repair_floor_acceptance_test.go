package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

func TestContactRepairFloorAcceptanceCollectingOldSequenceCannotRaiseNewFloor(t *testing.T) {
	f := namespaceFixture(t)
	_, err := f.raw.Exec(`INSERT INTO peer_message_floors(contact,pairing_epoch,floor) VALUES (?,1,1000)`, f.key)
	pairOK(t, err)
	namespaceAccept(t, f, messageWire("retired-floor", 1000))
	_, err = f.db.PeerMessages().MarkRead(context.Background(), f.key, []string{"retired-floor"}, messageAt)
	pairOK(t, err)
	pairOK(t, f.db.PeerMessages().ReadConfirmed(context.Background(), f.key, "retired-floor"))
	n, _, err := f.db.PeerMessages().Ack(context.Background(), f.key, store.AckSelector{IDs: []string{"retired-floor"}}, messageAt)
	pairOK(t, err)
	pairEqual(t, n, 1)
	namespaceRepair(t, f)
	now := messageAt.Add(messaging.TombstoneAge + time.Hour)
	pairOK(t, f.db.PeerMessages().Collect(context.Background(), now))
	first := messageWire("new-first", 1)
	first.CreatedAt = now
	answer, err := wireBound(t, f).Accept(context.Background(), f.key, first, now)
	pairOK(t, err)
	if !answer.Stored {
		t.Fatalf("old sequence raised new pairing replay floor: %+v", answer)
	}
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_messages WHERE pairing_epoch=1`), 0)
	pairEqual(t, epochCount(t, f.raw, `SELECT floor FROM peer_message_floors WHERE pairing_epoch=1`), 1001)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_message_floors WHERE pairing_epoch=2 AND floor>1`), 0)
}
