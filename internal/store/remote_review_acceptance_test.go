package store_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/review"
	"github.com/pairtask/pairtask/internal/store"
)

func remoteAsk(id string) messaging.Message {
	m := messageWire(id, 1)
	m.From, m.To = "sender/@\nverbatim", []string{"reviewer"}
	m.Review = &messaging.ReviewAsk{Forge: "github", Repository: "pairtask/pairtask", Change: "235", Base: "base-sha", Head: "head-sha", Scope: "security", Publish: true, Formal: true}
	return m
}

func reviewGrant(t *testing.T, db *store.Store, contact, mode string) {
	t.Helper()
	_, err := db.PeerMessages().SetReviewGrant(context.Background(), contact, store.ReviewGrantChange{Agents: []string{"reviewer"}, Mode: mode, Now: messageAt})
	pairOK(t, err)
}

func TestRemoteReviewAcceptanceApprovalStaysRequiredAfterAuto(t *testing.T) {
	db, contact := messageFixture(t)
	ctx := context.Background()
	routingRegister(t, db, "reviewer")
	reviewGrant(t, db, contact, store.ReviewAsk)
	m := remoteAsk("approval")
	a, err := db.PeerMessages().Accept(ctx, contact, m, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
	reviewGrant(t, db, contact, store.ReviewAuto)
	imp := store.RemoteImport{Contact: contact, RemoteID: m.ID}
	before, err := db.ImportRemote(ctx, imp, 1, messageAt)
	pairOK(t, err)
	pairEqual(t, before.State, store.ImportAwaiting)
	notices, err := db.ClaimPendingFor(ctx, protocol.Recipient{AgentID: "reviewer", SessionID: "review-session"}, 14)
	pairOK(t, err)
	pairEqual(t, len(notices), 0)
	state, err := db.DecideRemoteReview(ctx, contact, m.ID, true, messageAt)
	pairOK(t, err)
	pairEqual(t, state, store.ImportPending)
	// Caller-supplied mutations must not replace the accepted snapshot.
	imp.Sender, imp.Review = "forged", &messaging.ReviewAsk{Head: "forged"}
	got, err := db.ImportRemote(ctx, imp, 2, messageAt)
	pairOK(t, err)
	pairEqual(t, got.State, store.ImportImported)
	pairEqual(t, got.Msg.Origin, protocol.OriginRemote)
	if got.Msg.Remote == nil {
		t.Fatal("notice missing remote provenance")
	}
	pairEqual(t, got.Msg.Remote.Contact, contact)
	pairEqual(t, got.Msg.Remote.Sender, m.From)
	rec, found, err := db.GetReview(ctx, got.ReviewID)
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, rec.View.Pinned.Head, m.Review.Head)
	pairEqual(t, rec.View.Pinned.Base, m.Review.Base)
	pairEqual(t, rec.View.Remote.Sender, m.From)
	pairEqual(t, rec.View.Spec.Policy.Publish, false)
	pairEqual(t, rec.View.Spec.Policy.Formal, false)
	replay, err := db.ImportRemote(ctx, imp, 3, messageAt)
	pairOK(t, err)
	pairEqual(t, replay.Changed, false)
	notices, err = db.ClaimPendingFor(ctx, protocol.Recipient{AgentID: "reviewer", SessionID: "review-session"}, 14)
	pairOK(t, err)
	pairEqual(t, len(notices), 1)
}

func TestRemoteReviewAcceptanceRevocationIsTerminal(t *testing.T) {
	for _, mode := range []string{store.ReviewAsk, store.ReviewAuto} {
		t.Run(mode, func(t *testing.T) {
			db, contact := messageFixture(t)
			ctx := context.Background()
			reviewGrant(t, db, contact, mode)
			m := remoteAsk("revoke")
			a, err := db.PeerMessages().Accept(ctx, contact, m, messageAt)
			pairOK(t, err)
			pairEqual(t, a.Stored, true)
			reviewGrant(t, db, contact, store.ReviewOff)
			reviewGrant(t, db, contact, store.ReviewAuto)
			a, err = db.PeerMessages().Accept(ctx, contact, m, messageAt)
			pairOK(t, err)
			pairEqual(t, a.Stored, true)
			got, err := db.ImportRemote(ctx, store.RemoteImport{Contact: contact, RemoteID: m.ID}, 1, messageAt)
			pairOK(t, err)
			pairEqual(t, got.State, store.ImportWithheld)
		})
	}
}

