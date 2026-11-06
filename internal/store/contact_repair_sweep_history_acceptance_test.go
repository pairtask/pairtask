package store_test

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

func TestContactRepairSweepAcceptancePageDistinguishesReusedIDByEpoch(t *testing.T) {
	f := sweepHistoryFixture(t)
	rows := messagePage(t, f.db.PeerMessages(), f.key)
	pairEqual(t, len(rows), 2)
	epochs := map[int64]string{}
	for _, row := range rows {
		epochs[row.PairingEpoch] = row.Text
	}
	pairEqual(t, epochs[1], "text for history-shared")
	pairEqual(t, epochs[2], "epoch2 history body")
}

func TestContactRepairSweepAcceptanceOldReadIsLocalAndCurrentReadEmitsReceipt(t *testing.T) {
	f := sweepHistoryFixture(t)
	current := namespaceEpochRows(t, f, "peer_messages", 2)
	n, err := f.db.PeerMessages().MarkReadIn(context.Background(), store.ReadMark{Contact: f.key, Epoch: 1, IDs: []string{"history-shared"}, At: messageAt})
	pairOK(t, err)
	pairEqual(t, n, 1)
	var state, receipt string
	pairOK(t, f.raw.QueryRow(`SELECT state,receipt FROM peer_messages WHERE pairing_epoch=1 AND message_id='history-shared'`).Scan(&state, &receipt))
	pairEqual(t, state, messaging.StateRead)
	pairEqual(t, receipt, "local")
	if !reflect.DeepEqual(current, namespaceEpochRows(t, f, "peer_messages", 2)) {
		t.Fatal("old-epoch read changed current message")
	}
	bound := wireBound(t, f)
	reads, err := bound.PendingReads(context.Background(), f.key, 10)
	pairOK(t, err)
	pairEqual(t, len(reads), 0)
	n, err = f.db.PeerMessages().MarkReadIn(context.Background(), store.ReadMark{Contact: f.key, Epoch: 2, IDs: []string{"history-shared"}, At: messageAt})
	pairOK(t, err)
	pairEqual(t, n, 1)
	reads, err = bound.PendingReads(context.Background(), f.key, 10)
	pairOK(t, err)
	pairEqual(t, len(reads), 1)
}

func TestContactRepairSweepAcceptanceInvalidHistoryEpochDoesNotMutate(t *testing.T) {
	for _, epoch := range []int64{-1, 0, 3} {
		t.Run(strconv.FormatInt(epoch, 10), func(t *testing.T) {
			f := sweepHistoryFixture(t)
			before := wireMutationSnapshot(t, f)
			_, err := f.db.PeerMessages().MarkReadIn(context.Background(), store.ReadMark{Contact: f.key, Epoch: epoch, IDs: []string{"history-shared"}, At: messageAt})
			if !errors.Is(err, messaging.ErrInvalid) {
				t.Errorf("invalid history epoch %d: got %v", epoch, err)
			}
			_, ackErr := f.db.PeerMessages().AgentAcksIn(context.Background(), f.key, epoch, "history-shared")
			if !errors.Is(ackErr, messaging.ErrInvalid) {
				t.Errorf("invalid ACK history epoch %d: got %v", epoch, ackErr)
			}
			if !reflect.DeepEqual(before, wireMutationSnapshot(t, f)) {
				t.Fatal("invalid epoch changed message or receipt state")
			}
		})
	}
}

func TestContactRepairSweepAcceptanceAgentAckCurrentAndExplicitHistoryAreSeparate(t *testing.T) {
	f := namespaceFixture(t)
	_, err := f.db.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: f.key, ID: "same-agent-history", Text: "old", To: []string{"old-reader"}, Now: messageAt})
	pairOK(t, err)
	pairOK(t, f.db.PeerMessages().PeerAgentRead(context.Background(), f.key, messaging.AgentRead{ID: "same-agent-history", Agent: "old-reader"}, messageAt))
	namespaceRepair(t, f)
	_, err = f.db.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: f.key, ID: "same-agent-history", Text: "new", To: []string{"new-reader"}, Now: messageAt})
	pairOK(t, err)
	pairOK(t, wireBound(t, f).PeerAgentRead(context.Background(), f.key, messaging.AgentRead{ID: "same-agent-history", Agent: "new-reader"}, messageAt))
	agents, err := f.db.PeerMessages().AgentAcks(context.Background(), f.key, "same-agent-history")
	pairOK(t, err)
	routingSameAgents(t, agents, []string{"new-reader"})
	agents, err = f.db.PeerMessages().AgentAcksIn(context.Background(), f.key, 1, "same-agent-history")
	pairOK(t, err)
	routingSameAgents(t, agents, []string{"old-reader"})
}
