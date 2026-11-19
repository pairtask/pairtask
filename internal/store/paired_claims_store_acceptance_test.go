package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

type claimNoticeReceiver interface {
	AcceptClaimNotice(context.Context, string, messaging.ClaimNotice, time.Time) (messaging.ClaimAnswer, error)
}

func claimsBoundStore(t *testing.T, db *store.Store, contact string) claimNoticeReceiver {
	t.Helper()
	bound, err := db.PeerMessages().Admit(context.Background(), contact)
	pairOK(t, err)
	receiver, ok := any(bound).(claimNoticeReceiver)
	if !ok {
		t.Fatal("admitted store has no claim receiver")
	}
	return receiver
}

func claimsStoredSubject(t *testing.T, db *store.Store, contact string) messaging.ClaimNotice {
	t.Helper()
	_, err := db.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: contact, ID: "claim-subject", Text: "investigate", To: []string{"worker"}, Now: messageAt})
	pairOK(t, err)
	return messaging.ClaimNotice{MessageID: "claim-subject", Agent: "worker", Role: "owner", Task: "investigate", State: "active", Seq: 1, At: messageAt}
}

func claimsNoticeApply(t *testing.T, s claimNoticeReceiver, contact string, n messaging.ClaimNotice, stored bool, reason string) {
	t.Helper()
	answer, err := s.AcceptClaimNotice(context.Background(), contact, n, messageAt)
	pairOK(t, err)
	pairEqual(t, answer.Stored, stored)
	pairEqual(t, answer.Reason, reason)
}

func claimsHeldState(t *testing.T, db *store.Store, contact, state string) {
	t.Helper()
	set, err := db.SentMessageClaims(context.Background(), "claim-subject", "")
	pairOK(t, err)
	pairEqual(t, len(set.Claims), 1)
	claim := set.Claims[0]
	pairEqual(t, claim.State, state)
	pairEqual(t, claim.MessageID, "claim-subject")
	pairEqual(t, claim.OperatorMessageID, "")
	if claim.Remote == nil {
		t.Fatal("remote claim missing provenance")
	}
	pairEqual(t, claim.Remote.Workspace, contact)
}

func TestPairedClaimsStoreAcceptanceSequenceReplayAndConflictNeverRegress(t *testing.T) {
	db, contact := messageFixture(t)
	n := claimsStoredSubject(t, db, contact)
	s := claimsBoundStore(t, db, contact)
	n.Seq = 2
	claimsNoticeApply(t, s, contact, n, true, "")
	claimsNoticeApply(t, s, contact, n, true, "")
	conflict := n
	conflict.State = "done"
	claimsNoticeApply(t, s, contact, conflict, false, "conflict")
	claimsHeldState(t, db, contact, "active")
	older := conflict
	older.Seq = 1
	claimsNoticeApply(t, s, contact, older, false, "stale")
	claimsHeldState(t, db, contact, "active")
	n.Seq = 3
	n.State = "released"
	claimsNoticeApply(t, s, contact, n, true, "")
	claimsHeldState(t, db, contact, "released")
}

func TestPairedClaimsStoreAcceptanceRejectsUnknownContactAndNonRecipient(t *testing.T) {
	db, contact := messageFixture(t)
	n := claimsStoredSubject(t, db, contact)
	s := claimsBoundStore(t, db, contact)
	unknown := n
	unknown.MessageID = "missing"
	claimsNoticeApply(t, s, contact, unknown, false, "unknown_message")
	stranger := n
	stranger.Agent = "shared-but-not-addressed"
	claimsNoticeApply(t, s, contact, stranger, false, "not_recipient")
	other := messageContact(t, db, 2)
	claimsNoticeApply(t, claimsBoundStore(t, db, other), other, n, false, "unknown_message")
	set, err := db.SentMessageClaims(context.Background(), "claim-subject", "")
	pairOK(t, err)
	pairEqual(t, len(set.Claims), 0)
	claimsNoticeApply(t, s, contact, n, true, "")
	claimsHeldState(t, db, contact, "active")
}

func TestPairedClaimsStoreAcceptanceOtherLocalAgentCannotRead(t *testing.T) {
	db, contact := messageFixture(t)
	n := claimsStoredSubject(t, db, contact)
	claimsNoticeApply(t, claimsBoundStore(t, db, contact), contact, n, true, "")
	_, err := db.SentMessageClaims(context.Background(), "claim-subject", "unrelated")
	if !errors.Is(err, store.ErrClaimTarget) {
		t.Fatalf("unrelated agent read remote claims: %v", err)
	}
}

func TestPairedClaimsStoreAcceptanceOldEpochCannotAttachToCurrentPairing(t *testing.T) {
	db, raw, contact := messageRawFixture(t)
	n := claimsStoredSubject(t, db, contact)
	old := claimsBoundStore(t, db, contact)
	_, err := raw.Exec(`UPDATE peer_contacts SET pairing_epoch=pairing_epoch+1,generation=generation+1 WHERE identity_key=?`, contact)
	pairOK(t, err)
	if _, err := old.AcceptClaimNotice(context.Background(), contact, n, messageAt); err == nil {
		t.Fatal("admitted old session crossed epoch fence")
	}
	current := claimsBoundStore(t, db, contact)
	claimsNoticeApply(t, current, contact, n, false, "unknown_message")
}
