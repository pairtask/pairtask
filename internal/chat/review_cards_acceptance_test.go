package chat_test

import (
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestActionCardsAcceptanceRemoteLookupAndQueueRemainOneCard(t *testing.T) {
	h, lookup := remoteHarness(t)
	v2Command(h, "/review pairtask/pairtask#305 @reviewer@Office")
	call := remoteCall(t, lookup)
	cards := h.m.State().Cards
	chatEqual(t, "lookup is visible as one card", len(cards), 1)
	chatEqual(t, "remote review kind", cards[0].Kind, "review-remote")
	v3Contains(t, strings.Join(cards[0].Steps, "\n"), "Looking up")
	call.reply <- remoteResolution{pins: remotePins()}
	req := h.take(protocol.TypeOperatorContact)
	actionQueueAnswer(h, req)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	cards = h.m.State().Cards
	chatEqual(t, "lookup transitions in place", len(cards), 1)
	chatEqual(t, "queued card bound to actual ask", cards[0].ID, req.MessageID)
	for _, want := range []string{"Queued here", "pinned 2222222", "base 1111111", "no publish"} {
		v3Contains(t, strings.Join(cards[0].Steps, "\n"), want)
	}
	chatEqual(t, "local queue is not completed remote work", cards[0].Active, true)
}

func actionQueueAnswer(h *chatHarness, req protocol.Message) {
	h.t.Helper()
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorContactResult, ContactMessage: &protocol.ContactMessageView{Contact: req.ContactKey, ID: req.MessageID, Direction: "out", State: "queued", ReviewAsk: req.ReviewAsk}})
}

func actionRemoteQueued(t *testing.T) (*chatHarness, protocol.Message) {
	t.Helper()
	h, _ := remoteHarness(t)
	v2Command(h, remotePinnedCommand("@reviewer@Office", 40))
	req := h.take(protocol.TypeOperatorContact)
	actionQueueAnswer(h, req)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	read := actionRemoteRefresh(t, h, req.ContactKey)
	chatEqual(t, "exact ask ID", read.ReviewAskRefs[0].AskID, req.MessageID)
	chatEqual(t, "exact workspace identity", read.ReviewAskRefs[0].Contact, req.ContactKey)
	actionAnswerAskRefs(h, read, "asked", false)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h, req
}

func actionCardWithID(t *testing.T, h *chatHarness, id string) chat.CardView {
	t.Helper()
	for _, card := range h.m.State().Cards {
		if card.ID == id {
			return card
		}
	}
	t.Fatalf("card %q missing", id)
	return chat.CardView{}
}

func TestActionCardsAcceptanceRemoteMirrorsShareStateAndClearSeparately(t *testing.T) {
	h, ask := actionRemoteQueued(t)
	read := actionRemoteRefresh(t, h, ask.ContactKey)
	actionAnswerAskRefs(h, read, "declined", true)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	card := actionCardWithID(t, h, ask.MessageID)
	chatEqual(t, "declined request is finished", card.Finished, true)
	v3Contains(t, strings.ToLower(ansi.Strip(h.m.View().Content)), "declined")
	v2Command(h, "/cards clear")
	if strings.Contains(ansi.Strip(h.m.View().Content), "Queued here") {
		t.Fatal("clear left finished card in All")
	}
	chatEqual(t, "other mirror retains the one card", len(h.m.State().Cards), 1)
	h.input(chatKey(tea.KeyTab, 0))
	page := h.take(protocol.TypeOperatorContact)
	h.reply(page, contactMessagesPage(ask.ContactKey))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, strings.ToLower(ansi.Strip(h.m.View().Content)), "declined")
	chatEqual(t, "mirrored card keeps ask identity", actionCardWithID(t, h, ask.MessageID).ID, ask.MessageID)
	v2Command(h, "/cards clear")
	chatEqual(t, "clearing both mirrors removes card", len(h.m.State().Cards), 0)
}

