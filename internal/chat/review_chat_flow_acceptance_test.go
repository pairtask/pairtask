package chat_test

import (
	"reflect"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func reviewChatHarness(t *testing.T) *chatHarness {
	t.Helper()
	h := newChatHarness(t, chat.Bounds{})
	h.input(tea.WindowSizeMsg{Width: 160, Height: 45})
	h.ready()
	return h
}

func reviewChatRow(id string) protocol.ReviewView {
	return protocol.ReviewView{ID: id, State: protocol.ReviewRequested,
		Spec:        protocol.ReviewSpec{ReviewTarget: protocol.ReviewTarget{Forge: "github", Repository: "github.com/pairtask/pairtask", Change: "201"}, Reviewer: "reviewer"},
		Publication: protocol.Publication{State: protocol.PublicationNone}}
}

func reviewChatPage(rows ...protocol.ReviewView) protocol.Message {
	return protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewPage: &protocol.ReviewPage{Reviews: rows, Total: len(rows)}}
}

func reviewChatSettle(h *chatHarness, req protocol.Message, row protocol.ReviewView) {
	h.t.Helper()
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorReviewResult, Review: &row})
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func TestReviewChatAcceptanceRequestPoliciesAndTarget(t *testing.T) {
	for _, tc := range []struct {
		flags  string
		policy *protocol.ReviewPolicy
	}{
		{"", nil}, {"--no-formal ", &protocol.ReviewPolicy{Publish: true}}, {"--no-publish ", &protocol.ReviewPolicy{}},
	} {
		t.Run(tc.flags, func(t *testing.T) {
			h := reviewChatHarness(t)
			v2Command(h, "/review "+tc.flags+"pairtask/pairtask#201 reviewer inspect retry safety")
			req := h.take(protocol.TypeOperatorReview)
			chatEqual(t, "request operation", req.ReviewOp, protocol.ReviewOpRequest)
			chatEqual(t, "durable ID present", req.MessageID != "", true)
			want := reviewChatRow(req.MessageID)
			want.Spec.Scope = "inspect retry safety"
			want.Spec.Policy = tc.policy
			if !reflect.DeepEqual(req.ReviewSpec, &want.Spec) {
				t.Fatalf("request spec = %+v, want %+v", req.ReviewSpec, want.Spec)
			}
			reviewChatSettle(h, req, want)
			chatEqual(t, "no transcript message", len(h.m.State().Entries), 0)
			v2Command(h, "/review pairtask/pairtask#201 reviewer changed scope")
			next := h.take(protocol.TypeOperatorReview)
			chatEqual(t, "new input has new ID", next.MessageID != req.MessageID, true)
		})
	}
}

func TestReviewChatAcceptanceReconnectRetainsOriginalSpecAndPolicy(t *testing.T) {
	for _, flags := range []string{"", "--no-formal ", "--no-publish "} {
		t.Run(flags, func(t *testing.T) {
			h := reviewChatHarness(t)
			v2Command(h, "/review "+flags+"https://github.com/pairtask/pairtask/pull/201 reviewer original scope")
			original := h.take(protocol.TypeOperatorReview)
			h.input(tea.PasteMsg{Content: "separate draft"})
			h.reconnectTo(newChatScript(), "epoch")
			inbox, history := h.baseline()
			h.reply(inbox, chatInbox())
			h.reply(history, chatHistory())
			retry := h.take(protocol.TypeOperatorReview)
			retryRequestID := retry.RequestID
			original.RequestID = ""
			retry.RequestID = ""
			if !reflect.DeepEqual(original, retry) {
				t.Fatalf("reconnect changed review intent: first=%+v retry=%+v", original, retry)
			}
			retry.RequestID = retryRequestID
			row := reviewChatRow(original.MessageID)
			row.Spec = *original.ReviewSpec
			h.reply(retry, protocol.Message{Type: protocol.TypeOperatorReviewResult, Review: &row})
			refresh := h.take(protocol.TypeOperatorReview)
			chatEqual(t, "list follows settled retry", refresh.ReviewOp, protocol.ReviewOpList)
			h.reply(refresh, reviewChatPage(row))
			h.await(func() bool { return h.m.State().InFlight == 0 })
			chatEqual(t, "separate draft retained", h.m.State().Draft, "separate draft")
			chatEqual(t, "single review", len(h.m.State().Reviews), 1)
		})
	}
}

func TestReviewChatAcceptanceOldBrokerCannotSendReviewCommands(t *testing.T) {
	caps := slices.DeleteFunc(slices.Clone(protocol.SessionCapabilities), func(s string) bool { return s == protocol.CapabilityReviews })
	h := pairingChatCapabilities(t, caps)
	for _, command := range []string{"/review pairtask/pairtask#201 reviewer", "/reviews", "/review status abc", "/review cancel abc"} {
		v2Command(h, command)
		chatEqual(t, "old broker has no request", h.m.State().InFlight, 0)
		chatEqual(t, "old broker has no queued operation", h.m.State().Queued, 0)
		v3Contains(t, h.m.State().Status, "upgrade")
	}
}

func TestReviewChatAcceptancePrefixResolutionAndExactMatch(t *testing.T) {
	h := reviewChatHarness(t)
	rows := []protocol.ReviewView{reviewChatRow("review-one"), reviewChatRow("review-one-long"), reviewChatRow("unique-two")}
	v2Command(h, "/reviews")
	req := h.take(protocol.TypeOperatorReview)
	h.reply(req, reviewChatPage(rows...))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	for _, ref := range []string{"review-", "missing"} {
		v2Command(h, "/review cancel "+ref+" stop")
		chatEqual(t, "ambiguous or unknown sends nothing", h.m.State().InFlight, 0)
		chatEqual(t, "ambiguous or unknown queues nothing", h.m.State().Queued, 0)
	}
	for _, tc := range []struct{ ref, id string }{{"review-one", "review-one"}, {"unique", "unique-two"}} {
		v2Command(h, "/review cancel "+tc.ref+" requested reason")
		req = h.take(protocol.TypeOperatorReview)
		chatEqual(t, "full resolved ID", req.ReviewID, tc.id)
		chatEqual(t, "cancel operation", req.ReviewOp, protocol.ReviewOpCancel)
		chatEqual(t, "cancel reason", req.ReviewReason, "requested reason")
		reviewChatSettle(h, req, reviewChatRow(tc.id))
	}
}

func TestReviewChatAcceptanceSecondRequestCannotLoseUnsettledIntent(t *testing.T) {
	h := reviewChatHarness(t)
	v2Command(h, "/review pairtask/pairtask#201 reviewer first scope")
	first := h.take(protocol.TypeOperatorReview)
	v2Command(h, "/review pairtask/pairtask#202 reviewer second scope")
	// A second request may be refused until settlement, or retained alongside
	// the first. It must never silently replace the retryable first request.
	h.reconnectTo(newChatScript(), "epoch")
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory())
	retry := h.take(protocol.TypeOperatorReview)
	chatEqual(t, "first unresolved intent survives", retry.MessageID, first.MessageID)
	chatEqual(t, "first target survives", retry.ReviewSpec.Change, "201")
}
