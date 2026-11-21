package store_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func claimsReceivedFixture(t *testing.T) (*store.Store, *sql.DB, string, store.ClaimRequest) {
	t.Helper()
	db, raw, contact := messageRawFixture(t)
	ctx := context.Background()
	routingRegister(t, db, "worker")
	_, err := db.PeerMessages().Share(ctx, contact, []string{"worker"}, messageAt)
	pairOK(t, err)
	m := messageWire("queue-subject", 1)
	m.To = []string{"worker"}
	a, err := db.PeerMessages().Accept(ctx, contact, m, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
	imports, err := db.PendingImports(ctx, 10)
	pairOK(t, err)
	pairEqual(t, len(imports), 1)
	imp, err := db.ImportRemote(ctx, imports[0], 1, messageAt)
	pairOK(t, err)
	_, err = raw.Exec(`UPDATE durable_inbox SET acknowledged_at=? WHERE message_id=?`, messageAt.Format("2006-01-02T15:04:05Z07:00"), imp.Msg.MessageID)
	pairOK(t, err)
	return db, raw, contact, store.ClaimRequest{MessageID: imp.Msg.MessageID, Agent: "worker", Role: "owner", Task: "investigate", At: messageAt}
}

func TestPairedClaimsQueueAcceptanceOldPeerSettlesAndReadExplains(t *testing.T) {
	db, _, contact, req := claimsReceivedFixture(t)
	ctx := context.Background()
	out, err := db.PairedClaim(ctx, protocol.TypeClaimSet, req)
	pairOK(t, err)
	pairEqual(t, out.Visible, true)
	bound, err := db.PeerMessages().Admit(ctx, contact)
	pairOK(t, err)
	pairOK(t, bound.SawVersion(ctx, 10))
	queued, err := bound.PendingClaimNotices(ctx, contact, 20)
	pairOK(t, err)
	pairEqual(t, len(queued), 0)
	out, err = db.PairedClaim(ctx, protocol.TypeClaimGet, req)
	pairOK(t, err)
	pairEqual(t, len(out.Set.Claims), 1)
	pairEqual(t, out.Visible, false)
	pairEqual(t, out.Reason, "unsupported")
}

func TestPairedClaimsQueueAcceptanceNoticeFailureRollsBackLocalClaim(t *testing.T) {
	db, raw, _, req := claimsReceivedFixture(t)
	_, err := raw.Exec(`CREATE TRIGGER refuse_notice BEFORE INSERT ON claim_notices_out BEGIN SELECT RAISE(ABORT,'test notice failure'); END`)
	pairOK(t, err)
	_, err = db.PairedClaim(context.Background(), protocol.TypeClaimSet, req)
	if err == nil {
		t.Fatal("notice failure was hidden")
	}
	out, err := db.PairedClaim(context.Background(), protocol.TypeClaimGet, req)
	pairOK(t, err)
	pairEqual(t, len(out.Set.Claims), 0)
}

func TestPairedClaimsQueueAcceptanceRemoteSubjectNeverLabeledOperatorRequest(t *testing.T) {
	db, _, _, req := claimsReceivedFixture(t)
	_, err := db.PairedClaim(context.Background(), protocol.TypeClaimSet, req)
	pairOK(t, err)
	sets, _, err := db.RecentClaims(context.Background(), 32)
	pairOK(t, err)
	for _, set := range sets {
		if set.OperatorMessageID == req.MessageID {
			t.Fatal("remote subject presented as an operator request")
		}
	}
}

func TestPairedClaimsQueueAcceptanceUpgradeDoesNotReplayUntilNextChange(t *testing.T) {
	db, _, contact, req := claimsReceivedFixture(t)
	ctx := context.Background()
	_, err := db.PairedClaim(ctx, protocol.TypeClaimSet, req)
	pairOK(t, err)
	bound, err := db.PeerMessages().Admit(ctx, contact)
	pairOK(t, err)
	pairOK(t, bound.SawVersion(ctx, 10))
	out, err := db.PairedClaim(ctx, protocol.TypeClaimRelease, req)
	pairOK(t, err)
	pairEqual(t, out.Reason, "unsupported")
	pairOK(t, bound.SawVersion(ctx, 11))
	queued, err := bound.PendingClaimNotices(ctx, contact, 20)
	pairOK(t, err)
	pairEqual(t, len(queued), 0)
	out, err = db.PairedClaim(ctx, protocol.TypeClaimGet, req)
	pairOK(t, err)
	pairEqual(t, out.Visible, false)
	pairEqual(t, out.Reason, "unsupported")
	out, err = db.PairedClaim(ctx, protocol.TypeClaimSet, req)
	pairOK(t, err)
	pairEqual(t, out.Visible, true)
	queued, err = bound.PendingClaimNotices(ctx, contact, 20)
	pairOK(t, err)
	pairEqual(t, len(queued), 1)
	if queued[0].Seq <= 1 {
		t.Fatal("new claim regressed the previously queued sequence")
	}
}

func TestPairedClaimsQueueAcceptanceUnshareKeepsEveryLaterMutationLocal(t *testing.T) {
	db, _, contact, req := claimsReceivedFixture(t)
	ctx := context.Background()
	_, err := db.PairedClaim(ctx, protocol.TypeClaimSet, req)
	pairOK(t, err)
	_, err = db.PeerMessages().Unshare(ctx, contact, []string{"worker"}, messageAt)
	pairOK(t, err)
	for _, kind := range []string{protocol.TypeClaimRelease, protocol.TypeClaimSet, protocol.TypeClaimDone} {
		out, err := db.PairedClaim(ctx, kind, req)
		pairOK(t, err)
		pairEqual(t, out.Visible, false)
		pairEqual(t, out.Reason, "not_shared")
		queued, err := db.PeerMessages().PendingClaimNotices(ctx, contact, 20)
		pairOK(t, err)
		pairEqual(t, len(queued), 0)
	}
}