func TestRemoteReviewAcceptanceExpiryCheckedBeforeApproval(t *testing.T) {
	db, contact := messageFixture(t)
	ctx := context.Background()
	reviewGrant(t, db, contact, store.ReviewAsk)
	m := remoteAsk("expire")
	a, err := db.PeerMessages().Accept(ctx, contact, m, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
	state, err := db.DecideRemoteReview(ctx, contact, m.ID, true, messageAt.Add(store.ReviewAskExpiry+time.Second))
	pairOK(t, err)
	pairEqual(t, state, store.ImportWithheld)
}

func TestRemoteReviewAcceptanceIndependentGrantAndPendingQuota(t *testing.T) {
	db, contact := messageFixture(t)
	ctx := context.Background()
	_, err := db.PeerMessages().Share(ctx, contact, []string{"reviewer"}, messageAt)
	pairOK(t, err)
	a, err := db.PeerMessages().Accept(ctx, contact, remoteAsk("no-grant"), messageAt)
	pairOK(t, err)
	pairEqual(t, a.Refusal.Reason, messaging.ReasonNotShared)
	reviewGrant(t, db, contact, store.ReviewAsk)
	_, err = db.PeerMessages().Unshare(ctx, contact, []string{"reviewer"}, messageAt)
	pairOK(t, err)
	for i := 0; i < store.ReviewAsksPerAgent; i++ {
		m := remoteAsk(fmt.Sprintf("ask-%d", i))
		m.N = uint64(i + 1)
		a, err = db.PeerMessages().Accept(ctx, contact, m, messageAt)
		pairOK(t, err)
		pairEqual(t, a.Stored, true)
	}
	overflow := remoteAsk("overflow")
	overflow.N = 100
	a, err = db.PeerMessages().Accept(ctx, contact, overflow, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Refusal.Reason, messaging.ReasonInboxFull)
	// A retry at the quota must retain the original successful acceptance.
	a, err = db.PeerMessages().Accept(ctx, contact, remoteAsk("ask-0"), messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
}

func TestRemoteReviewAcceptanceDeclineCannotBeRevived(t *testing.T) {
	db, contact := messageFixture(t)
	ctx := context.Background()
	reviewGrant(t, db, contact, store.ReviewAsk)
	m := remoteAsk("declined")
	_, err := db.PeerMessages().Accept(ctx, contact, m, messageAt)
	pairOK(t, err)
	state, err := db.DecideRemoteReview(ctx, contact, m.ID, false, messageAt)
	pairOK(t, err)
	pairEqual(t, state, store.ImportWithheld)
	state, err = db.DecideRemoteReview(ctx, contact, m.ID, true, messageAt)
	if !errors.Is(err, store.ErrNotAwaiting) {
		t.Fatalf("late approval: %v", err)
	}
	pairEqual(t, state, store.ImportWithheld)
	reviewGrant(t, db, contact, store.ReviewAuto)
	got, err := db.ImportRemote(ctx, store.RemoteImport{Contact: contact, RemoteID: m.ID}, 1, messageAt)
	pairOK(t, err)
	pairEqual(t, got.State, store.ImportWithheld)
}

func TestRemoteReviewAcceptanceOldPeerWaitsWithoutDowngrade(t *testing.T) {
	db, contact := messageFixture(t)
	ctx := context.Background()
	m := remoteAsk("outbound")
	_, err := db.PeerMessages().Queue(ctx, messaging.Outgoing{Contact: contact, ID: m.ID, Text: m.Text, Now: messageAt, To: m.To, Review: m.Review})
	pairOK(t, err)
	for _, version := range []byte{1, 2, 3} {
		rows, err := db.PeerMessages().Offer(ctx, contact, messageAt, routingLimit(version))
		pairOK(t, err)
		pairEqual(t, len(rows), 0)
		pairEqual(t, messagePage(t, db.PeerMessages(), contact)[0].Refusal.Reason, messaging.ReasonPeerUpgrade)
	}
	rows, err := db.PeerMessages().Offer(ctx, contact, messageAt, routingLimit(4))
	pairOK(t, err)
	pairEqual(t, len(rows), 1)
	if rows[0].Review == nil || *rows[0].Review != *m.Review {
		t.Fatal("upgrade lost pinned request")
	}
}

func TestRemoteReviewAcceptancePinsAndPublicationRevocation(t *testing.T) {
	db, contact := messageFixture(t)
	ctx := context.Background()
	_, err := db.PeerMessages().SetReviewGrant(ctx, contact, store.ReviewGrantChange{Agents: []string{"reviewer"}, Mode: store.ReviewAuto, Publish: true, Formal: true, Now: messageAt})
	pairOK(t, err)
	m := remoteAsk("publish")
	_, err = db.PeerMessages().Accept(ctx, contact, m, messageAt)
	pairOK(t, err)
	got, err := db.ImportRemote(ctx, store.RemoteImport{Contact: contact, RemoteID: m.ID}, 1, messageAt)
	pairOK(t, err)
	act := store.ReviewAction{Actor: "reviewer", Action: "test"}
	change := func(step func(review.Record) (review.Record, bool, error)) {
		t.Helper()
		_, _, err := db.ChangeReview(ctx, got.ReviewID, act, step)
		pairOK(t, err)
	}
	_, _, err = db.ChangeReview(ctx, got.ReviewID, act, func(r review.Record) (review.Record, bool, error) {
		return review.Prepare(r, protocol.Revisions{Base: m.Review.Base, Head: "moved"}, messageAt)
	})
	if !errors.Is(err, review.ErrMoved) {
		t.Fatalf("head movement accepted: %v", err)
	}
	change(func(r review.Record) (review.Record, bool, error) {
		return review.Prepare(r, protocol.Revisions{Base: m.Review.Base, Head: m.Review.Head}, messageAt)
	})
	change(func(r review.Record) (review.Record, bool, error) { return review.Accept(r, messageAt) })
	change(func(r review.Record) (review.Record, bool, error) {
		return review.Submit(r, 1, protocol.Verdict{Summary: "reviewed"}, messageAt)
	})
	reviewGrant(t, db, contact, store.ReviewOff)
	_, _, err = db.ChangeReview(ctx, got.ReviewID, act, func(r review.Record) (review.Record, bool, error) {
		next, step, err := review.Publish(r, 1, protocol.PublicationUpdate{From: protocol.PublicationPending, To: protocol.PublicationSubmitting, Token: "intent-token"}, messageAt)
		return next, step.Changed, err
	})
	if !errors.Is(err, review.ErrDenied) {
		t.Fatalf("revoked publication intent: %v", err)
	}
	rec, _, err := db.GetReview(ctx, got.ReviewID)
	pairOK(t, err)
	pairEqual(t, rec.View.Publication.State, protocol.PublicationPending)
	pairEqual(t, rec.View.Publication.Intent, 0)
}

func TestRemoteReviewAcceptanceRestartAndNoticeCollision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reviews.db")
	db := pairSQLite(t, path)
	contact := messageContact(t, db, 1)
	ctx := context.Background()
	reviewGrant(t, db, contact, store.ReviewAsk)
	m := remoteAsk("restart")
	_, err := db.PeerMessages().Accept(ctx, contact, m, messageAt)
	pairOK(t, err)
	_, err = db.DecideRemoteReview(ctx, contact, m.ID, true, messageAt)
	pairOK(t, err)
	pending, err := db.PendingImports(ctx, 10)
	pairOK(t, err)
	pairEqual(t, len(pending), 1)
	reserved := pending[0].LocalID
	// A local message takes the notice ID after staging but before dispatch.
	(routingLocalSender{db, "local"}).persist(t, "review:"+reserved, 1)
	pairOK(t, db.Close())
	db = pairSQLite(t, path)
	pending, err = db.PendingImports(ctx, 10)
	pairOK(t, err)
	pairEqual(t, len(pending), 1)
	got, err := db.ImportRemote(ctx, pending[0], 2, messageAt)
	pairOK(t, err)
	if got.ReviewID == reserved {
		t.Fatal("review notice reused a local message ID")
	}
	pairEqual(t, got.State, store.ImportImported)
	rec, found, err := db.GetReview(ctx, got.ReviewID)
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, rec.View.Pinned.Head, m.Review.Head)
	pairOK(t, db.Close())
	db = pairSQLite(t, path)
	replay, err := db.ImportRemote(ctx, pending[0], 3, messageAt)
	pairOK(t, err)
	pairEqual(t, replay.Changed, false)
	notices, err := db.ClaimPendingFor(ctx, protocol.Recipient{AgentID: "reviewer", SessionID: "after-restart"}, 14)
	pairOK(t, err)
	pairEqual(t, len(notices), 1)
	pairEqual(t, notices[0].ReviewID, got.ReviewID)
}
