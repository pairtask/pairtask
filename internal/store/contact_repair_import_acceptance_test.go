package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/store"
)

func TestContactRepairImportAcceptanceReusedIDKeepsHistoryAndDistinctLocalReservation(t *testing.T) {
	f := namespaceFixture(t)
	namespaceAccept(t, f, namespaceMessage("reused-id", "old epoch body"))
	old := namespacePending(t, f, "reused-id")
	namespaceRepair(t, f)
	oldRows := namespaceEpochRows(t, f, "peer_messages", 1)
	oldImports := namespaceEpochRows(t, f, "remote_imports", 1)
	namespaceShare(t, f, "recipient")
	namespaceAccept(t, f, namespaceMessage("reused-id", "new epoch body"))
	fresh := namespacePending(t, f, "reused-id")
	pairEqual(t, fresh.Epoch, int64(2))
	pairEqual(t, fresh.Text, "new epoch body")
	if fresh.LocalID == old.LocalID {
		t.Fatal("new epoch reused a local ID reserved by the retired staged import")
	}
	result, err := f.db.ImportRemote(context.Background(), fresh, 12, messageAt)
	pairOK(t, err)
	pairEqual(t, result.State, store.ImportImported)
	var source int64
	pairOK(t, f.raw.QueryRow(`SELECT remote_epoch FROM message_metadata WHERE message_id=?`, result.Msg.MessageID).Scan(&source))
	pairEqual(t, source, int64(2))
	if !reflect.DeepEqual(oldRows, namespaceEpochRows(t, f, "peer_messages", 1)) {
		t.Error("new import changed retired message history")
	}
	if !reflect.DeepEqual(oldImports, namespaceEpochRows(t, f, "remote_imports", 1)) {
		t.Error("new import changed retired staged evidence")
	}
	page := messagePage(t, f.db.PeerMessages(), f.key)
	texts := map[string]bool{}
	for _, r := range page {
		texts[r.Text] = true
	}
	if !texts["old epoch body"] || !texts["new epoch body"] {
		t.Fatal("contact history lost one pairing's message body")
	}
}

func TestContactRepairImportAcceptanceStaleSnapshotAndLegacyZeroCannotRetarget(t *testing.T) {
	f := namespaceFixture(t)
	namespaceAccept(t, f, namespaceMessage("stale-import", "retired source"))
	stale := namespacePending(t, f, "stale-import")
	namespaceRepair(t, f)
	op := &operatorContactFixture{db: f.db, raw: f.raw, contact: f.key}
	before := unpairSnapshot(t, op)
	for _, epoch := range []int64{1, 0} {
		stale.Epoch = epoch
		_, err := f.db.ImportRemote(context.Background(), stale, 11, messageAt)
		if !errors.Is(err, store.ErrUnpaired) {
			t.Errorf("stale epoch %d: expected ErrUnpaired family, got %v", epoch, err)
		}
		if !reflect.DeepEqual(before, unpairSnapshot(t, op)) {
			t.Fatal("stale async snapshot changed pairing or import state")
		}
	}
}

func TestContactRepairImportAcceptanceRetainedGrantCannotAuthorizeNewPairing(t *testing.T) {
	f := namespaceFixture(t)
	namespaceRepair(t, f)
	metadata := epochRows(t, f.raw, "message_metadata", epochColumns(t, f.raw, "message_metadata"))
	a, err := f.db.PeerMessages().Accept(context.Background(), f.key, namespaceMessage("old-authority", "must not import"), messageAt)
	pairOK(t, err)
	if a.Stored {
		t.Error("retained epoch-1 grant authorized an epoch-2 legacy message")
	}
	if !reflect.DeepEqual(metadata, epochRows(t, f.raw, "message_metadata", epochColumns(t, f.raw, "message_metadata"))) {
		t.Fatal("unapproved message entered a durable inbox")
	}
	namespaceShare(t, f, "recipient")
	approved := namespaceMessage("explicit-new-authority", "approved new body")
	approved.N = 2
	namespaceAccept(t, f, approved)
	fresh := namespacePending(t, f, "explicit-new-authority")
	pairEqual(t, fresh.Epoch, int64(2))
	result, err := f.db.ImportRemote(context.Background(), fresh, 13, messageAt)
	pairOK(t, err)
	pairEqual(t, result.State, store.ImportImported)
}
