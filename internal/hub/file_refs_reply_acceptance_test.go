package hub

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestFileRefsReplyAcceptanceImportedParentRetainsMetadata(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "inferred-contact", true: "explicit-contact"}[explicit], func(t *testing.T) {
			h, key := remoteHubFixture(t)
			a := remoteHubClient(t, h, "participant", 31)
			parent := remoteHubImported(t, h, key, a)
			msg := remoteHubReply(parent, "reply-with-files")
			if explicit {
				msg.Workspace = key
			}
			files := refsRefusalFiles()
			refsHubWithFiles(t, &msg, files)
			messageSyncEqual(t, workspaceRouteResult(t, h, a, msg).Stage, protocol.StageAccepted)
			limit := messaging.SessionLimit
			limit.Version = 12
			rows, err := h.store.PeerMessages().Offer(context.Background(), key, time.Now(), limit)
			messageWorkerOK(t, err)
			messageSyncEqual(t, len(rows), 1)
			data, err := json.Marshal(rows[0])
			messageWorkerOK(t, err)
			var got struct {
				Files []map[string]any `json:"files"`
			}
			messageWorkerOK(t, json.Unmarshal(data, &got))
			files[0]["size"] = float64(7)
			if !reflect.DeepEqual(got.Files, files) {
				t.Fatalf("reply references lost: %v", got.Files)
			}
			messageSyncEqual(t, rows[0].ReplyTo.ID, "parent")
		})
	}
}

func TestFileRefsReplyAcceptanceLocalMessageRefusedByBroker(t *testing.T) {
	h, _ := remoteHubFixture(t)
	a := remoteHubClient(t, h, "sender", 31)
	msg := workspaceMessage("", "local-files")
	msg.To = protocol.OperatorID
	refsHubWithFiles(t, &msg, refsRefusalFiles())
	messageSyncEqual(t, workspaceRouteResult(t, h, a, msg).Code, protocol.ErrMalformedFrame)
	msg.MessageID = "plain-local-control"
	refsHubWithFiles(t, &msg, nil)
	messageSyncEqual(t, workspaceRouteResult(t, h, a, msg).Stage, protocol.StageAccepted)
}
