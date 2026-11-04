package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"
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
