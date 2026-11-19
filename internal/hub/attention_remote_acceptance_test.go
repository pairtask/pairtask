package hub

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestAttentionAcceptanceRemoteOperatorCannotBypass(t *testing.T) {
	for _, sender := range []string{"", "remote-agent"} {
		name := sender
		if name == "" {
			name = "remote-operator"
		}
		t.Run(name, func(t *testing.T) {
			h, contact := remoteHubFixture(t)
			receiver := remoteHubClient(t, h, "quiet", 32)
			clients := map[string]*Client{receiver.ID: receiver}
			var request protocol.Message
			messageWorkerOK(t, json.Unmarshal([]byte(`{"type":"operator_attention","agent_id":"quiet","attention_op":"on"}`), &request))
			response := h.routeOperator(context.Background(), clients, operatorRequest{ctx: context.Background(), msg: request})
			messageSyncEqual(t, response.Type, "operator_attention_result")
			remoteHubShare(t, h, contact, receiver.ID)
			// Both senders are remote data, including the other workspace's
			// authenticated operator. No local urgency field crosses this wire.
			remoteHubStage(t, h, contact, messaging.Message{ID: "remote-attention", N: 1, From: sender, To: []string{receiver.ID}})
			h.drainImports(context.Background(), clients)
			got := recv(t, receiver)
			messageSyncEqual(t, got.Origin, protocol.OriginRemote)
			if got.Remote == nil {
				t.Fatal("remote attribution missing")
			}
			messageSyncEqual(t, got.Remote.Contact, contact)
			data, err := json.Marshal(got)
			messageWorkerOK(t, err)
			var disposition struct {
				Attention string `json:"attention"`
			}
			messageWorkerOK(t, json.Unmarshal(data, &disposition))
			messageSyncEqual(t, disposition.Attention, "suppressed")
			outbound, err := h.store.PeerMessages().Offer(context.Background(), contact, time.Now(), messaging.SessionLimit)
			messageWorkerOK(t, err)
			messageSyncEqual(t, len(outbound), 0) // no cross-workspace DND message in v0
		})
	}
}
