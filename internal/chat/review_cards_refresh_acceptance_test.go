package chat_test

import (
	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
	"strings"
	"testing"
	"time"
)

func actionTakeAskRefs(t *testing.T, h *chatHarness) protocol.Message {
	t.Helper()
	index := -1
	h.await(func() bool {
		for i, req := range h.requests {
			if len(req.ReviewAskRefs) > 0 {
				index = i
				return true
			}
		}
		return false
	})
	req := h.requests[index]
	h.requests = append(h.requests[:index], h.requests[index+1:]...)
	chatEqual(t, "card refresh reads asked", req.ReviewOp, protocol.ReviewOpAsked)
	chatEqual(t, "exact reads never use newest-page cursor", req.ReviewBefore, "")
	if len(req.ReviewAskRefs) > 8 {
		t.Fatal("card read exceeded eight-ref bound")
	}
	return req
}

func actionAnswerAskRefs(h *chatHarness, req protocol.Message, state string, settled bool) {
	h.t.Helper()
	items := make([]protocol.ReviewAskedItem, 0, len(req.ReviewAskRefs))
	for _, ref := range req.ReviewAskRefs {
		items = append(items, protocol.ReviewAskedItem{Contact: ref.Contact, AskID: ref.AskID, Reviewer: "reviewer", State: state, Settled: settled, UpdatedAt: time.Now()})
	}
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewOp: protocol.ReviewOpAsked, ReviewAsked: &protocol.ReviewAskedPage{Items: items, Total: len(items)}})
}

func actionRemoteRefresh(t *testing.T, h *chatHarness, contact string) protocol.Message {
	t.Helper()
	revision := h.m.State().Revision + 1
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventContactMessagesChanged, ContactKey: contact, Watermark: protocol.Watermark{Epoch: "epoch", Revision: revision}}}
	return actionTakeAskRefs(t, h)
}

func TestActionCardsAcceptanceRemoteReadFailureIsUnknownAndSuccessfulOmissionIsMissing(t *testing.T) {
	h, ask := actionRemoteQueued(t)
	read := actionRemoteRefresh(t, h, ask.ContactKey)
	h.reply(read, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInternal, Detail: "read unavailable"})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	card := actionCardWithID(t, h, ask.MessageID)
	v3Contains(t, strings.Join(card.Steps, "\n"), "status unknown (read failed)")
	chatEqual(t, "failed status read never settles request", card.Active, true)
	read = actionRemoteRefresh(t, h, ask.ContactKey)
	h.reply(read, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewOp: protocol.ReviewOpAsked, ReviewAsked: &protocol.ReviewAskedPage{Items: []protocol.ReviewAskedItem{}, Total: 0}})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	card = actionCardWithID(t, h, ask.MessageID)
	v3Contains(t, strings.Join(card.Steps, "\n"), "no longer kept here")
	chatEqual(t, "successful omission finishes tracking", card.Finished, true)
}

func TestActionCardsAcceptanceClearFencesLateReadAndRefreshesActiveCard(t *testing.T) {
	h, ask := actionRemoteQueued(t)
	old := actionRemoteRefresh(t, h, ask.ContactKey)
	v2Command(h, "/cards clear")
	fresh := actionTakeAskRefs(t, h)
	chatEqual(t, "replacement read has new request identity", fresh.RequestID != old.RequestID, true)
	items := []protocol.ReviewAskedItem{{Contact: ask.ContactKey, AskID: ask.MessageID, Reviewer: "reviewer", State: "completed", Settled: true, Verdict: "approve"}}
	h.launch(h.holdResponse(old, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewOp: protocol.ReviewOpAsked, ReviewAsked: &protocol.ReviewAskedPage{Items: items, Total: 1}}))
	card := actionCardWithID(t, h, ask.MessageID)
	chatEqual(t, "stale read cannot finish active card", card.Active, true)
	if strings.Contains(strings.Join(card.Steps, "\n"), "completed") {
		t.Fatal("stale generation mutated card")
	}
	actionAnswerAskRefs(h, fresh, "accepted", false)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	card = actionCardWithID(t, h, ask.MessageID)
	v3Contains(t, strings.Join(card.Steps, "\n"), "accepted by reviewer@Office")
}

func TestActionCardsAcceptanceEarlyEventIsReadAfterAskIDBinds(t *testing.T) {
	h, _ := remoteHarness(t)
	v2Command(h, remotePinnedCommand("@reviewer@Office", 40))
	ask := h.take(protocol.TypeOperatorContact)
	revision := h.m.State().Revision + 1
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventContactMessagesChanged, ContactKey: ask.ContactKey, Watermark: protocol.Watermark{Epoch: "epoch", Revision: revision}}}
	h.await(func() bool { return h.m.State().Revision == revision })
	actionQueueAnswer(h, ask)
	read := actionTakeAskRefs(t, h)
	chatEqual(t, "early event eventually reads bound ask", read.ReviewAskRefs[0].AskID, ask.MessageID)
	actionAnswerAskRefs(h, read, "accepted", false)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, strings.Join(actionCardWithID(t, h, ask.MessageID).Steps, "\n"), "accepted by reviewer@Office")
}

