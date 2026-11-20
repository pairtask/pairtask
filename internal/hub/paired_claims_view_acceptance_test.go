package hub

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestPairedClaimsViewAcceptanceContactRowsCarryAttributedClaims(t *testing.T) {
	f := newHistoryHubCase(t)
	row := f.queue(t, "claim-view", []string{"worker"})
	notice := messaging.ClaimNotice{MessageID: row.ID, Agent: "worker", Role: "owner", Task: "investigate", State: "active", Seq: 1, At: time.Now().UTC()}
	answer, err := f.h.store.PeerMessages().AcceptClaimNotice(context.Background(), f.key, notice, notice.At)
	messageWorkerOK(t, err)
	messageSyncEqual(t, answer.Stored, true)
	view, err := f.h.messageView(context.Background(), row)
	messageWorkerOK(t, err)
	encoded, err := json.Marshal(view)
	messageWorkerOK(t, err)
	var page protocol.ClaimPage
	messageWorkerOK(t, json.Unmarshal(encoded, &page))
	messageSyncEqual(t, page.Total, 1)
	messageSyncEqual(t, len(page.Claims), 1)
	claim := page.Claims[0]
	if claim.Remote == nil {
		t.Fatal("remote claim lost its workspace attribution")
	}
	messageSyncEqual(t, claim.Remote.Workspace, f.key)
	messageSyncEqual(t, claim.MessageID, row.ID)
	messageSyncEqual(t, claim.OperatorMessageID, "")
	f.repair(t)
	f.queue(t, row.ID, []string{"worker"})
	view, err = f.h.messageView(context.Background(), row)
	messageWorkerOK(t, err)
	encoded, err = json.Marshal(view)
	messageWorkerOK(t, err)
	page = protocol.ClaimPage{}
	messageWorkerOK(t, json.Unmarshal(encoded, &page))
	messageSyncEqual(t, len(page.Claims), 0)
}
