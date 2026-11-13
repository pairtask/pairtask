package messaging_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func TestSealStoreAcceptanceOutboundSealSurvivesRestartAndRetry(t *testing.T) {
	db, path := sessionFixture(t)
	ctx := context.Background()
	firstSeal := []byte{0, 1, 255, 42}
	o := messaging.Outgoing{Contact: sessionContact, ID: "signed-outbound", Text: "same content", Now: sessionAt, Seal: firstSeal}
	first, err := db.PeerMessages().Queue(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Seal, firstSeal) {
		t.Fatalf("queued seal=%x, want %x", first.Seal, firstSeal)
	}
	// Retrying the same send must return the same row and seal, with no new sequence.
	retry, err := db.PeerMessages().Queue(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if retry.N != first.N || !bytes.Equal(retry.Seal, firstSeal) {
		t.Fatalf("retry changed row: first=%+v retry=%+v", first, retry)
	}
	// The message identity hash excludes the seal, but a changed non-empty
	// signature for the same ID must still be rejected as a conflict.
	o.Seal = []byte("second seal")
	if _, err := db.PeerMessages().Queue(ctx, o); !errors.Is(err, messaging.ErrConflict) {
		t.Fatalf("different seal on retry: want conflict, got %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = sessionDB(t, path)
	rows, err := db.PeerMessages().Offer(ctx, sessionContact, sessionAt, messaging.Limit{Count: 5, Bytes: 1024, Version: messaging.SealVersion})
	if err != nil || len(rows) != 1 {
		t.Fatalf("offer after restart: rows=%+v err=%v", rows, err)
	}
	if rows[0].N != first.N || !bytes.Equal(rows[0].Seal, firstSeal) {
		t.Fatalf("restarted offer lost seal: %+v", rows[0])
	}
	page, _, err := db.PeerMessages().Page(ctx, sessionContact, 0, 10)
	if err != nil || len(page) != 1 || !bytes.Equal(page[0].Seal, firstSeal) {
		t.Fatalf("page after restart lost seal: rows=%+v err=%v", page, err)
	}
}

func TestSealStoreAcceptanceDowngradeIsVisibleAndDurable(t *testing.T) {
	db, path := sessionFixture(t)
	ctx := context.Background()
	seal := []byte("signed message")
	row, err := db.PeerMessages().Queue(ctx, messaging.Outgoing{
		Contact: sessionContact, ID: "old-peer", Text: "legacy delivery", Now: sessionAt, Seal: seal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if row.SealDowngraded {
		t.Fatal("newly queued seal is already marked downgraded")
	}
	if err := db.PeerMessages().SealDowngraded(ctx, sessionContact, row.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = sessionDB(t, path)
	page, _, err := db.PeerMessages().Page(ctx, sessionContact, 0, 10)
	if err != nil || len(page) != 1 {
		t.Fatalf("page after downgrade: rows=%+v err=%v", page, err)
	}
	if !page[0].SealDowngraded || !bytes.Equal(page[0].Seal, seal) {
		t.Fatalf("downgrade lost status or original seal: %+v", page[0])
	}
}
