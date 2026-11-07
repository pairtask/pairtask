package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func staleStoreOutcome(t *testing.T, f workspaceStoreFixture, reason string) protocol.Message {
	t.Helper()
	o := messaging.ReviewOutcome{AskID: "ask", Reviewer: "target", Seq: 2, State: "declined", Reason: reason}
	result, err := f.db.PeerMessages().AcceptOutcome(context.Background(), f.key, o, messageAt)
	pairOK(t, err)
	pairEqual(t, result.Stored, true)
	imports, err := f.db.PendingImports(context.Background(), 10)
	pairOK(t, err)
	for _, imp := range imports {
		if imp.Kind != "outcome" {
			continue
		}
		result, err := f.db.ImportRemote(context.Background(), imp, 3, messageAt)
		pairOK(t, err)
		return result.Msg
	}
	t.Fatal("no outcome to import")
	return protocol.Message{}
}

func staleStoreJSON(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	pairOK(t, err)
	var fields map[string]any
	pairOK(t, json.Unmarshal(data, &fields))
	return fields
}

func staleStoreRestart(t *testing.T, f workspaceStoreFixture) *store.Store {
	t.Helper()
	var seq int
	var name, path string
	pairOK(t, f.raw.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path))
	pairOK(t, f.db.Close())
	reopened, err := store.Open(path)
	pairOK(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	return reopened
}

func staleStoreReplay(t *testing.T, db *store.Store, version int) map[string]protocol.Message {
	t.Helper()
	messages, err := db.ClaimPendingFor(context.Background(), protocol.Recipient{AgentID: "sender", SessionID: "restart"}, version)
	pairOK(t, err)
	result := map[string]protocol.Message{}
	for _, m := range messages {
		result[m.MessageID] = m
	}
	return result
}
