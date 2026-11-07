package store_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

func TestContactRepairSweepAcceptanceRetiredIdentitiesDoNotConsumeCurrentCap(t *testing.T) {
	f := namespaceFixture(t)
	namespaceAccept(t, f, messageWire("retired-cap", 1))
	_, err := f.db.PeerMessages().MarkRead(context.Background(), f.key, []string{"retired-cap"}, messageAt)
	pairOK(t, err)
	messageSeedSettled(t, f.raw, f.key, messaging.Tombstones)
	namespaceRepair(t, f)
	answer, err := wireBound(t, f).Accept(context.Background(), f.key, messageWire("fresh-after-cap", 1), messageAt)
	pairOK(t, err)
	if !answer.Stored {
		t.Fatalf("retired identities consumed new pairing capacity: %+v", answer)
	}
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_messages WHERE pairing_epoch=1`), messaging.Tombstones)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_messages WHERE pairing_epoch=2`), 1)
}

func TestContactRepairSweepAcceptancePruneDoesNotUseOtherEpochMessageAsParent(t *testing.T) {
	f := namespaceFixture(t)
	namespaceAccept(t, f, namespaceMessage("prune-shared", "old body"))
	namespaceRepair(t, f)
	_, err := f.raw.Exec(`DELETE FROM peer_messages WHERE pairing_epoch=1`)
	pairOK(t, err)
	namespaceShare(t, f, "recipient")
	namespaceAccept(t, f, namespaceMessage("prune-shared", "new body"))
	fresh := namespaceEpochRows(t, f, "remote_imports", 2)
	pairOK(t, f.db.PeerMessages().Collect(context.Background(), messageAt))
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM remote_imports WHERE pairing_epoch=1`), 0)
	if !reflect.DeepEqual(fresh, namespaceEpochRows(t, f, "remote_imports", 2)) {
		t.Fatal("pruning retired import changed current pending import")
	}
}

func TestContactRepairSweepAcceptancePruneOutcomeNumberingMatchesImportEpoch(t *testing.T) {
	c := newConsentPublishCase(t, "once", true)
	c.decide(t)
	c.dispatch(t)
	namespaceRepair(t, c.f)
	_, err := c.f.raw.Exec(`DELETE FROM remote_imports WHERE pairing_epoch=1`)
	pairOK(t, err)
	_, err = c.f.raw.Exec(`DELETE FROM review_outcomes_out WHERE pairing_epoch=1`)
	pairOK(t, err)
	_, err = c.f.db.PeerMessages().SetReviewGrant(context.Background(), c.f.key, store.ReviewGrantChange{Agents: []string{"reviewer"}, Mode: store.ReviewAuto, Publish: true, Formal: true, Now: messageAt})
	pairOK(t, err)
	namespaceAccept(t, c.f, c.m)
	current := namespaceEpochRows(t, c.f, "review_outcome_seq", 2)
	imports := namespaceEpochRows(t, c.f, "remote_imports", 2)
	pairOK(t, c.f.db.PeerMessages().Collect(context.Background(), messageAt))
	pairEqual(t, epochCount(t, c.f.raw, `SELECT count(*) FROM review_outcome_seq WHERE pairing_epoch=1`), 0)
	if !reflect.DeepEqual(current, namespaceEpochRows(t, c.f, "review_outcome_seq", 2)) {
		t.Error("pruning retired numbering changed current numbering")
	}
	if !reflect.DeepEqual(imports, namespaceEpochRows(t, c.f, "remote_imports", 2)) {
		t.Error("pruning retired numbering changed current import")
	}
}
