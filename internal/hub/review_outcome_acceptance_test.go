package hub

import (
	"context"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestReviewOutcomeHubAcceptanceListingUsesAuthenticatedCaller(t *testing.T) {
	h, key := remoteHubFixture(t)
	a := remoteHubClient(t, h, "asker", 15)
	b := remoteHubClient(t, h, "outsider", 15)
	remoteHubShare(t, h, key, a.ID)
	m := remoteReviewMessage(key)
	messageSyncEqual(t, workspaceRouteResult(t, h, a, m).Stage, protocol.StageAccepted)
	request := protocol.Message{Type: protocol.TypeReviewsAskedGet, RequestID: "list", From: a.ID}
	own := h.reviewsAsked(context.Background(), Inbound{From: a, Msg: request})
	messageSyncEqual(t, own.Type, protocol.TypeReviewsAskedResult)
	messageSyncEqual(t, len(own.ReviewsAsked), 1)
	other := h.reviewsAsked(context.Background(), Inbound{From: b, Msg: request})
	messageSyncEqual(t, len(other.ReviewsAsked), 0)
	b.ProtocolVersion = 14
	old := h.reviewsAsked(context.Background(), Inbound{From: b, Msg: request})
	messageSyncEqual(t, old.Code, protocol.ErrMalformedFrame)
}
