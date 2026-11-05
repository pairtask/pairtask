package chat_test

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestPairedTabStyleAcceptanceRecipientsMatchLocalAudienceEmphasis(t *testing.T) {
	for _, draft := range []string{"", "@reviewer question"} {
		t.Run(draft, func(t *testing.T) {
			local := v3DisplayHarness(t, false)
			warning := v3StyleAt(t, v3AudienceRow(t, local), "to everyone")
			h, _ := recipientScopeHarness(t)
			h.input(tea.WindowSizeMsg{Width: 180, Height: 36})
			v2Type(h, draft)
			row := v3AudienceRow(t, h)
			who := "Office's operator"
			if draft != "" {
				who = "reviewer@Office"
			}
			recipient := v3StyleAt(t, row, who)
			v3ColorEqual(t, "paired recipient uses local audience colour", recipient.Fg, warning.Fg)
			chatEqual(t, "paired audience remains bold", recipient.Attrs&uv.AttrBold != 0, true)
			departure := "leaves when they next sync"
			if strings.Contains(h.m.State().Hint, "sent at once") {
				departure = "sent at once"
			}
			for _, text := range []string{"your agents here won't get it", departure} {
				quiet := v3StyleAt(t, row, text)
				if reflect.DeepEqual(quiet, recipient) {
					t.Fatalf("guidance %q has the same emphasis as the recipient", text)
				}
			}
			chatEqual(t, "styling never sends draft", h.m.State().InFlight, 0)
		})
	}
}

func TestPairedTabStyleAcceptanceHeldDraftKeepsErrorColour(t *testing.T) {
	local := v3DisplayHarness(t, false)
	v2Type(local, "@all @codex-1 blocked local draft")
	want := v3StyleAt(t, v3AudienceRow(t, local), "✗")
	h, _ := recipientScopeHarness(t)
	v2Type(h, "@local-only keep this draft")
	style := v3StyleAt(t, v3AudienceRow(t, h), "✗")
	if style.Fg == nil {
		t.Fatal("held paired draft lost its error colour")
	}
	v3ColorEqual(t, "held paired draft keeps local error colour", style.Fg, want.Fg)
	chatEqual(t, "error remains bold", style.Attrs&uv.AttrBold != 0, true)
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "held draft unchanged", h.m.State().Draft, "@local-only keep this draft")
	chatEqual(t, "held draft writes nothing", h.m.State().InFlight, 0)
}

func TestPairedTabStyleAcceptanceInactiveBandAndSelectionRemainDistinct(t *testing.T) {
	h, _ := recipientScopeHarness(t)
	h.input(tea.WindowSizeMsg{Width: 180, Height: 36})
	selected := v3StyleAt(t, formattingRow(t, h, "⇄ Office"), "Office")
	h.input(chatKey(tea.KeyTab, tea.ModShift))
	inactive := v3StyleAt(t, formattingRow(t, h, "⇄ Office"), "Office")
	formattingReadableStyle(t, "inactive paired tab", inactive)
	formattingReadableStyle(t, "selected paired tab", selected)
	if reflect.DeepEqual(selected, inactive) {
		t.Fatal("paired background obscures selected tab")
	}
	local := v3StyleAt(t, formattingRow(t, h, "All"), "All")
	if reflect.DeepEqual(inactive.Bg, local.Bg) {
		t.Fatal("paired tab has no dedicated background")
	}
	chatEqual(t, "tab styling never sends", h.m.State().InFlight, 0)
}

func TestPairedTabStyleAcceptancePendingMarkMatchesLocalAndClearsAfterAck(t *testing.T) {
	local := v3DisplayHarness(t, false)
	localPending := v3StyleAt(t, formattingRow(t, local, "needs ack"), "needs ack")
	h, _ := ackContactHarness(t, true, protocol.ContactAckSummary{Unacked: 1, AckUpto: 13}, ackContactRow(13, "needs_ack"))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	pending := v3StyleAt(t, formattingRow(t, h, "needs ack"), "needs ack")
	v3ColorEqual(t, "paired pending mark uses local pending colour", pending.Fg, localPending.Fg)
	chatEqual(t, "paired pending mark has local emphasis", pending.Attrs&uv.AttrBold, localPending.Attrs&uv.AttrBold)
	line := ansi.Strip(formattingRow(t, h, "MESSAGE 13"))
	chatEqual(t, "paired pending body uses local indentation", strings.Index(line, "MESSAGE 13"), 2)
	v2Command(h, "/ack #13")
	request := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "only explicit ack changes state", request.ContactOp, protocol.ContactOpAck)
	chatEqual(t, "ack retains real inbound identity", request.ContactAck[0], "in-13")
	ackContactReply(h, request, contactAckReply{count: 1, rows: []protocol.ContactMessageView{ackContactRow(13, "acked")}})
	settled := v3StyleAt(t, formattingRow(t, h, "acknowledged"), "acknowledged")
	chatEqual(t, "acknowledged mark no longer bold", settled.Attrs&uv.AttrBold != 0, false)
	chatEqual(t, "ack leaves body alignment unchanged", strings.Index(ansi.Strip(formattingRow(t, h, "MESSAGE 13")), "MESSAGE 13"), 2)
	chatEqual(t, "styling adds no follow-up mutation", h.m.State().InFlight, 0)
}

func TestPairedTabStyleAcceptanceMonochromeAndNarrowKeepMeaningWithoutMutation(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	h, _ := ackContactHarness(t, true, protocol.ContactAckSummary{Unacked: 1, AckUpto: 13}, ackContactRow(13, "needs_ack"))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(tea.ColorProfileMsg{Profile: colorprofile.ASCII})
	for _, width := range []int{45, 60, 100} {
		h.input(tea.WindowSizeMsg{Width: width, Height: 36})
		v2FrameFits(t, h, width, 36)
		view := ansi.Strip(h.m.View().Content)
		v3Contains(t, view, "⇄ remote")
		v3Contains(t, strings.Join(strings.Fields(view), " "), "needs ack")
		v3Contains(t, view, "MESSAGE 13")
		chatEqual(t, "render never queues human acknowledgment", len(h.m.State().QueuedAcks), 0)
		chatEqual(t, "render never mutates broker", h.m.State().InFlight, 0)
	}
}