func TestActionCardsAcceptanceWithoutRefCapabilityNeverClaimsMissing(t *testing.T) {
	h, _ := remoteHarness(t)
	older := newChatScript()
	t.Cleanup(func() { older.Close() })
	caps := slices.DeleteFunc(slices.Clone(protocol.SessionCapabilities), func(c string) bool { return c == protocol.CapabilityReviewAskRefs })
	h.dials <- chatDialResult{wire: older, session: protocol.SessionInfo{SessionID: "older-owner", Epoch: "epoch", Capabilities: caps}}
	h.wire.Close()
	h.wire = older
	key := "sha256:" + strings.Repeat("a", 64)
	contactChatBaseline(h, contactChatRow(key, "Office", true))
	v2Command(h, remotePinnedCommand("@reviewer@Office", 40))
	ask := h.take(protocol.TypeOperatorContact)
	actionQueueAnswer(h, ask)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	card := actionCardWithID(t, h, ask.MessageID)
	v3Contains(t, strings.Join(card.Steps, "\n"), "status: see /reviews")
	if strings.Contains(strings.Join(card.Steps, "\n"), "no longer kept") {
		t.Fatal("older broker supplied no missing-ref evidence")
	}
	for _, req := range h.requests {
		if len(req.ReviewAskRefs) > 0 {
			t.Fatal("sent unsupported targeted refs")
		}
	}
}

func TestActionCardsAcceptanceLocalCardTracksVerdictAndPublication(t *testing.T) {
	h := reviewChatHarness(t)
	v2Command(h, "/review pairtask/pairtask#305 reviewer")
	req := h.take(protocol.TypeOperatorReview)
	reviewChatSettle(h, req, reviewChatRow(req.MessageID))
	h.input(chatKey(tea.KeyEscape, 0))
	v2Type(h, "keep local draft")
	row := reviewChatCompleted(req.MessageID)
	reviewChatChanged(h, h.m.State().Revision+1)
	h.await(func() bool {
		for i := 0; i < len(h.requests); {
			read := h.requests[i]
			if read.Type != protocol.TypeOperatorReview {
				i++
				continue
			}
			h.requests = append(h.requests[:i], h.requests[i+1:]...)
			switch read.ReviewOp {
			case protocol.ReviewOpList:
				h.reply(read, reviewChatPage(row))
			case protocol.ReviewOpStatus:
				h.reply(read, protocol.Message{Type: protocol.TypeOperatorReviewResult, Review: &row})
			default:
				t.Fatalf("unexpected local card read: %s", read.ReviewOp)
			}
		}
		cards := h.m.State().Cards
		return len(cards) == 1 && cards[0].Finished && h.m.State().InFlight == 0
	})
	card := actionCardWithID(t, h, row.ID)
	for _, want := range []string{"approve", row.Publication.Receipt.URL} {
		v3Contains(t, strings.Join(card.Steps, "\n"), want)
	}
	chatEqual(t, "terminal local card is inactive", card.Active, false)
	chatEqual(t, "local card refresh preserves draft", h.m.State().Draft, "keep local draft")
}

func TestActionCardsAcceptanceCancelledLookupCannotReviveCard(t *testing.T) {
	h, lookup := remoteHarness(t)
	lookup.ignoreCancel = true
	v2Command(h, "/review pairtask/pairtask#305 @reviewer@Office")
	remoteCall(t, lookup)
	h.input(chatKey(tea.KeyEscape, 0))
	cards := h.m.State().Cards
	chatEqual(t, "cancelled lookup remains one visible result", len(cards), 1)
	chatEqual(t, "cancelled lookup is finished", cards[0].Finished, true)
	v3Contains(t, strings.Join(cards[0].Steps, "\n"), "Lookup cancelled: nothing sent")
	before := strings.Join(cards[0].Steps, "\n")
	remoteApplyLate(h, lookup)
	chatEqual(t, "late lookup leaves settled card unchanged", strings.Join(h.m.State().Cards[0].Steps, "\n"), before)
	chatEqual(t, "cancelled lookup queued no send", h.m.State().InFlight, 0)
}

func TestActionCardsAcceptanceRefusalGuidanceMatchesActualReason(t *testing.T) {
	for _, reason := range []string{"not_shared", "invalid"} {
		t.Run(reason, func(t *testing.T) {
			h, ask := actionRemoteQueued(t)
			read := actionRemoteRefresh(t, h, ask.ContactKey)
			item := protocol.ReviewAskedItem{Contact: ask.ContactKey, AskID: ask.MessageID, Reviewer: "reviewer", State: "refused", Reason: reason, Settled: true, UpdatedAt: time.Now()}
			h.reply(read, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewAsked: &protocol.ReviewAskedPage{Items: []protocol.ReviewAskedItem{item}, Total: 1}})
			h.await(func() bool { return h.m.State().InFlight == 0 })
			card := actionCardWithID(t, h, ask.MessageID)
			steps := strings.Join(card.Steps, "\n")
			v3Contains(t, steps, "Refused: "+reason)
			chatEqual(t, "refused card is finished", card.Finished, true)
			if reason == "not_shared" {
				v3Contains(t, steps, "/pair reviews reviewer ask; then send a new request")
			} else if strings.Contains(steps, "/pair reviews") {
				t.Fatal("unrelated refusal offered a grant fix")
			}
		})
	}
}

