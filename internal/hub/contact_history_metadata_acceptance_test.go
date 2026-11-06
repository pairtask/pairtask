package hub

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func (f historyHubCase) queue(t *testing.T, id string, to []string) messaging.Row {
	t.Helper()
	row, err := f.h.store.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: f.key, ID: id, Text: "history body", To: to, Now: time.Now()})
	messageWorkerOK(t, err)
	return row
}

// rowViews exercises the same remote ID on both sides of an actual unpair.
func (f historyHubCase) rowViews(t *testing.T, frame func(*store.PeerMessages, messaging.Row) error) [2]protocol.ContactMessageView {
	t.Helper()
	old := f.queue(t, "reused-metadata", []string{"old-reader"})
	messageWorkerOK(t, frame(f.h.store.PeerMessages(), old))
	f.repair(t)
	current := f.queue(t, "reused-metadata", []string{"new-reader"})
	bound, err := f.h.store.PeerMessages().Admit(context.Background(), f.key)
	messageWorkerOK(t, err)
	messageWorkerOK(t, frame(bound, current))
	var views [2]protocol.ContactMessageView
	for i, row := range []messaging.Row{old, current} {
		views[i], err = f.h.messageView(context.Background(), row)
		messageWorkerOK(t, err)
	}
	return views
}

func TestContactHistoryHubAcceptanceEachRowKeepsItsAgentAcks(t *testing.T) {
	f := newHistoryHubCase(t)
	views := f.rowViews(t, func(p *store.PeerMessages, row messaging.Row) error {
		return p.PeerAgentRead(context.Background(), f.key, messaging.AgentRead{ID: row.ID, Agent: row.To[0]}, time.Now())
	})
	messageSyncEqual(t, len(views[0].AgentAcks), 1)
	messageSyncEqual(t, views[0].AgentAcks[0], "old-reader")
	messageSyncEqual(t, len(views[1].AgentAcks), 1)
	messageSyncEqual(t, views[1].AgentAcks[0], "new-reader")
}

func TestContactHistoryHubAcceptanceEachRowKeepsItsHeldState(t *testing.T) {
	f := newHistoryHubCase(t)
	views := f.rowViews(t, func(p *store.PeerMessages, row messaging.Row) error {
		return p.HeldBy(context.Background(), f.key, messaging.Held{ID: row.ID, HeldID: row.To[0]}, time.Now())
	})
	messageSyncEqual(t, views[0].Consent, "ended")
	messageSyncEqual(t, views[1].Consent, "held")
}

func (f historyHubCase) ask(t *testing.T) messaging.Row {
	t.Helper()
	remoteHubShare(t, f.h, f.key, "sender")
	row, err := f.h.store.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: f.key, ID: "reused-ask", Text: "review it", From: "sender", To: []string{"reviewer"}, Review: &messaging.ReviewAsk{Forge: "github", Repository: "pairtask/pairtask", Change: "276", Base: "base-pin", Head: "head-pin"}, Now: time.Now()})
	messageWorkerOK(t, err)
	return row
}

func historyHubAsks(t *testing.T) historyHubCase {
	t.Helper()
	f := newHistoryHubCase(t)
	f.ask(t)
	f.repair(t)
	f.ask(t)
	return f
}

func TestContactHistoryHubAcceptanceEachRowKeepsItsReviewOutcome(t *testing.T) {
	f := newHistoryHubCase(t)
	old := f.ask(t)
	f.repair(t)
	current := f.ask(t)
	oldView, err := f.h.messageView(context.Background(), old)
	messageWorkerOK(t, err)
	if oldView.ReviewOutcome == nil {
		t.Fatal("old review outcome missing")
	}
	messageSyncEqual(t, oldView.ReviewOutcome.State, "contact_removed")
	messageSyncEqual(t, oldView.ReviewOutcome.PairingEpoch, int64(1))
	currentView, err := f.h.messageView(context.Background(), current)
	messageWorkerOK(t, err)
	if currentView.ReviewOutcome == nil {
		t.Fatal("current review outcome missing")
	}
	messageSyncEqual(t, currentView.ReviewOutcome.State, "asked")
	messageSyncEqual(t, currentView.ReviewOutcome.PairingEpoch, int64(2))
}

func TestContactHistoryHubAcceptanceAgentListsRespectVersion(t *testing.T) {
	for _, version := range []int{24, 25} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			f := historyHubAsks(t)
			agent := remoteHubClient(t, f.h, "sender", version)
			got := f.h.reviewsAsked(context.Background(), Inbound{From: agent, Msg: protocol.Message{Type: protocol.TypeReviewsAskedGet, RequestID: "asked"}})
			messageSyncEqual(t, got.Type, protocol.TypeReviewsAskedResult)
			if version == 24 {
				messageSyncEqual(t, len(got.ReviewsAsked), 1)
				messageSyncEqual(t, got.ReviewsAsked[0].State, "asked")
				messageSyncEqual(t, got.ReviewsAsked[0].PairingEpoch, int64(0))
				return
			}
			messageSyncEqual(t, len(got.ReviewsAsked), 2)
			seen := map[int64]bool{}
			for _, ask := range got.ReviewsAsked {
				seen[ask.PairingEpoch] = true
			}
			messageSyncEqual(t, seen[1], true)
			messageSyncEqual(t, seen[2], true)
		})
	}
}

func TestContactHistoryHubAcceptanceLegacyAgentCannotSelectOldEpoch(t *testing.T) {
	f := historyHubAsks(t)
	agent := remoteHubClient(t, f.h, "sender", 24)
	msg := protocol.Message{Type: protocol.TypeReviewsAskedGet, RequestID: "asked", ReviewAskRefs: []protocol.ReviewAskRef{{Contact: f.key, AskID: "reused-ask", PairingEpoch: 1}}}
	got := f.h.reviewsAsked(context.Background(), Inbound{From: agent, Msg: msg})
	messageSyncEqual(t, got.Type, protocol.TypeError)
}

func TestContactHistoryHubAcceptanceCapableAgentReadsExplicitOldEpoch(t *testing.T) {
	f := historyHubAsks(t)
	agent := remoteHubClient(t, f.h, "sender", 25)
	msg := protocol.Message{Type: protocol.TypeReviewsAskedGet, RequestID: "asked", ReviewAskRefs: []protocol.ReviewAskRef{{Contact: f.key, AskID: "reused-ask", PairingEpoch: 1}}}
	got := f.h.reviewsAsked(context.Background(), Inbound{From: agent, Msg: msg})
	messageSyncEqual(t, got.Type, protocol.TypeReviewsAskedResult)
	messageSyncEqual(t, len(got.ReviewsAsked), 1)
	messageSyncEqual(t, got.ReviewsAsked[0].State, "contact_removed")
	messageSyncEqual(t, got.ReviewsAsked[0].PairingEpoch, int64(1))
}
