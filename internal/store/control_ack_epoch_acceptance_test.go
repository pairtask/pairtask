package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func TestControlAckEpochAcceptanceCurrentStoreCannotAcknowledgeRetiredPairing(t *testing.T) {
	f, _, oldID := controlStoreFixture(t)
	out, _, err := f.db.UnpairContact(context.Background(), unpairRequest(t, f, "old-control-notice"))
	pairOK(t, err)
	f.installedEpoch(t, 102)
	fresh, err := f.db.PeerMessages().AdmitControl(context.Background(), f.contact)
	pairOK(t, err)
	before := epochRows(t, f.raw, "contact_notices_out", epochColumns(t, f.raw, "contact_notices_out"))
	err = fresh.NoticeStored(context.Background(), messaging.ContactStateStored{PairingID: oldID, Gen: uint64(out.Generation)})
	pairEqual(t, errors.Is(err, messaging.ErrProtocol), true)
	after := epochRows(t, f.raw, "contact_notices_out", epochColumns(t, f.raw, "contact_notices_out"))
	if !reflect.DeepEqual(before, after) {
		t.Fatal("current control store changed another pairing's notice")
	}
}
