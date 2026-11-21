package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

func TestFileRefsRetentionAcceptanceForgottenMessageDropsReferences(t *testing.T) {
	f := workspaceStore(t)
	ctx := context.Background()
	m := messageWire("collected-files", 1)
	data, err := json.Marshal(map[string]any{"files": refsStoreMetadata()})
	pairOK(t, err)
	pairOK(t, json.Unmarshal(data, &m))
	a, err := f.db.PeerMessages().Accept(ctx, f.key, m, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
	var count int
	pairOK(t, f.raw.QueryRow(`SELECT COUNT(*) FROM peer_message_files WHERE contact=?`, f.key).Scan(&count))
	pairEqual(t, count, 1)
	_, err = f.db.PeerMessages().MarkRead(ctx, f.key, []string{m.ID}, messageAt)
	pairOK(t, err)
	pairOK(t, f.db.PeerMessages().ReadConfirmed(ctx, f.key, m.ID))
	_, _, err = f.db.PeerMessages().Ack(ctx, f.key, store.AckSelector{IDs: []string{m.ID}}, messageAt)
	pairOK(t, err)
	later := messageAt.Add(messaging.TombstoneAge + time.Hour)
	pairOK(t, f.db.PeerMessages().Collect(ctx, later))
	pairEqual(t, len(messagePage(t, f.db.PeerMessages(), f.key)), 0)
	pairOK(t, f.raw.QueryRow(`SELECT COUNT(*) FROM peer_message_files WHERE contact=?`, f.key).Scan(&count))
	if count != 0 {
		t.Fatalf("forgotten message left %d orphan reference rows", count)
	}
	answer, err := f.db.PeerMessages().Accept(ctx, f.key, m, later)
	pairOK(t, err)
	pairEqual(t, answer.Refusal.Reason, messaging.ReasonTooOld)
}

func TestFileRefsRetentionAcceptanceUnreadMessageKeepsReferences(t *testing.T) {
	f := workspaceStore(t)
	ctx := context.Background()
	m := messageWire("unread-files", 1)
	files := refsStoreMetadata()
	data, err := json.Marshal(map[string]any{"files": files})
	pairOK(t, err)
	pairOK(t, json.Unmarshal(data, &m))
	a, err := f.db.PeerMessages().Accept(ctx, f.key, m, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
	pairOK(t, f.db.PeerMessages().Collect(ctx, messageAt.Add(365*24*time.Hour)))
	rows := messagePage(t, f.db.PeerMessages(), f.key)
	pairEqual(t, len(rows), 1)
	refsStoreAssert(t, rows[0], files)
}
