package store_test

import (
	"context"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func historyCompletedAsk(t *testing.T) workspaceStoreFixture {
	t.Helper()
	f := namespaceFixture(t)
	out := f.sweepQueueAsk(t)
	out.State, out.Attempt, out.Verdict = "completed", 1, "comment"
	out = snapshotOutcome(t, out, snapshotFindings)
	answer, err := wireBound(t, f).AcceptOutcome(context.Background(), f.key, out, messageAt)
	pairOK(t, err)
	pairEqual(t, answer.Stored, true)
	namespaceRepair(t, f)
	namespaceShare(t, f, "sender")
	f.sweepQueueAsk(t)
	return f
}

func TestContactHistoryAcceptanceAskLookupSelectsSourceEpoch(t *testing.T) {
	f := historyCompletedAsk(t)
	p := f.db.PeerMessages()
	old, found, err := p.AskOutIn(context.Background(), store.MessageKey{Contact: f.key, Epoch: 1, ID: "ask"})
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, old.Epoch, int64(1))
	pairEqual(t, old.State, "completed")
	if old.Findings == nil {
		t.Fatal("old findings disappeared behind reused ID")
	}
	current, found, err := p.AskOutIn(context.Background(), store.MessageKey{Contact: f.key, ID: "ask"})
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, current.Epoch, int64(2))
	pairEqual(t, current.State, "asked")
}

func TestContactHistoryAcceptanceHeldLookupSelectsSourceEpoch(t *testing.T) {
	f := namespaceFixture(t)
	wireQueue(t, f, "held-history", "old")
	pairOK(t, f.db.PeerMessages().HeldBy(context.Background(), f.key, messaging.Held{ID: "held-history", HeldID: "old"}, messageAt))
	namespaceRepair(t, f)
	wireQueue(t, f, "held-history", "new")
	pairOK(t, wireBound(t, f).HeldBy(context.Background(), f.key, messaging.Held{ID: "held-history", HeldID: "new"}, messageAt))
	old, found, err := f.db.PeerMessages().HeldStatusIn(context.Background(), store.MessageKey{Contact: f.key, Epoch: 1, ID: "held-history"})
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, old.State, "ended")
	current, found, err := f.db.PeerMessages().HeldStatusIn(context.Background(), store.MessageKey{Contact: f.key, ID: "held-history"})
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, current.State, "held")
}

func TestContactHistoryAcceptanceAskedDetailAndRefsKeepOldFindings(t *testing.T) {
	f := historyCompletedAsk(t)
	ctx := context.Background()
	old := protocol.ReviewAskRef{Contact: f.key, AskID: "ask", PairingEpoch: 1}
	ask, found, err := f.db.AskBy(ctx, "sender", old)
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, ask.Epoch, int64(1))
	pairEqual(t, ask.View().PairingEpoch, int64(1))
	detail, err := f.db.AskedDetail(ctx, old)
	pairOK(t, err)
	pairEqual(t, len(detail.Items), 1)
	pairEqual(t, detail.Items[0].PairingEpoch, int64(1))
	pairEqual(t, detail.Items[0].State, "completed")
	refs, err := f.db.AskedByRefs(ctx, []protocol.ReviewAskRef{old, {Contact: f.key, AskID: "ask", PairingEpoch: 2}, old})
	pairOK(t, err)
	pairEqual(t, len(refs.Items), 2)
	pairEqual(t, refs.Items[0].PairingEpoch, int64(1))
	pairEqual(t, refs.Items[1].PairingEpoch, int64(2))
	page, err := f.db.AskedReviews(ctx, "")
	pairOK(t, err)
	pairEqual(t, len(page.Items), 2)
	epochs := map[int64]bool{}
	for _, item := range page.Items {
		epochs[item.PairingEpoch] = true
	}
	pairEqual(t, epochs[1], true)
	pairEqual(t, epochs[2], true)
}

func TestContactHistoryAcceptanceImportedAskLookupSelectsOldReview(t *testing.T) {
	old := newConsentPublishCase(t, protocol.ConsentOnce, true)
	old.decide(t)
	old.dispatch(t)
	original, found, err := old.f.db.PeerMessages().AskStatus(context.Background(), old.f.key, old.m.ID)
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, original.State, "imported")
	namespaceRepair(t, old.f)
	fresh := &consentPublishCase{f: old.f, m: old.m, kind: protocol.ConsentOnce, view: consentHeld(t, old.f, old.m, messageAt)}
	fresh.decide(t)
	got, found, err := old.f.db.PeerMessages().AskStatusIn(context.Background(), store.MessageKey{Contact: old.f.key, Epoch: 1, ID: old.m.ID})
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, got.State, "imported")
	pairEqual(t, got.ReviewID, original.ReviewID)
}

func TestContactHistoryAcceptanceHomeRouteDoesNotBorrowOtherEpoch(t *testing.T) {
	f := namespaceFixture(t)
	op := &operatorContactFixture{db: f.db, raw: f.raw, contact: f.key}
	_, err := f.db.OperatorContactSend(context.Background(), store.ContactSend{OperationID: "old-home", Contact: f.key, Text: "old route", Here: []string{"home-a"}, OperatorHere: true, AuditID: op.audit(t, contactAuditInput{"send", "old-home"}), Now: messageAt})
	pairOK(t, err)
	namespaceRepair(t, f)
	old, err := f.db.HomeRoutingIn(context.Background(), store.MessageKey{Contact: f.key, Epoch: 1, ID: "old-home"})
	pairOK(t, err)
	pairEqual(t, len(old.Agents), 1)
	pairEqual(t, old.Operator, true)
	other, err := f.db.HomeRoutingIn(context.Background(), store.MessageKey{Contact: f.key, Epoch: 2, ID: "old-home"})
	pairOK(t, err)
	pairEqual(t, len(other.Agents), 0)
	pairEqual(t, other.Operator, false)
}
