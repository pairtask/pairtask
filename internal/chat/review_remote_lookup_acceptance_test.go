package chat_test

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/forge"
	"github.com/pairtask/pairtask/internal/protocol"
	"slices"
	"strings"
	"testing"
	"time"
)

type remoteResolution struct {
	pins protocol.Revisions
	err  error
}

type remoteLookupCall struct {
	ctx    context.Context
	target protocol.ReviewTarget
	reply  chan remoteResolution
}

type remoteLookup struct {
	calls        chan remoteLookupCall
	results      chan remoteResolution
	done         chan struct{}
	ignoreCancel bool
}

func (r *remoteLookup) resolve(ctx context.Context, target protocol.ReviewTarget) (protocol.Revisions, error) {
	reply := make(chan remoteResolution, 1)
	r.calls <- remoteLookupCall{ctx: ctx, target: target, reply: reply}
	cancelled := ctx.Done()
	if r.ignoreCancel {
		cancelled = nil
	}
	select {
	case result := <-reply:
		return result.pins, result.err
	case result := <-r.results:
		return result.pins, result.err
	case <-cancelled:
		return protocol.Revisions{}, ctx.Err()
	case <-r.done:
		return protocol.Revisions{}, context.Canceled
	}
}

func remotePins() protocol.Revisions {
	return protocol.Revisions{Base: strings.Repeat("1", 40), Head: strings.Repeat("2", 40)}
}

func remoteHarness(t *testing.T, contacts ...protocol.PairingContactView) (*chatHarness, *remoteLookup) {
	t.Helper()
	r := &remoteLookup{calls: make(chan remoteLookupCall, 4), results: make(chan remoteResolution, 4), done: make(chan struct{})}
	h := newChatHarness(t, chat.Bounds{ResolveChange: r.resolve})
	t.Cleanup(func() { close(r.done) })
	h.input(tea.WindowSizeMsg{Width: 200, Height: 40})
	if len(contacts) == 0 {
		contacts = []protocol.PairingContactView{contactChatRow("sha256:"+strings.Repeat("a", 64), "Office", true)}
	}
	contactChatBaseline(h, contacts...)
	return h, r
}

func remoteCall(t *testing.T, r *remoteLookup) remoteLookupCall {
	t.Helper()
	select {
	case call := <-r.calls:
		return call
	case <-time.After(3 * time.Second):
		t.Fatal("review command never invoked resolver")
	}
	return remoteLookupCall{}
}

func remoteSettle(h *chatHarness, req protocol.Message) {
	h.t.Helper()
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorContactResult, ContactMessage: &protocol.ContactMessageView{Contact: req.ContactKey, ID: req.MessageID, Direction: "out", State: "queued", ReviewAsk: req.ReviewAsk}})
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func remoteRefreshContacts(h *chatHarness, contacts ...protocol.PairingContactView) {
	h.t.Helper()
	contactChatChanged(h, h.m.State().Revision+1)
	h.reply(pairingChatRequest(h, "list"), contactChatReply(contacts...))
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func TestReviewRemoteAcceptanceLookupFreezesKeyAcrossAliasReassignment(t *testing.T) {
	first := contactChatRow("sha256:"+strings.Repeat("a", 64), "Office", true)
	second := contactChatRow("sha256:"+strings.Repeat("b", 64), "Other", true)
	h, lookup := remoteHarness(t, first, second)
	v2Command(h, "/review pairtask/pairtask#295 @reviewer@Office")
	remoteCall(t, lookup)
	first.Name, second.Name = "Renamed", "Office"
	remoteRefreshContacts(h, first, second)
	lookup.results <- remoteResolution{pins: remotePins()}
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "lookup result never re-resolves mutable alias", req.ContactKey, first.IdentityKey)
	remoteSettle(h, req)
}

func TestReviewRemoteAcceptanceLookupRechecksContactAvailability(t *testing.T) {
	for _, change := range []string{"paused", "unconfirmed", "removed"} {
		t.Run(change, func(t *testing.T) {
			c := contactChatRow("sha256:"+strings.Repeat("a", 64), "Office", true)
			h, lookup := remoteHarness(t, c)
			v2Command(h, "/review pairtask/pairtask#295 @reviewer@Office")
			remoteCall(t, lookup)
			contacts := []protocol.PairingContactView{c}
			switch change {
			case "paused":
				now := time.Now()
				contacts[0].PausedAt = &now
			case "unconfirmed":
				contacts[0].Confirmed = false
			case "removed":
				contacts = nil
			}
			remoteRefreshContacts(h, contacts...)
			lookup.results <- remoteResolution{pins: remotePins()}
			h.await(func() bool { return strings.Contains(h.m.State().Status, "not sent") })
			chatEqual(t, "unavailable contact never receives review", h.m.State().InFlight, 0)
		})
	}
}

func remoteApplyLate(h *chatHarness, lookup *remoteLookup) {
	h.t.Helper()
	lookup.results <- remoteResolution{pins: remotePins()}
	select {
	case msg := <-h.results:
		h.input(msg)
	case <-time.After(3 * time.Second):
		h.t.Fatal("late resolver result never reached model")
	}
}

