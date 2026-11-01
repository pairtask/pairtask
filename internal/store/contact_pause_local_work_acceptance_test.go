package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/review"
	"github.com/pairtask/pairtask/internal/store"
)

func TestContactPauseLocalWorkAcceptancePendingImportIsHeldUntilResume(t *testing.T) {
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	routingRegister(t, f.db, "worker")
	_, err := f.db.PeerMessages().Share(ctx, f.contact, []string{"worker"}, messageAt)
	pairOK(t, err)
	m := messageWire("pending-import", 1)
	m.From = "remote"
	m.To = []string{"worker"}
	_, err = f.db.PeerMessages().Accept(ctx, f.contact, m, messageAt)
	pairOK(t, err)
	pending, err := f.db.PendingImports(ctx, 10)
	pairOK(t, err)
	pairEqual(t, len(pending), 1)
	pauseApply(t, f, pauseRequest(t, f, "pause", true))
	_, err = f.db.ImportRemote(ctx, pending[0], 1, messageAt)
	pairEqual(t, errors.Is(err, store.ErrPaused), true)
	still, err := f.db.PendingImports(ctx, 10)
	pairOK(t, err)
	pairEqual(t, len(still), 1)
	notices, err := f.db.ClaimPendingFor(ctx, protocol.Recipient{AgentID: "worker", SessionID: "worker-session"}, 14)
	pairOK(t, err)
	pairEqual(t, len(notices), 0)
	pauseApply(t, f, pauseRequest(t, f, "resume", false))
	got, err := f.db.ImportRemote(ctx, pending[0], 1, messageAt)
	pairOK(t, err)
	pairEqual(t, got.State, store.ImportImported)
}

func TestContactPauseLocalWorkAcceptanceReviewApprovalAndDispatchAreHeld(t *testing.T) {
	for _, mode := range []string{store.ReviewAsk, store.ReviewAuto} {
		t.Run(mode, func(t *testing.T) {
			f := newOperatorContactFixture(t)
			ctx := context.Background()
			routingRegister(t, f.db, "reviewer")
			reviewGrant(t, f.db, f.contact, mode)
			m := remoteAsk("review-held")
			_, err := f.db.PeerMessages().Accept(ctx, f.contact, m, messageAt)
			pairOK(t, err)
			pauseApply(t, f, pauseRequest(t, f, "pause", true))
			if mode == store.ReviewAsk {
				_, err = f.db.DecideRemoteReview(ctx, store.RemoteDecision{Contact: f.contact, RemoteID: m.ID, Approve: true, Now: messageAt})
				pairEqual(t, errors.Is(err, store.ErrPaused), true)
			}
			_, err = f.db.ImportRemote(ctx, store.RemoteImport{Contact: f.contact, RemoteID: m.ID}, 1, messageAt)
			pairEqual(t, errors.Is(err, store.ErrPaused), true)
			reviews, _, err := f.db.Reviews(ctx, "reviewer", 10)
			pairOK(t, err)
			pairEqual(t, len(reviews), 0)
			pauseApply(t, f, pauseRequest(t, f, "resume", false))
			if mode == store.ReviewAsk {
				_, err = f.db.DecideRemoteReview(ctx, store.RemoteDecision{Contact: f.contact, RemoteID: m.ID, Approve: true, Now: messageAt})
				pairOK(t, err)
			}
			got, err := f.db.ImportRemote(ctx, store.RemoteImport{Contact: f.contact, RemoteID: m.ID}, 1, messageAt)
			pairOK(t, err)
			pairEqual(t, got.State, store.ImportImported)
		})
	}
}

func TestContactPauseLocalWorkAcceptanceNewOperatorAndAgentSendsAreAtomic(t *testing.T) {
	w := workspaceStore(t)
	f := &operatorContactFixture{db: w.db, raw: w.raw, contact: w.key}
	ctx := context.Background()
	pauseApply(t, f, pauseRequest(t, f, "pause", true))
	_, err := f.db.PeerMessages().Queue(ctx, messaging.Outgoing{Contact: f.contact, ID: "new-root-send", Text: "held", Now: messageAt})
	pairEqual(t, errors.Is(err, store.ErrPaused), true)
	_, err = f.send(t, contactSendInput{"new-operator-send", "held"})
	pairEqual(t, errors.Is(err, store.ErrPaused), true)
	req := w.request("new-agent-send")
	_, err = f.db.QueueWorkspaceSend(ctx, req)
	pairEqual(t, errors.Is(err, store.ErrPaused), true)
	pairEqual(t, len(messagePage(t, f.db.PeerMessages(), f.contact)), 0)
	var count int
	pairOK(t, f.raw.QueryRow("SELECT COUNT(*) FROM message_metadata WHERE message_id=?", req.Msg.MessageID).Scan(&count))
	pairEqual(t, count, 0)
	pauseApply(t, f, pauseRequest(t, f, "resume", false))
	_, err = f.send(t, contactSendInput{"new-operator-send", "held"})
	pairOK(t, err)
	_, err = f.db.QueueWorkspaceSend(ctx, req)
	pairOK(t, err)
}