func TestActionCardsAcceptanceNineRemoteCardsReadInBoundedBatches(t *testing.T) {
	h, key, asks := actionQueueNineRemoteCards(t)
	first := actionRemoteRefresh(t, h, key)
	// A batch may be dispatched concurrently or after the previous response.
	h.launch(h.holdResponse(first, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewOp: protocol.ReviewOpAsked, ReviewAsked: &protocol.ReviewAskedPage{Items: []protocol.ReviewAskedItem{}}}))
	second := actionTakeAskRefs(t, h)
	chatEqual(t, "nine refs split into two batches", len(first.ReviewAskRefs)+len(second.ReviewAskRefs), 9)
	coverage := actionBatchCoverage{asks: asks, seen: map[string]bool{}, contact: key}
	coverage.check(t, first)
	coverage.check(t, second)
	chatEqual(t, "all nine active asks stay reachable", len(coverage.seen), 9)
	// Successful omission is evidence only for this batch, never the other one.
	actionCheckBatchActive(t, h, second)
	actionAnswerAskRefs(h, second, "accepted", false)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	actionCheckBatchSteps(t, h, first, "no longer kept here")
	actionCheckBatchSteps(t, h, second, "accepted by reviewer@Office")
}

func actionQueueNineRemoteCards(t *testing.T) (*chatHarness, string, map[string]bool) {
	t.Helper()
	h, _ := remoteHarness(t)
	asks := map[string]bool{}
	key := ""
	for i := 0; i < 9; i++ {
		v2Command(h, remotePinnedCommand("@reviewer@Office", 40))
		ask := h.take(protocol.TypeOperatorContact)
		asks[ask.MessageID], key = true, ask.ContactKey
		actionQueueAnswer(h, ask)
		actionDrainAskRefs(t, h)
	}
	return h, key, asks
}

type actionBatchCoverage struct {
	asks, seen map[string]bool
	contact    string
}

func (coverage actionBatchCoverage) check(t *testing.T, read protocol.Message) {
	t.Helper()
	for _, ref := range read.ReviewAskRefs {
		if !coverage.asks[ref.AskID] || coverage.seen[ref.AskID] {
			t.Fatal("batched lookup duplicated or invented a request")
		}
		coverage.seen[ref.AskID] = true
		chatEqual(t, "every ref is workspace scoped", ref.Contact, coverage.contact)
	}
}

func actionCheckBatchActive(t *testing.T, h *chatHarness, read protocol.Message) {
	t.Helper()
	for _, ref := range read.ReviewAskRefs {
		chatEqual(t, "other batch omission does not settle this card", actionCardWithID(t, h, ref.AskID).Active, true)
	}
}

func actionCheckBatchSteps(t *testing.T, h *chatHarness, read protocol.Message, want string) {
	t.Helper()
	for _, ref := range read.ReviewAskRefs {
		v3Contains(t, strings.Join(actionCardWithID(t, h, ref.AskID).Steps, "\n"), want)
	}
}

func actionDrainAskRefs(t *testing.T, h *chatHarness) {
	t.Helper()
	h.await(func() bool {
		for i := 0; i < len(h.requests); {
			req := h.requests[i]
			if len(req.ReviewAskRefs) == 0 {
				i++
				continue
			}
			if len(req.ReviewAskRefs) > 8 {
				t.Fatal("creation-time card read exceeded eight refs")
			}
			h.requests = append(h.requests[:i], h.requests[i+1:]...)
			actionAnswerAskRefs(h, req, "asked", false)
		}
		return h.m.State().InFlight == 0
	})
}

