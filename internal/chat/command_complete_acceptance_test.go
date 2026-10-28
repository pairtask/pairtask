package chat_test

import (
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func commandCompleteHarness(t *testing.T, extra ...string) *chatHarness {
	t.Helper()
	h := newChatHarness(t, chat.Bounds{})
	h.input(tea.WindowSizeMsg{Width: 120, Height: 30})
	agents := []protocol.AgentPresence{{AgentID: "bob"}, {AgentID: "alice"}, {AgentID: "id with spaces"}}
	for _, id := range extra {
		agents = append(agents, protocol.AgentPresence{AgentID: id})
	}
	h.reply(h.take(protocol.TypeOperatorSnapshot), protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{
		Watermark: protocol.Watermark{Epoch: "epoch", Revision: 1}, Agents: agents, AgentsTotal: len(agents),
		Rooms: []protocol.RoomInfo{{ID: "beta", Members: []string{"bob"}}, {ID: "alpha", Members: []string{"zoe", "alice"}}}, RoomsTotal: 2,
	}})
	inbox, history := h.take(protocol.TypeOperatorReceive), h.take(protocol.TypeOperatorHistory)
	h.reply(inbox, chatInbox())
	message := chatIncoming("offline-author", 7)
	message.From = "carol"
	h.reply(history, chatHistory(message))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h
}

func commandCompleteLine(h *chatHarness, text string) {
	h.t.Helper()
	h.input(chatKey('k', tea.ModCtrl))
	v2Type(h, text)
}

func commandCompleteItems(t *testing.T, h *chatHarness, want []string) {
	t.Helper()
	p := h.m.State().Picker
	if p == nil {
		t.Fatal("argument picker missing")
	}
	chatEqual(t, "argument picker", p.Kind, "argument")
	if !slices.Equal(p.Items, want) {
		t.Fatalf("eligible candidates = %q, want %q", p.Items, want)
	}
}

func TestCommandCompleteAcceptancePositionsAndEligibleMembers(t *testing.T) {
	for _, tc := range []struct {
		line string
		want []string
	}{
		{"room ", []string{"add", "remove", "delete", "alpha", "beta"}},
		{"room a", []string{"add", "alpha"}},
		{"rooms remove al", []string{"alpha"}},
		{"room add ", []string{"alpha", "beta"}},
		{"room add alpha @", []string{"@bob", "@carol"}},
		{"room add alpha @bob @", []string{"@carol"}},
		{"room remove alpha @", []string{"@alice", "@zoe"}},
		{"room remove alpha @alice @", []string{"@zoe"}},
		{"chat @", []string{"@alice", "@bob", "@carol"}},
		{"chat @alice @", []string{"@bob", "@carol"}},
		{"chat @c", []string{"@carol"}},
	} {
		t.Run(tc.line, func(t *testing.T) {
			h := commandCompleteHarness(t)
			commandCompleteLine(h, tc.line)
			commandCompleteItems(t, h, tc.want)
			chatEqual(t, "suggesting does not act", h.m.State().InFlight, 0)
		})
	}
}

func TestCommandCompleteAcceptanceTabAndEnterOnlyCompleteThenExecute(t *testing.T) {
	h := commandCompleteHarness(t)
	v2Type(h, "preserved draft")
	commandCompleteLine(h, "room add al")
	h.input(chatKey(tea.KeyTab, 0))
	chatEqual(t, "room token completed", h.m.State().CommandLine.Text, "room add alpha ")
	v2Type(h, "@")
	h.input(chatKey(tea.KeyDown, 0))
	h.input(chatKey(tea.KeyUp, 0))
	h.input(chatKey(tea.KeyDown, 0))
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "selected agent completed", h.m.State().CommandLine.Text, "room add alpha @carol ")
	chatEqual(t, "completion does not send room request", h.m.State().InFlight, 0)
	chatEqual(t, "draft preserved", h.m.State().Draft, "preserved draft")
	h.input(chatKey(tea.KeyEnter, 0))
	req := h.take(protocol.TypeOperatorRoom)
	chatEqual(t, "correct operation", req.RoomOp, protocol.RoomOpAdd)
	chatEqual(t, "correct room", req.Room, "alpha")
	chatEqual(t, "only selected agent added", req.AgentID, "carol")
}

func TestCommandCompleteAcceptanceRepeatedChatMembersOpenExactGroup(t *testing.T) {
	h := commandCompleteHarness(t)
	commandCompleteLine(h, "chat @al")
	h.input(chatKey(tea.KeyTab, 0))
	v2Type(h, "@bo")
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "group line completed", h.m.State().CommandLine.Text, "chat @alice @bob ")
	chatEqual(t, "first Enter stays in All", h.m.State().SelectedAll, true)
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "command executed", h.m.State().CommandLine, nil)
	if !slices.Equal(h.m.State().Audience.IDs, []string{"alice", "bob"}) {
		t.Fatalf("wrong selected group: %+v", h.m.State().Audience)
	}
	chatEqual(t, "group selection sends nothing", h.m.State().Intent, nil)
}

func TestCommandCompleteAcceptanceWholeTypedCandidateRunsOnFirstEnter(t *testing.T) {
	h := commandCompleteHarness(t)
	commandCompleteLine(h, "chat @alice @bob")
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "whole command executed without extra Enter", h.m.State().CommandLine, nil)
	if !slices.Equal(h.m.State().Audience.IDs, []string{"alice", "bob"}) {
		t.Fatalf("wrong group: %+v", h.m.State().Audience)
	}
}

