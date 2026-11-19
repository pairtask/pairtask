package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/pairtask/pairtask/internal/store"
)

func TestPairedClaimsVisibilityAcceptanceReusedIDDoesNotShowEarlierEpoch(t *testing.T) {
	db, raw, contact := messageRawFixture(t)
	n := claimsStoredSubject(t, db, contact)
	claimsNoticeApply(t, claimsBoundStore(t, db, contact), claimNoticeCase{contact, n, true, ""})
	_, err := raw.Exec(`UPDATE peer_contacts SET pairing_epoch=pairing_epoch+1,generation=generation+1 WHERE identity_key=?`, contact)
	pairOK(t, err)
	current := claimsStoredSubject(t, db, contact)
	set, err := db.SentMessageClaims(context.Background(), current.MessageID, "")
	pairOK(t, err)
	pairEqual(t, len(set.Claims), 0)
	current.State = "done"
	claimsNoticeApply(t, claimsBoundStore(t, db, contact), claimNoticeCase{contact, current, true, ""})
	claimsHeldState(t, db, contact, "done")
}

func TestPairedClaimsVisibilityAcceptanceUnpairEndsCurrentVisibility(t *testing.T) {
	db, raw, contact := messageRawFixture(t)
	n := claimsStoredSubject(t, db, contact)
	claimsNoticeApply(t, claimsBoundStore(t, db, contact), claimNoticeCase{contact, n, true, ""})
	_, err := raw.Exec(`UPDATE peer_contacts SET unpaired_at=?,generation=generation+1 WHERE identity_key=?`, messageAt.Format("2006-01-02T15:04:05Z07:00"), contact)
	pairOK(t, err)
	set, err := db.SentMessageClaims(context.Background(), n.MessageID, "")
	if err != nil && !errors.Is(err, store.ErrClaimTarget) {
		t.Fatal(err)
	}
	pairEqual(t, len(set.Claims), 0)
}
