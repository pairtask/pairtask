package hub

import (
	"context"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func workspaceRoutingAlias(t *testing.T, h *Hub, key, name string) {
	t.Helper()
	ctx := context.Background()
	audit, err := h.store.RecordOperatorAudit(ctx, store.OperatorAudit{Action: "contact name", Scope: key, Result: store.AuditPending})
	messageWorkerOK(t, err)
	_, err = h.store.SetContactLocalName(ctx, store.ContactName{Contact: key, Name: name, AuditID: audit})
	messageWorkerOK(t, err)
}

func workspaceRoutingCount(t *testing.T, h *Hub, key string, want int) {
	t.Helper()
	rows, err := h.store.PeerMessages().Offer(context.Background(), key, time.Now(), messaging.SessionLimit)
	messageWorkerOK(t, err)
	messageSyncEqual(t, len(rows), want)
}

func TestWorkspaceLocalNamesRoutingAcceptanceSharingAndRevocation(t *testing.T) {
	h, key := remoteHubFixture(t)
	a := remoteHubClient(t, h, "sender", 13)
	workspaceRoutingAlias(t, h, key, "Office")
	msg := workspaceMessage("Office", "alias-message")
	messageSyncEqual(t, workspaceRouteResult(t, h, a, msg).Code, protocol.ErrRecipientUnavailable)
	workspaceRoutingCount(t, h, key, 0)
	remoteHubShare(t, h, key, a.ID)
	messageSyncEqual(t, workspaceRouteResult(t, h, a, msg).Stage, protocol.StageAccepted)
	workspaceRoutingCount(t, h, key, 1)
	_, err := h.store.PeerMessages().Unshare(context.Background(), key, []string{a.ID}, time.Now())
	messageWorkerOK(t, err)
	msg.MessageID = "after-revocation"
	messageSyncEqual(t, workspaceRouteResult(t, h, a, msg).Code, protocol.ErrRecipientUnavailable)
}

func TestWorkspaceLocalNamesRoutingAcceptanceReassignmentCannotRerouteRetry(t *testing.T) {
	h, key := remoteHubFixture(t)
	other := remoteHubContact(t, h, 2)
	a := remoteHubClient(t, h, "sender", 13)
	remoteHubShare(t, h, key, a.ID)
	remoteHubShare(t, h, other, a.ID)
	workspaceRoutingAlias(t, h, key, "Office")
	msg := workspaceMessage("Office", "stable-alias-message")
	first := workspaceRouteResult(t, h, a, msg)
	messageSyncEqual(t, first.Stage, protocol.StageAccepted)
	workspaceRoutingAlias(t, h, key, "Old office")
	workspaceRoutingAlias(t, h, other, "Office")
	messageSyncEqual(t, workspaceRouteResult(t, h, a, msg).Code, protocol.ErrDuplicateMessage)
	workspaceRoutingCount(t, h, other, 0)
	msg.Workspace = key
	retry := workspaceRouteResult(t, h, a, msg)
	messageSyncEqual(t, retry.Stage, protocol.StageAccepted)
	messageSyncEqual(t, retry.Repeated, true)
	messageSyncEqual(t, retry.Seq, first.Seq)
	workspaceRoutingCount(t, h, key, 1)
	msg.Workspace = "Office"
	msg.MessageID = "new-alias-message"
	messageSyncEqual(t, workspaceRouteResult(t, h, a, msg).Stage, protocol.StageAccepted)
	workspaceRoutingCount(t, h, other, 1)
}
