package hub

import (
	"context"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func TestPairStatusSummaryAcceptanceSnapshotUsesStoredReviewsAndSync(t *testing.T) {
	h, key := remoteHubFixture(t)
	ctx := context.Background()
	remoteHubClient(t, h, "reviewer", 14)
	remoteHubShare(t, h, key, "reviewer")
	now := time.Now()
	_, err := h.store.PeerMessages().SetReviewGrant(ctx, key, store.ReviewGrantChange{Agents: []string{"reviewer"}, Mode: store.ReviewAsk, Now: now})
	messageWorkerOK(t, err)
	remoteHubStage(t, h, key, messaging.Message{ID: "pending-review", N: 1, From: "remote-agent", To: []string{"reviewer"}, Review: &messaging.ReviewAsk{Forge: "github", Repository: "pairtask/pairtask", Change: "256", Base: "base", Head: "head"}})
	h.recordSync(key, "dial timeout", false)
	snapshot := &protocol.PairingSnapshot{}
	messageWorkerOK(t, h.contactsSnapshot(ctx, snapshot, nil))
	if len(snapshot.Contacts) != 1 || snapshot.Contacts[0].Summary == nil {
		t.Fatalf("contact summary missing: %+v", snapshot.Contacts)
	}
	s := snapshot.Contacts[0].Summary
	messageSyncEqual(t, s.ReviewsAwaiting, 1)
	messageSyncEqual(t, s.Sync.LastFailure, "dial timeout")
	messageSyncEqual(t, s.Sync.Failures, 1)
	_, err = h.store.DecideRemoteReview(ctx, store.RemoteDecision{Contact: key, RemoteID: "pending-review", Approve: false, Now: now})
	messageWorkerOK(t, err)
	messageWorkerOK(t, h.contactsSnapshot(ctx, snapshot, nil))
	messageSyncEqual(t, snapshot.Contacts[0].Summary.ReviewsAwaiting, 0)
}
