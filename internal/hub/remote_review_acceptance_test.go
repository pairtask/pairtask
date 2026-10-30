package hub

import (
	"context"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

func remoteReviewMessage(key string) protocol.Message {
	m := workspaceMessage(key, "review-ask")
	m.Payload = nil
	m.ReviewAsk = &protocol.ReviewAsk{ReviewTarget: protocol.ReviewTarget{Forge: "github", Repository: "pairtask/pairtask", Change: "235"}, Base: "base", Head: "head"}
	return m
}

func TestRemoteReviewHubAcceptanceRejectsInvalidEnvelope(t *testing.T) {
	for _, scenario := range []string{"old", "no-workspace", "reply", "operator", "no-base", "no-head"} {
		t.Run(scenario, func(t *testing.T) {
			h, key := remoteHubFixture(t)
			a := remoteHubClient(t, h, "sender", 14)
			remoteHubShare(t, h, key, a.ID)
			m := remoteReviewMessage(key)
			switch scenario {
			case "old":
				a.ProtocolVersion = 13
			case "no-workspace":
				m.Workspace = ""
			case "reply":
				m.InReplyTo = "parent"
			case "operator":
				m.To = protocol.OperatorID
			case "no-base":
				m.ReviewAsk.Base = ""
			case "no-head":
				m.ReviewAsk.Head = ""
			}
			got := workspaceRouteResult(t, h, a, m)
			messageSyncEqual(t, got.Code, protocol.ErrMalformedFrame)
			rows, err := h.store.PeerMessages().Offer(context.Background(), key, time.Now(), messaging.SessionLimit)
			messageWorkerOK(t, err)
			messageSyncEqual(t, len(rows), 0)
		})
	}
}

func TestRemoteReviewHubAcceptanceCallerGrantAndPinnedRetry(t *testing.T) {
	h, key := remoteHubFixture(t)
	a := remoteHubClient(t, h, "sender", 14)
	m := remoteReviewMessage(key)
	messageSyncEqual(t, workspaceRouteResult(t, h, a, m).Code, protocol.ErrRecipientUnavailable)
	remoteHubShare(t, h, key, a.ID)
	first := workspaceRouteResult(t, h, a, m)
	messageSyncEqual(t, first.Stage, protocol.StageAccepted)
	replay := workspaceRouteResult(t, h, a, m)
	messageSyncEqual(t, replay.Repeated, true)
	messageSyncEqual(t, replay.Seq, first.Seq)
	m.ReviewAsk.Head = "changed"
	messageSyncEqual(t, workspaceRouteResult(t, h, a, m).Code, protocol.ErrDuplicateMessage)
	rows, err := h.store.PeerMessages().Offer(context.Background(), key, time.Now(), messaging.SessionLimit)
	messageWorkerOK(t, err)
	messageSyncEqual(t, len(rows), 1)
	messageSyncEqual(t, rows[0].From, a.ID)
	messageSyncEqual(t, rows[0].Review.Head, "head")
}