func TestCommandCompleteAcceptanceExplicitSelectionWinsOverExactPrefix(t *testing.T) {
	h := commandCompleteHarness(t, "bobby")
	commandCompleteLine(h, "chat @bob")
	commandCompleteItems(t, h, []string{"@bob", "@bobby"})
	h.input(chatKey(tea.KeyDown, 0))
	h.input(chatKey(tea.KeyEnter, 0))
	if h.m.State().CommandLine == nil {
		t.Fatal("Enter ran typed prefix instead of completing highlighted agent")
	}
	chatEqual(t, "highlighted agent completed", h.m.State().CommandLine.Text, "chat @bobby ")
	chatEqual(t, "selection did not execute", h.m.State().SelectedAll, true)
}

func TestCommandCompleteAcceptanceNameCompletionOpensArgumentsAndUnknownRoomStillWorks(t *testing.T) {
	h := commandCompleteHarness(t)
	commandCompleteLine(h, "ro")
	h.input(chatKey(tea.KeyTab, 0))
	chatEqual(t, "command name completed", h.m.State().CommandLine.Text, "room ")
	commandCompleteItems(t, h, []string{"add", "remove", "delete", "alpha", "beta"})
	h.input(chatKey(tea.KeyEscape, 0))
	h.input(chatKey(tea.KeyEscape, 0))
	commandCompleteLine(h, "room unlisted")
	chatEqual(t, "unknown name needs no picker", h.m.State().Picker, nil)
	h.input(chatKey(tea.KeyEnter, 0))
	req := h.take(protocol.TypeOperatorRoom)
	chatEqual(t, "unknown room still creates", req.RoomOp, protocol.RoomOpCreate)
	chatEqual(t, "unknown name unchanged", req.Room, "unlisted")
}

func TestCommandCompleteAcceptancePickerRendersCandidatesAtNarrowWidths(t *testing.T) {
	h := commandCompleteHarness(t)
	commandCompleteLine(h, "room add alpha @")
	for _, width := range []int{120, 60, 30} {
		h.input(tea.WindowSizeMsg{Width: width, Height: 30})
		view := ansi.Strip(h.m.View().Content)
		v3Contains(t, view, "@bob")
		v3Contains(t, view, "@carol")
		v2FrameFits(t, h, width, 30)
		chatEqual(t, "resizing preserves command", h.m.State().CommandLine.Text, "room add alpha @")
	}
}

func TestCommandCompleteAcceptanceEscStaysDismissedUntilEdit(t *testing.T) {
	h := commandCompleteHarness(t)
	commandCompleteLine(h, "chat @")
	h.input(chatKey(tea.KeyEscape, 0))
	chatEqual(t, "Esc closes picker", h.m.State().Picker, nil)
	chatEqual(t, "Esc keeps command line", h.m.State().CommandLine.Text, "chat @")
	h.input(chatKey(tea.KeyLeft, 0))
	h.input(chatKey(tea.KeyEnd, 0))
	h.input(tea.WindowSizeMsg{Width: 60, Height: 20})
	chatEqual(t, "navigation and resize do not reopen", h.m.State().Picker, nil)
	v2Type(h, "b")
	commandCompleteItems(t, h, []string{"@bob"})
	h.input(chatKey(tea.KeyEscape, 0))
	h.input(chatKey(tea.KeyEscape, 0))
	chatEqual(t, "second Esc closes line", h.m.State().CommandLine, nil)
}

func TestCommandCompleteAcceptanceDoesNotReplaceSuffixWhileEditingEarlierToken(t *testing.T) {
	h := commandCompleteHarness(t)
	commandCompleteLine(h, "room add alpha @b")
	h.input(chatKey(tea.KeyHome, 0))
	chatEqual(t, "midline picker suppressed", h.m.State().Picker, nil)
	h.input(chatKey(tea.KeyTab, 0))
	chatEqual(t, "Tab preserves trailing token", h.m.State().CommandLine.Text, "room add alpha @b")
	h.input(chatKey(tea.KeyEnd, 0))
	commandCompleteItems(t, h, []string{"@bob"})
	h.input(chatKey(tea.KeyTab, 0))
	chatEqual(t, "end-of-line completion works", h.m.State().CommandLine.Text, "room add alpha @bob ")
}

func TestCommandCompleteAcceptanceUnsupportedAndPastedTextDoNotComplete(t *testing.T) {
	for _, line := range []string{"chat alice", "chat @missing", "cha @", "room remove missing @", "find @", "room alpha extra", "room add alpha bob"} {
		t.Run(line, func(t *testing.T) {
			h := commandCompleteHarness(t)
			commandCompleteLine(h, line)
			chatEqual(t, "no inappropriate suggestions", h.m.State().Picker, nil)
			h.input(chatKey(tea.KeyTab, 0))
			chatEqual(t, "Tab leaves literal text", h.m.State().CommandLine.Text, line)
		})
	}
	h := commandCompleteHarness(t)
	commandCompleteLine(h, "room add alpha @")
	h.input(tea.PasteMsg{Content: "bo"})
	h.input(chatKey(tea.KeyTab, 0))
	chatEqual(t, "pasted argument is not completed", h.m.State().CommandLine.Text, "room add alpha @bo")
	chatEqual(t, "paste opens no completion picker", h.m.State().Picker, nil)
	chatEqual(t, "paste and Tab do not act", h.m.State().InFlight, 0)
}
