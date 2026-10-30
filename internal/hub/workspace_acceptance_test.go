package hub

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

func workspaceMessage(key, id string) protocol.Message {
	return protocol.Message{Type: protocol.TypeMsg, Workspace: key, MessageID: id, To: "remote:agent/@\nname", Payload: []byte(`{"text":"hello"}`), AckRequested: true, Durable: true}
}

func workspaceRouteResult(t *testing.T, h *Hub, a *Client, msg protocol.Message) protocol.Message {
	t.Helper()
	h.route(context.Background(), map[string]*Client{a.ID: a}, Inbound{From: a, Msg: msg})
	return recv(t, a)
}

func TestWorkspaceAcceptanceVersionAndEnvelope(t *testing.T) {
	for _, scenario := range []string{"old", "broadcast", "room", "long"} {
		t.Run(scenario, func(t *testing.T) {
			h, key := remoteHubFixture(t)
			a := remoteHubClient(t, h, "sender", 13)
			remoteHubShare(t, h, key, a.ID)
			msg := workspaceMessage(key, "invalid")
			switch scenario {
			case "old":
				a.ProtocolVersion = 12
			case "broadcast":
				msg.To = "*"
			case "room":
				msg.Room = "room"
			case "long":
				msg.Workspace = strings.Repeat("x", 129)
			}
			got := workspaceRouteResult(t, h, a, msg)
			messageSyncEqual(t, got.Code, protocol.ErrMalformedFrame)
			rows, err := h.store.PeerMessages().Offer(context.Background(), key, time.Now(), messaging.SessionLimit)
			messageWorkerOK(t, err)
			messageSyncEqual(t, len(rows), 0)
		})
	}
}

func TestWorkspaceAcceptanceNamesKeysAndCallerDiscovery(t *testing.T) {
	h, key := remoteHubFixture(t)
	a := remoteHubClient(t, h, "sender", 13)
	b := remoteHubClient(t, h, "outsider", 13)
	remoteHubShare(t, h, key, a.ID)
	msg := workspaceMessage("Remote workspace", "by-name")
	messageSyncEqual(t, workspaceRouteResult(t, h, a, msg).Stage, protocol.StageAccepted)
	remoteHubContact(t, h, 2) // duplicate display names never select an arbitrary workspace
	msg.MessageID = "ambiguous"
	messageSyncEqual(t, workspaceRouteResult(t, h, a, msg).Code, protocol.ErrRecipientUnavailable)
	msg.Workspace = key[:20]
	messageSyncEqual(t, workspaceRouteResult(t, h, a, msg).Code, protocol.ErrRecipientUnavailable)
	msg.Workspace = key
	messageSyncEqual(t, workspaceRouteResult(t, h, a, msg).Stage, protocol.StageAccepted)
	for _, caller := range []*Client{a, b} {
		request := protocol.Message{Type: protocol.TypeWorkspacesGet, RequestID: "discover", From: a.ID}
		result := h.workspaces(context.Background(), Inbound{From: caller, Msg: request})
		messageSyncEqual(t, result.Type, protocol.TypeWorkspacesResult)
		want := 0
		if caller == a {
			want = 1
		}
		messageSyncEqual(t, len(result.Workspaces), want)
		if want == 1 {
			messageSyncEqual(t, result.Workspaces[0].Key, key)
		}
	}
	a.ProtocolVersion = 12
	messageSyncEqual(t, h.workspaces(context.Background(), Inbound{From: a, Msg: protocol.Message{RequestID: "old"}}).Code, protocol.ErrMalformedFrame)
}

func TestWorkspaceAcceptanceRetryAndConflictingIdentity(t *testing.T) {
	h, key := remoteHubFixture(t)
	a := remoteHubClient(t, h, "sender", 13)
	remoteHubShare(t, h, key, a.ID)
	msg := workspaceMessage(key, "stable")
	first := workspaceRouteResult(t, h, a, msg)
	messageSyncEqual(t, first.Stage, protocol.StageAccepted)
	again := workspaceRouteResult(t, h, a, msg)
	messageSyncEqual(t, again.Stage, protocol.StageAccepted)
	messageSyncEqual(t, again.Repeated, true)
	messageSyncEqual(t, again.Seq, first.Seq)
	for _, change := range []string{"text", "target", "contact"} {
		altered := msg
		switch change {
		case "text":
			altered.Payload = []byte(`{"text":"changed"}`)
		case "target":
			altered.To = protocol.OperatorID
		case "contact":
			altered.Workspace = remoteHubContact(t, h, 2)
			remoteHubShare(t, h, altered.Workspace, a.ID)
		}
		messageSyncEqual(t, workspaceRouteResult(t, h, a, altered).Code, protocol.ErrDuplicateMessage)
	}
	rows, err := h.store.PeerMessages().Offer(context.Background(), key, time.Now(), messaging.SessionLimit)
	messageWorkerOK(t, err)
	messageSyncEqual(t, len(rows), 1)
	messageSyncEqual(t, rows[0].From, a.ID)
	messageSyncEqual(t, rows[0].To[0], msg.To)
}

func TestWorkspaceAcceptanceGrantRevocationAndOperatorTarget(t *testing.T) {
	h, key := remoteHubFixture(t)
	a := remoteHubClient(t, h, "sender", 13)
	msg := workspaceMessage(key, "unshared")
	messageSyncEqual(t, workspaceRouteResult(t, h, a, msg).Code, protocol.ErrRecipientUnavailable)
	remoteHubShare(t, h, key, a.ID)
	// Failed transaction must not reserve the ID in local history.
	msg.To = protocol.OperatorID
	messageSyncEqual(t, workspaceRouteResult(t, h, a, msg).Stage, protocol.StageAccepted)
	rows, err := h.store.PeerMessages().Offer(context.Background(), key, time.Now(), messaging.SessionLimit)
	messageWorkerOK(t, err)
	messageSyncEqual(t, len(rows), 1)
	messageSyncEqual(t, len(rows[0].To), 0)
	_, err = h.store.PeerMessages().Unshare(context.Background(), key, []string{a.ID}, time.Now())
	messageWorkerOK(t, err)
	msg.MessageID = "revoked"
	messageSyncEqual(t, workspaceRouteResult(t, h, a, msg).Code, protocol.ErrRecipientUnavailable)
}

func TestWorkspaceAcceptanceExplicitReplyMustMatchContact(t *testing.T) {
	h, key := remoteHubFixture(t)
	other := remoteHubContact(t, h, 2)
	a := remoteHubClient(t, h, "sender", 13)
	parent := remoteHubImported(t, h, key, a)
	remoteHubShare(t, h, other, a.ID)
	msg := remoteHubReply(parent, "reply")
	msg.Workspace = other
	messageSyncEqual(t, workspaceRouteResult(t, h, a, msg).Code, protocol.ErrMalformedFrame)
	msg.Workspace = key
	messageSyncEqual(t, workspaceRouteResult(t, h, a, msg).Stage, protocol.StageAccepted)
}
