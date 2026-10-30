package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

type workspaceStoreFixture struct {
	db  *store.Store
	raw *sql.DB
	key string
}

func workspaceStore(t *testing.T) workspaceStoreFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workspaces.db")
	db := pairSQLite(t, path)
	raw, err := sql.Open("sqlite", path)
	pairOK(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	key := messageContact(t, db, 1)
	routingRegister(t, db, "sender")
	_, err = db.PeerMessages().Share(context.Background(), key, []string{"sender"}, messageAt)
	pairOK(t, err)
	return workspaceStoreFixture{db, raw, key}
}

func (f workspaceStoreFixture) request(id string) store.WorkspaceSend {
	return store.WorkspaceSend{Msg: protocol.Message{Type: protocol.TypeMsg, MessageID: id, Seq: 1, TS: &messageAt, From: "sender", To: "target", Origin: protocol.OriginAgent, Payload: []byte(`{"text":"hello"}`)}, SenderSession: "session-sender", Contact: f.key, Text: "hello", Now: messageAt}
}

func TestWorkspaceStoreAcceptanceNameReassignmentCannotRerouteRetry(t *testing.T) {
	f := workspaceStore(t)
	ctx := context.Background()
	w := f.request("stable")
	key, err := f.db.ResolveWorkspace(ctx, "Alice")
	pairOK(t, err)
	pairEqual(t, key, f.key)
	_, err = f.db.QueueWorkspaceSend(ctx, w)
	pairOK(t, err)
	other := messageContact(t, f.db, 2)
	_, err = f.db.PeerMessages().Share(ctx, other, []string{"sender"}, messageAt)
	pairOK(t, err)
	_, err = f.raw.Exec("UPDATE peer_contacts SET name = 'renamed' WHERE identity_key = ?", f.key)
	pairOK(t, err)
	w.Contact, err = f.db.ResolveWorkspace(ctx, "Alice")
	pairOK(t, err)
	pairEqual(t, w.Contact, other)
	w.Msg.Seq = 2
	_, err = f.db.QueueWorkspaceSend(ctx, w)
	if !errors.Is(err, store.ErrDuplicateMessage) {
		t.Fatalf("name reassignment retry: %v", err)
	}
	pairEqual(t, len(messagePage(t, f.db.PeerMessages(), other)), 0)
}

func TestWorkspaceStoreAcceptanceQueueFailureRollsBackHistory(t *testing.T) {
	f := workspaceStore(t)
	_, err := f.raw.Exec(`CREATE TRIGGER fail_workspace_queue BEFORE INSERT ON peer_messages BEGIN SELECT RAISE(ABORT, 'injected queue failure'); END`)
	pairOK(t, err)
	w := f.request("atomic")
	_, err = f.db.QueueWorkspaceSend(context.Background(), w)
	if err == nil {
		t.Fatal("injected queue failure succeeded")
	}
	var count int
	pairOK(t, f.raw.QueryRow("SELECT COUNT(*) FROM message_metadata WHERE message_id = ?", w.Msg.MessageID).Scan(&count))
	pairEqual(t, count, 0)
	_, err = f.raw.Exec("DROP TRIGGER fail_workspace_queue")
	pairOK(t, err)
	result, err := f.db.QueueWorkspaceSend(context.Background(), w)
	pairOK(t, err)
	pairEqual(t, result.Repeated, false)
	pairEqual(t, len(messagePage(t, f.db.PeerMessages(), f.key)), 1)
}

func TestWorkspaceStoreAcceptanceLocalMessageIDCannotBeReused(t *testing.T) {
	f := workspaceStore(t)
	w := f.request("local-first")
	local := w.Msg
	local.To = "local-recipient"
	pairOK(t, f.db.PersistMessage(context.Background(), local, w.SenderSession, nil))
	w.Msg.Seq = 2
	_, err := f.db.QueueWorkspaceSend(context.Background(), w)
	if !errors.Is(err, store.ErrDuplicateMessage) {
		t.Fatalf("local ID reused remotely: %v", err)
	}
	pairEqual(t, len(messagePage(t, f.db.PeerMessages(), f.key)), 0)
}
