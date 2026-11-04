package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

func advisoryDirectoryAt(t *testing.T, f workspaceStoreFixture) sql.NullString {
	t.Helper()
	var at sql.NullString
	pairOK(t, f.raw.QueryRow(`SELECT directory_at FROM peer_contacts WHERE identity_key=?`, f.key).Scan(&at))
	return at
}

func advisoryDirectoryTime(t *testing.T, f workspaceStoreFixture, want time.Time) {
	t.Helper()
	at := advisoryDirectoryAt(t, f)
	if !at.Valid {
		t.Fatal("received directory has no observation timestamp")
	}
	got, err := time.Parse(time.RFC3339Nano, at.String)
	pairOK(t, err)
	pairEqual(t, got.Equal(want), true)
}

func TestReviewAdvisoriesStoreAcceptanceEmptyAndUnchangedDirectoryAreKnown(t *testing.T) {
	f := workspaceStore(t)
	pairEqual(t, advisoryDirectoryAt(t, f).Valid, false)
	ctx := context.Background()
	for i, agents := range [][]string{{}, {}, {"reviewer"}, {"reviewer"}, {}} {
		at := messageAt.Add(time.Duration(i+1) * time.Minute)
		_, err := f.db.PeerMessages().ReplacePeerDirectory(ctx, f.key, agents, at)
		pairOK(t, err)
		advisoryDirectoryTime(t, f, at)
		listed, err := f.db.PeerMessages().PeerDirectory(ctx, f.key)
		pairOK(t, err)
		pairEqual(t, len(listed), len(agents))
	}
}

func TestReviewAdvisoriesStoreAcceptanceInvalidDirectoryCannotRefreshObservation(t *testing.T) {
	f := workspaceStore(t)
	ctx := context.Background()
	pairOK(t, f.db.PeerMessages().SetPeerDirectory(ctx, f.key, []string{"reviewer"}, messageAt))
	_, err := f.db.PeerMessages().ReplacePeerDirectory(ctx, f.key, []string{""}, messageAt.Add(time.Minute))
	if err == nil {
		t.Fatal("invalid directory accepted")
	}
	advisoryDirectoryTime(t, f, messageAt)
	listed, err := f.db.PeerMessages().PeerDirectory(ctx, f.key)
	pairOK(t, err)
	pairEqual(t, len(listed), 1)
	pairEqual(t, listed[0], "reviewer")
}

func advisoryQueueOperatorAsk(t *testing.T, f *operatorContactFixture, id string, at time.Time) {
	t.Helper()
	_, err := f.db.OperatorContactSend(context.Background(), store.ContactSend{
		OperationID: id, Contact: f.contact, Text: "review requested", To: []string{"target"}, Review: remoteAsk(id).Review,
		AuditID: f.audit(t, contactAuditInput{"send", id}), Now: at,
	})
	pairOK(t, err)
}

func advisoryRequireStoredRefusal(t *testing.T, f *operatorContactFixture, id string) {
	t.Helper()
	advice, err := store.ReviewAdvisories(context.Background(), f.raw, f.contact, "target")
	pairOK(t, err)
	if len(advice) != 1 {
		t.Fatalf("retained not_shared evidence produced %d warnings, want one: %+v", len(advice), advice)
	}
	pairEqual(t, advice[0].Code, "refused_not_shared")
	pairEqual(t, advice[0].AskID, id)
}

func TestReviewAdvisoriesStoreAcceptanceLocalCollectionDoesNotBecomeRemoteProgress(t *testing.T) {
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	start := time.Now().UTC().Add(-time.Minute)
	advisoryQueueOperatorAsk(t, f, "A", start)
	// A really left this workspace; collection must not take the never-sent
	// expiry path when it later loses its confirmed contact.
	pairOK(t, f.db.PeerMessages().Delivered(ctx, f.contact, "A", start.Add(time.Second)))
	a, err := f.db.PeerMessages().AcceptOutcome(ctx, f.contact, messaging.ReviewOutcome{AskID: "A", Reviewer: "target", Seq: 1, State: "requested"}, start.Add(2*time.Second))
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
	advisoryQueueOperatorAsk(t, f, "B", start.Add(3*time.Second))
	pairOK(t, f.db.PeerMessages().Refused(ctx, f.contact, messaging.Refused{ID: "B", Refusal: messaging.Refuse(messaging.ReasonNotShared)}))
	refusal, found, err := f.db.PeerMessages().AskOutFor(ctx, f.contact, "B")
	pairOK(t, err)
	pairEqual(t, found, true)
	advisoryRequireStoredRefusal(t, f, "B")
	// Withdraw the authoritative contact pin as the existing pin-loss
	// fixtures do. Let the public collector perform the actual settlement;
	// no ask state or evidence timestamp is written by the fixture.
	_, err = f.raw.Exec(`DELETE FROM peer_contacts WHERE identity_key=?`, f.contact)
	pairOK(t, err)
	pairOK(t, f.db.PeerMessages().Collect(ctx, refusal.UpdatedAt.Add(time.Second)))
	settled, found, err := f.db.PeerMessages().AskOutFor(ctx, f.contact, "A")
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, settled.State, "contact_removed")
	pairEqual(t, settled.Seq, uint64(1))
	pairEqual(t, settled.UpdatedAt.After(refusal.UpdatedAt), true)
	advisoryRequireStoredRefusal(t, f, "B")
}
