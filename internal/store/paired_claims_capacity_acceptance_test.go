package store_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestPairedClaimsCapacityAcceptanceReleasedKeysFreeHeldCapacity(t *testing.T) {
	db, contact := messageFixture(t)
	n := claimsStoredSubject(t, db, contact)
	s := claimsBoundStore(t, db, contact)
	for i := 0; i < 16; i++ {
		n.Task, n.Seq, n.State = fmt.Sprintf("task-%d", i), 1, "active"
		claimsNoticeApply(t, s, claimNoticeCase{contact, n, true, ""})
		n.Seq, n.State = 2, "released"
		claimsNoticeApply(t, s, claimNoticeCase{contact, n, true, ""})
	}
	n.Task, n.Seq, n.State = "seventeenth", 1, "active"
	claimsNoticeApply(t, s, claimNoticeCase{contact, n, true, ""})
	set, err := db.SentMessageClaims(context.Background(), n.MessageID, "")
	pairOK(t, err)
	pairEqual(t, set.Total, 17)
	pairEqual(t, set.Claims[0].Task, "seventeenth")
}

func TestPairedClaimsCapacityAcceptanceLifetimeBoundKeepsReplayTombstones(t *testing.T) {
	db, contact := messageFixture(t)
	n := claimsStoredSubject(t, db, contact)
	s := claimsBoundStore(t, db, contact)
	n.Seq, n.State = 2, "released"
	for i := 0; i < 64; i++ {
		n.Task = fmt.Sprintf("task-%d", i)
		claimsNoticeApply(t, s, claimNoticeCase{contact, n, true, ""})
	}
	n.Task = "overflow"
	claimsNoticeApply(t, s, claimNoticeCase{contact, n, false, "full"})
	n.Task, n.Seq, n.State = "task-0", 1, "active"
	claimsNoticeApply(t, s, claimNoticeCase{contact, n, false, "stale"})
	n.Seq, n.State = 3, "done"
	claimsNoticeApply(t, s, claimNoticeCase{contact, n, true, ""})
	set, err := db.SentMessageClaims(context.Background(), n.MessageID, "")
	pairOK(t, err)
	pairEqual(t, set.Total, 64)
}

func TestPairedClaimsCapacityAcceptanceReactivationHonorsHeldBound(t *testing.T) {
	db, contact := messageFixture(t)
	n := claimsStoredSubject(t, db, contact)
	s := claimsBoundStore(t, db, contact)
	n.Task, n.State = "released-key", "released"
	claimsNoticeApply(t, s, claimNoticeCase{contact, n, true, ""})
	for i := 0; i < 16; i++ {
		n.Task, n.State = fmt.Sprintf("task-%d", i), "active"
		claimsNoticeApply(t, s, claimNoticeCase{contact, n, true, ""})
	}
	n.Task, n.Seq = "released-key", 2
	claimsNoticeApply(t, s, claimNoticeCase{contact, n, false, "full"})
	n.Task, n.State = "task-0", "released"
	claimsNoticeApply(t, s, claimNoticeCase{contact, n, true, ""})
	n.Task, n.State = "released-key", "active"
	claimsNoticeApply(t, s, claimNoticeCase{contact, n, true, ""})
}

func TestPairedClaimsCapacityAcceptanceReadExplainsMixedHiddenReasons(t *testing.T) {
	db, _, contact, req := claimsReceivedFixture(t)
	ctx := context.Background()
	_, err := db.PairedClaim(ctx, protocol.TypeClaimSet, req)
	pairOK(t, err)
	bound, err := db.PeerMessages().Admit(ctx, contact)
	pairOK(t, err)
	pairOK(t, bound.SawVersion(ctx, 10))
	pairOK(t, bound.SawVersion(ctx, 11))
	req.Task = "second-task"
	_, err = db.PairedClaim(ctx, protocol.TypeClaimSet, req)
	pairOK(t, err)
	_, err = db.PeerMessages().Unshare(ctx, contact, []string{"worker"}, messageAt)
	pairOK(t, err)
	_, err = db.PeerMessages().Share(ctx, contact, []string{"worker"}, messageAt)
	pairOK(t, err)
	out, err := db.PairedClaim(ctx, protocol.TypeClaimGet, req)
	pairOK(t, err)
	pairEqual(t, out.Visible, false)
	pairEqual(t, out.Reason, "mixed")
}
