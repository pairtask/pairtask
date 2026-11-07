package store_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
)

func TestPairingAddressMoveAcceptanceReceiptIsTerminalAndKeepsInstalledAuthority(t *testing.T) {
	f := installFixture(t)
	ctx := context.Background()
	original := f.installedEpoch(t, 101)
	before := f.exchange(t, original.OperationID)
	join := f.epochJoinReady(t, "address-move-join")
	pairEqual(t, string(join.State), "address_moved")
	pairEqual(t, join.DecisionConfirmedAt.IsZero(), true)
	pairEqual(t, f.exchange(t, join.OperationID).Kind, peer.KindAddressMove)
	pairEqual(t, f.exchange(t, join.OperationID).Reason, peer.RefusedAddressMove)
	if !reflect.DeepEqual(before, f.exchange(t, original.OperationID)) {
		t.Fatal("address move replaced the installed pairing")
	}
	replayed, err := f.db.RecordReceipt(ctx, join.OperationID, joinReceipt(join), joinAt)
	pairOK(t, err)
	pairEqual(t, string(replayed.State), "address_moved")
	pairEqual(t, replayed.DecisionConfirmedAt.IsZero(), true)
	confirmed, err := f.db.PeerMessages().Confirmed(ctx, f.contact)
	pairOK(t, err)
	pairEqual(t, confirmed, true)
}
