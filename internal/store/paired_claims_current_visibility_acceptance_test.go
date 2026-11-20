package store_test

import (
	"context"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestPairedClaimsCurrentVisibilityAcceptanceUnpairHidesPreviouslyDeliveredClaim(t *testing.T) {
	db, raw, contact, req := claimsReceivedFixture(t)
	ctx := context.Background()
	_, err := db.PairedClaim(ctx, protocol.TypeClaimSet, req)
	pairOK(t, err)
	queued, err := db.PeerMessages().PendingClaimNotices(ctx, contact, 20)
	pairOK(t, err)
	pairEqual(t, len(queued), 1)
	n := queued[0]
	pairOK(t, db.PeerMessages().ClaimNoticeAnswered(ctx, contact, messaging.ClaimReply{MessageID: n.MessageID, Agent: n.Agent, Role: n.Role, Task: n.Task, Seq: n.Seq}))
	f := &operatorContactFixture{db: db, raw: raw, contact: contact}
	_, _, err = db.UnpairContact(ctx, unpairRequest(t, f, "delivered-claim-unpair"))
	pairOK(t, err)
	out, err := db.PairedClaim(ctx, protocol.TypeClaimGet, req)
	pairOK(t, err)
	pairEqual(t, len(out.Set.Claims), 1)
	pairEqual(t, out.Visible, false)
	pairEqual(t, out.Reason, "unpaired")
}
