package messaging_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

// v9PeerStore presents the same real SQLite store without the v10 SealStore
// capability, as an older peer does during version negotiation.
type v9PeerStore interface {
	messaging.ConsentStore
	messaging.OutcomeStore
	messaging.EpochStore
	messaging.VersionRecorder
}

type legacySealPeer struct{ v9PeerStore }

func TestSealSessionAcceptanceNewAndV9BothDirections(t *testing.T) {
	newSide, _ := sessionFixture(t)
	oldSide, _ := sessionFixture(t)
	ctx := context.Background()
	seal := []byte("opaque signature from new side")
	_, err := newSide.PeerMessages().Queue(ctx, messaging.Outgoing{
		Contact: sessionContact, ID: "new-to-old", Text: "legacy recipient", Now: sessionAt, Seal: seal,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = oldSide.PeerMessages().Queue(ctx, messaging.Outgoing{
		Contact: sessionContact, ID: "old-to-new", Text: "unsigned sender", Now: sessionAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionSuccess(t, sessionExchange(t, newSide.PeerMessages(), legacySealPeer{oldSide.PeerMessages()}, 0))
	assertNewSideLegacyRows(t, sessionRows(t, newSide.PeerMessages(), 2), seal)
	assertOldSideLegacyRows(t, sessionRows(t, oldSide.PeerMessages(), 2))
}

func assertNewSideLegacyRows(t *testing.T, rows []messaging.Row, seal []byte) {
	t.Helper()
	for _, row := range rows {
		switch row.ID {
		case "new-to-old":
			if row.State != messaging.StateDelivered || !row.SealDowngraded || !bytes.Equal(row.Seal, seal) {
				t.Fatalf("new sender lost downgrade evidence: %+v", row)
			}
		case "old-to-new":
			if row.Direction != messaging.DirIn || len(row.Seal) != 0 {
				t.Fatalf("legacy message appeared signed: %+v", row)
			}
		default:
			t.Fatalf("unexpected new-side row: %+v", row)
		}
	}
}

func assertOldSideLegacyRows(t *testing.T, rows []messaging.Row) {
	t.Helper()
	for _, row := range rows {
		if row.ID == "new-to-old" && (row.Direction != messaging.DirIn || len(row.Seal) != 0) {
			t.Fatalf("v9 peer received a v10 seal: %+v", row)
		}
	}
}

func TestSealSessionAcceptanceNewPeersCarrySealEndToEnd(t *testing.T) {
	left, _ := sessionFixture(t)
	right := sealInboundFixture(t)
	ctx := context.Background()
	base := sealBaseInbound()
	m, keyID := sealOperatorMessage(t, base, sealSigner{"session-person", "session-device", sessionContact})
	_, err := left.PeerMessages().Queue(ctx, messaging.Outgoing{
		Contact: sessionContact, ID: m.ID, Text: m.Text, Now: sessionAt, Seal: m.Seal,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionSuccess(t, sessionExchange(t, left.PeerMessages(), right.PeerMessages(), 0))
	rows := sessionRows(t, right.PeerMessages(), 1)
	if rows[0].Direction != messaging.DirIn {
		t.Fatalf("v10 recipient row has wrong direction: %+v", rows[0])
	}
	if !bytes.Equal(rows[0].Seal, m.Seal) {
		t.Fatalf("v10 session did not carry seal end to end: %+v", rows[0])
	}
	keys, err := right.PeerMessages().PendingPersonKeys(ctx, sessionContact)
	if err != nil || len(keys) != 1 || keys[0] != keyID {
		t.Fatalf("recipient did not notice signer key: keys=%v err=%v", keys, err)
	}
}

func TestSealSessionAcceptanceReceiverRefusesTamperedSignedTraffic(t *testing.T) {
	left, _ := sessionFixture(t)
	right := sealInboundFixture(t)
	ctx := context.Background()
	m, _ := sealOperatorMessage(t, sealBaseInbound(), sealSigner{"wire-person", "wire-device", sessionContact})
	_, err := left.PeerMessages().Queue(ctx, messaging.Outgoing{
		Contact: sessionContact, ID: m.ID, Text: "relay altered the signed text", Now: sessionAt, Seal: m.Seal,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionSuccess(t, sessionExchange(t, left.PeerMessages(), right.PeerMessages(), 0))
	leftRows := sessionRows(t, left.PeerMessages(), 1)
	if leftRows[0].State != messaging.StateRefused || leftRows[0].Refusal.Reason != messaging.ReasonInvalidSeal {
		t.Fatalf("sender did not learn receiver refused tampering: %+v", leftRows[0])
	}
	rightRows, _, err := right.PeerMessages().Page(ctx, sessionContact, 0, 10)
	if err != nil || len(rightRows) != 0 {
		t.Fatalf("recipient persisted tampered signed traffic: rows=%+v err=%v", rightRows, err)
	}
}