func TestActionCardsAcceptanceNewCardFencesEarlierRead(t *testing.T) {
	for _, tc := range []struct{ name, command, requestType, op string }{
		{"remote", remotePinnedCommand("@reviewer@Office", 40), protocol.TypeOperatorContact, protocol.ContactOpReviewAsk},
		{"local", "/review pairtask/pairtask#305 reviewer", protocol.TypeOperatorReview, protocol.ReviewOpRequest},
		{"share", "/pair share sender", protocol.TypeOperatorContact, protocol.ContactOpShare},
		{"unshare", "/pair unshare sender", protocol.TypeOperatorContact, protocol.ContactOpUnshare},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, first := actionRemoteQueued(t)
			pairedTab := tc.name == "share" || tc.name == "unshare"
			if pairedTab {
				actionSelectRemoteCard(t, h, first.ContactKey)
			}
			old := actionRemoteRefresh(t, h, first.ContactKey)
			if pairedTab {
				page := h.take(protocol.TypeOperatorContact)
				h.launch(h.holdResponse(page, contactMessagesPage(first.ContactKey)))
			}
			v2Command(h, tc.command)
			second := actionTakeNewRequest(t, h, tc.requestType, tc.op)
			chatEqual(t, "second command keeps a distinct request", second.MessageID != first.MessageID, true)
			h.launch(h.holdResponse(old, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewAsked: &protocol.ReviewAskedPage{Items: []protocol.ReviewAskedItem{}}}))
			chatEqual(t, "new card invalidates older read generation", actionCardWithID(t, h, first.MessageID).Active, true)
			fresh := actionTakeAskRefs(t, h)
			chatEqual(t, "surviving active card gets replacement read", fresh.RequestID != old.RequestID, true)
		})
	}
}

func actionTakeNewRequest(t *testing.T, h *chatHarness, kind, op string) protocol.Message {
	t.Helper()
	index := -1
	h.await(func() bool {
		for i, req := range h.requests {
			if actionRequestMatches(req, kind, op) {
				index = i
				return true
			}
		}
		return false
	})
	req := h.requests[index]
	h.requests = append(h.requests[:index], h.requests[index+1:]...)
	return req
}

func actionRequestMatches(req protocol.Message, kind, op string) bool {
	if req.Type != kind {
		return false
	}
	if req.ReviewOp == op {
		return true
	}
	return req.ContactOp == op
}

func TestActionCardsAcceptanceOlderEventReadCannotReplaceNewerOutcome(t *testing.T) {
	for _, older := range []string{"requested", "omitted"} {
		t.Run(older, func(t *testing.T) {
			h, ask := actionRemoteQueued(t)
			old := actionRemoteRefresh(t, h, ask.ContactKey)
			fresh := actionRemoteRefresh(t, h, ask.ContactKey)
			completed := protocol.ReviewAskedItem{Contact: ask.ContactKey, AskID: ask.MessageID, Reviewer: "reviewer", State: "completed", Verdict: "approve", Settled: true}
			h.launch(h.holdResponse(fresh, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewAsked: &protocol.ReviewAskedPage{Items: []protocol.ReviewAskedItem{completed}, Total: 1}}))
			before := actionCardWithID(t, h, ask.MessageID)
			chatEqual(t, "newer outcome is finished", before.Finished, true)
			items := []protocol.ReviewAskedItem{}
			if older == "requested" {
				items = append(items, protocol.ReviewAskedItem{Contact: ask.ContactKey, AskID: ask.MessageID, Reviewer: "reviewer", State: "requested"})
			}
			h.launch(h.holdResponse(old, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewAsked: &protocol.ReviewAskedPage{Items: items, Total: len(items)}}))
			after := actionCardWithID(t, h, ask.MessageID)
			chatEqual(t, "older event response cannot replace newer outcome", strings.Join(after.Steps, "\n"), strings.Join(before.Steps, "\n"))
			chatEqual(t, "older event cannot reopen completed card", after.Active, false)
		})
	}
}

func actionSelectRemoteCard(t *testing.T, h *chatHarness, key string) {
	t.Helper()
	h.input(chatKey(tea.KeyTab, 0))
	page := h.take(protocol.TypeOperatorContact)
	h.reply(page, contactMessagesPage(key))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	revision := h.m.State().Revision + 1
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventPresenceChanged, AgentID: "sender", SessionID: "session-sender", State: "connected", Watermark: protocol.Watermark{Epoch: "epoch", Revision: revision}}}
	h.await(func() bool { return h.m.State().Revision == revision })
}

func TestActionCardsAcceptanceReconnectRefreshesActiveCardsAndRejectsOldRead(t *testing.T) {
	h, ask := actionRemoteQueued(t)
	old := actionRemoteRefresh(t, h, ask.ContactKey)
	h.reconnectTo(newChatScript(), "epoch")
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory())
	pairing := pairingChatRequest(h, "list")
	h.reply(pairing, contactChatReply(contactChatRow(ask.ContactKey, "Office", true)))
	fresh := actionTakeAskRefs(t, h)
	chatEqual(t, "reconnect still reads the original ask", fresh.ReviewAskRefs[0].AskID, ask.MessageID)
	h.launch(h.holdResponse(old, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewAsked: &protocol.ReviewAskedPage{Items: []protocol.ReviewAskedItem{}}}))
	chatEqual(t, "old missing result cannot settle card", actionCardWithID(t, h, ask.MessageID).Active, true)
	actionAnswerAskRefs(h, fresh, "accepted", false)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, strings.Join(actionCardWithID(t, h, ask.MessageID).Steps, "\n"), "accepted by reviewer@Office")
}
