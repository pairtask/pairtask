package store_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func TestPeerSealEpochAcceptanceReusedIDKeepsEachPairingsSeal(t *testing.T) {
	f := namespaceFixture(t)
	ctx := context.Background()
	const id = "reused-sealed-id"
	firstSeal, secondSeal := []byte("first pairing seal"), []byte("second pairing seal")
	first, err := f.db.PeerMessages().Queue(ctx, messaging.Outgoing{
		Contact: f.key, ID: id, Text: "old pairing", Now: messageAt, Seal: firstSeal,
	})
	pairOK(t, err)
	if first.PairingEpoch != 1 || !bytes.Equal(first.Seal, firstSeal) {
		t.Fatalf("first pairing seal: %+v", first)
	}
	namespaceRepair(t, f)
	second, err := f.db.PeerMessages().Queue(ctx, messaging.Outgoing{
		Contact: f.key, ID: id, Text: "new pairing", Now: messageAt, Seal: secondSeal,
	})
	pairOK(t, err)
	if second.PairingEpoch != 2 || !bytes.Equal(second.Seal, secondSeal) {
		t.Fatalf("reused ID in new pairing did not keep its own seal: %+v", second)
	}
	page := messagePage(t, f.db.PeerMessages(), f.key)
	if len(page) != 2 {
		t.Fatalf("both pairing histories not retained: %+v", page)
	}
	for _, row := range page {
		want := firstSeal
		if row.PairingEpoch == 2 {
			want = secondSeal
		}
		if !bytes.Equal(row.Seal, want) {
			t.Fatalf("epoch %d picked another pairing's seal: %+v", row.PairingEpoch, row)
		}
	}
}
