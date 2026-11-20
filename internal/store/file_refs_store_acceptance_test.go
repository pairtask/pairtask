package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func refsStoreMetadata() []map[string]any {
	return []map[string]any{{"path": "../opaque/report.bin", "size": float64(42), "sha256": strings.Repeat("a", 64)}}
}

func refsStoreOutgoing(t *testing.T, contact string, files []map[string]any) messaging.Outgoing {
	t.Helper()
	data, err := json.Marshal(map[string]any{"Contact": contact, "ID": "file-message", "Text": "metadata only", "Now": messageAt, "files": files})
	pairOK(t, err)
	var out messaging.Outgoing
	pairOK(t, json.Unmarshal(data, &out))
	return out
}

func refsStoreAssert(t *testing.T, value any, want []map[string]any) {
	t.Helper()
	data, err := json.Marshal(value)
	pairOK(t, err)
	var out struct {
		Files []map[string]any `json:"files"`
	}
	pairOK(t, json.Unmarshal(data, &out))
	if !reflect.DeepEqual(out.Files, want) {
		t.Fatalf("persisted references changed: got%v want%v", out.Files, want)
	}
}

func TestFileRefsStoreAcceptanceSurviveRestartAndOffer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "refs.db")
	db := pairSQLite(t, path)
	contact := messageContact(t, db, 1)
	refs := refsStoreMetadata()
	_, err := db.PeerMessages().Queue(context.Background(), refsStoreOutgoing(t, contact, refs))
	pairOK(t, err)
	pairOK(t, db.Close())
	db = pairSQLite(t, path)
	rows := messagePage(t, db.PeerMessages(), contact)
	pairEqual(t, len(rows), 1)
	refsStoreAssert(t, rows[0], refs)
	limit := messaging.SessionLimit
	limit.Version = 12
	offer, err := db.PeerMessages().Offer(context.Background(), contact, messageAt, limit)
	pairOK(t, err)
	pairEqual(t, len(offer), 1)
	refsStoreAssert(t, offer[0], refs)
}

func TestFileRefsStoreAcceptanceReplayCannotReplaceMetadata(t *testing.T) {
	db, contact := messageFixture(t)
	refs := refsStoreMetadata()
	request := refsStoreOutgoing(t, contact, refs)
	_, err := db.PeerMessages().Queue(context.Background(), request)
	pairOK(t, err)
	refs[0]["sha256"] = strings.Repeat("b", 64)
	_, err = db.PeerMessages().Queue(context.Background(), refsStoreOutgoing(t, contact, refs))
	if !errors.Is(err, messaging.ErrConflict) {
		t.Fatalf("metadata substitution was not a conflict: %v", err)
	}
}

func TestFileRefsStoreAcceptanceKnownOldPeerRefusedBeforeQueue(t *testing.T) {
	db, contact := messageFixture(t)
	bound, err := db.PeerMessages().Admit(context.Background(), contact)
	pairOK(t, err)
	versioned, ok := any(bound).(pairingVersionStore)
	if !ok {
		t.Fatal("missing version recorder")
	}
	pairOK(t, versioned.SawVersion(context.Background(), 11))
	_, err = db.PeerMessages().Queue(context.Background(), refsStoreOutgoing(t, contact, refsStoreMetadata()))
	if err == nil || !strings.Contains(err.Error(), "refs_unsupported") {
		t.Fatalf("known old peer not explicitly refused: %v", err)
	}
	pairEqual(t, len(messagePage(t, db.PeerMessages(), contact)), 0)
}

func TestFileRefsStoreAcceptanceUnknownPeerBecomesTerminalFailureAtOldVersion(t *testing.T) {
	db, contact := messageFixture(t)
	refs := refsStoreMetadata()
	_, err := db.PeerMessages().Queue(context.Background(), refsStoreOutgoing(t, contact, refs))
	pairOK(t, err)
	limit := messaging.SessionLimit
	limit.Version = 11
	for i := 0; i < 2; i++ {
		offer, err := db.PeerMessages().Offer(context.Background(), contact, messageAt, limit)
		pairOK(t, err)
		pairEqual(t, len(offer), 0)
	}
	rows := messagePage(t, db.PeerMessages(), contact)
	pairEqual(t, len(rows), 1)
	pairEqual(t, rows[0].State, messaging.StateRefused)
	pairEqual(t, rows[0].Refusal.Reason, "refs_unsupported")
	refsStoreAssert(t, rows[0], refs)
}
