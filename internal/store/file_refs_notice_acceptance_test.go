package store_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

func refsNoticeOffer(t *testing.T, f workspaceStoreFixture) {
	t.Helper()
	limit := messaging.SessionLimit
	limit.Version = 11
	offer, err := f.db.PeerMessages().Offer(context.Background(), f.key, messageAt, limit)
	pairOK(t, err)
	pairEqual(t, len(offer), 0)
}

func TestFileRefsNoticeAcceptanceQueuedAgentGetsOneAttributedFailure(t *testing.T) {
	f := workspaceStore(t)
	w := f.request("refs-not-carried")
	data, err := json.Marshal(map[string]any{"files": refsStoreMetadata()})
	pairOK(t, err)
	pairOK(t, json.Unmarshal(data, &w.Msg))
	_, err = f.db.QueueWorkspaceSend(context.Background(), w)
	pairOK(t, err)
	refsNoticeOffer(t, f)
	refsNoticeOffer(t, f)
	imports, err := f.db.PendingImports(context.Background(), 100)
	pairOK(t, err)
	pairEqual(t, len(imports), 1)
	imp := imports[0]
	pairEqual(t, imp.Contact, f.key)
	pairEqual(t, imp.Sender, "pairtask")
	pairEqual(t, len(imp.Recipients), 1)
	pairEqual(t, imp.Recipients[0], "sender")
	for _, want := range []string{"refs-not-carried", "refs_unsupported", "Nothing was sent without them", "Don't reply"} {
		if !strings.Contains(imp.Text, want) {
			t.Fatalf("failure notice lacks %q: %s", want, imp.Text)
		}
	}
	result := refusalImport(t, f, imp)
	pairEqual(t, result.Msg.To, "sender")
	pairEqual(t, result.Msg.From, "pairtask")
	pairEqual(t, result.Msg.Origin, protocol.OriginRemote)
	pairEqual(t, result.Msg.Durable, true)
	pairEqual(t, result.Msg.AckRequested, true)
	if result.Msg.Remote == nil {
		t.Fatal("failure notice lost workspace attribution")
	}
	pairEqual(t, result.Msg.Remote.Contact, f.key)
	refsNoticeOffer(t, f)
	imports, err = f.db.PendingImports(context.Background(), 100)
	pairOK(t, err)
	pairEqual(t, len(imports), 0)
}

func TestFileRefsNoticeAcceptanceOperatorFailureDoesNotNotifyAgents(t *testing.T) {
	f := workspaceStore(t)
	_, err := f.db.PeerMessages().Queue(context.Background(), refsStoreOutgoing(t, f.key, refsStoreMetadata()))
	pairOK(t, err)
	refsNoticeOffer(t, f)
	imports, err := f.db.PendingImports(context.Background(), 100)
	pairOK(t, err)
	pairEqual(t, len(imports), 0)
	rows := messagePage(t, f.db.PeerMessages(), f.key)
	pairEqual(t, len(rows), 1)
	pairEqual(t, rows[0].Refusal.Reason, "refs_unsupported")
}