func TestReviewRemoteAcceptanceCancelledLookupNeverSendsLateResult(t *testing.T) {
	for _, cancel := range []string{"escape", "tab"} {
		t.Run(cancel, func(t *testing.T) {
			h, lookup := remoteHarness(t)
			lookup.ignoreCancel = true
			v2Command(h, "/review pairtask/pairtask#295 @reviewer@Office")
			call := remoteCall(t, lookup)
			if cancel == "escape" {
				h.input(chatKey(tea.KeyEscape, 0))
			} else {
				h.input(chatKey(tea.KeyTab, 0))
				req := h.take(protocol.TypeOperatorContact)
				h.reply(req, contactMessagesPage(req.ContactKey))
				h.await(func() bool { return h.m.State().InFlight == 0 })
			}
			if call.ctx.Err() == nil {
				t.Fatal("navigation did not cancel lookup context")
			}
			before := h.m.State().Status
			remoteApplyLate(h, lookup)
			chatEqual(t, "late lookup result never sends", h.m.State().InFlight, 0)
			chatEqual(t, "late lookup result never overwrites status", h.m.State().Status, before)
		})
	}
}

func TestReviewRemoteAcceptanceNilResolverExplainsPinOverride(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	contactChatBaseline(h, contactChatRow("sha256:"+strings.Repeat("a", 64), "Office", true))
	v2Command(h, "/review pairtask/pairtask#295 @reviewer@Office")
	v3Contains(t, h.m.State().Status, "add --pin BASE..HEAD")
	chatEqual(t, "missing resolver never sends", h.m.State().InFlight, 0)
}

func TestReviewRemoteAcceptanceReplacementLookupFencesEarlierResult(t *testing.T) {
	h, lookup := remoteHarness(t)
	lookup.ignoreCancel = true
	v2Command(h, "/review pairtask/pairtask#295 @first@Office")
	first := remoteCall(t, lookup)
	v2Command(h, "/review pairtask/pairtask#295 @second@Office")
	second := remoteCall(t, lookup)
	if first.ctx.Err() == nil {
		t.Fatal("replacement lookup did not cancel previous context")
	}
	before := h.m.State().Status
	first.reply <- remoteResolution{pins: remotePins()}
	select {
	case msg := <-h.results:
		h.input(msg)
	case <-time.After(3 * time.Second):
		t.Fatal("old lookup never returned")
	}
	chatEqual(t, "old replacement result never sends", h.m.State().InFlight, 0)
	chatEqual(t, "old replacement result preserves new status", h.m.State().Status, before)
	second.reply <- remoteResolution{pins: remotePins()}
	req := h.take(protocol.TypeOperatorContact)
	if !slices.Equal(req.ContactTo, []string{"second"}) {
		t.Fatalf("old lookup replaced new reviewer: %v", req.ContactTo)
	}
	remoteSettle(h, req)
}

func TestReviewRemoteAcceptanceDisconnectCancelsAndFencesLookup(t *testing.T) {
	h, lookup := remoteHarness(t)
	lookup.ignoreCancel = true
	v2Command(h, "/review pairtask/pairtask#295 @reviewer@Office")
	call := remoteCall(t, lookup)
	h.wire.Close()
	h.await(func() bool { return call.ctx.Err() != nil })
	before := h.m.State().Status
	remoteApplyLate(h, lookup)
	chatEqual(t, "disconnected lookup never sends", h.m.State().InFlight, 0)
	chatEqual(t, "disconnected lookup preserves status", h.m.State().Status, before)
}

func TestReviewRemoteAcceptanceBaselineChangeCancelsAndFencesLookup(t *testing.T) {
	h, lookup := remoteHarness(t)
	lookup.ignoreCancel = true
	v2Command(h, "/review pairtask/pairtask#295 @reviewer@Office")
	call := remoteCall(t, lookup)
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventReviewsChanged, Watermark: protocol.Watermark{Epoch: "epoch", Revision: 10}}}
	snapshot := h.take(protocol.TypeOperatorSnapshot)
	h.launch(h.holdResponse(snapshot, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 10}}}))
	if call.ctx.Err() == nil {
		t.Fatal("new baseline did not cancel lookup context")
	}
	before := h.m.State()
	remoteApplyLate(h, lookup)
	chatEqual(t, "old baseline lookup cannot add broker send", h.m.State().InFlight, before.InFlight)
	chatEqual(t, "old baseline lookup cannot replace status", h.m.State().Status, before.Status)
}

func TestReviewRemoteAcceptanceDeadlineErrorIsSanitized(t *testing.T) {
	h, lookup := remoteHarness(t)
	v2Command(h, "/review pairtask/pairtask#295 @reviewer@Office")
	remoteCall(t, lookup)
	lookup.results <- remoteResolution{err: context.DeadlineExceeded}
	h.await(func() bool { return strings.Contains(h.m.State().Status, "timed out") })
	chatEqual(t, "timed out lookup never sends", h.m.State().InFlight, 0)
}

