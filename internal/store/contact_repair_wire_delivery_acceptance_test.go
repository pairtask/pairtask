package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

type wireDeliveryCase struct {
	name, want string
	apply      func(*store.PeerMessages, string, string) error
}

func wireDeliveryCases() []wireDeliveryCase {
	ctx := context.Background()
	return []wireDeliveryCase{
		{"delivered", messaging.StateDelivered, func(p *store.PeerMessages, c, id string) error {
			return p.Delivered(ctx, c, id, messageAt.Add(time.Hour))
		}},
		{"read", messaging.StateRead, func(p *store.PeerMessages, c, id string) error {
			return p.PeerRead(ctx, c, id, messageAt.Add(time.Hour))
		}},
		{"refused", messaging.StateRefused, func(p *store.PeerMessages, c, id string) error {
			return p.Refused(ctx, c, messaging.Refused{ID: id, Refusal: messaging.Refuse(messaging.ReasonNotShared)})
		}},
	}
}

func TestContactRepairWireAcceptanceDeliveryTargetsOnlyCurrentReusedID(t *testing.T) {
	for _, tc := range wireDeliveryCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := namespaceFixture(t)
			wireQueue(t, f, "same-out", "old outgoing")
			pairOK(t, f.db.PeerMessages().Delivered(context.Background(), f.key, "same-out", messageAt))
			namespaceRepair(t, f)
			old := namespaceEpochRows(t, f, "peer_messages", 1)
			wireQueue(t, f, "same-out", "new outgoing")
			bound := wireBound(t, f)
			limit := messaging.SessionLimit
			limit.Version = 8
			offered, err := bound.Offer(context.Background(), f.key, messageAt, limit)
			pairOK(t, err)
			pairEqual(t, len(offered), 1)
			pairEqual(t, offered[0].Text, "new outgoing")
			pairOK(t, tc.apply(bound, f.key, "same-out"))
			pairEqual(t, wireCurrentState(t, f, "same-out"), tc.want)
			if !reflect.DeepEqual(old, namespaceEpochRows(t, f, "peer_messages", 1)) {
				t.Fatal("delivery result changed retired outgoing history")
			}
		})
	}
}

func TestContactRepairWireAcceptanceOldOnlyDeliveryIDIsNoop(t *testing.T) {
	for _, tc := range wireDeliveryCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := namespaceFixture(t)
			wireQueue(t, f, "old-only", "retired outgoing")
			namespaceRepair(t, f)
			before := wireMutationSnapshot(t, f)
			pairOK(t, tc.apply(wireBound(t, f), f.key, "old-only"))
			if !reflect.DeepEqual(before, wireMutationSnapshot(t, f)) {
				t.Fatal("old-only ID changed state under new session")
			}
		})
	}
}

func TestContactRepairWireAcceptanceDirectoryStartsEmptyAndPreservesOldRows(t *testing.T) {
	f := namespaceFixture(t)
	pairOK(t, f.db.PeerMessages().SetPeerDirectory(context.Background(), f.key, []string{"old-agent"}, messageAt))
	oldBound := wireBound(t, f)
	namespaceRepair(t, f)
	old := namespaceEpochRows(t, f, "peer_directory", 1)
	bound := wireBound(t, f)
	agents, err := bound.PeerDirectory(context.Background(), f.key)
	pairOK(t, err)
	if len(agents) != 0 {
		t.Errorf("new pairing inherited directory: %v", agents)
	}
	pairOK(t, bound.SetPeerDirectory(context.Background(), f.key, []string{"new-agent"}, messageAt))
	agents, err = bound.PeerDirectory(context.Background(), f.key)
	pairOK(t, err)
	routingSameAgents(t, agents, []string{"new-agent"})
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_directory WHERE pairing_epoch=2`), 1)
	if !reflect.DeepEqual(old, namespaceEpochRows(t, f, "peer_directory", 1)) {
		t.Error("directory replacement rewrote retired evidence")
	}
	_, err = oldBound.PeerDirectory(context.Background(), f.key)
	if !errors.Is(err, store.ErrFenced) {
		t.Errorf("old session directory read must be fenced, got %v", err)
	}
}
