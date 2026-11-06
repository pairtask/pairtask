package store_test

import (
	"context"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

func wireBound(t *testing.T, f workspaceStoreFixture) *store.PeerMessages {
	t.Helper()
	p, err := f.db.PeerMessages().Admit(context.Background(), f.key)
	pairOK(t, err)
	return p
}

func wireQueue(t *testing.T, f workspaceStoreFixture, id, text string) {
	t.Helper()
	_, err := f.db.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: f.key, ID: id, Text: text, To: []string{"target"}, Now: messageAt})
	pairOK(t, err)
}

func (f workspaceStoreFixture) wireImported(t *testing.T, id, text string, seq uint64) store.ImportResult {
	t.Helper()
	namespaceAccept(t, f, namespaceMessage(id, text))
	imp := namespacePending(t, f, id)
	result, err := f.db.ImportRemote(context.Background(), imp, seq, messageAt)
	pairOK(t, err)
	pairEqual(t, result.State, store.ImportImported)
	return result
}

func wireMutationSnapshot(t *testing.T, f workspaceStoreFixture) map[string][]string {
	t.Helper()
	tables := append([]string{}, epochRebuildOrder...)
	tables = append(tables, "peer_contacts", "peer_pairing_requests", "pairing_exchanges", "pairing_marks", "operator_audit", "operator_contact_requests", "messages", "message_metadata", "durable_messages", "durable_inbox", "review_events", "paired_threads", "paired_reply_outcomes")
	out := map[string][]string{}
	for _, table := range tables {
		if epochCount(t, f.raw, `SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?`, table) == 0 {
			continue
		}
		out[table] = epochRows(t, f.raw, table, epochColumns(t, f.raw, table))
	}
	return out
}

func wireCurrentState(t *testing.T, f workspaceStoreFixture, id string) string {
	t.Helper()
	var state string
	pairOK(t, f.raw.QueryRow(`SELECT state FROM peer_messages WHERE contact=? AND pairing_epoch=2 AND direction='out' AND message_id=?`, f.key, id).Scan(&state))
	return state
}