func TestReviewRemoteAcceptanceMalformedResolvedPinsNeverSend(t *testing.T) {
	for _, base := range []string{"", "main", strings.Repeat("A", 40)} {
		t.Run(base, func(t *testing.T) {
			h, lookup := remoteHarness(t)
			v2Command(h, "/review pairtask/pairtask#295 @reviewer@Office")
			remoteCall(t, lookup)
			pins := remotePins()
			pins.Base = base
			lookup.results <- remoteResolution{pins: pins}
			h.await(func() bool { return strings.Contains(h.m.State().Status, "unexpected answer") })
			chatEqual(t, "malformed code-host answer never sends", h.m.State().InFlight, 0)
		})
	}
}

func TestReviewRemoteAcceptanceURLResolvesAndSendsFrozenPins(t *testing.T) {
	h, lookup := remoteHarness(t)
	v2Command(h, "/review --no-publish https://github.com/pairtask/pairtask/pull/295 @remote-only@Office inspect retries")
	call := remoteCall(t, lookup)
	chatEqual(t, "URL forge", call.target.Forge, "github")
	chatEqual(t, "URL pull", call.target.Change, "295")
	chatEqual(t, "lookup sends no broker request", h.m.State().InFlight, 0)
	remoteRequireDeadline(t, call.ctx)
	lookup.results <- remoteResolution{pins: remotePins()}
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "remote review operation", req.ContactOp, protocol.ContactOpReviewAsk)
	chatEqual(t, "canonical contact frozen", req.ContactKey, "sha256:"+strings.Repeat("a", 64))
	if !slices.Equal(req.ContactTo, []string{"remote-only"}) {
		t.Fatalf("wrong remote reviewer: %v", req.ContactTo)
	}
	chatEqual(t, "resolved base", req.ReviewAsk.Base, remotePins().Base)
	chatEqual(t, "resolved head", req.ReviewAsk.Head, remotePins().Head)
	chatEqual(t, "scope retained", req.ReviewAsk.Scope, "inspect retries")
	chatEqual(t, "explicit no publication", req.ReviewAsk.Publish, false)
	chatEqual(t, "no implicit formal publication", req.ReviewAsk.Formal, false)
	remoteSettle(h, req)
	for _, want := range []string{"2222222", "1111111", "no publish", "out of date"} {
		v3Contains(t, h.m.State().Status, want)
	}
}

func TestReviewRemoteAcceptanceLookupFailureClassesNeverLeakRawErrors(t *testing.T) {
	for _, tc := range []struct{ class, label string }{
		{forge.ClassForbidden, "no access"}, {forge.ClassNotFound, "not found"}, {forge.ClassRateLimited, "rate limit"}, {forge.ClassUnavailable, "network error"}, {forge.ClassInvalid, "unexpected answer"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			h, lookup := remoteHarness(t)
			v2Command(h, "/review pairtask/pairtask#295 reviewer@Office")
			remoteCall(t, lookup)
			err := forge.Errorf(tc.class, "SECRET raw credential detail")
			lookup.results <- remoteResolution{err: err}
			h.await(func() bool { return strings.Contains(h.m.State().Status, "can't look up") })
			v3Contains(t, h.m.State().Status, tc.label)
			if strings.Contains(h.m.State().Status, "SECRET") {
				t.Fatal("lookup leaked raw error")
			}
			chatEqual(t, "failed lookup never sends", h.m.State().InFlight, 0)
		})
	}
}

func TestReviewRemoteAcceptanceAnyNewReviewCommandFencesEarlierLookup(t *testing.T) {
	for _, command := range []string{
		"/review pairtask/pairtask#295 @local-only",
		"/review status existing-local",
		"/review cancel existing-local changed-plan",
	} {
		t.Run(command, func(t *testing.T) {
			h, lookup := remoteHarness(t)
			lookup.ignoreCancel = true
			v2Command(h, "/review pairtask/pairtask#295 @remote-only@Office")
			call := remoteCall(t, lookup)
			v2Command(h, command)
			req := h.take(protocol.TypeOperatorReview)
			row := reviewChatRow("existing-local")
			if req.ReviewOp == protocol.ReviewOpRequest {
				row.ID, row.Spec = req.MessageID, *req.ReviewSpec
			}
			if req.ReviewOp == protocol.ReviewOpCancel {
				row.State = protocol.ReviewCancelled
			}
			reviewChatSettle(h, req, row)
			before := h.m.State().Status
			remoteApplyLate(h, lookup)
			chatEqual(t, "new review command prevents old remote send", h.m.State().InFlight, 0)
			chatEqual(t, "new review command preserves its status", h.m.State().Status, before)
			if call.ctx.Err() == nil {
				t.Fatal("new review command did not cancel previous lookup context")
			}
		})
	}
}

func remoteRequireDeadline(t *testing.T, ctx context.Context) {
	t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("lookup lacks deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > 20*time.Second {
		t.Fatal("lookup lacks bounded 20-second deadline")
	}
}