func TestContactPauseLocalWorkAcceptanceLocalReadsAndExpiryContinue(t *testing.T) {
	f := newOperatorContactFixture(t)
	p, ctx := f.db.PeerMessages(), context.Background()
	_, err := p.Accept(ctx, f.contact, messageWire("read-while-paused", 1), messageAt)
	pairOK(t, err)
	_, err = p.Queue(ctx, messaging.Outgoing{Contact: f.contact, ID: "expires-while-paused", Text: "queued", Now: messageAt})
	pairOK(t, err)
	pauseApply(t, f, pauseRequest(t, f, "pause", true))
	n, err := p.MarkRead(ctx, f.contact, []string{"read-while-paused"}, messageAt)
	pairOK(t, err)
	pairEqual(t, n, 1)
	reads, err := p.PendingReads(ctx, f.contact, 10)
	pairOK(t, err)
	pairEqual(t, len(reads), 1)
	pairOK(t, p.Collect(ctx, messageAt.Add(messaging.QueuedExpiry+time.Second)))
	found := false
	for _, row := range messagePage(t, p, f.contact) {
		if row.ID == "expires-while-paused" {
			found = true
			pairEqual(t, row.State, messaging.StateExpired)
		}
	}
	pairEqual(t, found, true)
	pauseApply(t, f, pauseRequest(t, f, "resume", false))
	fresh, err := p.Admit(ctx, f.contact)
	pairOK(t, err)
	reads, err = fresh.PendingReads(ctx, f.contact, 10)
	pairOK(t, err)
	pairEqual(t, len(reads), 1)
}

func TestContactPauseLocalWorkAcceptanceRunningReviewContinuesAndQueuesOutcome(t *testing.T) {
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	reviewGrant(t, f.db, f.contact, store.ReviewAuto)
	m := remoteAsk("running-review")
	_, err := f.db.PeerMessages().Accept(ctx, f.contact, m, messageAt)
	pairOK(t, err)
	got, err := f.db.ImportRemote(ctx, store.RemoteImport{Contact: f.contact, RemoteID: m.ID}, 1, messageAt)
	pairOK(t, err)
	pauseApply(t, f, pauseRequest(t, f, "pause", true))
	act := store.ReviewAction{Actor: "reviewer", Action: "continue while paused"}
	_, _, err = f.db.ChangeReview(ctx, got.ReviewID, act, func(r review.Record) (review.Record, bool, error) {
		return review.Prepare(r, protocol.Revisions{Base: m.Review.Base, Head: m.Review.Head}, messageAt)
	})
	pairOK(t, err)
	_, _, err = f.db.ChangeReview(ctx, got.ReviewID, act, func(r review.Record) (review.Record, bool, error) { return review.Accept(r, messageAt) })
	pairOK(t, err)
	_, _, err = f.db.ChangeReview(ctx, got.ReviewID, act, func(r review.Record) (review.Record, bool, error) {
		return review.Submit(r, 1, protocol.Verdict{Summary: "completed locally"}, messageAt)
	})
	pairOK(t, err)
	rows, err := f.db.PeerMessages().PendingOutcomes(ctx, f.contact, 10)
	pairOK(t, err)
	pairEqual(t, len(rows), 1)
	pairEqual(t, rows[0].State, "completed")
	pauseApply(t, f, pauseRequest(t, f, "resume", false))
	fresh, err := f.db.PeerMessages().Admit(ctx, f.contact)
	pairOK(t, err)
	rows, err = fresh.PendingOutcomes(ctx, f.contact, 10)
	pairOK(t, err)
	pairEqual(t, len(rows), 1)
	pairEqual(t, rows[0].State, "completed")
}

func TestContactPauseLocalWorkAcceptancePausedContactStillConsumesCapacity(t *testing.T) {
	f := newOperatorContactFixture(t)
	pauseApply(t, f, pauseRequest(t, f, "pause", true))
	_, err := f.raw.Exec(`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n < ?)
INSERT INTO peer_contacts(identity_key,name,destination,established_at,joined,invited)
SELECT 'capacity-'||n,'Peer','','2026-11-01T12:00:00Z',0,1 FROM seq`, peer.MaxPairingIdentities-1)
	pairOK(t, err)
	o := pairOutcome(999)
	o.IdentityKey = joinKey(999)
	pairSaved(t, f.db, o)
	_, err = f.db.Accept(context.Background(), o.IdentityKey)
	pairOK(t, err)
	_, err = f.db.Deliver(context.Background(), o.IdentityKey, peer.PairingAccepted, messageAt)
	pairEqual(t, errors.Is(err, peer.ErrContactCapacity), true)
}
