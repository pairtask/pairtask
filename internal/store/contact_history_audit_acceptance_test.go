package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

func historyRead(t *testing.T, f workspaceStoreFixture, epoch int64, id string) (int, error) {
	t.Helper()
	audit := (&operatorContactFixture{db: f.db, raw: f.raw, contact: f.key}).audit(t, contactAuditInput{"read", id})
	return f.db.OperatorContactRead(context.Background(), store.ContactRead{OperationID: id, Contact: f.key, Epoch: epoch, IDs: []string{"history-shared"}, AuditID: audit, Now: messageAt})
}

func historyAck(t *testing.T, f workspaceStoreFixture, c store.ContactAck) (int, error) {
	t.Helper()
	c.Contact, c.Now = f.key, messageAt
	c.AuditID = (&operatorContactFixture{db: f.db, raw: f.raw, contact: f.key}).audit(t, contactAuditInput{"ack", c.OperationID})
	n, _, err := f.db.OperatorContactAck(context.Background(), c)
	return n, err
}

func TestContactHistoryAcceptanceAuditedReadSelectsOldEpochAndFingerprintsIt(t *testing.T) {
	f := sweepHistoryFixture(t)
	n, err := historyRead(t, f, 1, "history-read")
	pairOK(t, err)
	pairEqual(t, n, 1)
	var oldState, oldReceipt, currentState string
	pairOK(t, f.raw.QueryRow(`SELECT state,receipt FROM peer_messages WHERE pairing_epoch=1 AND message_id='history-shared'`).Scan(&oldState, &oldReceipt))
	pairEqual(t, oldState, messaging.StateRead)
	pairEqual(t, oldReceipt, "local")
	pairOK(t, f.raw.QueryRow(`SELECT state FROM peer_messages WHERE pairing_epoch=2 AND message_id='history-shared'`).Scan(&currentState))
	pairEqual(t, currentState, messaging.StateReceived)
	_, err = historyRead(t, f, 2, "history-read")
	pairEqual(t, errors.Is(err, messaging.ErrConflict), true)
}

func TestContactHistoryAcceptanceAuditedAckSelectsOldEpochAndFingerprintsIt(t *testing.T) {
	f := sweepHistoryFixture(t)
	c := store.ContactAck{OperationID: "history-ack", Epoch: 1, Selector: store.AckSelector{IDs: []string{"history-shared"}}}
	n, err := historyAck(t, f, c)
	pairOK(t, err)
	pairEqual(t, n, 1)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_messages WHERE pairing_epoch=1 AND acked_at IS NOT NULL`), 1)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_messages WHERE pairing_epoch=2 AND acked_at IS NOT NULL`), 0)
	c.Epoch = 2
	_, err = historyAck(t, f, c)
	pairEqual(t, errors.Is(err, messaging.ErrConflict), true)
}

func TestContactHistoryAcceptanceAckCursorBoundUsesSelectedEpoch(t *testing.T) {
	f := sweepHistoryFixture(t)
	var newer int64
	pairOK(t, f.raw.QueryRow(`SELECT cursor FROM peer_messages WHERE pairing_epoch=2 AND message_id='history-shared'`).Scan(&newer))
	_, err := historyAck(t, f, store.ContactAck{OperationID: "history-bound", Epoch: 1, Selector: store.AckSelector{Upto: newer}})
	pairEqual(t, errors.Is(err, store.ErrAckBound), true)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_messages WHERE acked_at IS NOT NULL`), 0)
}

func TestContactHistoryAcceptanceRemovedHistoryDoesNotCreateReceiptOrAuthority(t *testing.T) {
	f := sweepHistoryFixture(t)
	_, err := f.raw.Exec(`DELETE FROM peer_contacts WHERE identity_key=?`, f.key)
	pairOK(t, err)
	epoch, err := f.db.ContactEpoch(context.Background(), f.key)
	pairOK(t, err)
	pairEqual(t, epoch, int64(2))
	n, err := f.db.PeerMessages().MarkReadIn(context.Background(), store.ReadMark{Contact: f.key, Epoch: 2, IDs: []string{"history-shared"}, At: messageAt})
	pairOK(t, err)
	pairEqual(t, n, 1)
	var receipt string
	pairOK(t, f.raw.QueryRow(`SELECT receipt FROM peer_messages WHERE pairing_epoch=2 AND message_id='history-shared'`).Scan(&receipt))
	pairEqual(t, receipt, "local")
	_, err = f.db.PeerMessages().AgentAcksIn(context.Background(), f.key, 2, "history-shared")
	pairOK(t, err)
	_, err = f.db.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: f.key, ID: "no-authority", Text: "must refuse", Now: messageAt})
	pairEqual(t, errors.Is(err, messaging.ErrUnknownContact), true)
}

func TestContactHistoryAcceptanceOperationReplayCannotChangeEpoch(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		f := sweepHistoryFixture(t)
		_, err := historyRead(t, f, 1, "epoch-bound-read")
		pairOK(t, err)
		_, err = historyRead(t, f, 2, "epoch-bound-read")
		pairEqual(t, errors.Is(err, messaging.ErrConflict), true)
	})
	t.Run("ack", func(t *testing.T) {
		f := sweepHistoryFixture(t)
		c := store.ContactAck{OperationID: "epoch-bound-ack", Epoch: 1, Selector: store.AckSelector{IDs: []string{"history-shared"}}}
		_, err := historyAck(t, f, c)
		pairOK(t, err)
		c.Epoch = 2
		_, err = historyAck(t, f, c)
		pairEqual(t, errors.Is(err, messaging.ErrConflict), true)
	})
}

func TestContactHistoryAcceptanceLegacyOperationFingerprintsStayByteIdentical(t *testing.T) {
	f := sweepHistoryFixture(t)
	_, err := historyRead(t, f, 0, "legacy-read")
	pairOK(t, err)
	_, err = historyAck(t, f, store.ContactAck{OperationID: "legacy-ack", Selector: store.AckSelector{IDs: []string{"history-shared"}}})
	pairOK(t, err)
	// Captured from8539820 using the existing zero-epoch operations.
	for id, want := range map[string]string{
		"legacy-read": "37E6FE60B9745C8B5929B9B6F588B5437CC7CE57753478F70D6461C4EE7AD9A9",
		"legacy-ack":  "05B1E45443CD6BD204D7AD035BBABE8F3D5B3B594E2A336B9F5E52A0B42CB022",
	} {
		var fingerprint string
		pairOK(t, f.raw.QueryRow(`SELECT hex(fingerprint) FROM operator_contact_requests WHERE operation_id=?`, id).Scan(&fingerprint))
		pairEqual(t, fingerprint, want)
	}
}
