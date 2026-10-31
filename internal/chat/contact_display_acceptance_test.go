package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func contactDisplayBody(t *testing.T, body string) *chatHarness {
	t.Helper()
	h, _ := contactMessagesHarness(t, protocol.ContactMessageView{ID: "format", Cursor: 1, Direction: "in", State: "read", Text: body})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(tea.WindowSizeMsg{Width: 100, Height: 45})
	return h
}

func TestContactDisplayAcceptanceMarkdownStylesAndInertCommands(t *testing.T) {
	h := contactDisplayBody(t, "## Remote heading\nplain **strong** and `code`\n- list item\n/ack all @operator")
	heading := formattingRow(t, h, "Remote heading")
	chatEqual(t, "heading syntax removed", strings.TrimSpace(ansi.Strip(heading)), "Remote heading")
	chatEqual(t, "heading styled", v3StyleAt(t, heading, "Remote heading").Attrs&uv.AttrBold != 0, true)
	row := formattingRow(t, h, "strong")
	chatEqual(t, "inline syntax removed", strings.TrimSpace(ansi.Strip(row)), "plain strong and code")
	chatEqual(t, "strong styled", v3StyleAt(t, row, "strong").Attrs&uv.AttrBold != 0, true)
	v3Contains(t, ansi.Strip(h.m.View().Content), "/ack all @operator")
	chatEqual(t, "body never executes", h.m.State().InFlight, 0)
	chatEqual(t, "body never queues acknowledgments", len(h.m.State().QueuedAcks), 0)
}

func TestContactDisplayAcceptanceEscapesControlsAcrossMarkdownBlocks(t *testing.T) {
	attack := "\x1b[2J\x1b]52;c;YWJj\a"
	for _, body := range []string{
		"## heading " + attack, "- item " + attack,
		"| Col |\n| --- |\n| cell " + attack + " |",
		"```text\ncode " + attack + "\n```",
	} {
		t.Run(body[:2], func(t *testing.T) {
			h := contactDisplayBody(t, body)
			view := h.m.View().Content
			for _, forbidden := range []string{"\x1b[2J", "\x1b]52;", "\a"} {
				if strings.Contains(view, forbidden) {
					t.Fatalf("terminal control survived formatting: %q", forbidden)
				}
			}
			chatEqual(t, "formatting makes no request", h.m.State().InFlight, 0)
		})
	}
}

func TestContactDisplayAcceptanceDiagramHasNoLocalExportCommand(t *testing.T) {
	h := contactDisplayBody(t, "```mermaid\nflowchart LR\n A --> B\n```")
	view := ansi.Strip(h.m.View().Content)
	if strings.Contains(view, "/diagram") {
		t.Fatal("remote diagram offered a local message export command")
	}
	v3Contains(t, view, "flowchart LR")
}

func TestContactDisplayAcceptanceFormattedBodyFitsNarrowFrames(t *testing.T) {
	h := contactDisplayBody(t, "## A remote heading\n- alpha bravo charlie delta echo foxtrot golf hotel TAIL\n\n| Column one | Column two |\n| --- | --- |\n| longer text | another value |")
	for _, width := range []int{45, 60, 100} {
		h.input(tea.WindowSizeMsg{Width: width, Height: 45})
		v2FrameFits(t, h, width, 45)
		v3Contains(t, ansi.Strip(h.m.View().Content), "TAIL")
	}
}

func contactDisplayRows(n int) []protocol.ContactMessageView {
	rows := contactUnreadRows(n)
	for i := range rows {
		rows[i].State = "read"
	}
	return rows
}

func contactDisplayedMessages(h *chatHarness) string {
	var rows []string
	for _, line := range strings.Split(ansi.Strip(h.m.View().Content), "\n") {
		if strings.Contains(line, "VISIBLE MESSAGE") {
			rows = append(rows, line)
		}
	}
	return strings.Join(rows, "\n")
}

func TestContactDisplayAcceptanceWheelMovesContactAndPreservesDraft(t *testing.T) {
	h, _ := contactMessagesHarness(t, contactDisplayRows(40)...)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Type(h, "unsent draft")
	before := contactDisplayedMessages(h)
	h.input(tea.MouseWheelMsg{X: 5, Y: 10, Button: tea.MouseWheelUp})
	after := contactDisplayedMessages(h)
	if before == after {
		t.Fatal("wheel did not move contact messages")
	}
	chatEqual(t, "wheel does not request local history", h.m.State().InFlight, 0)
	chatEqual(t, "draft retained", h.m.State().Draft, "unsent draft")
	h.input(tea.MouseWheelMsg{X: 5, Y: 10, Button: tea.MouseWheelDown})
	chatEqual(t, "down returns to newest view", contactDisplayedMessages(h), before)
}

func TestContactDisplayAcceptanceWheelLoadsOlderContactPage(t *testing.T) {
	h, key := contactMessagesHarness(t, contactDisplayRows(3)...)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventContactMessagesChanged, ContactKey: key, Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
	refresh := h.take(protocol.TypeOperatorContact)
	page := contactMessagesPage(key, contactDisplayRows(3)...)
	page.ContactPage.More = true
	h.reply(refresh, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(tea.MouseWheelMsg{X: 5, Y: 10, Button: tea.MouseWheelUp})
	older := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "older contact page", older.ContactOp, protocol.ContactOpList)
	chatEqual(t, "same contact", older.ContactKey, key)
	chatEqual(t, "older boundary", older.ContactBefore, int64(1))
}
