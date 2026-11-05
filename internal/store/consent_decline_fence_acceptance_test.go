package store_test

import (
	"context"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
	"testing"
	"time"
)

type consentOwnerControl interface {
	SetConsentOwner(context.Context, string, uint64) error
	EndConsentOwner(context.Context, string, uint64) error
}

func consentRequireOwner(t *testing.T, f workspaceStoreFixture) consentOwnerControl {
	t.Helper()
	owner, ok := any(f.db.PeerMessages()).(consentOwnerControl)
	if !ok {
		t.Fatal("live consent owner store API is not available")
	}
	return owner
}
func consentScopeMessage(id string, n uint64, review bool, targets ...string) messaging.Message {
	if !review {
		return consentMessage(id, n, targets...)
	}
	m := remoteAsk(id)
	m.N = n
	m.To = targets
	m.Review.Publish = false
	m.Review.Formal = false
	return m
}
func consentDeclineFixture(t *testing.T, review bool) workspaceStoreFixture {
	t.Helper()
	f := consentFixture(t, "X", "Y")
	if review {
		for _, agent := range []string{"X", "Y"} {
			pairOK(t, f.db.ResetConsentScope(context.Background(), f.key, agent, protocol.ConsentReview, 0, messageAt))
		}
	}
	return f
}
func consentApproveBeforeDeny(t *testing.T, f workspaceStoreFixture, review bool, kind string) {
	t.Helper()
	group := []string{"X", "Y"}
	if review {
		group = []string{"X"}
	}
	if kind == protocol.ConsentSession {
		pairOK(t, consentRequireOwner(t, f).SetConsentOwner(context.Background(), "broker-epoch", 7))
	}
	messages := []messaging.Message{consentScopeMessage("approved-A", 1, review, group...), consentScopeMessage("declined-B", 2, review, "X"), consentScopeMessage("unaffected-Y", 3, review, "Y")}
	views := make([]protocol.ConsentView, 0, 3)
	for _, m := range messages {
		views = append(views, consentHeld(t, f, m, messageAt))
	}
	for _, i := range []int{0, 2} {
		_, err := f.db.DecideConsent(context.Background(), consentDecision(views[i], kind, messageAt))
		pairOK(t, err)
	}
	_, err := f.db.DecideConsent(context.Background(), consentDecision(views[1], protocol.ConsentDecline, messageAt.Add(time.Minute)))
	pairOK(t, err)
}
func consentAssertDeniedDispatch(t *testing.T, f workspaceStoreFixture) {
	t.Helper()
	ctx := context.Background()
	now := messageAt.Add(2 * time.Minute)
	blocked, err := f.db.ImportRemote(ctx, store.RemoteImport{Contact: f.key, RemoteID: "approved-A"}, 1, now)
	pairOK(t, err)
	pairEqual(t, blocked.State, store.ImportWithheld)
	y, err := f.db.ImportRemote(ctx, store.RemoteImport{Contact: f.key, RemoteID: "unaffected-Y"}, 2, now)
	pairOK(t, err)
	pairEqual(t, y.State, store.ImportImported)
	rows, err := f.db.ClaimPendingFor(ctx, protocol.Recipient{AgentID: "X", SessionID: "inspect-X"}, 24)
	pairOK(t, err)
	pairEqual(t, len(rows), 0)
	rows, err = f.db.ClaimPendingFor(ctx, protocol.Recipient{AgentID: "Y", SessionID: "inspect-Y"}, 24)
	pairOK(t, err)
	pairEqual(t, len(rows), 1)
}
func TestConsentDeclineFenceAcceptanceDenialRevokesEarlierQueuedAuthority(t *testing.T) {
	for _, review := range []bool{false, true} {
		for _, kind := range []string{protocol.ConsentOnce, protocol.ConsentSession} {
			name := kind + "-message"
			if review {
				name = kind + "-review"
			}
			t.Run(name, func(t *testing.T) {
				f := consentDeclineFixture(t, review)
				consentApproveBeforeDeny(t, f, review, kind)
				consentAssertDeniedDispatch(t, f)
			})
		}
	}
}
