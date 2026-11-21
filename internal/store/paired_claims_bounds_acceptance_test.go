package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestPairedClaimsBoundsAcceptanceFullDropsNewKeysButUpdatesExisting(t *testing.T) {
	db, contact := messageFixture(t)
	n := claimsStoredSubject(t, db, contact)
	s := claimsBoundStore(t, db, contact)
	for i := 0; i < protocol.ClaimsPerMessage; i++ {
		n.Task = fmt.Sprintf("task-%d", i)
		claimsNoticeApply(t, s, claimNoticeCase{contact, n, true, ""})
	}
	n.Task = "overflow"
	claimsNoticeApply(t, s, claimNoticeCase{contact, n, false, "full"})
	n.Task, n.Seq, n.State = "task-0", 2, "done"
	claimsNoticeApply(t, s, claimNoticeCase{contact, n, true, ""})
	set, err := db.SentMessageClaims(context.Background(), n.MessageID, "")
	pairOK(t, err)
	pairEqual(t, set.Total, protocol.ClaimsPerMessage)
}

func TestPairedClaimsBoundsAcceptanceCollectionFollowsSubjects(t *testing.T) {
	db, raw, contact, req := claimsReceivedFixture(t)
	ctx := context.Background()
	_, err := db.PairedClaim(ctx, protocol.TypeClaimSet, req)
	pairOK(t, err)
	n := claimsStoredSubject(t, db, contact)
	claimsNoticeApply(t, claimsBoundStore(t, db, contact), claimNoticeCase{contact, n, true, ""})
	// Model the replay floor forgetting both subjects; acknowledged imports
	// are now collectable, while no operator-authored claim is involved.
	_, err = raw.Exec(`DELETE FROM peer_messages WHERE contact=?`, contact)
	pairOK(t, err)
	_, err = raw.Exec(`UPDATE durable_inbox SET acknowledged_at=? WHERE message_id=?`, messageAt.Format(time.RFC3339Nano), req.MessageID)
	pairOK(t, err)
	pairOK(t, db.PeerMessages().Collect(ctx, messageAt.Add(time.Hour)))
	for _, table := range []string{"remote_claims", "claim_notices_out", "claims", "remote_imports"} {
		var count int
		pairOK(t, raw.QueryRow(`SELECT count(*) FROM `+table).Scan(&count))
		if count != 0 {
			t.Errorf("%s retained %d orphan rows", table, count)
		}
	}
}

func TestPairedClaimsBoundsAcceptanceUnpairSettlesNoticesAtomically(t *testing.T) {
	db, raw, contact, req := claimsReceivedFixture(t)
	_, err := db.PairedClaim(context.Background(), protocol.TypeClaimSet, req)
	pairOK(t, err)
	f := &operatorContactFixture{db: db, raw: raw, contact: contact}
	_, _, err = db.UnpairContact(context.Background(), unpairRequest(t, f, "claim-unpair"))
	pairOK(t, err)
	var pending int
	var reason string
	pairOK(t, raw.QueryRow(`SELECT pending,dropped FROM claim_notices_out WHERE contact=?`, contact).Scan(&pending, &reason))
	pairEqual(t, pending, 0)
	pairEqual(t, reason, "unpaired")
}
