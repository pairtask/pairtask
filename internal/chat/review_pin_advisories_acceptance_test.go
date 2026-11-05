package chat_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func advisoryPinChat(t *testing.T, capable bool) *chatHarness {
	t.Helper()
	caps := slices.Clone(protocol.SessionCapabilities)
	if capable {
		if !slices.Contains(caps, "review_advisories") {
			caps = append(caps, "review_advisories")
		}
	} else {
		caps = slices.DeleteFunc(caps, func(c string) bool { return c == "review_advisories" })
	}
	h := pairingChatCapabilities(t, caps)
	v2Command(h, "/pair list")
	key := "sha256:" + strings.Repeat("a", 64)
	h.reply(pairingChatRequest(h, "list"), contactChatReply(contactChatRow(key, "Office", true)))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey(tea.KeyEscape, 0))
	return h
}

func advisoryPinQueue(t *testing.T, h *chatHarness) protocol.Message {
	t.Helper()
	v2Command(h, remotePinnedCommand("@reviewer@Office", 40))
	ask := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "one command queues valid request", ask.ContactOp, protocol.ContactOpReviewAsk)
	chatEqual(t, "explicit head remains unchanged", ask.ReviewAsk.Head, remotePins().Head)
	return ask
}

func advisoryPinReply(t *testing.T, h *chatHarness, ask protocol.Message, notes []map[string]any) {
	t.Helper()
	response := map[string]any{"type": protocol.TypeOperatorContactResult, "review_advisories": notes,
		"contact_message": protocol.ContactMessageView{Contact: ask.ContactKey, ID: ask.MessageID, Direction: "out", State: "queued", ReviewAsk: ask.ReviewAsk}}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var wire protocol.Message
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	h.reply(ask, wire)
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func TestReviewPinAdvisoriesAcceptanceNilCheckerIsExplicitAfterSending(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	h := advisoryPinChat(t, true)
	ask := advisoryPinQueue(t, h)
	advisoryPinReply(t, h, ask, nil)
	card := actionCardWithID(t, h, ask.MessageID)
	v3Contains(t, strings.Join(card.Steps, "\n"), "Pins not checked: unsupported")
	v3Contains(t, ansi.Strip(h.m.View().Content), "Queued here")
	h.input(tea.WindowSizeMsg{Width: 60, Height: 24})
	v2FrameFits(t, h, 60, 24)
}

func TestReviewPinAdvisoriesAcceptanceBrokerDirectoryNoteDoesNotRequireConfirmation(t *testing.T) {
	h := advisoryPinChat(t, true)
	ask := advisoryPinQueue(t, h)
	at := time.Date(2026, 11, 4, 12, 34, 0, 0, time.UTC)
	advisoryPinReply(t, h, ask, []map[string]any{{"code": "not_in_directory", "at": at}})
	card := actionCardWithID(t, h, ask.MessageID)
	v3Contains(t, strings.Join(card.Steps, "\n"), "Note: reviewer isn't in what Office shares (as of "+at.Local().Format("15:04")+")")
	chatEqual(t, "advisory does not freeze another intent", h.m.State().Intent, nil)
	chatEqual(t, "advisory never queues a second send", h.m.State().Queued, 0)
}

func TestReviewPinAdvisoriesAcceptanceOlderBrokerNeverDisplaysUnsupportedNotes(t *testing.T) {
	h := advisoryPinChat(t, false)
	ask := advisoryPinQueue(t, h)
	advisoryPinReply(t, h, ask, []map[string]any{{"code": "not_in_directory", "at": time.Now()}})
	card := actionCardWithID(t, h, ask.MessageID)
	if strings.Contains(strings.Join(card.Steps, "\n"), "Note:") {
		t.Fatal("operator rendered advisory data without the broker capability")
	}
	v3Contains(t, strings.Join(card.Steps, "\n"), "Queued here")
}

type advisoryPinCall struct {
	ctx    context.Context
	target protocol.ReviewTarget
	pins   protocol.Revisions
	reply  chan []protocol.ReviewAdvisory
}

type advisoryPinLookup struct {
	calls        chan advisoryPinCall
	done         chan struct{}
	ignoreCancel bool
}

func (p *advisoryPinLookup) check(ctx context.Context, target protocol.ReviewTarget, pins protocol.Revisions) []protocol.ReviewAdvisory {
	reply := make(chan []protocol.ReviewAdvisory, 1)
	p.calls <- advisoryPinCall{ctx, target, pins, reply}
	cancelled := ctx.Done()
	if p.ignoreCancel {
		cancelled = nil
	}
	select {
	case answer := <-reply:
		return answer
	case <-cancelled:
		return []protocol.ReviewAdvisory{{Code: "not_checked", Class: "timeout"}}
	case <-p.done:
		return nil
	}
}

func advisoryPinHarness(t *testing.T, ignoreCancel bool) (*chatHarness, *advisoryPinLookup) {
	t.Helper()
	p := &advisoryPinLookup{calls: make(chan advisoryPinCall, 4), done: make(chan struct{}), ignoreCancel: ignoreCancel}
	t.Cleanup(func() { close(p.done) })
	h := newChatHarness(t, chat.Bounds{CheckPins: p.check, ResolveChange: func(context.Context, protocol.ReviewTarget) (protocol.Revisions, error) {
		return remotePins(), nil
	}})
	h.input(tea.WindowSizeMsg{Width: 120, Height: 30})
	contactChatBaseline(h, contactChatRow("sha256:"+strings.Repeat("a", 64), "Office", true))
	return h, p
}

func advisoryPinStarted(t *testing.T, p *advisoryPinLookup) advisoryPinCall {
	t.Helper()
	select {
	case call := <-p.calls:
		return call
	case <-time.After(3 * time.Second):
		t.Fatal("sent pinned request never started its asynchronous pin check")
	}
	return advisoryPinCall{}
}

func advisoryPinSteps(t *testing.T, h *chatHarness, id string) string {
	t.Helper()
	return strings.Join(actionCardWithID(t, h, id).Steps, "\n")
}

func TestReviewPinAdvisoriesAcceptanceOperatorQueuesBeforeCheckerFinishes(t *testing.T) {
	h, lookup := advisoryPinHarness(t, false)
	ask := advisoryPinQueue(t, h)
	advisoryPinReply(t, h, ask, nil)
	call := advisoryPinStarted(t, lookup)
	chatEqual(t, "checker sees immutable pins", call.pins, remotePins())
	chatEqual(t, "checker sees original change", call.target.Change, "295")
	v3Contains(t, advisoryPinSteps(t, h, ask.MessageID), "Queued here")
	chatEqual(t, "checker never blocks broker queue", h.m.State().InFlight, 0)
	call.reply <- []protocol.ReviewAdvisory{{Code: "head_moved", Current: strings.Repeat("3", 40)}}
	h.await(func() bool {
		return strings.Contains(advisoryPinSteps(t, h, ask.MessageID), "Pins checked after sending:")
	})
	v3Contains(t, advisoryPinSteps(t, h, ask.MessageID), "HEAD 2222222 isn't the change's head (now 3333333)")
	chatEqual(t, "check never sends replacement pins", h.m.State().InFlight, 0)
}

func TestReviewPinAdvisoriesAcceptanceCheckerGetsFiveSecondBudget(t *testing.T) {
	h, lookup := advisoryPinHarness(t, false)
	start := time.Now()
	ask := advisoryPinQueue(t, h)
	advisoryPinReply(t, h, ask, nil)
	call := advisoryPinStarted(t, lookup)
	deadline, ok := call.ctx.Deadline()
	if !ok {
		t.Fatal("asynchronous pin checker has no deadline")
	}
	if deadline.After(start.Add(5100 * time.Millisecond)) {
		t.Fatal("asynchronous pin checker exceeds five-second budget")
	}
	select {
	case <-call.ctx.Done():
	case <-time.After(6 * time.Second):
		t.Fatal("asynchronous pin check never expired")
	}
	h.await(func() bool {
		return strings.Contains(advisoryPinSteps(t, h, ask.MessageID), "Pins not checked: timeout")
	})
}

func TestReviewPinAdvisoriesAcceptanceClearAndDisconnectFenceLateChecks(t *testing.T) {
	for _, event := range []string{"clear", "disconnect"} {
		t.Run(event, func(t *testing.T) {
			h, lookup := advisoryPinHarness(t, true)
			ask := advisoryPinQueue(t, h)
			advisoryPinReply(t, h, ask, nil)
			call := advisoryPinStarted(t, lookup)
			if event == "clear" {
				v2Command(h, "/cards clear")
				refresh := actionTakeAskRefs(t, h)
				actionAnswerAskRefs(h, refresh, "requested", false)
				h.await(func() bool { return h.m.State().InFlight == 0 })
			} else {
				h.wire.Close()
			}
			h.await(func() bool { return call.ctx.Err() != nil })
			call.reply <- []protocol.ReviewAdvisory{{Code: "head_moved", Current: strings.Repeat("d", 40)}}
			select {
			case msg := <-h.results:
				h.input(msg)
			case <-time.After(3 * time.Second):
				t.Fatal("deliberately late pin checker never returned")
			}
			if strings.Contains(advisoryPinSteps(t, h, ask.MessageID), "now ddddddd") {
				t.Fatal("old card generation accepted a late pin result")
			}
		})
	}
}

func TestReviewPinAdvisoriesAcceptanceResolvedPinsDoNotTriggerExplicitPinCheck(t *testing.T) {
	h, lookup := advisoryPinHarness(t, false)
	v2Command(h, "/review pairtask/pairtask#295 @reviewer@Office")
	ask := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "normal resolver still pins request", ask.ReviewAsk.Head, remotePins().Head)
	advisoryPinReply(t, h, ask, nil)
	select {
	case <-lookup.calls:
		t.Fatal("request without --pin ran the explicit-pin advisory checker")
	case <-time.After(40 * time.Millisecond):
	}
}