func actionFillLocalCardWindow(t *testing.T, h *chatHarness) {
	t.Helper()
	for i := 0; i < 32; i++ {
		v2Command(h, "/review pairtask/pairtask#305 reviewer")
		req := actionTakeNewRequest(t, h, protocol.TypeOperatorReview, protocol.ReviewOpRequest)
		reviewChatSettle(h, req, reviewChatRow(req.MessageID))
	}
	h.input(chatKey(tea.KeyEscape, 0))
}

func TestActionCardsAcceptanceEvictedRemoteFailurePreservesUnclearedMirrors(t *testing.T) {
	for _, tc := range []struct {
		name          string
		clearOriginal bool
	}{{"shown", false}, {"cleared", true}} {
		t.Run(tc.name, func(t *testing.T) {
			h, ask := actionRemoteQueued(t)
			read := actionRemoteRefresh(t, h, ask.ContactKey)
			actionAnswerAskRefs(h, read, "refused", true)
			h.await(func() bool { return h.m.State().InFlight == 0 })
			if tc.clearOriginal {
				v2Command(h, "/cards clear")
			}
			actionFillLocalCardWindow(t, h)
			wantOriginal := 1
			if tc.clearOriginal {
				wantOriginal = 0
			}
			chatEqual(t, "eviction preserves original dismissal", h.m.State().CardOverflow.Failures, wantOriginal)
			h.input(chatKey(tea.KeyTab, 0))
			page := h.take(protocol.TypeOperatorContact)
			h.reply(page, contactMessagesPage(ask.ContactKey))
			h.await(func() bool { return h.m.State().InFlight == 0 })
			chatEqual(t, "uncleared contact retains evicted failure", h.m.State().CardOverflow.Failures, 1)
		})
	}
}

func TestActionCardsAcceptanceEvictedLocalPublicationFailureRemainsCounted(t *testing.T) {
	h := reviewChatHarness(t)
	v2Command(h, "/review pairtask/pairtask#305 reviewer")
	req := h.take(protocol.TypeOperatorReview)
	row := reviewChatCompleted(req.MessageID)
	row.Publication = protocol.Publication{State: protocol.PublicationFailed, Reason: "provider rejected publication"}
	reviewChatSettle(h, req, row)
	chatEqual(t, "failed publication is a finished card", actionCardWithID(t, h, row.ID).Finished, true)
	actionFillLocalCardWindow(t, h)
	chatEqual(t, "evicted local publication failure remains counted", h.m.State().CardOverflow.Failures, 1)
}

func TestActionCardsAcceptanceLocalOutcomeSurvivesUnrelatedListPage(t *testing.T) {
	h := reviewChatHarness(t)
	v2Command(h, "/review pairtask/pairtask#305 reviewer")
	req := h.take(protocol.TypeOperatorReview)
	row := reviewChatCompleted(req.MessageID)
	reviewChatSettle(h, req, row)
	before := actionCardWithID(t, h, row.ID)
	v2Command(h, "/reviews")
	list := h.take(protocol.TypeOperatorReview)
	h.reply(list, reviewChatPage())
	h.await(func() bool { return h.m.State().InFlight == 0 })
	after := actionCardWithID(t, h, row.ID)
	chatEqual(t, "another list page cannot erase known local outcome", strings.Join(after.Steps, "\n"), strings.Join(before.Steps, "\n"))
	chatEqual(t, "known completed card stays finished", after.Finished, true)
}

func TestActionCardsAcceptanceLocalRequestKeepsCardBoundToReview(t *testing.T) {
	h := reviewChatHarness(t)
	v2Command(h, "/review --no-publish pairtask/pairtask#305 reviewer inspect cards")
	req := h.take(protocol.TypeOperatorReview)
	row := reviewChatRow(req.MessageID)
	row.Spec = *req.ReviewSpec
	reviewChatSettle(h, req, row)
	cards := h.m.State().Cards
	chatEqual(t, "local review has one persistent card", len(cards), 1)
	chatEqual(t, "local kind", cards[0].Kind, "review-local")
	chatEqual(t, "local card uses review ID", cards[0].ID, row.ID)
	chatEqual(t, "requested local review is active", cards[0].Active, true)
	v3Contains(t, strings.ToLower(strings.Join(cards[0].Steps, "\n")), "requested")
}