func TestReviewPinAdvisoriesAcceptanceCommandHintUsesOnlyAlreadyLoadedDirectory(t *testing.T) {
	for _, tc := range []struct {
		name   string
		known  bool
		listed []string
		warn   bool
	}{{"unknown", false, nil, false}, {"known-empty", true, []string{}, true}, {"listed", true, []string{"reviewer"}, false}} {
		t.Run(tc.name, func(t *testing.T) {
			h := advisoryPinChat(t, true)
			if tc.known {
				h.input(chatKey(tea.KeyTab, 0))
				read := h.take(protocol.TypeOperatorContact)
				page := contactMessagesPage(read.ContactKey)
				page.ContactPage.Directory = tc.listed
				h.reply(read, page)
				h.await(func() bool { return h.m.State().InFlight == 0 })
			}
			commandCompleteLine(h, strings.TrimPrefix(remotePinnedCommand("@reviewer@Office", 40), "/"))
			view := ansi.Strip(h.m.View().Content)
			if got := strings.Contains(view, "reviewer isn't in what Office shares, as last read"); got != tc.warn {
				t.Fatalf("hint warning=%v, want %v; hint=%q view=%q", got, tc.warn, h.m.State().Hint, view)
			}
			chatEqual(t, "typing hint never fetches directory", h.m.State().InFlight, 0)
			h.input(chatKey(tea.KeyEnter, 0))
			ask := h.take(protocol.TypeOperatorContact)
			chatEqual(t, "one Enter queues despite directory warning", ask.ContactOp, protocol.ContactOpReviewAsk)
			advisoryPinReply(t, h, ask, nil)
		})
	}
}
